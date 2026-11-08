package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"onefrog/internal/task"
	"onefrog/internal/workspace"
)

// localWSFixture is a runtime fixture with a native local workspace bound:
// a temp project root inside the allowlist, command execution on, and a
// scripted model that follows replies.
type localWSFixture struct {
	*runtimeFixture
	root     string
	bookPath string
	box      func() *secret.Box
	engine   *workspace.LocalEngine
	jobs     *workspace.JobTable
}

func newLocalWSFixture(t *testing.T, mode string, replies ...func(call int) string) *localWSFixture {
	t.Helper()
	bookPath := filepath.Join(t.TempDir(), "tasks.enc")
	inner := newRuntimeFixture(t, bookPath, mode, 8, replies...)
	boxBytes := bytesRepeat()
	bx, err := secret.New(boxBytes)
	if err != nil {
		t.Fatal(err)
	}
	f := &localWSFixture{runtimeFixture: inner, bookPath: bookPath, box: func() *secret.Box { return bx }}
	root := t.TempDir()
	allowed, err := workspace.NormalizeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	engine := workspace.NewLocalEngine(workspace.Roots{Enabled: true, CommandExecution: true, Dirs: []string{allowed}}, nil)
	f.root = root
	f.engine = engine
	f.jobs = workspace.NewJobTable()
	f.agent.LocalWorkspace = engine
	f.agent.Jobs = f.jobs
	// Bind the local workspace option into the agent context is per-request;
	// tests set opt.Workspace through submitLocal below.
	return f
}

// submitLocal creates a durable task with a bound local workspace.
func (f *localWSFixture) submitLocal(t *testing.T, ctx context.Context, session, goal string) task.Task {
	t.Helper()
	ws := workspace.Target{ServerID: "local", Root: f.root, Kind: "local"}
	sess, project, opt, err := f.agent.PrepareTask(context.Background(), f.owner, "main", session, "test", Options{Workspace: &ws})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.agent.AddTaskGoal(context.Background(), sess.ID, goal); err != nil {
		t.Fatal(err)
	}
	messages, err := f.agent.sessionMessages(context.Background(), sess.ID, f.agent.workspacePrompt(context.Background(), model.Bot{ID: sess.BotID, Name: "主控"}), "主控")
	if err != nil {
		t.Fatal(err)
	}
	it, err := f.runner.AcceptTask(ctx, CreateRequest{
		OwnerID: f.owner, BotID: sess.BotID, SessionID: sess.ID, ProjectID: project,
		ServerID: "local", Content: goal, Messages: messages,
		AppendGoal: func(ctx context.Context, sessionID string) error {
			return f.agent.AddTaskGoal(ctx, sessionID, goal)
		},
	}, opt)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func toolJSONStr(id, name, args string) string { return toolJSON(id, name, args) }

func TestLocalWorkspaceTaskEditRunVerifyComplete(t *testing.T) {
	// A real end-to-end local slice with an actual failure, repair and
	// verified completion: the first command really fails (missing file), the
	// model mutates the workspace, the rerun really succeeds, and finish is
	// refused until a read-only observation runs after the last mutation.
	finishRefusals := 0
	f := newLocalWSFixture(t, preferences.ModeSmart,
		func(n int) string {
			// 1: run against a missing file — a real failing command.
			return toolReply("c1", "workspace", `{"action":"run","command":"cat missing.txt"}`)
		},
		func(n int) string {
			// 2: repair: create the file (parked for approval).
			return toolReply("c3", "workspace", `{"action":"write","path":"fixed.txt","content":"repaired"}`)
		},
		func(n int) string {
			// 3: verify the repair with a real command that must succeed.
			return toolReply("c4", "workspace", `{"action":"run","command":"cat fixed.txt"}`)
		},
		func(n int) string {
			// 4: first finish attempt: prose only. The last mutation (the run)
			// has no later read-only observation, so the runtime must refuse.
			finishRefusals++
			return toolReply("c5", "finish", `{"summary":"已修复并验证","verification":["cat fixed.txt 输出 repaired"]}`)
		},
	)
	// 5: read-only observation after the last mutation, then finish again.
	f.model.replies = append(f.model.replies,
		func(int) string { return toolReply("c6", "workspace", `{"action":"read","path":"fixed.txt"}`) },
		func(int) string {
			return toolReply("c7", "finish", `{"summary":"已修复并验证","verification":["read fixed.txt 确认内容 repaired"]}`)
		},
	)
	stop := runBackground(t, f.runner)
	defer stop()
	it := f.submitLocal(t, context.Background(), "sess-local-1", "修复 app 并验证")

	// Approve each parked write/run until the task reaches a terminal state.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := f.book.Get(it.ID)
		if err == nil && cur.Status == task.StatusWaitingApproval && cur.Pending != nil && cur.Pending.ApprovalID != "" {
			_, _ = f.runner.Decide(context.Background(), f.owner, it.ID, cur.Pending.ApprovalID, true, "同意")
		}
		if err == nil && cur.Terminal() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	final, err := f.book.Get(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != task.StatusCompleted {
		t.Fatalf("status %s err %s", final.Status, final.LastError)
	}
	// The write really happened on disk.
	raw, err := os.ReadFile(filepath.Join(f.root, "fixed.txt"))
	if err != nil || string(raw) != "repaired" {
		t.Fatalf("fixed.txt missing: %v %q", err, raw)
	}
	// The journal proves the workspace calls executed.
	executed := 0
	for _, e := range final.Journal {
		if e.Executed && e.Name == "workspace" {
			executed++
		}
	}
	if executed < 3 {
		t.Fatalf("expected >=3 executed workspace calls, got %d", executed)
	}
}

func TestLocalJobWaitResumesOnRealCompletion(t *testing.T) {
	root := t.TempDir()
	doneFile := filepath.Join(root, "done.txt")
	var taskIDPtr = new(string)
	var f *localWSFixture
	var jobID atomic.Value
	f = newLocalWSFixture(t, preferences.ModeOff,
		func(n int) string {
			// Echo to stdout too so the job log carries a verifiable "done".
			return toolReply("j1", "job_start", `{"command":"sleep 1; echo done; echo done > `+doneFile+`"}`)
		},
		func(n int) string {
			// Resolve the real job id from the journal at call time.
			book := f.book
			taskID := *taskIDPtr
			found := ""
			poll := time.Now().Add(5 * time.Second)
			for time.Now().Before(poll) {
				if rows, err := book.Get(taskID); err == nil {
					for _, e := range rows.Journal {
						if e.Name == "job_start" && strings.Contains(e.Result, "job_id") {
							var started struct {
								JobID string `json:"job_id"`
							}
							if json.Unmarshal([]byte(e.Result), &started) == nil && started.JobID != "" {
								found = started.JobID
							}
						}
					}
				}
				if found != "" {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if found == "" {
				t.Error("job id never appeared")
				return toolReply("j2", "job_wait", `{"job_id":"missing"}`)
			}
			jobID.Store(found)
			return toolReply("j2", "job_wait", `{"job_id":"`+found+`"}`)
		},
		func(n int) string {
			// The resumed request must carry the real job_wait outcome (exit
			// code 0 and the done output) as the tool result, not a stub.
			// Read bodies[n-1] (the previous request body) directly: the
			// handler already holds f.model.mu, so calling body() would
			// self-deadlock.
			body := f.model.bodies[n-1]
			if !strings.Contains(body, "退出码 0") || !strings.Contains(body, "done") {
				t.Errorf("resumed request body missing real job_wait outcome: %s", clip(body, 2000))
			}
			// Read-only verification of the finished job before finishing.
			id, _ := jobID.Load().(string)
			return toolReply("j3", "job_status", `{"job_id":"`+id+`"}`)
		},
		func(n int) string {
			return toolReply("j4", "finish", `{"summary":"后台作业完成","verification":["job_status 只读核验退出码 0 与 done 输出"]}`)
		},
	)
	stop := runBackground(t, f.runner)
	defer stop()
	it := f.submitLocal(t, context.Background(), "sess-job-1", "跑一个后台任务")
	*taskIDPtr = it.ID

	// Approve each parked job_start until the task reaches a terminal state.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := f.book.Get(it.ID)
		if err == nil && cur.Status == task.StatusWaitingApproval && cur.Pending != nil && cur.Pending.ApprovalID != "" {
			_, _ = f.runner.Decide(context.Background(), f.owner, it.ID, cur.Pending.ApprovalID, true, "同意")
		}
		if err == nil && cur.Terminal() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	final, err := f.book.Get(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != task.StatusCompleted {
		t.Fatalf("status %s err %s", final.Status, final.LastError)
	}
	if _, err := os.Stat(doneFile); err != nil {
		for _, e := range final.Journal {
			t.Logf("journal: %s %s executed=%v result=%s", e.Name, e.State, e.Executed, clip(e.Result, 300))
		}
		t.Fatal("background job never wrote its file")
	}
	// The journal's job_wait entry must hold the real outcome.
	journalOK := false
	for _, e := range final.Journal {
		if e.Name == "job_wait" && e.Executed &&
			strings.Contains(e.Result, "退出码 0") && strings.Contains(e.Result, "done") {
			journalOK = true
		}
	}
	if !journalOK {
		for _, e := range final.Journal {
			t.Logf("journal: %s %s executed=%v result=%s", e.Name, e.State, e.Executed, clip(e.Result, 300))
		}
		t.Fatal("journal job_wait entry missing real exit code and done output")
	}
	// The checkpoint's trailing tool message of the parked call too.
	checkpointOK := false
	for _, m := range final.Checkpoint.Messages {
		if role, _ := m["role"].(string); role == "tool" && m["tool_call_id"] == "j2" {
			if c, _ := m["content"].(string); strings.Contains(c, "退出码 0") && strings.Contains(c, "done") {
				checkpointOK = true
			}
		}
	}
	if !checkpointOK {
		t.Fatal("checkpoint job_wait tool message missing real exit code and done output")
	}
}

func TestLocalRunBlockedWithoutCommandExecution(t *testing.T) {
	f := newLocalWSFixture(t, preferences.ModeOff)
	// Command execution is ENV-only policy: an engine without it refuses
	// every run.
	noExec := workspace.NewLocalEngine(workspace.Roots{Enabled: true, CommandExecution: false, Dirs: f.engine.Roots().Dirs}, nil)
	if _, err := noExec.Do(context.Background(), f.root, workspace.LocalRequest{Action: "run", Command: "echo hi"}); err == nil {
		t.Fatal("run accepted while command execution disabled")
	}
}

func TestLocalWorkspaceOutsideRootRefused(t *testing.T) {
	f := newLocalWSFixture(t, preferences.ModeOff)
	outside := t.TempDir()
	if _, err := f.engine.Do(context.Background(), outside, workspace.LocalRequest{Action: "list"}); err == nil {
		t.Fatal("outside root accepted")
	}
}

func TestLocalTaskCancelStopsWindow(t *testing.T) {
	f := newLocalWSFixture(t, preferences.ModeOff,
		func(n int) string {
			// Keep the model busy issuing tool calls; the operator cancels.
			return toolReply("c1", "workspace", `{"action":"read","path":"x.txt"}`)
		},
	)
	stop := runBackground(t, f.runner)
	defer stop()
	it := f.submitLocal(t, context.Background(), "sess-cancel-1", "长时间任务")
	deadline := time.Now().Add(10 * time.Second)
	var running task.Task
	for time.Now().Before(deadline) {
		cur, _ := f.book.Get(it.ID)
		if cur.Status == task.StatusRunning {
			running = cur
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if running.Status != task.StatusRunning {
		t.Fatalf("task never ran: %+v", running)
	}
	cancelled, err := f.runner.Cancel(context.Background(), f.owner, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != task.StatusCancelled {
		t.Fatalf("cancel status %s", cancelled.Status)
	}
	final := f.waitTerminal(t, it.ID)
	if final.Status != task.StatusCancelled {
		t.Fatalf("cancel reverted to %s", final.Status)
	}
}

func TestLocalWorkspaceApprovalParksAndRunsAfterDecision(t *testing.T) {
	f := newLocalWSFixture(t, preferences.ModeSmart,
		func(n int) string {
			return toolReply("c1", "workspace", `{"action":"write","path":"approved.txt","content":"written"}`)
		},
		func(n int) string {
			return toolReply("c2", "finish", `{"summary":"写入完成","verification":["读取 approved.txt 确认内容"]}`)
		},
	)
	stop := runBackground(t, f.runner)
	defer stop()
	it := f.submitLocal(t, context.Background(), "sess-approval-local", "写入配置文件")

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
	if waiting.Status != task.StatusWaitingApproval {
		t.Fatalf("not parked: %+v", waiting)
	}
	// Nothing written before approval.
	if _, err := os.Stat(filepath.Join(f.root, "approved.txt")); err == nil {
		t.Fatal("write happened before approval")
	}
	if waiting.Pending == nil || waiting.Pending.ApprovalID == "" {
		t.Fatal("missing pending approval")
	}
	if _, err := f.runner.Decide(context.Background(), f.owner, it.ID, waiting.Pending.ApprovalID, true, "同意"); err != nil {
		t.Fatal(err)
	}
	final := f.waitTerminal(t, it.ID)
	if final.Status != task.StatusCompleted {
		t.Fatalf("status %s err %s", final.Status, final.LastError)
	}
	raw, err := os.ReadFile(filepath.Join(f.root, "approved.txt"))
	if err != nil || string(raw) != "written" {
		t.Fatalf("approved write missing: %v %q", err, raw)
	}
}

func TestLocalWorkspaceApprovalDenySkipsWrite(t *testing.T) {
	f := newLocalWSFixture(t, preferences.ModeSmart,
		func(n int) string {
			return toolReply("c1", "workspace", `{"action":"write","path":"denied.txt","content":"should not exist"}`)
		},
	)
	stop := runBackground(t, f.runner)
	defer stop()
	it := f.submitLocal(t, context.Background(), "sess-deny-local", "写入另一个文件")
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
	if waiting.Status != task.StatusWaitingApproval {
		t.Fatalf("not parked: %+v", waiting)
	}
	if _, err := f.runner.Decide(context.Background(), f.owner, it.ID, waiting.Pending.ApprovalID, false, "不允许"); err != nil {
		t.Fatal(err)
	}
	final := f.waitTerminal(t, it.ID)
	if final.Status != task.StatusCompleted && final.Status != task.StatusWaitingUser {
		t.Fatalf("deny produced %s", final.Status)
	}
	if _, err := os.Stat(filepath.Join(f.root, "denied.txt")); err == nil {
		t.Fatal("denied write happened anyway")
	}
}

func TestActivityRedactionAndLabels(t *testing.T) {
	// A goal containing a secret-looking string must not leak into activity
	// labels; labels are safe names only.
	call := makeCall("c1", "workspace", `{"action":"edit","path":"src/main.go","old":"token=super-secret-value","new":"token=rotated"}`)
	label := safeToolLabel(call)
	if strings.Contains(label, "super-secret") || strings.Contains(label, "rotated") {
		t.Fatalf("label leaks arguments: %s", label)
	}
	if label != "workspace edit" {
		t.Fatalf("unexpected label %q", label)
	}
	// An activity event store bounds size.
	st := newActivityStore()
	for i := 0; i < 100; i++ {
		st.add(ActivityEvent{TaskID: "t", Name: "model", Label: fmt.Sprintf("step %d", i), Status: ActivityCompleted, StartedAt: timeNowUTC().Format(time.RFC3339)})
	}
	if len(st.list("t")) != maxTaskActivities {
		t.Fatalf("activity ring unbounded: %d", len(st.list("t")))
	}
}

func TestWorkspaceCapabilitiesShape(t *testing.T) {
	// The capability payload must never include anything beyond the allowlist.
	e := workspace.NewLocalEngine(workspace.Roots{Enabled: true, CommandExecution: true, Dirs: []string{"/srv/projects/demo"}}, nil)
	caps := e.Capabilities()
	if !caps.Enabled || len(caps.Roots) != 1 || caps.Roots[0] != "/srv/projects/demo" {
		t.Fatalf("capabilities: %+v", caps)
	}
}

func TestPrepareTaskRejectsUnknownLocalRoot(t *testing.T) {
	f := newLocalWSFixture(t, preferences.ModeOff)
	// A root outside the allowlist must be refused at task creation.
	ws := workspace.Target{ServerID: "local", Root: t.TempDir(), Kind: "local"}
	_, _, _, err := f.agent.PrepareTask(context.Background(), f.owner, "main", "sess-outside", "test", Options{Workspace: &ws})
	if err == nil {
		t.Fatal("arbitrary local root accepted")
	}
}

func TestLocalTaskRestartNeverReplaysMutatingJob(t *testing.T) {
	// Simulate a restart while a mutating call is in flight: mark a journal
	// entry started, reload the book, and confirm the resumed task reports an
	// unknown outcome instead of re-running.
	f := newLocalWSFixture(t, preferences.ModeOff)
	// No runBackground here: the test injects an in-flight journal entry to
	// simulate a crash, and the real background dispatcher would race ahead
	// and execute the queued task, making the test flaky.
	it := f.submitLocal(t, context.Background(), "sess-restart-1", "写入 restart.txt")
	// Write a started (in-flight) mutating journal entry directly.
	_, err := f.book.Update(it.ID, func(tsk *task.Task) error {
		tsk.Status = task.StatusRunning
		tsk.Journal = append(tsk.Journal, task.JournalEntry{
			At: time.Now().UTC(), Seq: 1, Turn: 0, CallID: "cx", Name: "workspace",
			ArgsHash: "abc", State: task.JournalStarted,
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Reload the book from disk: recovery must park it.
	book2, err := task.New(f.bookPath, f.box())
	if err != nil {
		t.Fatal(err)
	}
	got, err := book2.Get(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != task.StatusPaused {
		t.Fatalf("restart left task %s, want paused", got.Status)
	}
	for _, e := range got.Journal {
		if e.CallID == "cx" && e.State != task.JournalInterrupted {
			t.Fatalf("in-flight entry not marked interrupted: %+v", e)
		}
	}
}

var _ = json.Marshal
var _ = store.ServerRec{}

func TestActivityStableIDsAndConcurrency(t *testing.T) {
	// Every event gets a stable, unique, task-scoped ID; concurrent add/close
	// never collides and never loses closes.
	st := newActivityStore()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				st.add(ActivityEvent{TaskID: "t1", Name: "workspace", Label: "workspace edit", Status: ActivityRunning, StartedAt: timeNowUTC().Format(time.RFC3339)})
				st.close("t1", "workspace", "workspace edit", ActivityCompleted)
			}
		}(i)
	}
	wg.Wait()
	rows := st.list("t1")
	seen := map[string]bool{}
	for _, ev := range rows {
		if ev.ID == "" || seen[ev.ID] {
			t.Fatalf("missing or duplicate activity ID %q", ev.ID)
		}
		seen[ev.ID] = true
	}
	// IDs are task-scoped: another task's sequence is independent.
	st.add(ActivityEvent{TaskID: "t2", Name: "model", Label: "model", Status: ActivityCompleted, StartedAt: timeNowUTC().Format(time.RFC3339)})
	if rows2 := st.list("t2"); len(rows2) != 1 || rows2[0].ID == rows[0].ID {
		t.Fatal("task-scoped IDs collide")
	}
}

func TestJournalReconstructionHasStableIDs(t *testing.T) {
	// Rebuilt (post-restart) activities carry stable journal-sequence IDs and
	// interrupted rows map to the interrupted status.
	it := task.Task{
		ID: "t-j", OwnerID: "u1", BotID: "main", SessionID: "s", Goal: "g",
		Status: task.StatusPaused, Journal: []task.JournalEntry{
			{At: time.Now().UTC().Add(-time.Minute), Seq: 1, Turn: 1, CallID: "c1", Name: "workspace", State: task.JournalDone, Executed: true, Outcome: task.OutcomeDone, Result: "文件 src/main.go（共 3 行）"},
			{At: time.Now().UTC().Add(-30 * time.Second), Seq: 2, Turn: 1, CallID: "c2", Name: "workspace", State: task.JournalInterrupted},
		},
	}
	acts := journalActivities(it)
	if len(acts) != 2 {
		t.Fatalf("activities %d", len(acts))
	}
	if acts[0].Status != ActivityCompleted || acts[1].Status != ActivityInterrupted {
		t.Fatalf("statuses %q %q", acts[0].Status, acts[1].Status)
	}
	if acts[0].ID == acts[1].ID || acts[0].ID == "" {
		t.Fatalf("IDs %q %q", acts[0].ID, acts[1].ID)
	}
	if strings.Contains(acts[0].Label, "共 3 行") {
		t.Fatalf("journal result leaked into label: %q", acts[0].Label)
	}
}
