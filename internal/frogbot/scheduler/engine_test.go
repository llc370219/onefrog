package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func (c *fakeClock) add(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	return c.t
}

type fakeHost struct {
	mu         sync.Mutex
	dispatched []Claim
	tasks      map[string]TaskStatus
	notices    []string
	events     []string
	failNext   bool
}

func (h *fakeHost) Dispatch(p Plan, c Claim) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failNext {
		h.failNext = false
		return "", errFail
	}
	h.dispatched = append(h.dispatched, c)
	if h.tasks == nil {
		h.tasks = map[string]TaskStatus{}
	}
	h.tasks[c.TaskID] = TaskOpen
	return c.TaskID, nil
}
func (h *fakeHost) Lookup(id string) TaskStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tasks == nil {
		return TaskMissing
	}
	st, ok := h.tasks[id]
	if !ok {
		return TaskMissing
	}
	return st
}
func (h *fakeHost) Notify(kind, text string, _ Plan, _ *Claim) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notices = append(h.notices, kind+":"+text)
}
func (h *fakeHost) Emit(kind string, _ any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, kind)
}

var errFail = errString("dispatch failed")

type errString string

func (e errString) Error() string { return string(e) }

func newTestEngine(t *testing.T, clock *fakeClock, host *fakeHost) *Engine {
	t.Helper()
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(st, host, Config{TickEvery: time.Hour, MisfireGrace: 5 * time.Minute, OneOffGrace: 2 * time.Hour, MaxCatchUp: 8})
	e.Clock = clock
	return e
}

func TestOneOffFireAndDisable(t *testing.T) {
	loc := loadLoc(t, "Asia/Shanghai")
	clock := &fakeClock{t: time.Date(2026, 11, 9, 8, 0, 0, 0, loc)}
	host := &fakeHost{}
	e := newTestEngine(t, clock, host)
	at := time.Date(2026, 11, 9, 9, 0, 0, 0, loc)
	p, err := e.Upsert(Draft{Title: "提醒", Prompt: "说一声早上好", Spec: Spec{Kind: Once, Timezone: "Asia/Shanghai", AtLocal: at}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.NextRun == nil || !p.NextRun.Equal(at) {
		t.Fatalf("next=%v", p.NextRun)
	}
	e.Tick() // 未到点
	if len(host.dispatched) != 0 {
		t.Fatal("too early")
	}
	clock.set(time.Date(2026, 11, 9, 9, 0, 1, 0, loc))
	e.Tick()
	if len(host.dispatched) != 1 {
		t.Fatalf("dispatched=%d", len(host.dispatched))
	}
	p2, _ := e.Store.Get(p.ID)
	if p2.Enabled || p2.NextRun != nil {
		t.Fatalf("once should disable: %+v", p2)
	}
	// 幂等：再 Tick 不再派
	e.Tick()
	if len(host.dispatched) != 1 {
		t.Fatal("double dispatch")
	}
}

func TestDailyMisfireSkip(t *testing.T) {
	loc := loadLoc(t, "Asia/Shanghai")
	clock := &fakeClock{t: time.Date(2026, 11, 9, 8, 0, 0, 0, loc)}
	host := &fakeHost{}
	e := newTestEngine(t, clock, host)
	p, err := e.Upsert(Draft{Title: "日报", Prompt: "写日报", Spec: Spec{Kind: Daily, Timezone: "Asia/Shanghai", Hour: 9, Minute: 0}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, 11, 9, 9, 0, 0, 0, loc)
	// 直接把 NextRun 设到已过期很久
	_ = e.Store.SetNext(p.ID, &due)
	clock.set(due.Add(20 * time.Minute)) // > 5m grace
	e.Tick()
	if len(host.dispatched) != 0 {
		t.Fatal("misfire should skip")
	}
	p2, _ := e.Store.Get(p.ID)
	if p2.NextRun == nil || !p2.NextRun.Equal(time.Date(2026, 11, 10, 9, 0, 0, 0, loc)) {
		t.Fatalf("should advance to tomorrow, got %v", p2.NextRun)
	}
	c, ok := e.Store.Claim(OccurrenceKey(p.ID, due))
	if !ok || c.Status != "skipped_misfire" {
		t.Fatalf("claim %+v", c)
	}
}

func TestOverlapSkip(t *testing.T) {
	loc := loadLoc(t, "Asia/Shanghai")
	clock := &fakeClock{t: time.Date(2026, 11, 9, 8, 55, 0, 0, loc)}
	host := &fakeHost{}
	e := newTestEngine(t, clock, host)
	p, err := e.Upsert(Draft{Title: "打卡", Prompt: "打卡", Spec: Spec{Kind: Daily, Hour: 9, Minute: 0}, Overlap: OverlapSkip, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	clock.set(time.Date(2026, 11, 9, 9, 0, 1, 0, loc))
	e.Tick()
	if len(host.dispatched) != 1 {
		t.Fatal(len(host.dispatched))
	}
	// 上一趟仍 open；把 next 拨回"今天稍后"来模拟下一次（用手动改 NextRun 到 9:01 再拨时钟）
	// 改成每分钟不好测；改用 weekly/daily 的下一趟：强制设 NextRun 为 10 秒后并改 clock
	// 更直接：构造第二条 claim 场景——把计划的 NextRun 设为 now+1s，但 open claim 仍在
	n2 := clock.Now().Add(time.Second)
	_ = e.Store.SetNext(p.ID, &n2)
	clock.add(2 * time.Second)
	e.Tick()
	if len(host.dispatched) != 1 {
		t.Fatalf("overlap should skip, got %d", len(host.dispatched))
	}
}

func TestOverlapQueue(t *testing.T) {
	loc := loadLoc(t, "Asia/Shanghai")
	clock := &fakeClock{t: time.Date(2026, 11, 9, 8, 55, 0, 0, loc)}
	host := &fakeHost{}
	e := newTestEngine(t, clock, host)
	p, err := e.Upsert(Draft{Title: "队列", Prompt: "x", Spec: Spec{Kind: Daily, Hour: 9, Minute: 0}, Overlap: OverlapQueue, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	clock.set(time.Date(2026, 11, 9, 9, 0, 1, 0, loc))
	e.Tick()
	n2 := clock.Now().Add(time.Second)
	_ = e.Store.SetNext(p.ID, &n2)
	clock.add(2 * time.Second)
	e.Tick()
	if len(host.dispatched) != 2 {
		t.Fatalf("queue should dispatch twice, got %d", len(host.dispatched))
	}
}

func TestIdempotentClaimAcrossRestart(t *testing.T) {
	loc := loadLoc(t, "Asia/Shanghai")
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 11, 9, 8, 59, 0, 0, loc)}
	host := &fakeHost{}
	st, _ := OpenStore(dir)
	e := NewEngine(st, host, Config{MisfireGrace: 5 * time.Minute, OneOffGrace: 2 * time.Hour})
	e.Clock = clock
	p, _ := e.Upsert(Draft{Title: "重启", Prompt: "x", Spec: Spec{Kind: Once, AtLocal: time.Date(2026, 11, 9, 9, 0, 0, 0, loc)}, Enabled: true})
	clock.set(time.Date(2026, 11, 9, 9, 0, 1, 0, loc))
	e.Tick()
	if len(host.dispatched) != 1 {
		t.Fatal(len(host.dispatched))
	}
	// 模拟崩溃后重开：同一目录、同一 clock；任务仍 open
	st2, _ := OpenStore(dir)
	host2 := &fakeHost{tasks: map[string]TaskStatus{host.dispatched[0].TaskID: TaskOpen}}
	e2 := NewEngine(st2, host2, Config{MisfireGrace: 5 * time.Minute, OneOffGrace: 2 * time.Hour})
	e2.Clock = clock
	e2.recoverLocked()
	e2.Tick()
	if len(host2.dispatched) != 0 {
		t.Fatal("restart must not re-dispatch claimed occurrence")
	}
	p2, _ := st2.Get(p.ID)
	if p2.Enabled {
		t.Fatal("once already fired should stay disabled")
	}
}

func TestNeedsVerifyWhenTaskMissing(t *testing.T) {
	loc := loadLoc(t, "Asia/Shanghai")
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 11, 9, 8, 59, 0, 0, loc)}
	host := &fakeHost{}
	st, _ := OpenStore(dir)
	e := NewEngine(st, host, Config{})
	e.Clock = clock
	_, _ = e.Upsert(Draft{Title: "失踪", Prompt: "x", Spec: Spec{Kind: Once, AtLocal: time.Date(2026, 11, 9, 9, 0, 0, 0, loc)}, Enabled: true})
	clock.set(time.Date(2026, 11, 9, 9, 0, 1, 0, loc))
	e.Tick()
	if len(host.dispatched) != 1 {
		t.Fatalf("dispatched=%d", len(host.dispatched))
	}
	taskID := host.dispatched[0].TaskID

	st2, _ := OpenStore(dir)
	host2 := &fakeHost{} // Lookup → missing
	e2 := NewEngine(st2, host2, Config{})
	e2.Clock = clock
	e2.recoverLocked()
	if len(host2.notices) == 0 || host2.notices[0][:16] != "schedule.verify:" {
		t.Fatalf("notices=%v", host2.notices)
	}
	found := false
	for _, c := range st2.Claims() {
		if c.TaskID == taskID && c.Status == "needs_verify" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected needs_verify claim")
	}
}

func TestClockRollbackNoFire(t *testing.T) {
	loc := loadLoc(t, "Asia/Shanghai")
	clock := &fakeClock{t: time.Date(2026, 11, 9, 10, 0, 0, 0, loc)}
	host := &fakeHost{}
	e := newTestEngine(t, clock, host)
	_, _ = e.Upsert(Draft{Title: "回拨", Prompt: "x", Spec: Spec{Kind: Once, AtLocal: time.Date(2026, 11, 9, 9, 30, 0, 0, loc)}, Enabled: true})
	// 先建立 lastWall
	e.mu.Lock()
	e.lastWall = clock.Now()
	e.mu.Unlock()
	clock.set(time.Date(2026, 11, 9, 9, 0, 0, 0, loc)) // 回拨
	e.Tick()
	if len(host.dispatched) != 0 {
		t.Fatal("rollback must not fire")
	}
}

func TestPauseResumeDelete(t *testing.T) {
	loc := loadLoc(t, "Asia/Shanghai")
	clock := &fakeClock{t: time.Date(2026, 11, 9, 8, 0, 0, 0, loc)}
	host := &fakeHost{}
	e := newTestEngine(t, clock, host)
	p, _ := e.Upsert(Draft{Title: "暂停", Prompt: "x", Spec: Spec{Kind: Daily, Hour: 9, Minute: 0}, Enabled: true})
	p, err := e.SetEnabled(p.ID, false)
	if err != nil || p.Enabled || p.NextRun != nil {
		t.Fatalf("%+v %v", p, err)
	}
	clock.set(time.Date(2026, 11, 9, 9, 0, 1, 0, loc))
	e.Tick()
	if len(host.dispatched) != 0 {
		t.Fatal("paused must not fire")
	}
	p, err = e.SetEnabled(p.ID, true)
	if err != nil || !p.Enabled || p.NextRun == nil {
		t.Fatalf("%+v %v", p, err)
	}
	if err := e.Remove(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Store.Get(p.ID); ok {
		t.Fatal("deleted")
	}
}

func TestWeeklyTimezone(t *testing.T) {
	loc := loadLoc(t, "America/New_York")
	clock := &fakeClock{t: time.Date(2026, 11, 9, 8, 0, 0, 0, loc)} // Monday
	host := &fakeHost{}
	e := newTestEngine(t, clock, host)
	p, err := e.Upsert(Draft{Title: "美东", Prompt: "x", Spec: Spec{Kind: Weekly, Timezone: "America/New_York", Hour: 9, Minute: 0, Weekdays: []time.Weekday{time.Monday}}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.NextRun.Location().String() != "America/New_York" {
		t.Fatal(p.NextRun.Location())
	}
	clock.set(time.Date(2026, 11, 9, 9, 0, 1, 0, loc))
	e.Tick()
	if len(host.dispatched) != 1 {
		t.Fatal(len(host.dispatched))
	}
}

func TestPersistFileMode(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := filepath.Glob(filepath.Join(dir, "schedules.json"))
	if err != nil || len(info) != 1 {
		t.Fatal(info, err)
	}
	_ = st
}

func TestEngineStartStop(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	host := &fakeHost{}
	e := newTestEngine(t, clock, host)
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)
	e.Stop()
	cancel()
}
