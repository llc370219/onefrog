package automation

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

func testBox(t *testing.T) *secret.Box {
	t.Helper()
	box, err := secret.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func waitDone(t *testing.T, b *Book, id string) Automation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		it, err := b.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		b.mu.Lock()
		busy := b.running[id]
		b.mu.Unlock()
		if !busy && it.LastStatus != StatusRunning {
			return it
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not finish")
	return Automation{}
}

func TestNext(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	// 2026-11-06 10:30 local (Friday).
	after := time.Date(2026, 11, 6, 10, 30, 0, 0, loc)
	cases := []struct {
		name string
		s    Schedule
		want time.Time
	}{
		{"interval", Schedule{Kind: KindInterval, Minutes: 30}, after.Add(30 * time.Minute)},
		{"daily later today", Schedule{Kind: KindDaily, Time: "11:00"}, time.Date(2026, 11, 6, 11, 0, 0, 0, loc)},
		{"daily tomorrow", Schedule{Kind: KindDaily, Time: "09:00"}, time.Date(2026, 11, 7, 9, 0, 0, 0, loc)},
		{"daily same minute", Schedule{Kind: KindDaily, Time: "10:30"}, time.Date(2026, 11, 7, 10, 30, 0, 0, loc)},
		{"weekly monday", Schedule{Kind: KindWeekly, Time: "08:00", Weekday: 1}, time.Date(2026, 11, 9, 8, 0, 0, 0, loc)},
		{"weekly today later", Schedule{Kind: KindWeekly, Time: "12:00", Weekday: 5}, time.Date(2026, 11, 6, 12, 0, 0, 0, loc)},
		{"weekly today passed", Schedule{Kind: KindWeekly, Time: "08:00", Weekday: 5}, time.Date(2026, 11, 13, 8, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		got := Next(c.s, after, loc)
		if got == nil || !got.Equal(c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	future := after.Add(time.Hour)
	if got := Next(Schedule{Kind: KindOnce, At: &future}, after, loc); got == nil || !got.Equal(future) {
		t.Errorf("once future: got %v", got)
	}
	past := after.Add(-time.Hour)
	if got := Next(Schedule{Kind: KindOnce, At: &past}, after, loc); got != nil {
		t.Errorf("once past: got %v", got)
	}
}

func TestValidation(t *testing.T) {
	b, _ := New("", nil, nil)
	base := Automation{Name: "巡检", Prompt: "检查磁盘", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindDaily, Time: "9:05"}}
	it, err := b.Create(base, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if it.Schedule.Time != "09:05" || it.NextRunAt == nil || it.OwnerID != "u1" {
		t.Fatalf("unexpected item: %+v", it)
	}
	past := time.Now().Add(-time.Hour)
	bad := []Automation{
		{Prompt: "x", BotID: "main", Schedule: Schedule{Kind: KindDaily, Time: "09:00"}},
		{Name: "a", BotID: "main", Schedule: Schedule{Kind: KindDaily, Time: "09:00"}},
		{Name: "a", Prompt: "x", Schedule: Schedule{Kind: KindDaily, Time: "09:00"}},
		{Name: "a", Prompt: "x", BotID: "main", Schedule: Schedule{Kind: KindInterval, Minutes: 5}},
		{Name: "a", Prompt: "x", BotID: "main", Schedule: Schedule{Kind: KindDaily, Time: "25:00"}},
		{Name: "a", Prompt: "x", BotID: "main", Schedule: Schedule{Kind: KindWeekly, Time: "09:00", Weekday: 7}},
		{Name: "a", Prompt: "x", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindOnce, At: &past}},
		{Name: "a", Prompt: "x", BotID: "main", Schedule: Schedule{Kind: "cron"}},
		{Name: "a", Prompt: "x", BotID: "main", ApprovalMode: "off", Schedule: Schedule{Kind: KindDaily, Time: "09:00"}},
		{Name: strings.Repeat("名", MaxName+1), Prompt: "x", BotID: "main", Schedule: Schedule{Kind: KindDaily, Time: "09:00"}},
	}
	for i, in := range bad {
		var ve *ValidationError
		if _, err := b.Create(in, "u1"); !errors.As(err, &ve) {
			t.Errorf("case %d: want validation error, got %v", i, err)
		}
	}
	disabled := base
	disabled.Enabled = false
	it, err = b.Update(it.ID, disabled)
	if err != nil || it.NextRunAt != nil {
		t.Fatalf("disabled update: %+v %v", it, err)
	}
	if _, err := b.Update("missing", base); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing update: %v", err)
	}
}

func TestPersistAndResetRunning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "automations.enc")
	box := testBox(t)
	b, err := New(path, box, nil)
	if err != nil {
		t.Fatal(err)
	}
	it, err := b.Create(Automation{Name: "巡检", Prompt: "检查服务器的密码 hunter2", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2") || strings.Contains(string(raw), "巡检") {
		t.Fatal("automation file is not encrypted")
	}
	// Simulate a crash in the middle of a run.
	b.mu.Lock()
	x := b.items[it.ID]
	x.LastStatus = StatusRunning
	b.items[it.ID] = x
	if err := b.saveLocked(); err != nil {
		t.Fatal(err)
	}
	b.mu.Unlock()

	again, err := New(path, box, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.Get(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != it.Prompt || got.LastStatus != StatusFailed || got.LastError == "" {
		t.Fatalf("reload: %+v", got)
	}
	if err := again.Delete(it.ID); err != nil {
		t.Fatal(err)
	}
	third, _ := New(path, box, nil)
	if len(third.List()) != 0 {
		t.Fatal("delete was not persisted")
	}
}

func TestRunNowWritesHistory(t *testing.T) {
	st := store.NewMemory()
	ctx := context.Background()
	if _, err := st.CreateBot(ctx, model.Bot{ID: "main", Name: "主 Bot"}); err != nil {
		t.Fatal(err)
	}
	b, _ := New("", nil, st)
	var seen Automation
	b.SetRunner(func(_ context.Context, it Automation) (string, string, error) {
		seen = it
		return SessionID(it), "一切正常，磁盘使用 40%", nil
	})
	it, err := b.Create(Automation{Name: "巡检", Prompt: "检查磁盘", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindDaily, Time: "09:00"}}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	next := *it.NextRunAt
	started, err := b.RunNow(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.LastStatus != StatusRunning {
		t.Fatalf("status: %s", started.LastStatus)
	}
	done := waitDone(t, b, it.ID)
	if done.LastStatus != StatusSuccess || !strings.Contains(done.LastSummary, "一切正常") {
		t.Fatalf("done: %+v", done)
	}
	if done.SessionID != SessionID(it) || !IsSession(done.SessionID) {
		t.Fatalf("session: %s", done.SessionID)
	}
	if done.NextRunAt == nil || !done.NextRunAt.Equal(next) {
		t.Fatalf("next run changed: %v vs %v", done.NextRunAt, next)
	}
	if seen.Prompt != "检查磁盘" {
		t.Fatalf("runner got %+v", seen)
	}
	hist, err := st.ListHistory(ctx, 10)
	if err != nil || len(hist) != 1 {
		t.Fatalf("history: %v %v", hist, err)
	}
	if hist[0].BotName != "主 Bot" || hist[0].Status != StatusSuccess || !strings.Contains(hist[0].Command, "一切正常") {
		t.Fatalf("history entry: %+v", hist[0])
	}
}

func TestRunnerFailureAndMissingRunner(t *testing.T) {
	b, _ := New("", nil, nil)
	it, _ := b.Create(Automation{Name: "a", Prompt: "x", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 30}}, "u1")
	if _, err := b.RunNow(it.ID); err != nil {
		t.Fatal(err)
	}
	done := waitDone(t, b, it.ID)
	if done.LastStatus != StatusFailed || done.LastError != "Bot 服务未配置" {
		t.Fatalf("no runner: %+v", done)
	}
	b.SetRunner(func(context.Context, Automation) (string, string, error) {
		return "", "", errors.New("模型不可用")
	})
	b.RunNow(it.ID)
	done = waitDone(t, b, it.ID)
	if done.LastStatus != StatusFailed || done.LastError != "模型不可用" {
		t.Fatalf("runner error: %+v", done)
	}
}

func TestRunNowWhileRunning(t *testing.T) {
	b, _ := New("", nil, nil)
	release := make(chan struct{})
	b.SetRunner(func(context.Context, Automation) (string, string, error) {
		<-release
		return "", "ok", nil
	})
	it, _ := b.Create(Automation{Name: "a", Prompt: "x", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 30}}, "u1")
	if _, err := b.RunNow(it.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RunNow(it.ID); !errors.Is(err, ErrRunning) {
		t.Fatalf("want ErrRunning, got %v", err)
	}
	close(release)
	waitDone(t, b, it.ID)
}

func TestScheduledOnceDisablesItself(t *testing.T) {
	b, _ := New("", nil, nil)
	ran := make(chan struct{}, 1)
	b.SetRunner(func(context.Context, Automation) (string, string, error) {
		ran <- struct{}{}
		return "", "done", nil
	})
	clock := time.Now().UTC()
	b.now = func() time.Time { return clock }
	at := clock.Add(10 * time.Minute)
	it, err := b.Create(Automation{Name: "一次", Prompt: "x", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindOnce, At: &at}}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if b.dispatchDue(); len(ran) != 0 {
		t.Fatal("ran too early")
	}
	clock = clock.Add(11 * time.Minute)
	b.dispatchDue()
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled run did not start")
	}
	done := waitDone(t, b, it.ID)
	if done.Enabled || done.NextRunAt != nil || done.LastStatus != StatusSuccess {
		t.Fatalf("once after run: %+v", done)
	}
	b.dispatchDue()
	if len(ran) != 0 {
		t.Fatal("once ran twice")
	}
}

func TestForgetServer(t *testing.T) {
	b, _ := New("", nil, nil)
	it, _ := b.Create(Automation{Name: "a", Prompt: "x", BotID: "main", ServerID: "s1", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 30}}, "u1")
	if err := b.ForgetServer("s1"); err != nil {
		t.Fatal(err)
	}
	got, _ := b.Get(it.ID)
	if got.ServerID != "" {
		t.Fatalf("server not cleared: %+v", got)
	}
}
