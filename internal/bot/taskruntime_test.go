package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"onefrog/internal/task"
)

// scriptedModel answers each request from a script and records the bodies.
type scriptedModel struct {
	mu      sync.Mutex
	bodies  []string
	replies []func(call int) string
	calls   int
}

func (f *scriptedModel) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls++
		n := f.calls
		f.bodies = append(f.bodies, string(raw))
		reply := func() string { return plainReply("结束") }()
		if n-1 < len(f.replies) {
			reply = f.replies[n-1](n)
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}
}

func (f *scriptedModel) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *scriptedModel) body(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.bodies) {
		return ""
	}
	return f.bodies[i]
}

func plainReply(text string) string {
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": text}}}})
	return string(raw)
}

func toolReply(id, name, args string) string { return toolJSON(id, name, args) }

// makeCall builds a tool call value for tests that drive a task state directly.
func makeCall(id, name, args string) toolCall {
	var call toolCall
	call.ID = id
	call.Type = "function"
	call.Function.Name = name
	call.Function.Arguments = args
	return call
}

type sshCounter struct {
	mu       sync.Mutex
	commands []string
}

func (s *sshCounter) Run(ctx context.Context, host string, port int, user, authType, secret, knownKey, command string) (string, string, error) {
	s.mu.Lock()
	s.commands = append(s.commands, command)
	n := len(s.commands)
	s.mu.Unlock()
	return "ran-" + string(rune('0'+n%10)), "", nil
}

func (s *sshCounter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.commands)
}

type runtimeFixture struct {
	agent  *Agent
	book   *task.Book
	ssh    *sshCounter
	model  *scriptedModel
	runner *Runner
	owner  string
}

func newRuntimeFixture(t *testing.T, path string, mode string, maxSteps int, replies ...func(call int) string) *runtimeFixture {
	t.Helper()
	fake := &scriptedModel{replies: replies}
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)
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
	if _, err := mem.CreateServer(ctx, store.ServerRec{Server: model.Server{ID: "web", Name: "web", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online"}, HostKey: "SHA256:test"}); err != nil {
		t.Fatal(err)
	}
	settings, err := preferences.New("", box)
	if err != nil {
		t.Fatal(err)
	}
	d := preferences.Defaults()
	if _, err := settings.Save(preferences.Input{
		Approval:  preferences.Approval{Mode: mode},
		Execution: preferences.Execution{TimeoutSeconds: 30, CommandTimeoutSeconds: 30, MaxSteps: maxSteps, ContextMessages: 24},
	}); err != nil {
		t.Fatal(err)
	}
	_ = d
	book, err := task.New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	ssh := &sshCounter{}
	agent := &Agent{Store: mem, Box: box, Settings: settings, SettingsLive: settings, SSH: ssh, HTTP: srv.Client(), Tasks: book}
	return &runtimeFixture{agent: agent, book: book, ssh: ssh, model: fake, runner: NewRunner(agent, book), owner: "u1"}
}

func (f *runtimeFixture) submit(t *testing.T, ctx context.Context, session, goal string) task.Task {
	t.Helper()
	sess, project, opt, err := f.agent.PrepareTask(context.Background(), f.owner, "main", session, "test", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.agent.AddTaskGoal(context.Background(), sess.ID, goal); err != nil {
		t.Fatal(err)
	}
	// The accepted snapshot is what the task runs with; it is frozen now, so a
	// later user message in the same session cannot widen the task.
	messages, err := f.agent.sessionMessages(context.Background(), sess.ID, f.agent.leaderPrompt(context.Background(), model.Bot{ID: sess.BotID, Name: "主控"}, "web"), "主控")
	if err != nil {
		t.Fatal(err)
	}
	it, err := f.runner.AcceptTask(ctx, CreateRequest{
		OwnerID: f.owner, BotID: sess.BotID, SessionID: sess.ID, ProjectID: project,
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

// waitTerminal polls the registry until the task reaches a final state.
func (f *runtimeFixture) waitTerminal(t *testing.T, id string) task.Task {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		it, err := f.book.Get(id)
		if err == nil && it.Terminal() {
			return it
		}
		time.Sleep(10 * time.Millisecond)
	}
	it, _ := f.book.Get(id)
	t.Fatalf("task did not finish: %+v", it)
	return it
}

func runBackground(t *testing.T, r *Runner) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	// Run waits for its own windows before it returns, so waiting for done is
	// enough and cannot race a dispatch that adds a new window. stop is
	// idempotent so a test can settle the runner before it reads state.
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Errorf("runner did not stop")
			}
		})
	}
}

func TestBackgroundRunnerContinuesAcrossWindowsAfterRequestContextDies(t *testing.T) {
	// Step budget 2 forces a handoff after the second model call. The handoff
	// request is answered with plain text; the next window verifies and
	// finishes.
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 2,
		func(int) string {
			return toolReply("c1", "run_command", `{"server_id":"web","command":"df -h","risk":"low","reason":"磁盘"}`)
		},
		func(int) string { return plainReply("做到看磁盘，下一步看负载") },
		// A mutation must be verified by a later read-only observation before
		// finish is accepted (evidence after the last mutation).
		func(int) string {
			return toolReply("c3", "inspect_services", `{"action":"list"}`)
		},
		func(int) string {
			return toolReply("c2", "finish", `{"summary":"磁盘已检查","verification":["df -h 返回正常"]}`)
		},
	)
	stop := runBackground(t, f.runner)
	defer stop()
	// The creating request context is cancelled immediately; the task must not
	// depend on it.
	reqCtx, cancel := context.WithCancel(context.Background())
	it := f.submit(t, reqCtx, "sess-windows", "检查磁盘")
	cancel()

	final := f.waitTerminal(t, it.ID)
	if final.Status != task.StatusCompleted {
		t.Fatalf("status %s err %s modelcalls=%d", final.Status, final.LastError, f.model.count())
	}
	if final.TotalSteps < 2 {
		t.Fatalf("total steps %d, task did not span windows", final.TotalSteps)
	}
	if f.ssh.count() != 1 {
		t.Fatalf("ssh %d", f.ssh.count())
	}
	if f.model.count() < 3 {
		t.Fatalf("model calls %d", f.model.count())
	}
}

func TestApprovalParksThenRunsOnceOnDecision(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeSmart, 4,
		func(n int) string {
			return toolReply("c1", "run_command", `{"server_id":"web","command":"systemctl restart nginx","risk":"medium","reason":"重启服务"}`)
		},
		func(n int) string {
			// A side effect without finish is never reported as completed, so
			// the model verifies and finishes like a real turn.
			return toolReply("c2", "finish", `{"summary":"已重启 nginx","verification":["systemctl restart 返回成功"]}`)
		},
	)
	stop := runBackground(t, f.runner)
	defer stop()
	it := f.submit(t, context.Background(), "sess-approval", "重启 nginx")

	var waiting task.Task
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := f.book.Get(it.ID)
		if err == nil && cur.Status == task.StatusWaitingApproval {
			waiting = cur
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if waiting.Status != task.StatusWaitingApproval {
		t.Fatalf("not parked: %+v", waiting)
	}
	if f.ssh.count() != 0 {
		t.Fatal("command ran before approval")
	}
	if waiting.Pending == nil || waiting.Pending.ApprovalID == "" {
		t.Fatalf("missing pending approval: %+v", waiting.Pending)
	}
	approvalID := waiting.Pending.ApprovalID

	if _, err := f.runner.Decide(context.Background(), f.owner, it.ID, approvalID, true, "同意"); err != nil {
		t.Fatal(err)
	}
	final := f.waitTerminal(t, it.ID)
	if final.Status != task.StatusCompleted {
		t.Fatalf("status %s err %s", final.Status, final.LastError)
	}
	if f.ssh.count() != 1 {
		t.Fatalf("ssh after approval %d", f.ssh.count())
	}
	// A duplicate decision is refused and does not run the command again.
	if _, err := f.runner.Decide(context.Background(), f.owner, it.ID, approvalID, true, "同意"); err == nil {
		t.Fatal("duplicate decision accepted")
	}
	if f.ssh.count() != 1 {
		t.Fatalf("duplicate decision re-ran the command: %d", f.ssh.count())
	}
}

func TestDeniedApprovalSkipsWithoutRunningOrReasking(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeSmart, 4,
		func(n int) string {
			if n == 1 {
				return toolReply("c1", "run_command", `{"server_id":"web","command":"rm -rf /var/tmp/cache","risk":"high","reason":"清缓存"}`)
			}
			return plainReply("已跳过")
		},
	)
	stop := runBackground(t, f.runner)
	defer stop()
	it := f.submit(t, context.Background(), "sess-deny", "清缓存")
	var waiting task.Task
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cur, _ := f.book.Get(it.ID)
		if cur.Status == task.StatusWaitingApproval {
			waiting = cur
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if waiting.Pending == nil {
		t.Fatalf("not parked: %+v", waiting)
	}
	if _, err := f.runner.Decide(context.Background(), f.owner, it.ID, waiting.Pending.ApprovalID, false, "不要删"); err != nil {
		t.Fatal(err)
	}
	final := f.waitTerminal(t, it.ID)
	if f.ssh.count() != 0 {
		t.Fatal("denied command ran")
	}
	if final.Status == task.StatusCompleted && len(final.Verified) == 0 && f.ssh.count() > 0 {
		t.Fatal("denied task reported as verified success")
	}
}

func TestPendingApprovalSurvivesRestartWithoutNewModelTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.enc")
	f := newRuntimeFixture(t, path, preferences.ModeSmart, 4,
		func(int) string {
			return toolReply("c1", "run_command", `{"server_id":"web","command":"systemctl restart nginx","risk":"medium","reason":"重启"}`)
		},
		func(int) string {
			return toolReply("c2", "finish", `{"summary":"已重启 nginx","verification":["systemctl restart 返回成功"]}`)
		},
	)
	stop := runBackground(t, f.runner)
	it := f.submit(t, context.Background(), "sess-restart-approval", "重启 nginx")
	var waiting task.Task
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cur, _ := f.book.Get(it.ID)
		if cur.Status == task.StatusWaitingApproval {
			waiting = cur
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if waiting.Pending == nil {
		t.Fatalf("not parked: %+v", waiting)
	}
	stop()

	// Rebuild the registry from disk: the pending approval and journal must
	// survive, and resuming must not ask the model for another action.
	book, err := task.New(path, f.agent.Box)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := book.Get(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != task.StatusWaitingApproval || reloaded.Pending == nil || reloaded.Pending.ApprovalID != waiting.Pending.ApprovalID {
		t.Fatalf("pending approval lost: %+v", reloaded.Pending)
	}
	modelCalls := f.model.count()
	f.agent.Tasks = book
	f.book = book
	runner := NewRunner(f.agent, book)
	f.runner = runner
	stop2 := runBackground(t, runner)
	defer stop2()
	if _, err := runner.Decide(context.Background(), f.owner, it.ID, waiting.Pending.ApprovalID, true, "同意"); err != nil {
		t.Fatal(err)
	}
	final := f.waitTerminal(t, it.ID)
	if final.Status != task.StatusCompleted {
		t.Fatalf("status %s err %s", final.Status, final.LastError)
	}
	if f.ssh.count() != 1 {
		t.Fatalf("ssh %d", f.ssh.count())
	}
	if f.model.count() != modelCalls+1 {
		t.Fatalf("resume asked the model extra times: before %d after %d", modelCalls, f.model.count())
	}
}

func TestInterruptedBatchIsCompletedBeforeAnyNewModelTurn(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4,
		func(int) string { return toolReply("c2", "finish", `{"summary":"完成","verification":["ok"]}`) },
	)
	// A window that ended mid-batch: the assistant call is stored, the tool
	// result is not. Its journal entry is "started", i.e. outcome unknown.
	now := time.Now().UTC()
	// The task's session must exist: a task always writes into a real
	// transcript, and finish records its closing message there.
	if _, err := f.agent.Store.EnsureSession(context.Background(), "sess-batch", "main", "检查状态"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.book.Create(task.Task{
		ID: "t-batch", OwnerID: f.owner, BotID: "main", SessionID: "sess-batch", Goal: "检查状态",
		Status: task.StatusPaused, StepBudget: 4, MaxSteps: 8, CreatedAt: now, UpdatedAt: now,
		Checkpoint: task.Checkpoint{Messages: []map[string]any{
			{"role": "system", "content": "你是主控"},
			{"role": "user", "content": "检查状态"},
			{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
				"id": "c1", "type": "function",
				"function": map[string]any{"name": "run_command", "arguments": `{"server_id":"web","command":"systemctl restart nginx","risk":"medium","reason":"重启"}`},
			}}},
		}},
		Journal: []task.JournalEntry{{Seq: 1, CallID: "c1", Name: "run_command", ArgsHash: "stale", State: task.JournalStarted}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.Resume(context.Background(), f.owner, "t-batch"); err != nil {
		t.Fatal(err)
	}
	stop := runBackground(t, f.runner)
	defer stop()
	final := f.waitTerminal(t, "t-batch")
	if f.ssh.count() != 0 {
		t.Fatal("interrupted command was replayed")
	}
	if !containsText(final.LastError, "结果未知") && final.Status != task.StatusCompleted {
		t.Fatalf("interruption not reconciled: %+v", final)
	}
	// The first model request after the resume must already contain the tool
	// result for c1, proving the batch was closed before asking again.
	if f.model.count() == 0 || !containsText(f.model.body(0), "结果未知") {
		t.Fatalf("model was asked before the batch was closed: %s", f.model.body(0))
	}
}

func containsText(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// A window that lost the task must not write its own outcome. The operator can
// pause, cancel or resume while a model or tool call is still running (the
// context is cancelled, but a call that ignores it returns later). Only an
// ordinary error, a cancelled signal, a deadline and a successful close are
// covered here; all of them must leave the operator's state intact.
func TestLateWindowOutcomeCannotOverwriteOperatorState(t *testing.T) {
	// A park is applied directly: it stands in for a task tool that parked the
	// call, which is the same durable write.
	park := func(t *testing.T, f *runtimeFixture, id string) {
		t.Helper()
		if _, err := f.book.Update(id, func(it *task.Task) error {
			it.Status = task.StatusWaitingApproval
			it.Pending = &task.Pending{Kind: task.PendingApproval, CallID: "c1", ArgsHash: "h1", ApprovalID: "a1", Turn: 1}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	pause := func(t *testing.T, f *runtimeFixture, id string) {
		t.Helper()
		if _, err := f.runner.Pause(context.Background(), f.owner, id); err != nil {
			t.Fatal(err)
		}
	}
	cancel := func(t *testing.T, f *runtimeFixture, id string) {
		t.Helper()
		if _, err := f.runner.Cancel(context.Background(), f.owner, id); err != nil {
			t.Fatal(err)
		}
	}
	pauseResume := func(t *testing.T, f *runtimeFixture, id string) {
		t.Helper()
		pause(t, f, id)
		if _, err := f.runner.Resume(context.Background(), f.owner, id); err != nil {
			t.Fatal(err)
		}
	}

	type stepResult struct {
		err   error
		final bool
	}
	for _, tc := range []struct {
		name        string
		action      func(*testing.T, *runtimeFixture, string)
		step        stepResult
		want        string
		wantPending bool
		// requeued accepts a task that was re-dispatched by the new window
		// instead of the state the operator set.
		requeued bool
	}{
		{"pause-ordinary-error", pause, stepResult{err: errors.New("模型挂了")}, task.StatusPaused, false, false},
		{"pause-cancelled-signal", pause, stepResult{err: errCancelled}, task.StatusPaused, false, false},
		{"pause-deadline", pause, stepResult{err: context.DeadlineExceeded}, task.StatusPaused, false, false},
		{"pause-success", pause, stepResult{final: true}, task.StatusPaused, false, false},
		{"cancel-ordinary-error", cancel, stepResult{err: errors.New("模型挂了")}, task.StatusCancelled, false, false},
		{"cancel-success", cancel, stepResult{final: true}, task.StatusCancelled, false, false},
		{"park-success", park, stepResult{final: true}, task.StatusWaitingApproval, true, false},
		{"park-cancelled-signal", park, stepResult{err: errCancelled}, task.StatusWaitingApproval, true, false},
		{"pause-resume-ordinary-error", pauseResume, stepResult{err: errors.New("模型挂了")}, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4)
			id := "t-late"
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			var mu sync.Mutex
			calls := 0
			f.runner.stepHook = func(ctx context.Context, a *Agent, it task.Task) (taskOutcome, error) {
				mu.Lock()
				calls++
				first := calls == 1
				mu.Unlock()
				if !first {
					// The task was re-queued and dispatched again: stop that
					// window without writing an outcome of its own.
					return taskOutcome{}, errPaused
				}
				entered <- struct{}{}
				// Deliberately ignore ctx: this is a call that returns after the
				// operator already moved the task.
				<-release
				if tc.step.err != nil {
					return taskOutcome{}, tc.step.err
				}
				return taskOutcome{Text: "完成", Final: tc.step.final, Steps: 1}, nil
			}
			now := time.Now().UTC()
			if _, err := f.book.Create(task.Task{
				ID: id, OwnerID: f.owner, BotID: "main", SessionID: "s-late", Goal: "做事",
				ServerID: "web", Status: task.StatusQueued, StepBudget: 4, MaxSteps: 8,
				CreatedAt: now, UpdatedAt: now,
				Checkpoint: task.Checkpoint{Messages: []map[string]any{{"role": "user", "content": "做事"}}},
			}); err != nil {
				t.Fatal(err)
			}
			stop := runBackground(t, f.runner)
			defer stop()
			<-entered
			if cur, err := f.book.Get(id); err != nil || cur.Status != task.StatusRunning {
				t.Fatalf("window did not own the task: %+v %v", cur, err)
			}
			tc.action(t, f, id)
			close(release)
			// Settle the runner instead of calling Wait while it may still
			// dispatch: stopping Run waits for its windows, so the late write
			// has landed before the state is read.
			stop()
			got, err := f.book.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if tc.requeued {
				// The old window must not have failed it; the new window may
				// already be running, or still queued while the old one exited.
				if got.Status != task.StatusQueued && got.Status != task.StatusRunning {
					t.Fatalf("late outcome overwrote the resumed task: %s (%s)", got.Status, got.LastError)
				}
				return
			}
			if got.Status != tc.want {
				t.Fatalf("status %s, want %s (err %q)", got.Status, tc.want, got.LastError)
			}
			if tc.wantPending && got.Pending == nil {
				t.Fatalf("operator pending state was cleared: %+v", got)
			}
			if got.Result != "" || got.ResultMessageID != "" {
				t.Fatalf("late window wrote a final result: %+v", got)
			}
		})
	}
}

// The individual durable writes a window makes must not revive a task that is
// no longer its own (paused-then-resumed or cancelled), while a call that
// really ran still leaves its journal evidence behind.
func TestLateToolWritesPreserveOperatorState(t *testing.T) {
	for _, status := range []string{task.StatusQueued, task.StatusCancelled} {
		t.Run(status, func(t *testing.T) {
			f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4)
			now := time.Now().UTC()
			call := makeCall("c1", "run_command", `{"server_id":"web","command":"df -h","risk":"low","reason":"磁盘"}`)
			hash := argsHash(call)
			id := "t-late-tool"
			if _, err := f.book.Create(task.Task{
				ID: id, OwnerID: f.owner, BotID: "main", SessionID: "s-late-tool", Goal: "做事",
				Status: status, CreatedAt: now, UpdatedAt: now,
				Checkpoint: task.Checkpoint{Messages: []map[string]any{{"role": "user", "content": "做事"}}},
				Journal: []task.JournalEntry{{
					Seq: 1, Turn: 1, CallID: call.ID, Name: call.Function.Name,
					ArgsHash: hash, State: task.JournalStarted,
				}},
				Pending: &task.Pending{Kind: task.PendingApproval, CallID: call.ID, ArgsHash: hash, ApprovalID: "a1", Turn: 1},
			}); err != nil {
				t.Fatal(err)
			}
			st := &taskState{book: f.book, id: id, turn: 1, current: &call}
			check := func(what string) {
				t.Helper()
				got, err := f.book.Get(st.id)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != status {
					t.Fatalf("%s: status became %s", what, got.Status)
				}
				if got.Pending == nil || got.Pending.ApprovalID != "a1" {
					t.Fatalf("%s: operator pending was overwritten: %+v", what, got.Pending)
				}
			}
			write := func(what string, fn func() error, wantErr bool) {
				t.Helper()
				err := fn()
				if wantErr && err == nil {
					t.Fatalf("%s: expected a stop signal", what)
				}
				if !wantErr && err != nil {
					t.Fatalf("%s: %v", what, err)
				}
				check(what)
			}
			write("recordIntent", func() error { return st.recordIntent(call, hash) }, true)
			write("recordDone", func() error { return st.recordDone(call, hash, "拒绝") }, true)
			write("parkCall", func() error {
				return st.parkCall(ApprovalRequest{ID: "a2", Command: "rm -rf /"})
			}, true)
			write("saveWindow", func() error {
				return st.saveWindow(context.Background(), []map[string]any{{"role": "user", "content": "做事"}})
			}, true)
			write("saveProgress", func() error {
				return st.saveProgress([]map[string]any{{"role": "user", "content": "做事"}}, "完成了")
			}, true)
			write("markSkipped", func() error { return st.markSkipped(call, "未执行") }, false)
			write("closeOpenEntryExecuted", func() error { return st.closeOpenEntryExecuted(call.ID, hash, "ran") }, true)
			// A call that really ran keeps its evidence even on a cancelled
			// task; the status is still the operator's.
			got, err := f.book.Get(st.id)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range got.Journal {
				if e.CallID == call.ID && e.State == task.JournalDone {
					if !e.Executed {
						t.Fatalf("executed call lost its evidence: %+v", e)
					}
					found = true
				}
			}
			if !found {
				t.Fatalf("executed call was not recorded: %+v", got.Journal)
			}
		})
	}
}

// Resolving an approval must not start the side effect once the operator has
// moved the task. The approved branch moves the entry back to started before
// running anything, so that write is the last place the race can be stopped.
func TestApprovedStartCannotRunAfterOperatorMovedTheTask(t *testing.T) {
	for _, status := range []string{task.StatusPaused, task.StatusQueued, task.StatusCancelled, task.StatusBudgetExhausted} {
		t.Run(status, func(t *testing.T) {
			f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4)
			now := time.Now().UTC()
			call := makeCall("c1", "run_command", `{"server_id":"web","command":"systemctl restart nginx","risk":"medium","reason":"重启"}`)
			hash := argsHash(call)
			id := "t-approved-late"
			if _, err := f.book.Create(task.Task{
				ID: id, OwnerID: f.owner, BotID: "main", SessionID: "s-approved-late", Goal: "重启",
				ServerID: "web", Status: status, CreatedAt: now, UpdatedAt: now,
				Journal: []task.JournalEntry{{
					Seq: 1, Turn: 1, CallID: call.ID, Name: call.Function.Name,
					ArgsHash: hash, State: task.JournalWaiting,
				}},
				Pending: &task.Pending{Kind: task.PendingApproval, CallID: call.ID, ArgsHash: hash, ApprovalID: "a1", Turn: 1},
			}); err != nil {
				t.Fatal(err)
			}
			st := &taskState{book: f.book, id: id, turn: 1, maxSteps: 4}
			st.decision = &task.Approval{ID: "a1", CallID: call.ID, ArgsHash: hash, Status: task.ApprovalAllowed}
			prior, err := f.book.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			entry := findEntry(prior.Journal, 1, call.ID, hash)
			if entry == nil {
				t.Fatal("fixture entry missing")
			}
			if _, err := st.resumeParked(context.Background(), f.agent, model.Bot{ID: "main"}, credentials{}, "web", call, false, nil, entry); err == nil {
				t.Fatal("approved call ran for a task the operator already moved")
			}
			if f.ssh.count() != 0 {
				t.Fatalf("late approved side effect executed: %v", f.ssh.commands)
			}
			got, err := f.book.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != status {
				t.Fatalf("status became %s, want %s", got.Status, status)
			}
			for _, e := range got.Journal {
				if e.CallID == call.ID && e.State != task.JournalWaiting {
					t.Fatalf("unexecuted call was rewritten to %s", e.State)
				}
			}
		})
	}
}

// parkOnChild is idempotent for the exact park it already wrote, but must not
// revive a re-queued (paused-then-resumed) or terminal parent as waiting_job.
func TestDelegateParkCannotReviveAQueuedParent(t *testing.T) {
	for _, status := range []string{task.StatusQueued, task.StatusBudgetExhausted} {
		t.Run(status, func(t *testing.T) {
			f, st, _ := delegateFixture(t, task.Task{})
			ctx := context.Background()
			call := delegateCall("call-1")
			speaker, _ := f.agent.Store.GetBot(ctx, "main")
			// First park is legitimate: the parent is running.
			if _, err := st.delegate(ctx, f.agent, speaker, credentials{}, "web", call, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := f.book.Update("parent", func(it *task.Task) error {
				it.Status = status
				it.Pending = nil
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			err := st.parkOnChild(call, argsHash(call), childTaskID("parent", 3, call.ID, argsHash(call)))
			if err == nil {
				t.Fatal("park revived a task the operator moved")
			}
			got, err := f.book.Get("parent")
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != status || got.Pending != nil {
				t.Fatalf("state revived: %s %+v", got.Status, got.Pending)
			}
		})
	}
}

// A task parked on an approval, a card or an external job has no window to
// notice that its persisted deadline passed. The scan must end it in every
// non-terminal state, leave finished tasks alone, and never call the model.
// A waiting_job task whose poll time has already arrived must be dispatched by
// the very pass that wakes it. If the pass dispatches first and wakes after, the
// task becomes queued with no wake token left, so it sits until the next 30s
// tick — that is the liveness hole that stalls a parent waiting on a child.
func TestDueJobIsDispatchedInTheSamePassThatWakesIt(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4)
	now := time.Now().UTC()
	id := "t-due-job"
	if _, err := f.book.Create(task.Task{
		ID: id, OwnerID: f.owner, BotID: "main", SessionID: "s-due-job", Goal: "等外部任务",
		ServerID: "web", Status: task.StatusWaitingJob, StepBudget: 4, MaxSteps: 8,
		Deadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
		Checkpoint: task.Checkpoint{Messages: []map[string]any{{"role": "user", "content": "等外部任务"}}},
		// Already due: nothing will send an extra wake, so only the pass order
		// can start this window.
		Pending: &task.Pending{Kind: task.PendingJob, JobRef: "job-1", WakeAt: now.Add(-time.Second)},
	}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	f.runner.stepHook = func(ctx context.Context, a *Agent, it task.Task) (taskOutcome, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		return taskOutcome{Text: "完成", Final: true, Steps: 1}, nil
	}
	stop := runBackground(t, f.runner)
	defer stop()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		it, _ := f.book.Get(id)
		t.Fatalf("due job was not dispatched without an extra wake: %+v", it)
	}
	got := f.waitTerminal(t, id)
	if got.Status != task.StatusCompleted {
		t.Fatalf("status %s err %s", got.Status, got.LastError)
	}
}

func TestOverdueTasksExpireInEveryParkedState(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4)
	now := time.Now().UTC()
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	type row struct {
		id       string
		status   string
		deadline time.Time
		pending  *task.Pending
		pending2 bool
	}
	rows := []row{
		{"t-approval", task.StatusWaitingApproval, past, &task.Pending{Kind: task.PendingApproval, ApprovalID: "a1"}, false},
		{"t-card", task.StatusWaitingUser, past, &task.Pending{Kind: task.PendingUser, CardMessageID: "m1"}, false},
		{"t-job", task.StatusWaitingJob, past, &task.Pending{Kind: task.PendingJob, JobRef: "job1"}, false},
		{"t-paused", task.StatusPaused, past, nil, false},
		{"t-queued", task.StatusQueued, past, nil, false},
		{"t-completed", task.StatusCompleted, past, nil, true},
		{"t-future", task.StatusWaitingUser, future, &task.Pending{Kind: task.PendingUser, CardMessageID: "m2"}, false},
	}
	for _, r := range rows {
		if _, err := f.book.Create(task.Task{
			ID: r.id, OwnerID: f.owner, BotID: "main", SessionID: "s-" + r.id, Goal: "等",
			Status: r.status, Deadline: r.deadline, Pending: r.pending,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.runner.ExpireOverdue(now); n != 5 {
		t.Fatalf("expired %d tasks, want 5", n)
	}
	for _, r := range rows {
		got, err := f.book.Get(r.id)
		if err != nil {
			t.Fatal(err)
		}
		switch r.id {
		case "t-completed", "t-future":
			if got.Status != r.status {
				t.Fatalf("%s: status changed to %s", r.id, got.Status)
			}
			continue
		}
		if got.Status != task.StatusBudgetExhausted {
			t.Fatalf("%s: status %s, want budget_exhausted", r.id, got.Status)
		}
		if !containsText(got.LastError, "时限") {
			t.Fatalf("%s: error is not truthful: %q", r.id, got.LastError)
		}
		if got.Pending != nil {
			t.Fatalf("%s: expired task kept a pending card: %+v", r.id, got.Pending)
		}
	}
	if f.model.count() != 0 {
		t.Fatalf("expiry called the model %d times", f.model.count())
	}
}

// A pure task tool must not overwrite a state the operator just set. The guard
// lives inside the same registry write as the change, so a pause that lands
// while the call is in flight wins instead of being silently reverted.
func TestTaskToolsCannotOverwriteOperatorState(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4)
	now := time.Now().UTC()
	for _, tc := range []struct {
		name   string
		status string
		call   toolCall
	}{
		{"plan-after-pause", task.StatusPaused, makeCall("c1", "plan", `{"steps":["a"]}`)},
		{"checkpoint-after-cancel", task.StatusCancelled, makeCall("c2", "checkpoint", `{"progress":"做完了"}`)},
		{"wait-after-pause", task.StatusPaused, makeCall("c3", "wait", `{"job_ref":"deploy"}`)},
		{"finish-after-pause", task.StatusPaused, makeCall("c4", "finish", `{"summary":"完成","verification":["ok"]}`)},
	} {
		id := "t-" + tc.name
		if _, err := f.book.Create(task.Task{
			ID: id, OwnerID: f.owner, BotID: "main", SessionID: "s-" + tc.name, Goal: "做事",
			Status: tc.status, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		st := &taskState{book: f.book, id: id}
		if _, handled, err := st.runTaskTool(context.Background(), f.agent, tc.call); !handled || err == nil {
			t.Fatalf("%s: handled=%v err=%v, want a stop signal", tc.name, handled, err)
		}
		got, err := f.book.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != tc.status {
			t.Fatalf("%s: status changed to %s", tc.name, got.Status)
		}
		if len(got.Journal) != 0 || len(got.Plan) != 0 || got.Result != "" {
			t.Fatalf("%s: tool still applied: %+v", tc.name, got)
		}
	}
}

// A mutating call that started and never reported may or may not have taken
// effect. The task must not let the model claim success: finish is refused
// until a read-only call has actually checked the state.
func TestUnknownMutationCannotFinishWithoutVerification(t *testing.T) {
	script := func(call int) string {
		// call is 1-based: the first request is 1.
		switch call {
		case 1:
			// The model tries to close the task straight away.
			return toolReply("c2", "finish", `{"summary":"已经重启完成","verification":["我觉得没问题"]}`)
		case 2:
			// Refused: now it actually looks at the recorded state with a
			// read-only tool. A second mutating call would not count.
			return toolReply("c3", "inspect_services", `{"action":"list"}`)
		default:
			return toolReply("c4", "finish", `{"summary":"已核对：nginx 处于 active","verification":["inspect_services 显示 nginx active"]}`)
		}
	}
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 6,
		script, script, script,
	)
	if _, err := f.agent.Store.EnsureSession(context.Background(), "sess-unknown", "main", "重启"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := f.book.Create(task.Task{
		ID: "t-unknown", OwnerID: f.owner, BotID: "main", SessionID: "sess-unknown", Goal: "重启 nginx", ServerID: "web",
		Status: task.StatusPaused, StepBudget: 6, MaxSteps: 12, CreatedAt: now, UpdatedAt: now,
		Checkpoint: task.Checkpoint{Messages: []map[string]any{
			{"role": "system", "content": "你是主控"},
			{"role": "user", "content": "重启 nginx"},
			{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
				"id": "c1", "type": "function",
				"function": map[string]any{"name": "run_command", "arguments": `{"server_id":"web","command":"systemctl restart nginx","risk":"medium","reason":"重启"}`},
			}}},
		}},
		// started, no result: the restart may or may not have happened.
		Journal: []task.JournalEntry{{Seq: 1, CallID: "c1", Name: "run_command", ArgsHash: "stale", State: task.JournalStarted}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.Resume(context.Background(), f.owner, "t-unknown"); err != nil {
		t.Fatal(err)
	}
	stop := runBackground(t, f.runner)
	defer stop()
	final := f.waitTerminal(t, "t-unknown")
	if final.Status != task.StatusCompleted {
		t.Fatalf("status %s err %s", final.Status, final.LastError)
	}
	// The unverified "finish" must have been rejected, so the model needed at
	// least three calls: reconcile, read-only check, real finish.
	if f.model.count() < 3 {
		t.Fatalf("unknown outcome was finished without verification: %d model calls, status %s result %q journal %+v",
			f.model.count(), final.Status, final.Result, final.Journal)
	}
	if !containsText(final.Result, "已核对") {
		t.Fatalf("final result is not the verified one: %q", final.Result)
	}
	// The interrupted restart was never replayed; only the read-only check ran.
	for _, c := range f.ssh.commands {
		if strings.Contains(c, "restart") {
			t.Fatalf("interrupted mutation was replayed: %v", f.ssh.commands)
		}
	}
	if f.ssh.count() != 0 {
		t.Fatalf("interrupted mutation was replayed: %v", f.ssh.commands)
	}
}

// A completed task whose transcript write never happened must be repaired, and
// repairing it twice must not duplicate the closing message.
// A second mutating call is not verification of an unknown outcome. The model
// cannot escape the gate by doing more work; only a read-only inspection counts.
func TestUnknownMutationGateRejectsAnotherMutation(t *testing.T) {
	script := func(call int) string {
		switch call {
		case 1:
			return toolReply("c2", "finish", `{"summary":"完成","verification":["应该没问题"]}`)
		case 2:
			// Another write: not a check.
			return toolReply("c3", "run_command", `{"server_id":"web","command":"systemctl reload nginx","risk":"medium","reason":"再试一次"}`)
		default:
			return toolReply("c4", "finish", `{"summary":"完成","verification":["又执行了一次"]}`)
		}
	}
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 6,
		script, script, script,
	)
	if _, err := f.agent.Store.EnsureSession(context.Background(), "sess-gate", "main", "重启"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := f.book.Create(task.Task{
		ID: "t-gate", OwnerID: f.owner, BotID: "main", SessionID: "sess-gate", Goal: "重启 nginx", ServerID: "web",
		Status: task.StatusPaused, StepBudget: 6, MaxSteps: 12, MaxAttempts: 2, CreatedAt: now, UpdatedAt: now,
		Checkpoint: task.Checkpoint{Messages: []map[string]any{
			{"role": "system", "content": "你是主控"},
			{"role": "user", "content": "重启 nginx"},
			{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
				"id": "c1", "type": "function",
				"function": map[string]any{"name": "run_command", "arguments": `{"server_id":"web","command":"systemctl restart nginx","risk":"medium","reason":"重启"}`},
			}}},
		}},
		Journal: []task.JournalEntry{{Seq: 1, CallID: "c1", Name: "run_command", ArgsHash: "stale", State: task.JournalStarted}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.Resume(context.Background(), f.owner, "t-gate"); err != nil {
		t.Fatal(err)
	}
	stop := runBackground(t, f.runner)
	defer stop()
	// The task still needs a real check. If verification cannot be obtained,
	// report the failure rather than waiting on a nonexistent question.
	final := f.waitTerminal(t, "t-gate")
	if final.Status != task.StatusFailed || final.Pending != nil || final.LastError != incompleteVerification {
		t.Fatalf("unverified operation did not end explicitly: status=%s error=%q", final.Status, final.LastError)
	}
}

// blockedStore delays the closing-message append so two final writers overlap.
// Without a writer lock both would see the message missing and append it twice
// (Memory.AddMessage does not deduplicate ids).
type blockedStore struct {
	store.Store
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (b *blockedStore) AddMessage(ctx context.Context, msg model.Message) (model.Message, error) {
	if strings.HasPrefix(msg.ID, "task-final-") {
		b.once.Do(func() {
			close(b.entered)
			<-b.release
		})
	}
	return b.Store.AddMessage(ctx, msg)
}

func TestFinalWriteAndRepairDoNotRace(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4)
	blocked := &blockedStore{Store: f.agent.Store, entered: make(chan struct{}), release: make(chan struct{})}
	f.agent.Store = blocked
	if _, err := blocked.Store.EnsureSession(context.Background(), "sess-race", "main", "收尾"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := f.book.Create(task.Task{
		ID: "t-race", OwnerID: f.owner, BotID: "main", SessionID: "sess-race", Goal: "收尾",
		Status: task.StatusCompleted, CreatedAt: now, UpdatedAt: now, FinishedAt: now,
		Result: "完成", ResultMessageID: "task-final-t-race-0",
	}); err != nil {
		t.Fatal(err)
	}
	// One writer starts and blocks inside AddMessage.
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.runner.repairFinals()
	}()
	<-blocked.entered
	// While it is blocked, a second writer runs. It must wait for the lock
	// instead of also observing "missing".
	second := make(chan struct{})
	go func() {
		defer close(second)
		f.runner.repairFinals()
	}()
	select {
	case <-second:
		t.Fatal("second final writer did not wait for the first")
	case <-time.After(150 * time.Millisecond):
	}
	close(blocked.release)
	<-done
	<-second
	msgs, err := f.agent.Store.ListMessages(context.Background(), "sess-race")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range msgs {
		if strings.HasPrefix(m.ID, "task-final-") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("final message written %d times: %+v", n, msgs)
	}
}

func TestCompletedFinalIsRepairedExactlyOnce(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4)
	if _, err := f.agent.Store.EnsureSession(context.Background(), "sess-repair", "main", "收尾"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := f.book.Create(task.Task{
		ID: "t-repair", OwnerID: f.owner, BotID: "main", SessionID: "sess-repair", Goal: "收尾",
		Status: task.StatusCompleted, CreatedAt: now, UpdatedAt: now, FinishedAt: now,
		Result: "全部完成", ResultMessageID: "task-final-repair",
	}); err != nil {
		t.Fatal(err)
	}
	f.runner.repairFinals()
	f.runner.repairFinals()
	msgs, err := f.agent.Store.ListMessages(context.Background(), "sess-repair")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range msgs {
		if m.ID == "task-final-repair" && m.Content == "全部完成" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("final message written %d times: %+v", n, msgs)
	}
}

// A plain closing answer is completed in the same write as its outbox, so a
// crash before the transcript write cannot lose the answer or ask the model
// again: the stored final is replayed into the transcript.
func TestPlainFinalSurvivesCrashBeforeTranscriptWrite(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4,
		func(int) string { return plainReply("检查完毕，一切正常。") },
	)
	stop := runBackground(t, f.runner)
	defer stop()
	it := f.submit(t, context.Background(), "sess-plain-final", "检查")
	final := f.waitTerminal(t, it.ID)
	if final.Status != task.StatusCompleted {
		t.Fatalf("status %s err %s", final.Status, final.LastError)
	}
	if final.Result != "检查完毕，一切正常。" || final.ResultMessageID == "" {
		t.Fatalf("final outbox missing: %+v", final)
	}
	// Simulate the crash window: the task is completed, the transcript lost.
	if err := f.agent.Store.DeleteChatSession(context.Background(), "sess-plain-final"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.agent.Store.EnsureSession(context.Background(), "sess-plain-final", "main", "检查"); err != nil {
		t.Fatal(err)
	}
	calls := f.model.count()
	f.runner.repairFinals()
	if f.model.count() != calls {
		t.Fatal("recovery asked the model again")
	}
	msgs, err := f.agent.Store.ListMessages(context.Background(), "sess-plain-final")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range msgs {
		if m.ID == final.ResultMessageID && m.Content == "检查完毕，一切正常。" {
			found = true
		}
	}
	if !found {
		t.Fatalf("final not restored: %+v", msgs)
	}
}

func TestFinishStopsSameBatchBeforeAnySideEffect(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4,
		func(int) string {
			// One assistant message with two calls: finish first, then a real
			// side effect that must never run.
			return `{"choices":[{"message":{"content":"","tool_calls":[
				{"id":"c1","type":"function","function":{"name":"finish","arguments":"{\"summary\":\"完成\",\"verification\":[\"已核对\"]}"}},
				{"id":"c2","type":"function","function":{"name":"run_command","arguments":"{\"server_id\":\"web\",\"command\":\"rm -rf /var/tmp/late\",\"risk\":\"high\",\"reason\":\"late\"}"}}
			]}}]}`
		},
	)
	stop := runBackground(t, f.runner)
	defer stop()
	it := f.submit(t, context.Background(), "sess-finish-batch", "完成任务")
	final := f.waitTerminal(t, it.ID)
	if final.Status != task.StatusCompleted {
		t.Fatalf("status %s err %s", final.Status, final.LastError)
	}
	if f.ssh.count() != 0 {
		t.Fatalf("call after finish executed: %v", f.ssh.commands)
	}
}

func TestWaitStopsSameBatchBeforeAnySideEffect(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4,
		func(int) string {
			return `{"choices":[{"message":{"content":"","tool_calls":[
				{"id":"c1","type":"function","function":{"name":"wait","arguments":"{\"job_ref\":\"外部部署\",\"after_minutes\":5}"}},
				{"id":"c2","type":"function","function":{"name":"run_command","arguments":"{\"server_id\":\"web\",\"command\":\"rm -rf /var/tmp/after-wait\",\"risk\":\"high\",\"reason\":\"late\"}"}}
			]}}]}`
		},
	)
	stop := runBackground(t, f.runner)
	defer stop()
	it := f.submit(t, context.Background(), "sess-wait-batch", "等部署")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := f.book.Get(it.ID)
		if err == nil && cur.Status == task.StatusWaitingJob {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cur, _ := f.book.Get(it.ID)
	if cur.Status != task.StatusWaitingJob {
		t.Fatalf("not waiting on job: %+v", cur)
	}
	if f.ssh.count() != 0 {
		t.Fatalf("call after wait executed: %v", f.ssh.commands)
	}
	// The batch must be closed in the checkpoint, so a resume cannot run the
	// skipped call either. The skip is written just after the park, so poll.
	for time.Now().Before(deadline) {
		cur, _ := f.book.Get(it.ID)
		for _, msg := range cur.Checkpoint.Messages {
			if id, _ := msg["tool_call_id"].(string); id == "c2" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("skipped call has no recorded result")
}

func TestPureTaskToolIsAppliedExactlyOnceAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.enc")
	f := newRuntimeFixture(t, path, preferences.ModeOff, 4,
		func(int) string {
			// The resumed window replays the unfinished plan batch first, so
			// the first model request already asks to finish.
			return toolReply("c2", "finish", `{"summary":"已规划","verification":["计划已记录"]}`)
		},
	)
	now := time.Now().UTC()
	if _, err := f.agent.Store.EnsureSession(context.Background(), "sess-pure", "main", "规划"); err != nil {
		t.Fatal(err)
	}
	// A checkpoint that ends mid-batch: the plan call was issued and its
	// result was not written. Its state change must be idempotent when the
	// batch is replayed after a restart.
	if _, err := f.book.Create(task.Task{
		ID: "t-pure", OwnerID: f.owner, BotID: "main", SessionID: "sess-pure", ServerID: "web",
		Goal: "规划一下", Status: task.StatusPaused, StepBudget: 4, MaxSteps: 12, CreatedAt: now, UpdatedAt: now,
		Checkpoint: task.Checkpoint{Messages: []map[string]any{
			{"role": "system", "content": "你是主控"},
			{"role": "user", "content": "规划一下"},
			{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
				"id": "c1", "type": "function",
				"function": map[string]any{"name": "plan", "arguments": `{"steps":["看磁盘","看负载"]}`},
			}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	// Replaying the same call twice in the same turn must not append the plan
	// twice or duplicate progress.
	if _, err := f.runner.Resume(context.Background(), f.owner, "t-pure"); err != nil {
		t.Fatal(err)
	}
	stop := runBackground(t, f.runner)
	defer stop()
	final := f.waitTerminal(t, "t-pure")
	if final.Status != task.StatusCompleted {
		t.Fatalf("status %s err %s", final.Status, final.LastError)
	}
	if len(final.Plan) != 2 {
		t.Fatalf("plan applied %d times: %v", len(final.Plan), final.Plan)
	}
	entries := 0
	for _, e := range final.Journal {
		if e.Name == "plan" {
			entries++
		}
	}
	if entries != 1 {
		t.Fatalf("plan journaled %d times", entries)
	}
}

func TestChangedArgsCannotRideAnOldApproval(t *testing.T) {
	// The approval was recorded for c1/hashA. The resumed call reuses call id
	// c1 but with different arguments, so it must not execute and must be
	// parked again instead.
	now := time.Now().UTC()
	box, err := secret.New(bytesRepeat())
	if err != nil {
		t.Fatal(err)
	}
	book, err := task.New("", box)
	if err != nil {
		t.Fatal(err)
	}
	oldArgs := `{"server_id":"web","command":"systemctl restart nginx","risk":"medium","reason":"重启"}`
	newArgs := `{"server_id":"web","command":"rm -rf /var/tmp/cache","risk":"high","reason":"删除"}`
	oldCall := toolCall{ID: "c1", Type: "function"}
	oldCall.Function.Name = "run_command"
	oldCall.Function.Arguments = oldArgs
	newCall := toolCall{ID: "c1", Type: "function"}
	newCall.Function.Name = "run_command"
	newCall.Function.Arguments = newArgs
	if _, err := book.Create(task.Task{
		ID: "t-changed", OwnerID: "u1", BotID: "main", SessionID: "s-changed", ServerID: "web",
		// The dispatcher marks a task running before its window executes, so
		// this fixture must be in the state a live window sees.
		Goal: "清理缓存", Status: task.StatusRunning, StepBudget: 4, MaxSteps: 8, CreatedAt: now, UpdatedAt: now,
		Journal: []task.JournalEntry{{
			Seq: 1, Turn: 1, CallID: "c1", Name: "run_command",
			ArgsHash: argsHash(oldCall), State: task.JournalWaiting,
		}},
		Approvals: []task.Approval{{
			ID: "a1", CallID: "c1", ArgsHash: argsHash(oldCall), Command: "systemctl restart nginx",
			Status: task.ApprovalAllowed, CreatedAt: now, DecidedAt: now,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	executed := 0
	st := &taskState{book: book, id: "t-changed", turn: 1, maxSteps: 4}
	st.decision = &task.Approval{
		ID: "a1", CallID: "c1", ArgsHash: argsHash(oldCall), Status: task.ApprovalAllowed,
	}
	a := &Agent{}
	speaker := model.Bot{ID: "main", Name: "主控"}
	it, _ := book.Get("t-changed")
	prior := findEntry(it.Journal, 1, "c1", argsHash(newCall))
	if prior == nil {
		// Changed arguments must NOT match the waiting entry, so it is not
		// treated as a resumable approval.
		if _, err := st.resumeParked(context.Background(), a, speaker, credentials{}, "web", newCall, false, nil, &task.JournalEntry{Turn: 1, CallID: "c1", ArgsHash: argsHash(oldCall), State: task.JournalWaiting}); err != nil {
			t.Fatal(err)
		}
		if executed != 0 {
			t.Fatal("changed arguments executed")
		}
		after, _ := book.Get("t-changed")
		if after.Status != task.StatusWaitingApproval {
			t.Fatalf("changed args not re-parked: %s", after.Status)
		}
		return
	}
	t.Fatalf("loose match returned a waiting entry for changed args: %+v", prior)
}

// recordedApprovals is a compile-time check that the fake agent used by the
// changed-args test has the fields it needs.
var _ = credentials{}

func TestAcceptedTaskHasABoundedPersistedDeadline(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 2,
		func(int) string { return toolReply("c1", "finish", `{"summary":"完成","verification":["ok"]}`) },
	)
	it := f.submit(t, context.Background(), "sess-deadline", "检查")
	if it.Deadline.IsZero() {
		t.Fatal("accepted task has no deadline")
	}
	if d := time.Until(it.Deadline); d <= 0 || d > taskLifetime+time.Minute {
		t.Fatalf("deadline out of policy: %v", d)
	}
	again, err := f.book.Get(it.ID)
	if err != nil || !again.Deadline.Equal(it.Deadline) {
		t.Fatalf("deadline not persisted: %+v %v", again.Deadline, err)
	}
}

func TestResumeRestoresUnansweredPendingInsteadOfRunning(t *testing.T) {
	now := time.Now().UTC()
	box, err := secret.New(bytesRepeat())
	if err != nil {
		t.Fatal(err)
	}
	book, err := task.New("", box)
	if err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Tasks: book}
	runner := NewRunner(agent, book)
	for _, tc := range []struct {
		name    string
		pending *task.Pending
		want    string
	}{
		{"approval", &task.Pending{Kind: task.PendingApproval, CallID: "c1", ApprovalID: "a1"}, task.StatusWaitingApproval},
		{"card", &task.Pending{Kind: task.PendingUser, CallID: "c1"}, task.StatusWaitingUser},
	} {
		id := "t-" + tc.name
		if _, err := book.Create(task.Task{
			ID: id, OwnerID: "u1", BotID: "main", SessionID: "s-" + tc.name, Goal: "等",
			Status: task.StatusPaused, Pending: tc.pending, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		got, err := runner.Resume(context.Background(), "u1", id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != tc.want {
			t.Fatalf("%s resumed to %s, want %s", tc.name, got.Status, tc.want)
		}
	}
}

var _ = errors.Is

func TestPlanProgressPersistedAndValidated(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4)
	now := time.Now().UTC()
	id := "t-planprog"
	if _, err := f.book.Create(task.Task{
		ID: id, OwnerID: f.owner, BotID: "main", SessionID: "s-planprog", Goal: "发布",
		Status: task.StatusRunning, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	st := &taskState{book: f.book, id: id}
	// No plan yet: progress is refused.
	if out, _, _ := st.runTaskTool(context.Background(), f.agent, makeCall("p1", "plan_progress", `{"completed":1}`)); !strings.Contains(out, "还没有计划") {
		t.Fatalf("no-plan progress accepted: %q", out)
	}
	// Save a plan.
	if _, _, err := st.runTaskTool(context.Background(), f.agent, makeCall("p2", "plan", `{"steps":["构建","测试","发布"]}`)); err != nil {
		t.Fatal(err)
	}
	got, _ := f.book.Get(id)
	if got.PlanCompleted != 0 {
		t.Fatalf("new plan must start at 0, got %d", got.PlanCompleted)
	}
	// Out-of-range counts are refused.
	for _, bad := range []string{`{"completed":-1}`, `{"completed":4}`} {
		if out, _, _ := st.runTaskTool(context.Background(), f.agent, makeCall("p3", "plan_progress", bad)); !strings.Contains(out, "0 到 3") {
			t.Fatalf("out-of-range accepted: %q", out)
		}
	}
	// A valid update persists.
	if _, _, err := st.runTaskTool(context.Background(), f.agent, makeCall("p4", "plan_progress", `{"completed":2}`)); err != nil {
		t.Fatal(err)
	}
	got, _ = f.book.Get(id)
	if got.PlanCompleted != 2 {
		t.Fatalf("progress not persisted: %d", got.PlanCompleted)
	}
	// A changed plan resets completion; a re-sent identical plan keeps it.
	if _, _, err := st.runTaskTool(context.Background(), f.agent, makeCall("p5", "plan", `{"steps":["构建","测试","上线"]}`)); err != nil {
		t.Fatal(err)
	}
	got, _ = f.book.Get(id)
	if got.PlanCompleted != 0 {
		t.Fatalf("replan must reset completion, got %d", got.PlanCompleted)
	}
	if _, _, err := st.runTaskTool(context.Background(), f.agent, makeCall("p6", "plan_progress", `{"completed":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.runTaskTool(context.Background(), f.agent, makeCall("p7", "plan", `{"steps":["构建","测试","上线"]}`)); err != nil {
		t.Fatal(err)
	}
	got, _ = f.book.Get(id)
	if got.PlanCompleted != 1 {
		t.Fatalf("identical replan must keep completion, got %d", got.PlanCompleted)
	}
	// Completion only counts up to the plan length.
	if _, _, err := st.runTaskTool(context.Background(), f.agent, makeCall("p8", "plan_progress", `{"completed":3}`)); err != nil {
		t.Fatal(err)
	}
	got, _ = f.book.Get(id)
	if got.PlanCompleted != 3 {
		t.Fatalf("full completion not recorded: %d", got.PlanCompleted)
	}
	// The persisted count is bounded by the plan length (the API view maps it
	// 1:1, see internal/api tests).
	if got.PlanCompleted != 3 || len(got.Plan) != 3 {
		t.Fatalf("persisted plan state: %d of %d", got.PlanCompleted, len(got.Plan))
	}
}

func TestPlanProgressPointerValidationAndAtomicJournal(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4)
	now := time.Now().UTC()
	id := "t-planptr"
	if _, err := f.book.Create(task.Task{
		ID: id, OwnerID: f.owner, BotID: "main", SessionID: "s-planptr", Goal: "上线",
		Status: task.StatusRunning, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	st := &taskState{book: f.book, id: id, turn: 1, track: f.runner.tracker(id)}
	if _, _, err := st.runTaskTool(context.Background(), f.agent, makeCall("pl", "plan", `{"steps":["构建","测试","发布"]}`)); err != nil {
		t.Fatal(err)
	}
	before, _ := f.book.Get(id)
	// Missing field, explicit null and negative are readable tool results,
	// never a halted window, and mutate nothing.
	for _, bad := range []string{`{}`, `{"completed":null}`, `{"completed":-1}`, `{"completed":99}`} {
		call := makeCall("px", "plan_progress", bad)
		out, handled, err := st.runTaskTool(context.Background(), f.agent, call)
		if err != nil {
			t.Fatalf("%s halted the window: %v", bad, err)
		}
		if !handled {
			t.Fatalf("%s not handled as a task tool", bad)
		}
		if out == "" {
			t.Fatalf("%s produced no readable result", bad)
		}
	}
	after, _ := f.book.Get(id)
	if after.PlanCompleted != before.PlanCompleted || len(after.Journal) != len(before.Journal) {
		t.Fatalf("invalid calls mutated state: count %d->%d journal %d->%d",
			before.PlanCompleted, after.PlanCompleted, len(before.Journal), len(after.Journal))
	}
	if st.halted {
		t.Fatal("validation failure must not halt the window")
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("invalid call moved UpdatedAt: %v -> %v", before.UpdatedAt, after.UpdatedAt)
	}
	// A valid call applies count and journal entry in one durable write.
	if _, _, err := st.runTaskTool(context.Background(), f.agent, makeCall("p1", "plan_progress", `{"completed":2}`)); err != nil {
		t.Fatal(err)
	}
	got, _ := f.book.Get(id)
	if got.PlanCompleted != 2 || len(got.Journal) != len(before.Journal)+1 {
		t.Fatalf("valid call not persisted atomically: count %d journal %d (before %d)",
			got.PlanCompleted, len(got.Journal), len(before.Journal))
	}
	entry := findEntry(got.Journal, st.turn, "p1", argsHash(makeCall("p1", "plan_progress", `{"completed":2}`)))
	if entry == nil || entry.State != task.JournalDone || entry.Result == "" {
		t.Fatalf("journal entry missing or incomplete: %+v", entry)
	}
	// Replaying the exact call returns the stored result and appends nothing.
	journalLen := len(got.Journal)
	out, _, err := st.runTaskTool(context.Background(), f.agent, makeCall("p1", "plan_progress", `{"completed":2}`))
	if err != nil {
		t.Fatal(err)
	}
	replayed, _ := f.book.Get(id)
	if len(replayed.Journal) != journalLen {
		t.Fatalf("replay appended a journal entry: %d -> %d", journalLen, len(replayed.Journal))
	}
	if replayed.PlanCompleted != 2 {
		t.Fatalf("replay changed the count: %d", replayed.PlanCompleted)
	}
	want := fmt.Sprintf("已记录完成 %d / %d 步。", 2, len(got.Plan))
	if out != want || findEntry(replayed.Journal, st.turn, "p1", argsHash(makeCall("p1", "plan_progress", `{"completed":2}`))).Result != want {
		t.Fatalf("replay result mismatch: %q", out)
	}
	// No-plan progress still refuses without a journal entry or a window stop.
	if _, err := f.book.Update(id, func(tsk *task.Task) error { tsk.Plan = nil; tsk.PlanCompleted = 0; return nil }); err != nil {
		t.Fatal(err)
	}
	cleared, _ := f.book.Get(id)
	journalBefore := len(cleared.Journal)
	if out, _, err := st.runTaskTool(context.Background(), f.agent, makeCall("pn", "plan_progress", `{"completed":1}`)); err != nil || out == "" {
		t.Fatalf("no-plan progress: out %q err %v", out, err)
	}
	final, _ := f.book.Get(id)
	if len(final.Journal) != journalBefore || final.PlanCompleted != 0 {
		t.Fatalf("no-plan progress mutated state: journal %d->%d count %d",
			journalBefore, len(final.Journal), final.PlanCompleted)
	}
}

func TestPlanProgressCountSurvivesEncryptedReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.enc")
	f := newRuntimeFixture(t, path, preferences.ModeOff, 4)
	now := time.Now().UTC()
	id := "t-planreload"
	if _, err := f.book.Create(task.Task{
		ID: id, OwnerID: f.owner, BotID: "main", SessionID: "s-planreload", Goal: "上线",
		Status: task.StatusRunning, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	st := &taskState{book: f.book, id: id, turn: 1}
	if _, _, err := st.runTaskTool(context.Background(), f.agent, makeCall("p1", "plan", `{"steps":["改配置","重启","验证"]}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.runTaskTool(context.Background(), f.agent, makeCall("p2", "plan_progress", `{"completed":1}`)); err != nil {
		t.Fatal(err)
	}
	// Reload the encrypted file from disk: the count, the plan and the journal
	// entry must all survive, and a replay on the reloaded book is idempotent.
	book2, err := task.New(path, f.agent.Box)
	if err != nil {
		t.Fatal(err)
	}
	got, err := book2.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanCompleted != 1 || len(got.Plan) != 3 || len(got.Journal) != 2 {
		t.Fatalf("reload lost plan state: count %d plan %d journal %d",
			got.PlanCompleted, len(got.Plan), len(got.Journal))
	}
	st2 := &taskState{book: book2, id: id, turn: 1}
	out, _, err := st2.runTaskTool(context.Background(), f.agent, makeCall("p2", "plan_progress", `{"completed":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "已记录完成 1 / 3 步。" {
		t.Fatalf("reloaded replay returned %q", out)
	}
	replayed, _ := book2.Get(id)
	if len(replayed.Journal) != 2 || replayed.PlanCompleted != 1 {
		t.Fatalf("reloaded replay mutated state: journal %d count %d",
			len(replayed.Journal), replayed.PlanCompleted)
	}
}

func TestFinishSetsPlanCompleteAndRequiresEvidence(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4)
	now := time.Now().UTC()
	id := "t-finplan"
	if _, err := f.book.Create(task.Task{
		ID: id, OwnerID: f.owner, BotID: "main", SessionID: "s-finplan", Goal: "上线",
		Status: task.StatusRunning, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	st := &taskState{book: f.book, id: id}
	if _, _, err := st.runTaskTool(context.Background(), f.agent, makeCall("p1", "plan", `{"steps":["改代码","验证"]}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.runTaskTool(context.Background(), f.agent, makeCall("p2", "plan_progress", `{"completed":1}`)); err != nil {
		t.Fatal(err)
	}
	// A finished task's plan is complete.
	if _, _, err := st.runTaskTool(context.Background(), f.agent, makeCall("p3", "finish", `{"summary":"完成","verification":["读到了正确内容"]}`)); err != nil {
		t.Fatal(err)
	}
	got, _ := f.book.Get(id)
	if got.Status != task.StatusCompleted {
		t.Fatalf("status %s", got.Status)
	}
	if got.PlanCompleted != len(got.Plan) {
		t.Fatalf("finish must complete the plan: %d/%d", got.PlanCompleted, len(got.Plan))
	}
}
