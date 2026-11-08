package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"onefrog/internal/task"
)

// blockingModel holds every model request open until the test releases it, and
// records whether the server observed the request context being cancelled —
// the real end-to-end proof that operator cancellation reaches the in-flight
// model HTTP call, not just the runner's step hook.
type blockingModel struct {
	mu        sync.Mutex
	started   int
	cancelled int
	release   chan struct{}
	once      sync.Once
}

func (b *blockingModel) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Drain the body BEFORE anything else: net/http only arms the
		// background read that detects a client disconnect reliably once the
		// request body has been consumed. Without this the server never
		// observes the cancellation and httptest.Close hangs on the open
		// handler.
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		b.mu.Lock()
		b.started++
		b.mu.Unlock()
		select {
		case <-r.Context().Done():
			b.mu.Lock()
			b.cancelled++
			b.mu.Unlock()
			return
		case <-b.release:
		}
		w.Header().Set("Content-Type", "application/json")
		raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "结束"}}}})
		_, _ = io.WriteString(w, string(raw))
	}
}

func (b *blockingModel) counts() (started, cancelled int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started, b.cancelled
}

// releaseAll unblocks every held request. It is idempotent so the test body
// and the cleanup hook can both call it without a double-close panic — and so
// a failing test never leaves httptest.Server.Close waiting on a blocked
// handler.
func (b *blockingModel) releaseAll() {
	b.once.Do(func() { close(b.release) })
}

type lifecycleFixture struct {
	agent  *Agent
	book   *task.Book
	runner *Runner
	model  *blockingModel
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	m := &blockingModel{release: make(chan struct{})}
	srv := httptest.NewServer(m.handler())
	t.Cleanup(srv.Close)
	t.Cleanup(m.releaseAll)
	mem := store.NewMemory()
	box, err := secret.New(bytesRepeat())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i, b := range []model.Bot{{ID: "main", Name: "主控"}, {ID: "check", Name: "巡检"}} {
		b.BaseURL, b.ModelName, b.CreatedAt = srv.URL+"/v1", "demo", time.Now().UTC().Add(time.Duration(i)*time.Second)
		if _, err := mem.CreateBot(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := preferences.New("", box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settings.Save(preferences.Input{
		Approval:  preferences.Approval{Mode: preferences.ModeOff},
		Execution: preferences.Execution{TimeoutSeconds: 30, CommandTimeoutSeconds: 30, MaxSteps: 8, ContextMessages: 24},
	}); err != nil {
		t.Fatal(err)
	}
	book, err := task.New(filepath.Join(t.TempDir(), "tasks.enc"), box)
	if err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Store: mem, Box: box, Settings: settings, SettingsLive: settings, SSH: &sshCounter{}, HTTP: srv.Client(), Tasks: book}
	return &lifecycleFixture{agent: agent, book: book, runner: NewRunner(agent, book), model: m}
}

func (f *lifecycleFixture) submit(t *testing.T, botID, session, goal string) task.Task {
	t.Helper()
	sess, project, opt, err := f.agent.PrepareTask(context.Background(), "u1", botID, session, "test", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.agent.AddTaskGoal(context.Background(), sess.ID, goal); err != nil {
		t.Fatal(err)
	}
	messages, err := f.agent.sessionMessages(context.Background(), sess.ID,
		f.agent.leaderPrompt(context.Background(), model.Bot{ID: sess.BotID, Name: botID}, "web"), botID)
	if err != nil {
		t.Fatal(err)
	}
	it, err := f.runner.AcceptTask(context.Background(), CreateRequest{
		OwnerID: "u1", BotID: sess.BotID, SessionID: sess.ID, ProjectID: project,
		ServerID: "web", Content: goal, Messages: messages,
		AppendGoal: func(ctx context.Context, sessionID string) error {
			return f.agent.AddTaskGoal(ctx, sessionID, goal)
		},
	}, opt)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func waitFor(t *testing.T, d time.Duration, desc string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("超时：%s", desc)
}

// modelActivityRunning reports whether the task card trail shows an in-flight
// model call. This is what the UI animates, so pinning it pins the card
// regression surface, not just the durable status.
func modelActivityRunning(f *lifecycleFixture, taskID string) bool {
	for _, ev := range f.runner.tracker(taskID).store.list(taskID) {
		if ev.Name == "model" && ev.Status == ActivityRunning {
			return true
		}
	}
	return false
}

// modelActivityInterrupted reports whether the trail shows the model call
// closed as interrupted.
func modelActivityInterrupted(f *lifecycleFixture, taskID string) bool {
	for _, ev := range f.runner.tracker(taskID).store.list(taskID) {
		if ev.Name == "model" && ev.Status == ActivityInterrupted {
			return true
		}
	}
	return false
}

func (f *lifecycleFixture) status(id string) task.Task {
	it, err := f.book.Get(id)
	if err != nil {
		return task.Task{}
	}
	return it
}

// TestExecuteGateSkipsDeadContextAndNonRunningStatus pins the execute gate: a
// window must never step the model when its context is already dead or the
// persisted task is no longer running.
func TestExecuteGateSkipsDeadContextAndNonRunningStatus(t *testing.T) {
	book, err := task.New("", nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(&Agent{}, book)
	var steps int
	runner.stepHook = func(ctx context.Context, a *Agent, it task.Task) (taskOutcome, error) {
		steps++
		return taskOutcome{}, nil
	}
	if _, err := book.Create(task.Task{ID: "t-paused", OwnerID: "u1", BotID: "b", SessionID: "s", Goal: "g", Status: task.StatusPaused}); err != nil {
		t.Fatal(err)
	}
	runner.execute(context.Background(), "t-paused")
	if steps != 0 {
		t.Fatalf("execute stepped a paused task")
	}
	if _, err := book.Create(task.Task{ID: "t-ctx", OwnerID: "u1", BotID: "b", SessionID: "s2", Goal: "g", Status: task.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner.execute(ctx, "t-ctx")
	if steps != 0 {
		t.Fatalf("execute stepped a task whose context was already cancelled")
	}
	if _, err := book.Create(task.Task{ID: "t-run", OwnerID: "u1", BotID: "b", SessionID: "s3", Goal: "g", Status: task.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	runner.execute(context.Background(), "t-run")
	if steps != 1 {
		t.Fatalf("execute refused a legitimately running task: steps=%d", steps)
	}
}

// TestCancelReachesInFlightModelHTTPAndLeavesOtherSessionRunning proves the
// full cancellation path: two tasks in different sessions run simultaneously;
// cancelling one cancels its real in-flight model HTTP request (observed by the
// httptest server) and turns the task cancelled, while the other session keeps
// running to completion untouched.
func TestCancelReachesInFlightModelHTTPAndLeavesOtherSessionRunning(t *testing.T) {
	f := newLifecycleFixture(t)
	stop := runBackground(t, f.runner)
	defer stop()

	a := f.submit(t, "main", "sess-a", "检查磁盘")
	b := f.submit(t, "check", "sess-b", "检查负载")

	// Both windows must reach the model concurrently: two different sessions
	// dispatch in parallel.
	waitFor(t, 10*time.Second, "两个会话的任务都应同时进入模型调用", func() bool {
		started, _ := f.model.counts()
		return started >= 2
	})
	// The card trail must show both model calls as running while the HTTP
	// requests are blocked — this is what the UI animates.
	waitFor(t, 5*time.Second, "A 的模型活动应显示 running", func() bool {
		return modelActivityRunning(f, a.ID)
	})
	waitFor(t, 5*time.Second, "B 的模型活动应显示 running", func() bool {
		return modelActivityRunning(f, b.ID)
	})

	if _, err := f.runner.Cancel(context.Background(), "u1", a.ID); err != nil {
		t.Fatal(err)
	}
	// The server must observe the real HTTP request being cancelled — this is
	// the regression proof that the cancel handle was registered before the
	// task was claimed and reaches the in-flight model call.
	waitFor(t, 5*time.Second, "取消后模型 HTTP 请求应被取消", func() bool {
		_, cancelled := f.model.counts()
		return cancelled >= 1
	})
	waitFor(t, 5*time.Second, "任务 A 应变为 cancelled", func() bool {
		return f.status(a.ID).Status == task.StatusCancelled
	})
	// The interrupted trail entry is what the card shows after cancellation.
	waitFor(t, 5*time.Second, "A 的模型活动应关闭为 interrupted", func() bool {
		return modelActivityInterrupted(f, a.ID)
	})

	// B is untouched: its request was never cancelled, and it completes once
	// the model answers.
	f.model.releaseAll()
	waitFor(t, 10*time.Second, "任务 B 应完成", func() bool {
		return f.status(b.ID).Status == task.StatusCompleted
	})
	started, cancelled := f.model.counts()
	if cancelled != 1 {
		t.Fatalf("只有 A 的请求应被取消，实际取消 %d 次", cancelled)
	}
	if started != 2 {
		t.Fatalf("模型应恰好被调用 2 次，实际 %d 次", started)
	}
}

// TestPauseReachesInFlightModelHTTP proves the pause path end to end: while a
// real model HTTP request is in flight, Pause must interrupt it — the httptest
// server observes the request context cancellation — and the task becomes
// paused. Not just a step-hook status change: the network call itself stops.
func TestPauseReachesInFlightModelHTTP(t *testing.T) {
	f := newLifecycleFixture(t)
	stop := runBackground(t, f.runner)
	defer stop()

	it := f.submit(t, "main", "sess-pause", "检查磁盘")

	// The model request must actually be in flight before pausing: this
	// asserts the window was live, so the pause below interrupts real work.
	waitFor(t, 10*time.Second, "任务应先进入模型调用", func() bool {
		started, _ := f.model.counts()
		return started >= 1
	})
	waitFor(t, 5*time.Second, "暂停前任务应为 running", func() bool {
		return f.status(it.ID).Status == task.StatusRunning
	})
	waitFor(t, 5*time.Second, "暂停前模型活动应显示 running", func() bool {
		return modelActivityRunning(f, it.ID)
	})

	if _, err := f.runner.Pause(context.Background(), "u1", it.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "暂停后模型 HTTP 请求应被取消", func() bool {
		_, cancelled := f.model.counts()
		return cancelled >= 1
	})
	waitFor(t, 5*time.Second, "任务应变为 paused", func() bool {
		return f.status(it.ID).Status == task.StatusPaused
	})
	waitFor(t, 5*time.Second, "暂停后模型活动应关闭为 interrupted", func() bool {
		return modelActivityInterrupted(f, it.ID)
	})

	started, cancelled := f.model.counts()
	if started < 1 || cancelled != 1 {
		t.Fatalf("模型调用 %d 次、取消 %d 次，应恰为 ≥1/1", started, cancelled)
	}
}

// TestSameSessionTasksAreSerialized proves same-session serialization: a
// second task in the same session stays queued while the first runs, and only
// runs after the first window released the session slot — so the stale-window
// unregister in dispatch can never delete a newer window's cancel handle.
func TestSameSessionTasksAreSerialized(t *testing.T) {
	f := newLifecycleFixture(t)
	stop := runBackground(t, f.runner)
	defer stop()

	first := f.submit(t, "main", "sess-serial", "第一个任务")
	second := f.submit(t, "main", "sess-serial", "第二个任务")

	waitFor(t, 10*time.Second, "第一个任务应进入模型调用", func() bool {
		started, _ := f.model.counts()
		return started >= 1
	})

	// Repeated dispatch passes must not start the second task while the
	// session slot is held by the first.
	for i := 0; i < 5; i++ {
		f.runner.dispatch()
	}
	if got := f.status(second.ID).Status; got != task.StatusQueued {
		t.Fatalf("同会话第二个任务应保持排队，实际 %s", got)
	}

	if _, err := f.runner.Cancel(context.Background(), "u1", first.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "第一个任务应变为 cancelled", func() bool {
		return f.status(first.ID).Status == task.StatusCancelled
	})
	f.model.releaseAll()
	waitFor(t, 10*time.Second, "第二个任务应在槽位释放后运行并完成", func() bool {
		return f.status(second.ID).Status == task.StatusCompleted
	})
	if started, _ := f.model.counts(); started != 2 {
		t.Fatalf("模型应恰好被调用 2 次（每个窗口一次），实际 %d 次", started)
	}
}
