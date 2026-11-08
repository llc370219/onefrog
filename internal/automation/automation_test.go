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

// TestPersistedRunKeepsDurableTaskAcrossRestart: a run that was in flight when
// the process stopped is not failed if it has a durable task id; the scheduler
// re-reads that task on startup.
func TestPersistedRunKeepsDurableTaskAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "automations.enc")
	box := testBox(t)
	b, err := New(path, box, nil)
	if err != nil {
		t.Fatal(err)
	}
	it, err := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	x := b.items[it.ID]
	x.LastStatus = StatusRunning
	x.TaskID = "task-abc"
	x.RunID = "run-1"
	b.items[it.ID] = x
	if err := b.saveLocked(); err != nil {
		b.mu.Unlock()
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
	if got.TaskID != "task-abc" || got.RunID != "run-1" {
		t.Fatalf("task linkage lost: %+v", got)
	}
	if got.LastStatus == StatusFailed {
		t.Fatal("a run with a durable task was marked failed on restart")
	}
	if got.LastStatus != StatusWaiting {
		t.Fatalf("status after restart %q", got.LastStatus)
	}
}

// TestReconcileObservesInsteadOfResubmitting: on startup the scheduler reads
// the stored task. A parked run stays waiting, a completed one records success,
// and no run is submitted again.
func TestReconcileObservesInsteadOfResubmitting(t *testing.T) {
	b, _ := New("", nil, nil)
	it, err := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	x := b.items[it.ID]
	x.LastStatus = StatusWaiting
	x.TaskID = "task-abc"
	b.items[it.ID] = x
	b.mu.Unlock()

	var observed, submitted int
	b.SetDurableRunner(&DurableRunner{
		Submit: func(context.Context, Automation, string) (string, string, error) {
			submitted++
			return "", "", nil
		},
		Observe: func(context.Context, Automation, string) (Observation, error) {
			observed++
			return Observation{Status: "waiting_approval", Waiting: true}, nil
		},
	})
	b.reconcile()
	if submitted != 0 {
		t.Fatalf("reconcile resubmitted the run %d times", submitted)
	}
	if observed != 1 {
		t.Fatalf("observe calls %d", observed)
	}
	got, _ := b.Get(it.ID)
	if got.LastStatus != StatusWaiting {
		t.Fatalf("parked run became %q after reconcile", got.LastStatus)
	}
	if got.LastSummary != "" {
		t.Fatal("parked run recorded a success summary")
	}

	// A completed task is recorded as success with its text.
	b.SetDurableRunner(&DurableRunner{
		Observe: func(context.Context, Automation, string) (Observation, error) {
			return Observation{Status: "completed", Done: true, Text: "全部正常"}, nil
		},
	})
	b.reconcile()
	got, _ = b.Get(it.ID)
	if got.LastStatus != StatusSuccess || got.LastSummary != "全部正常" {
		t.Fatalf("completed run not recorded: %+v", got)
	}
}

// TestDurableRunPersistsTaskIDBeforeWork: the scheduler stores the task id
// before waiting, and a run that needs a human decision is recorded as waiting.
// TestEachFiringGetsItsOwnRunIdentity: two consecutive firings must not reuse
// the previous run identity, otherwise the second one would resolve to the
// first one's task and report its result.
func TestEachFiringGetsItsOwnRunIdentity(t *testing.T) {
	b, _ := New("", nil, nil)
	it, err := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a completed previous firing with its own identity.
	b.mu.Lock()
	x := b.items[it.ID]
	x.RunID = "run-old"
	x.TaskID = "task-old"
	x.LastStatus = StatusSuccess
	b.items[it.ID] = x
	b.mu.Unlock()
	identities := []string{}
	b.SetDurableRunner(&DurableRunner{
		Submit: func(_ context.Context, _ Automation, runID string) (string, string, error) {
			identities = append(identities, runID)
			return "task-" + runID, "sess", nil
		},
		Wait: func(context.Context, Automation, string) (Observation, error) {
			return Observation{Status: "completed", Done: true, Text: "ok"}, nil
		},
	})
	first := b.startLocked(mustGet(t, b, it.ID), false)
	b.wg.Wait()
	second := b.startLocked(mustGet(t, b, it.ID), false)
	b.wg.Wait()
	if first.RunID == "" || second.RunID == "" {
		t.Fatal("firing without a run identity")
	}
	if first.RunID == "run-old" || second.RunID == "run-old" {
		t.Fatal("reused the previous firing's run identity")
	}
	if first.RunID == second.RunID {
		t.Fatalf("two firings shared run identity %q", first.RunID)
	}
	if len(identities) != 2 || identities[0] == identities[1] {
		t.Fatalf("submit identities %v", identities)
	}
}

func mustGet(t *testing.T, b *Book, id string) Automation {
	t.Helper()
	it, err := b.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

// TestRunIdentityRecoversSubmitCrashGap: a crash between submit and the TaskID
// write leaves a RunID without a TaskID. Recovery must re-submit the SAME
// identity and adopt the same task, never invent a new run.
func TestRunIdentityRecoversSubmitCrashGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "automations.enc")
	box := testBox(t)
	b, err := New(path, box, nil)
	if err != nil {
		t.Fatal(err)
	}
	it, err := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	// Durable identity written, task id lost: the submit had already happened.
	b.mu.Lock()
	x := b.items[it.ID]
	x.RunID = "run-crash"
	x.TaskID = ""
	x.LastStatus = StatusRunning
	b.items[it.ID] = x
	if err := b.saveLocked(); err != nil {
		b.mu.Unlock()
		t.Fatal(err)
	}
	b.mu.Unlock()

	reloaded, err := New(path, box, nil)
	if err != nil {
		t.Fatal(err)
	}
	var identities []string
	reloaded.SetDurableRunner(&DurableRunner{
		Submit: func(_ context.Context, _ Automation, runID string) (string, string, error) {
			identities = append(identities, runID)
			return "task-recovered", "sess", nil
		},
		Observe: func(context.Context, Automation, string) (Observation, error) {
			return Observation{Status: "completed", Done: true, Text: "已恢复"}, nil
		},
	})
	reloaded.reconcile()
	if len(identities) != 1 || identities[0] != "run-crash" {
		t.Fatalf("recovery used identities %v, want the persisted run-crash", identities)
	}
	got, _ := reloaded.Get(it.ID)
	if got.TaskID != "task-recovered" {
		t.Fatalf("recovered task id %q", got.TaskID)
	}
	if got.LastStatus == StatusFailed {
		t.Fatal("recoverable run was marked failed")
	}
}

// TestReconcileCompletesRunWithoutRestart: a run approved while the process is
// running must be observed on the periodic tick, not only at startup.
func TestReconcileCompletesRunWithoutRestart(t *testing.T) {
	b, _ := New("", nil, nil)
	it, err := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	x := b.items[it.ID]
	x.RunID = "run-1"
	x.TaskID = "task-1"
	x.LastStatus = StatusWaiting
	b.items[it.ID] = x
	b.mu.Unlock()
	state := "waiting_approval"
	b.SetDurableRunner(&DurableRunner{
		Observe: func(context.Context, Automation, string) (Observation, error) {
			if state == "completed" {
				return Observation{Status: "completed", Done: true, Text: "审批后完成"}, nil
			}
			return Observation{Status: state, Waiting: true}, nil
		},
	})
	b.reconcile()
	if got, _ := b.Get(it.ID); got.LastStatus != StatusWaiting {
		t.Fatalf("parked run %q", got.LastStatus)
	}
	// The operator approves; the next periodic reconcile must pick it up.
	state = "completed"
	b.reconcile()
	got, _ := b.Get(it.ID)
	if got.LastStatus != StatusSuccess || got.LastSummary != "审批后完成" {
		t.Fatalf("periodic reconcile did not record completion: %+v", got)
	}
}

// TestTerminalFailureIsNotSuccess: a failed, cancelled or budget-exhausted task
// must not be recorded as a successful run.
func TestTerminalFailureIsNotSuccess(t *testing.T) {
	for _, tc := range []struct{ status, want string }{
		{"failed", StatusFailed},
		{"cancelled", StatusFailed},
		{"budget_exhausted", StatusFailed},
		{"paused", StatusWaiting},
		{"running", StatusRunning},
		{"completed", StatusSuccess},
	} {
		b, _ := New("", nil, nil)
		it, _ := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
		b.mu.Lock()
		x := b.items[it.ID]
		x.RunID, x.TaskID, x.LastStatus = "r", "t", StatusRunning
		b.items[it.ID] = x
		b.mu.Unlock()
		obs := Observation{Status: tc.status}
		switch tc.status {
		case "completed":
			obs.Done, obs.Text = true, "完成"
		case "failed", "cancelled", "budget_exhausted":
			obs.Done, obs.Text = true, ""
		case "paused":
			obs.Waiting = true
		}
		b.applyObservation(it.ID, "r", "t", obs)
		got, _ := b.Get(it.ID)
		if got.LastStatus != tc.want {
			t.Fatalf("%s became %q, want %q", tc.status, got.LastStatus, tc.want)
		}
		if tc.want == StatusFailed && got.LastSummary != "" {
			t.Fatalf("%s recorded a success summary %q", tc.status, got.LastSummary)
		}
	}
}

// TestStaleObservationIsDiscarded: an observation for a previous firing must
// not overwrite the current run's state.
func TestStaleObservationIsDiscarded(t *testing.T) {
	b, _ := New("", nil, nil)
	it, _ := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
	b.mu.Lock()
	x := b.items[it.ID]
	x.RunID, x.TaskID, x.LastStatus = "run-new", "task-new", StatusRunning
	b.items[it.ID] = x
	b.mu.Unlock()
	b.applyObservation(it.ID, "run-old", "task-old", Observation{Status: "completed", Done: true, Text: "旧结果"})
	got, _ := b.Get(it.ID)
	if got.LastStatus != StatusRunning || got.LastSummary != "" {
		t.Fatalf("stale observation applied: %+v", got)
	}
}

// TestNoOverlapWhileWaiting: a pending durable run must not be replaced by a
// new firing, so its task id and card stay intact.
func TestNoOverlapWhileWaiting(t *testing.T) {
	b, _ := New("", nil, nil)
	it, _ := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
	b.mu.Lock()
	x := b.items[it.ID]
	x.RunID, x.TaskID, x.LastStatus = "run-wait", "task-wait", StatusWaiting
	b.items[it.ID] = x
	b.mu.Unlock()
	submitted := 0
	b.SetDurableRunner(&DurableRunner{
		Submit: func(context.Context, Automation, string) (string, string, error) {
			submitted++
			return "task-new", "", nil
		},
	})
	b.startLocked(mustGet(t, b, it.ID), true)
	b.wg.Wait()
	if submitted != 0 {
		t.Fatal("a waiting run was replaced by a new submission")
	}
	got, _ := b.Get(it.ID)
	if got.TaskID != "task-wait" || got.RunID != "run-wait" || got.LastStatus != StatusWaiting {
		t.Fatalf("waiting run was overwritten: %+v", got)
	}
}

// TestNoOverlapWhileRunningOrRecovering covers the two windows that are not
// "waiting with a task id": an observation turned the run back to running with
// no runner goroutine, and a RunID exists whose TaskID write never landed.
func TestNoOverlapWhileRunningOrRecovering(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		taskID string
	}{
		{"running after observation", StatusRunning, "task-run"},
		{"recovering without task id", StatusWaiting, ""},
		{"running without task id", StatusRunning, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := New("", nil, nil)
			it, _ := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
			b.mu.Lock()
			x := b.items[it.ID]
			x.RunID, x.TaskID, x.LastStatus = "run-cur", tc.taskID, tc.status
			b.items[it.ID] = x
			b.mu.Unlock()
			submitted := 0
			b.SetDurableRunner(&DurableRunner{
				Submit: func(context.Context, Automation, string) (string, string, error) {
					submitted++
					return "task-new", "", nil
				},
			})
			b.startLocked(mustGet(t, b, it.ID), true)
			b.wg.Wait()
			if submitted != 0 {
				t.Fatal("an unresolved run was replaced by a new firing")
			}
			got, _ := b.Get(it.ID)
			if got.RunID != "run-cur" || got.TaskID != tc.taskID || got.LastStatus != tc.status {
				t.Fatalf("unresolved run was overwritten: %+v", got)
			}
		})
	}
}

// TestDurableRunWithoutObserverIsNotSuccess: without an observer the outcome is
// unknown, so the run must not be recorded as a success.
func TestDurableRunWithoutObserverIsNotSuccess(t *testing.T) {
	b, _ := New("", nil, nil)
	it, _ := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
	b.SetDurableRunner(&DurableRunner{
		Submit: func(context.Context, Automation, string) (string, string, error) {
			return "task-x", "sess", nil
		},
	})
	started := b.startLocked(mustGet(t, b, it.ID), false)
	b.wg.Wait()
	got, _ := b.Get(started.ID)
	if got.LastStatus == StatusSuccess {
		t.Fatalf("unobserved run recorded as success: %+v", got)
	}
	if got.LastStatus != StatusWaiting {
		t.Fatalf("status %q", got.LastStatus)
	}
}

// TestDurableFailedRunIsNotSuccess: a task that ends failed, cancelled or out of
// budget must reach the scheduler as a failure, not a success.
func TestDurableFailedRunIsNotSuccess(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   string
	}{
		{"failed", StatusFailed},
		{"cancelled", StatusFailed},
		{"budget_exhausted", StatusFailed},
		{"waiting_approval", StatusWaiting},
		{"paused", StatusWaiting},
		{"completed", StatusSuccess},
	} {
		t.Run(tc.status, func(t *testing.T) {
			b, _ := New("", nil, nil)
			it, _ := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
			obs := Observation{Status: tc.status}
			switch tc.status {
			case "completed":
				obs.Done, obs.Text = true, "完成"
			case "failed", "cancelled", "budget_exhausted":
				obs.Done, obs.Text = true, "失败原因"
			case "waiting_approval", "paused":
				obs.Waiting = true
			}
			b.SetDurableRunner(&DurableRunner{
				Submit: func(context.Context, Automation, string) (string, string, error) {
					return "task-1", "sess", nil
				},
				Wait: func(context.Context, Automation, string) (Observation, error) {
					return obs, nil
				},
			})
			started := b.startLocked(mustGet(t, b, it.ID), false)
			b.wg.Wait()
			got, _ := b.Get(started.ID)
			if got.LastStatus != tc.want {
				t.Fatalf("%s recorded as %q, want %q (%+v)", tc.status, got.LastStatus, tc.want, got)
			}
			if tc.want == StatusFailed && got.LastSummary != "" {
				t.Fatalf("%s recorded a success summary %q", tc.status, got.LastSummary)
			}
		})
	}
}

// TestStaleRunCannotOverwriteNewerFiring: a late finish from an older firing
// must not replace the newer run's status or task id.
func TestStaleRunCannotOverwriteNewerFiring(t *testing.T) {
	b, _ := New("", nil, nil)
	it, _ := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
	b.mu.Lock()
	x := b.items[it.ID]
	x.RunID, x.TaskID, x.LastStatus = "run-new", "task-new", StatusWaiting
	b.items[it.ID] = x
	b.mu.Unlock()
	old := mustGet(t, b, it.ID)
	old.RunID = "run-old"
	b.finish(old, "old-sess", "旧结果", nil)
	got, _ := b.Get(it.ID)
	if got.LastStatus != StatusWaiting || got.TaskID != "task-new" || got.LastSummary != "" {
		t.Fatalf("stale finish overwrote the newer firing: %+v", got)
	}
}

// TestRecoveredRunGetsRecordedOutcome: startup recovery of a RunID without a
// TaskID must also record the observed outcome, so the automation does not sit
// in waiting forever.
func TestRecoveredRunGetsRecordedOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "automations.enc")
	box := testBox(t)
	b, err := New(path, box, nil)
	if err != nil {
		t.Fatal(err)
	}
	it, _ := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
	b.mu.Lock()
	x := b.items[it.ID]
	x.RunID, x.TaskID, x.LastStatus = "run-rec", "", StatusRunning
	b.items[it.ID] = x
	if err := b.saveLocked(); err != nil {
		b.mu.Unlock()
		t.Fatal(err)
	}
	b.mu.Unlock()
	reloaded, err := New(path, box, nil)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.SetDurableRunner(&DurableRunner{
		Submit: func(context.Context, Automation, string) (string, string, error) {
			return "task-rec", "sess", nil
		},
		Wait: func(context.Context, Automation, string) (Observation, error) {
			return Observation{Status: "completed", Done: true, Text: "恢复后完成"}, nil
		},
	})
	reloaded.reconcile()
	got, _ := reloaded.Get(it.ID)
	if got.LastStatus != StatusSuccess {
		t.Fatalf("recovered run left at %q (%+v)", got.LastStatus, got)
	}
	if got.LastSummary != "恢复后完成" {
		t.Fatalf("summary %q", got.LastSummary)
	}
	if got.TaskID != "task-rec" {
		t.Fatalf("task id %q", got.TaskID)
	}
}

// breakSaves makes every later save fail (the destination becomes a directory,
// so the atomic rename cannot replace it) while the file is still readable.
func breakSaves(t *testing.T, b *Book, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.path = path
	b.mu.Unlock()
}

// TestAcceptedTaskWithUnpersistedIDStaysWaiting: once the runtime has accepted
// a task, a failure to persist its id must not free the automation for a new
// firing. The run keeps its identity, stays unresolved, and a retry adopts the
// same task instead of starting another one.
func TestAcceptedTaskWithUnpersistedIDStaysWaiting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "automations.enc")
	b, err := New(path, testBox(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	it, err := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	x := b.items[it.ID]
	x.RunID, x.TaskID, x.LastStatus = "run-accepted", "", StatusRunning
	b.items[it.ID] = x
	b.mu.Unlock()
	submits := 0
	b.SetDurableRunner(&DurableRunner{
		Submit: func(context.Context, Automation, string) (string, string, error) {
			submits++
			return "task-accepted", "sess", nil
		},
		Wait: func(context.Context, Automation, string) (Observation, error) {
			return Observation{Status: "completed", Done: true, Text: "完成"}, nil
		},
	})
	breakSaves(t, b, path)

	run := mustGet(t, b, it.ID)
	sid, text, runErr := b.executeDurable(context.Background(), run, mustDurable(t, b))
	if !errors.Is(runErr, errWaiting) {
		t.Fatalf("accepted-but-unpersisted run returned %v, want waiting", runErr)
	}
	b.finish(run, sid, text, runErr)
	got, _ := b.Get(it.ID)
	if got.LastStatus != StatusWaiting {
		t.Fatalf("status %q, want waiting so the task is not orphaned", got.LastStatus)
	}
	if got.RunID != "run-accepted" {
		t.Fatalf("run identity %q", got.RunID)
	}
	// No overlap: a manual run must report busy rather than silently no-op.
	if _, err := b.RunNow(it.ID); !errors.Is(err, ErrRunning) {
		t.Fatalf("RunNow on an unresolved run returned %v, want ErrRunning", err)
	}
	if submits != 1 {
		t.Fatalf("submits %d", submits)
	}

	// Recovery re-submits the same identity and adopts the same task.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.reconcile()
	if submits != 2 {
		t.Fatalf("recovery did not retry the same run: submits %d", submits)
	}
	got, _ = b.Get(it.ID)
	if got.TaskID != "task-accepted" || got.RunID != "run-accepted" {
		t.Fatalf("recovery invented a new identity: %+v", got)
	}
}

func mustDurable(t *testing.T, b *Book) *DurableRunner {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.durable == nil {
		t.Fatal("durable runner not set")
	}
	return b.durable
}

// TestUncertainSubmitKeepsIdentity: an uncertain submission error (network,
// store, timeout) may mean the task was accepted, so the run must stay waiting
// on the same identity rather than fail and allow a duplicate firing.
func TestUncertainSubmitKeepsIdentity(t *testing.T) {
	b, _ := New("", nil, nil)
	it, _ := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
	b.mu.Lock()
	x := b.items[it.ID]
	x.RunID, x.TaskID, x.LastStatus = "run-net", "", StatusRunning
	b.items[it.ID] = x
	b.mu.Unlock()
	attempts := []string{}
	b.SetDurableRunner(&DurableRunner{
		Submit: func(_ context.Context, _ Automation, runID string) (string, string, error) {
			attempts = append(attempts, runID)
			return "", "", errors.New("dial tcp: connection reset")
		},
	})
	run := mustGet(t, b, it.ID)
	sid, text, err := b.executeDurable(context.Background(), run, mustDurable(t, b))
	if !errors.Is(err, errWaiting) {
		t.Fatalf("uncertain submit returned %v, want waiting", err)
	}
	b.finish(run, sid, text, err)
	got, _ := b.Get(it.ID)
	if got.LastStatus != StatusWaiting || got.RunID != "run-net" {
		t.Fatalf("uncertain submit was not kept waiting: %+v", got)
	}
	if _, err := b.RunNow(it.ID); !errors.Is(err, ErrRunning) {
		t.Fatalf("RunNow returned %v, want ErrRunning", err)
	}
	b.reconcile()
	if len(attempts) != 2 || attempts[0] != "run-net" || attempts[1] != "run-net" {
		t.Fatalf("retry identities %v, want the same run twice", attempts)
	}
}

// TestDefiniteRejectionFails: a locally refused run can never succeed, so it is
// a failure and does not block the next firing.
func TestDefiniteRejectionFails(t *testing.T) {
	b, _ := New("", nil, nil)
	it, _ := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1")
	b.SetDurableRunner(&DurableRunner{
		Submit: func(context.Context, Automation, string) (string, string, error) {
			return "", "", rejectedStub{}
		},
	})
	run := mustGet(t, b, it.ID)
	sid, text, err := b.executeDurable(context.Background(), run, mustDurable(t, b))
	b.finish(run, sid, text, err)
	got, _ := b.Get(it.ID)
	if got.LastStatus != StatusFailed {
		t.Fatalf("rejected run status %q, want failed", got.LastStatus)
	}
	if b.mu.Lock(); b.pendingLocked(got) {
		b.mu.Unlock()
		t.Fatal("a rejected run still blocks the next firing")
	} else {
		b.mu.Unlock()
	}
}

type rejectedStub struct{}

func (rejectedStub) Error() string  { return "自动化任务被拒绝：缺少所有者或 Bot" }
func (rejectedStub) Rejected() bool { return true }

func TestDurableRunPersistsTaskIDBeforeWork(t *testing.T) {
	b, _ := New("", nil, nil)
	if _, err := b.Create(Automation{Name: "巡检", Prompt: "检查", BotID: "main", Enabled: true, Schedule: Schedule{Kind: KindInterval, Minutes: 60}}, "u1"); err != nil {
		t.Fatal(err)
	}
	var seenTaskID string
	b.SetDurableRunner(&DurableRunner{
		Submit: func(_ context.Context, it Automation, runID string) (string, string, error) {
			if runID == "" {
				t.Error("run identity missing")
			}
			// The scheduler must have persisted the run id by now.
			if cur, err := b.Get(it.ID); err != nil || cur.RunID != runID {
				t.Errorf("run id not persisted before submit: %+v", cur)
			}
			return "task-durable", "auto-sess", nil
		},
		Wait: func(_ context.Context, it Automation, taskID string) (Observation, error) {
			seenTaskID = taskID
			// The task id is persisted before wait is entered.
			if cur, err := b.Get(it.ID); err != nil || cur.TaskID != taskID {
				t.Errorf("task id not persisted before wait: %+v", cur)
			}
			return Observation{Status: "waiting_approval", Waiting: true}, nil
		},
	})
	list := b.List()
	b.startLocked(list[0], false)
	b.wg.Wait()
	got, _ := b.Get(list[0].ID)
	if got.TaskID != "task-durable" || got.SessionID != "auto-sess" {
		t.Fatalf("task linkage missing: %+v", got)
	}
	if got.LastStatus != StatusWaiting {
		t.Fatalf("waiting run recorded as %q", got.LastStatus)
	}
	if got.LastSummary != "" {
		t.Fatalf("waiting run recorded a summary: %q", got.LastSummary)
	}
	if seenTaskID != "task-durable" {
		t.Fatalf("wait saw task %q", seenTaskID)
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
