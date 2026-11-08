package bot

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onefrog/internal/preferences"
	"onefrog/internal/task"
)

// newSubmissionFixture builds a real runtime with a durable book, a gateway
// that never answers the model (submission tests do not run windows), and the
// runner wired on the agent.
func newSubmissionFixture(t *testing.T) *runtimeFixture {
	t.Helper()
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), "off", 3,
		func(int) string { return plainReply("不该被调用") })
	f.agent.TasksRunner = f.runner
	return f
}

func TestSubmitTaskAcceptsDurableTaskWithProvenance(t *testing.T) {
	f := newSubmissionFixture(t)
	ctx := context.Background()
	it, err := f.agent.SubmitTask(ctx, SubmitRequest{
		OwnerID: f.owner, BotID: "check", ServerID: "web",
		ClientRequestID: "req-1", Content: "检查磁盘",
		Purpose: "inbox", Hop: 2, Untrusted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if it.ID != exactTaskID(f.owner, "req-1") {
		t.Fatalf("task id %q is not the owner+request id", it.ID)
	}
	if it.Status != task.StatusQueued || it.AcceptancePending {
		t.Fatalf("task not queued: %+v", it)
	}
	if it.Purpose != "inbox" || it.Hop != 2 || !it.Untrusted {
		t.Fatalf("provenance not persisted: %+v", it)
	}
	// The goal is in the transcript exactly once and is also in the accepted
	// snapshot: no duplicate user turn.
	msgs, err := f.agent.Store.ListMessages(ctx, it.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	goals := 0
	for _, m := range msgs {
		if m.Role == "user" && strings.Contains(m.Content, "检查磁盘") {
			goals++
		}
	}
	if goals != 1 {
		t.Fatalf("goal transcript entries = %d", goals)
	}
	inSnapshot := 0
	for _, m := range it.Checkpoint.Messages {
		if m["role"] == "user" && strings.Contains(m["content"].(string), "检查磁盘") {
			inSnapshot++
		}
	}
	if inSnapshot != 1 {
		t.Fatalf("snapshot goal entries = %d", inSnapshot)
	}
}

func TestSubmitTaskRetryIsIdempotentAndDoesNotDuplicateTranscript(t *testing.T) {
	f := newSubmissionFixture(t)
	ctx := context.Background()
	req := SubmitRequest{
		OwnerID: f.owner, BotID: "check", ServerID: "web",
		ClientRequestID: "req-same", Content: "检查磁盘",
	}
	first, err := f.agent.SubmitTask(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.agent.SubmitTask(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("retry created a second task: %s vs %s", first.ID, second.ID)
	}
	msgs, _ := f.agent.Store.ListMessages(ctx, first.SessionID)
	users := 0
	for _, m := range msgs {
		if m.Role == "user" {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("retry duplicated the transcript: %d user messages", users)
	}
	count := 0
	for _, it := range f.book.List("") {
		if strings.Contains(it.Goal, "检查磁盘") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("retry created %d tasks", count)
	}
}

func TestSubmitTaskConflictsOnDifferentPayloadUnderSameRequestID(t *testing.T) {
	f := newSubmissionFixture(t)
	ctx := context.Background()
	if _, err := f.agent.SubmitTask(ctx, SubmitRequest{
		OwnerID: f.owner, BotID: "check", ClientRequestID: "req-x", Content: "检查磁盘",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := f.agent.SubmitTask(ctx, SubmitRequest{
		OwnerID: f.owner, BotID: "check", ClientRequestID: "req-x", Content: "删除数据库",
	})
	if err == nil {
		t.Fatal("a different payload reused the same request id")
	}
}

func TestSubmitTaskRequiresARunner(t *testing.T) {
	f := newSubmissionFixture(t)
	f.agent.TasksRunner = nil
	if _, err := f.agent.SubmitTask(context.Background(), SubmitRequest{
		OwnerID: f.owner, BotID: "check", Content: "x",
	}); err != ErrNoRunner {
		t.Fatalf("got %v, want ErrNoRunner", err)
	}
}

// A retry must be resolved from the frozen task row alone. The session history,
// its saved options and the model configuration may all have moved on; none of
// that may turn a retry into a new task, a conflict, or a second write.
func TestSubmitTaskRetryIgnoresLaterSessionHistoryAndOptions(t *testing.T) {
	f := newSubmissionFixture(t)
	ctx := context.Background()
	req := SubmitRequest{
		OwnerID: f.owner, BotID: "check", ServerID: "web",
		ClientRequestID: "req-drift", Content: "检查磁盘",
	}
	first, err := f.agent.SubmitTask(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	// The conversation keeps going, the session options are rewritten (model
	// route, approval mode), and the transcript grows.
	if err := f.agent.AddTaskGoal(ctx, first.SessionID, "后来又说了一句"); err != nil {
		t.Fatal(err)
	}
	drifted := Options{Mode: "team", ApprovalMode: "manual", ModelRoute: &preferences.Route{ProviderID: "other", Model: "other"}}
	raw, err := json.Marshal(struct {
		Options
		OwnerID string `json:"owner_id,omitempty"`
	}{Options: drifted, OwnerID: f.owner})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.agent.Store.SaveChatOptions(ctx, first.SessionID, raw); err != nil {
		t.Fatal(err)
	}
	before, err := f.agent.Store.ListMessages(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}

	second, err := f.agent.SubmitTask(ctx, req)
	if err != nil {
		t.Fatalf("a retry after the session moved on failed: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("retry created a second task: %s vs %s", first.ID, second.ID)
	}
	if second.Goal != first.Goal || second.OptionsJSON != first.OptionsJSON ||
		second.SessionID != first.SessionID || second.ServerID != first.ServerID {
		t.Fatalf("retry did not return the frozen task: %+v vs %+v", second, first)
	}
	after, err := f.agent.Store.ListMessages(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("retry wrote %d messages", len(after)-len(before))
	}
	// The session options are not rewritten by a retry either.
	sess, err := f.agent.Store.GetChatSession(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if string(sess.Options) != string(raw) {
		t.Fatalf("retry rewrote the session options: %s", sess.Options)
	}
	if n := len(f.book.List("")); n != 1 {
		t.Fatalf("retry left %d tasks", n)
	}
}

// An explicitly changed option is still a conflict, even though the check runs
// before any normalization.
func TestSubmitTaskRetryConflictsOnExplicitOptionChange(t *testing.T) {
	f := newSubmissionFixture(t)
	ctx := context.Background()
	base := SubmitRequest{
		OwnerID: f.owner, BotID: "check", ServerID: "web",
		ClientRequestID: "req-opt", Content: "检查磁盘",
	}
	if _, err := f.agent.SubmitTask(ctx, base); err != nil {
		t.Fatal(err)
	}
	before, err := f.agent.Store.ListMessages(ctx, exactSession(t, f, base))
	if err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.Options = Options{ApprovalMode: "manual"}
	_, err = f.agent.SubmitTask(ctx, changed)
	if !errors.Is(err, ErrRequestPayloadChanged) {
		t.Fatalf("explicit option change returned %v, want a payload conflict", err)
	}
	after, err := f.agent.Store.ListMessages(ctx, exactSession(t, f, base))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("a conflicting retry wrote %d messages", len(after)-len(before))
	}
}

// The project scope is derived from the session and mode. A retry that names a
// different project must be refused, never applied as an override.
func TestSubmitTaskRejectsDifferentProject(t *testing.T) {
	f := newSubmissionFixture(t)
	ctx := context.Background()
	if _, err := f.agent.SubmitTask(ctx, SubmitRequest{
		OwnerID: f.owner, BotID: "check", ClientRequestID: "req-proj", Content: "检查磁盘",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := f.agent.SubmitTask(ctx, SubmitRequest{
		OwnerID: f.owner, BotID: "check", ClientRequestID: "req-proj", Content: "检查磁盘",
		ProjectID: "some-other-project",
	})
	if err == nil {
		t.Fatal("a different project was accepted")
	}
	if n := len(f.book.List("")); n != 1 {
		t.Fatalf("conflicting project left %d tasks", n)
	}
}

// A retry that changes provenance must be refused: purpose, hop and trust are
// part of the frozen authority, not transport hints.
func TestSubmitTaskRetryRejectsProvenanceChange(t *testing.T) {
	f := newSubmissionFixture(t)
	ctx := context.Background()
	base := SubmitRequest{
		OwnerID: f.owner, BotID: "check", ClientRequestID: "req-prov", Content: "检查磁盘",
		Purpose: "inbox", Hop: 1, Untrusted: true,
	}
	if _, err := f.agent.SubmitTask(ctx, base); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*SubmitRequest){
		func(r *SubmitRequest) { r.Purpose = "conversation" },
		func(r *SubmitRequest) { r.Hop = 2 },
	} {
		changed := base
		mutate(&changed)
		if _, err := f.agent.SubmitTask(ctx, changed); !errors.Is(err, ErrRequestPayloadChanged) {
			t.Fatalf("provenance change returned %v, want a conflict", err)
		}
	}
	// Omitting provenance entirely inherits the stored values.
	plain := base
	plain.Purpose, plain.Hop, plain.Untrusted = "", 0, false
	it, err := f.agent.SubmitTask(ctx, plain)
	if err != nil {
		t.Fatal(err)
	}
	if it.Purpose != "inbox" || it.Hop != 1 || !it.Untrusted {
		t.Fatalf("retry dropped provenance: %+v", it)
	}
}

func exactSession(t *testing.T, f *runtimeFixture, req SubmitRequest) string {
	t.Helper()
	it, ok := f.agent.RequestTask(req.OwnerID, req.ClientRequestID)
	if !ok {
		t.Fatal("request has no task")
	}
	return it.SessionID
}

func TestAwaitDoesNotBorrowALaterTaskResult(t *testing.T) {
	f := newSubmissionFixture(t)
	ctx := context.Background()
	// Two tasks in the same session. The first finishes with its own result;
	// the second runs later. Await of the first must never pick the second's
	// transcript message.
	first, err := f.agent.SubmitTask(ctx, SubmitRequest{
		OwnerID: f.owner, BotID: "check", SessionID: "sess-shared",
		ClientRequestID: "req-a", Content: "第一个任务",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.book.Update(first.ID, func(t *task.Task) error {
		t.Status = task.StatusCompleted
		t.Result = "第一个结果"
		t.ResultMessageID = "msg-first"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	second, err := f.agent.SubmitTask(ctx, SubmitRequest{
		OwnerID: f.owner, BotID: "check", SessionID: "sess-shared",
		ClientRequestID: "req-b", Content: "第二个任务",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.book.Update(second.ID, func(t *task.Task) error {
		t.Status = task.StatusCompleted
		t.Result = "第二个结果"
		t.ResultMessageID = "msg-second"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := f.runner.Await(ctx, f.owner, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Done || out.Result.Text != "第一个结果" || out.Result.MessageID != "msg-first" {
		t.Fatalf("await borrowed another task's result: %+v", out.Result)
	}
}

func TestAwaitReportsParkedTaskWithoutFakingSuccess(t *testing.T) {
	f := newSubmissionFixture(t)
	ctx := context.Background()
	it, err := f.agent.SubmitTask(ctx, SubmitRequest{
		OwnerID: f.owner, BotID: "check", ClientRequestID: "req-card", Content: "问一下",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.book.Update(it.ID, func(t *task.Task) error {
		t.Status = task.StatusWaitingUser
		t.Pending = &task.Pending{Kind: task.PendingUser, CallID: "c", CardMessageID: "card-1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := f.runner.Await(ctx, f.owner, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Done || out.CardID != "card-1" || out.Result.Text != "" {
		t.Fatalf("parked task reported as done: %+v", out)
	}
}

func TestWaitTaskReturnsOnWaitingNotOnlyTerminal(t *testing.T) {
	f := newSubmissionFixture(t)
	ctx := context.Background()
	it, err := f.agent.SubmitTask(ctx, SubmitRequest{
		OwnerID: f.owner, BotID: "check", ClientRequestID: "req-wait", Content: "等批准",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.book.Update(it.ID, func(t *task.Task) error {
		t.Status = task.StatusWaitingApproval
		t.Pending = &task.Pending{Kind: task.PendingApproval, CallID: "c", ApprovalID: "ap-1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := f.runner.WaitTask(waitCtx, f.owner, it.ID, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if out.Done || out.Task.Status != task.StatusWaitingApproval {
		t.Fatalf("wait did not return on waiting: %+v", out)
	}
}

func TestWaitTaskReturnsOnPause(t *testing.T) {
	f := newSubmissionFixture(t)
	ctx := context.Background()
	it, err := f.agent.SubmitTask(ctx, SubmitRequest{
		OwnerID: f.owner, BotID: "check", ClientRequestID: "req-pause", Content: "暂停",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.book.Update(it.ID, func(t *task.Task) error {
		t.Status = task.StatusPaused
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := f.runner.WaitTask(ctx, f.owner, it.ID, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if out.Task.Status != task.StatusPaused {
		t.Fatalf("wait did not return on pause: %+v", out)
	}
}

func TestProvenanceIsImmutable(t *testing.T) {
	f := newSubmissionFixture(t)
	it, err := f.agent.SubmitTask(context.Background(), SubmitRequest{
		OwnerID: f.owner, BotID: "check", ClientRequestID: "req-imm", Content: "x",
		Purpose: "inbox", Hop: 1, Untrusted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*task.Task) error{
		func(t *task.Task) error { t.Purpose = "conversation"; return nil },
		func(t *task.Task) error { t.Hop = 0; return nil },
		func(t *task.Task) error { t.Untrusted = false; return nil },
		func(t *task.Task) error { t.ParentGoal = "伪造"; return nil },
	} {
		if _, err := f.book.Update(it.ID, mutate); err == nil {
			t.Fatal("provenance mutation accepted")
		}
	}
}

func TestAwaitRejectsAnotherOwner(t *testing.T) {
	f := newSubmissionFixture(t)
	it, err := f.agent.SubmitTask(context.Background(), SubmitRequest{
		OwnerID: f.owner, BotID: "check", ClientRequestID: "req-own", Content: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.Await(context.Background(), "someone-else", it.ID); err == nil {
		t.Fatal("await leaked another owner's task")
	}
}
