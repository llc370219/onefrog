package bot

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/task"
)

// delegateFixture builds a registry with one parent task that is already
// running inside a window, plus a taskState wired for delegation.
func delegateFixture(t *testing.T, parent task.Task) (*runtimeFixture, *taskState, task.Task) {
	t.Helper()
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 6)
	now := time.Now().UTC()
	parent.ID = "parent"
	parent.OwnerID = f.owner
	parent.BotID = "main"
	parent.SessionID = "sess-parent"
	parent.ProjectID = "proj"
	parent.ServerID = "web"
	parent.Status = task.StatusRunning
	parent.StepBudget = 6
	parent.MaxSteps = 24
	parent.MaxAttempts = 3
	parent.Goal = "让巡检看一下磁盘"
	parent.CreatedAt, parent.UpdatedAt = now, now
	if _, err := f.book.Create(parent); err != nil {
		t.Fatal(err)
	}
	st := &taskState{book: f.book, id: parent.ID, maxSteps: 6, turn: 3}
	installDelegation(st)
	if st.delegateCall == nil {
		t.Fatal("delegation hook not installed")
	}
	return f, st, parent
}

func delegateCall(callID string) toolCall {
	call := toolCall{ID: callID, Type: "function"}
	call.Function.Name = "ask_agent"
	call.Function.Arguments = `{"bot_id":"check","task":"检查磁盘并报告"}`
	return call
}

func TestDelegateCreatesDeterministicChildAndParksParent(t *testing.T) {
	f, st, _ := delegateFixture(t, task.Task{})
	call := delegateCall("call-1")
	speaker, _ := f.agent.Store.GetBot(context.Background(), "main")
	out, err := st.delegate(context.Background(), f.agent, speaker, credentials{}, "web", call, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Fatalf("parent should be parked, got %q", *out)
	}
	wantID := childTaskID("parent", 3, "call-1", argsHash(call))
	child, err := f.book.Get(wantID)
	if err != nil {
		t.Fatalf("child missing: %v", err)
	}
	if child.ParentID != "parent" || child.ParentTurn != 3 || child.ParentCallID != "call-1" {
		t.Fatalf("child link wrong: %+v", child)
	}
	if child.OwnerID != f.owner || child.ProjectID != "proj" || child.ServerID != "web" || child.BotID != "check" {
		t.Fatalf("child did not inherit scope: %+v", child)
	}
	if child.SessionID == "sess-parent" || child.SessionID == "" {
		t.Fatalf("child must have its own session: %q", child.SessionID)
	}
	// The relationship is persisted before the parent intent, so the child is
	// released from the acceptance gate and queued only after the parent is
	// parked on it.
	if child.AcceptancePending || child.Status != task.StatusQueued {
		t.Fatalf("child acceptance not committed: %+v", child)
	}
	if child.MaxSteps > 24 || child.MaxSteps < child.StepBudget {
		t.Fatalf("child budget not bounded: %+v", child)
	}
	if child.MaxSteps > 24 || child.StepBudget > child.MaxSteps {
		t.Fatalf("child budget exceeds the parent's remaining budget: %+v", child)
	}
	// The child has its own accepted snapshot: a worker prompt plus the
	// delegated task, never the parent's conversation.
	msgs := child.Checkpoint.Messages
	if len(msgs) != 2 || msgs[0]["role"] != "system" || msgs[1]["role"] != "user" {
		t.Fatalf("child snapshot wrong: %+v", msgs)
	}
	if msgs[1]["content"] != "检查磁盘并报告" {
		t.Fatalf("child goal not accepted as its user message: %q", msgs[1]["content"])
	}
	if child.ParentGoal != "让巡检看一下磁盘" {
		t.Fatalf("parent goal not visible to the child reviewer: %q", child.ParentGoal)
	}
	parent, _ := f.book.Get("parent")
	if parent.Status != task.StatusWaitingJob || parent.Pending == nil || parent.Pending.Kind != task.PendingJob {
		t.Fatalf("parent not parked on child: %+v", parent)
	}
	if parent.Pending.JobRef != wantID || parent.Pending.Turn != 3 || parent.Pending.CallID != "call-1" {
		t.Fatalf("pending does not name the exact call/child: %+v", parent.Pending)
	}
	if e := findEntry(parent.Journal, 3, "call-1", argsHash(call)); e == nil || e.State != task.JournalWaiting {
		t.Fatalf("parent journal not waiting: %+v", parent.Journal)
	}
}

func TestDelegateResumeNeverCreatesSecondChild(t *testing.T) {
	f, st, _ := delegateFixture(t, task.Task{})
	ctx := context.Background()
	call := delegateCall("call-1")
	speaker, _ := f.agent.Store.GetBot(ctx, "main")
	if out, err := st.delegate(ctx, f.agent, speaker, credentials{}, "web", call, nil); err != nil || out != nil {
		t.Fatalf("first delegate %v %v", out, err)
	}
	childID := childTaskID("parent", 3, "call-1", argsHash(call))
	// A second window resumes the same call.
	st2 := &taskState{book: f.book, id: "parent", maxSteps: 6, turn: 3}
	installDelegation(st2)
	if out, err := st2.delegate(ctx, f.agent, speaker, credentials{}, "web", call, nil); err != nil || out != nil {
		t.Fatalf("resume delegate %v %v", out, err)
	}
	children := 0
	for _, it := range f.book.List("") {
		if it.ParentID == "parent" {
			children++
		}
	}
	if children != 1 {
		t.Fatalf("duplicate children created: %d", children)
	}
	parent, _ := f.book.Get("parent")
	waiting := 0
	for _, e := range parent.Journal {
		if e.State == task.JournalWaiting {
			waiting++
		}
	}
	if waiting != 1 {
		t.Fatalf("duplicate waiting entries: %d", waiting)
	}
	if _, err := f.book.Get(childID); err != nil {
		t.Fatal(err)
	}
}

func TestDelegateReturnsFinishedChildResultAndClosesCall(t *testing.T) {
	f, _, _ := delegateFixture(t, task.Task{})
	ctx := context.Background()
	call := delegateCall("call-1")
	speaker, _ := f.agent.Store.GetBot(ctx, "main")
	childState := &taskState{book: f.book, id: "parent", maxSteps: 6, turn: 3}
	installDelegation(childState)
	if out, err := childState.delegate(ctx, f.agent, speaker, credentials{}, "web", call, nil); err != nil || out != nil {
		t.Fatalf("delegate %v %v", out, err)
	}
	childID := childTaskID("parent", 3, "call-1", argsHash(call))
	if _, err := f.book.Update(childID, func(t *task.Task) error {
		t.AcceptancePending = false
		t.Status = task.StatusCompleted
		t.Result = "磁盘使用 40%"
		t.Verified = []string{"df -h 正常"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := childState.delegate(ctx, f.agent, speaker, credentials{}, "web", call, nil)
	if err != nil || out == nil {
		t.Fatalf("expected child result, got %v %v", out, err)
	}
	if !strings.Contains(*out, "磁盘使用 40%") || !strings.Contains(*out, "df -h 正常") {
		t.Fatalf("child result not propagated: %q", *out)
	}
	parent, _ := f.book.Get("parent")
	if parent.Pending != nil || parent.Status != task.StatusRunning {
		t.Fatalf("parent not resumed: %+v", parent)
	}
	e := findEntry(parent.Journal, 3, "call-1", argsHash(call))
	if e == nil || e.State != task.JournalDone || !strings.Contains(e.Result, "40%") {
		t.Fatalf("parent journal not closed with the result: %+v", e)
	}
}

func TestDelegateChildFailureIsExplicit(t *testing.T) {
	for _, tc := range []struct {
		status string
		note   string
	}{
		{task.StatusFailed, "执行失败"},
		{task.StatusBudgetExhausted, "预算用完"},
		{task.StatusCancelled, "取消"},
	} {
		f, st, _ := delegateFixture(t, task.Task{})
		ctx := context.Background()
		call := delegateCall("call-1")
		speaker, _ := f.agent.Store.GetBot(ctx, "main")
		if _, err := st.delegate(ctx, f.agent, speaker, credentials{}, "web", call, nil); err != nil {
			t.Fatal(err)
		}
		childID := childTaskID("parent", 3, "call-1", argsHash(call))
		if _, err := f.book.Update(childID, func(t *task.Task) error {
			t.AcceptancePending = false
			t.Status = tc.status
			t.LastError = "boom"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		out, err := st.delegate(ctx, f.agent, speaker, credentials{}, "web", call, nil)
		if err != nil || out == nil {
			t.Fatalf("%s: %v %v", tc.status, out, err)
		}
		if !strings.Contains(*out, tc.note) {
			t.Fatalf("%s: failure not explicit: %q", tc.status, *out)
		}
	}
}

func TestChildCannotDelegateAgain(t *testing.T) {
	f, _, parent := delegateFixture(t, task.Task{})
	childID := childTaskID("parent", 3, "call-1", argsHash(delegateCall("call-1")))
	now := time.Now().UTC()
	if _, err := f.book.Create(task.Task{
		ID: childID, OwnerID: f.owner, BotID: "check", SessionID: "child-s",
		ParentID: parent.ID, ParentTurn: 3, ParentCallID: "call-1",
		Goal: "子任务", Status: task.StatusRunning, StepBudget: 4, MaxSteps: 8,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	childSt := &taskState{book: f.book, id: childID, maxSteps: 4, turn: 1}
	installDelegation(childSt)
	speaker, _ := f.agent.Store.GetBot(context.Background(), "check")
	out, err := childSt.delegate(context.Background(), f.agent, speaker, credentials{}, "web", delegateCall("c2"), nil)
	if err != nil || out == nil {
		t.Fatalf("nested delegation not refused: %v %v", out, err)
	}
	if !strings.Contains(*out, "不能再派工") {
		t.Fatalf("unexpected nested delegation result: %q", *out)
	}
	for _, it := range f.book.List("") {
		if it.ParentID == childID {
			t.Fatal("nested child task created")
		}
	}
}

func TestChildUsesOwnSessionNotParentLock(t *testing.T) {
	f, st, _ := delegateFixture(t, task.Task{})
	speaker, _ := f.agent.Store.GetBot(context.Background(), "main")
	if _, err := st.delegate(context.Background(), f.agent, speaker, credentials{}, "web", delegateCall("call-1"), nil); err != nil {
		t.Fatal(err)
	}
	childID := childTaskID("parent", 3, "call-1", argsHash(delegateCall("call-1")))
	child, _ := f.book.Get(childID)
	if child.SessionID == "sess-parent" {
		t.Fatal("child shares the parent session (and its runner slot)")
	}
	if _, err := f.agent.Store.ListMessages(context.Background(), child.SessionID); err != nil {
		t.Fatalf("child session missing: %v", err)
	}
}

func TestPauseAndCancelPropagateToChildren(t *testing.T) {
	f, st, _ := delegateFixture(t, task.Task{})
	speaker, _ := f.agent.Store.GetBot(context.Background(), "main")
	if _, err := st.delegate(context.Background(), f.agent, speaker, credentials{}, "web", delegateCall("call-1"), nil); err != nil {
		t.Fatal(err)
	}
	childID := childTaskID("parent", 3, "call-1", argsHash(delegateCall("call-1")))
	if err := f.runner.PropagatePause("parent"); err != nil {
		t.Fatal(err)
	}
	if child, _ := f.book.Get(childID); child.Status != task.StatusPaused {
		t.Fatalf("child not paused: %s", child.Status)
	}
	if err := f.runner.PropagateCancel("parent"); err != nil {
		t.Fatal(err)
	}
	if child, _ := f.book.Get(childID); child.Status != task.StatusCancelled {
		t.Fatalf("child not cancelled: %s", child.Status)
	}
}

func TestDelegateParkedChildIsRecoveredNotDuplicated(t *testing.T) {
	f, st, _ := delegateFixture(t, task.Task{})
	speaker, _ := f.agent.Store.GetBot(context.Background(), "main")
	call := delegateCall("call-1")
	if _, err := st.delegate(context.Background(), f.agent, speaker, credentials{}, "web", call, nil); err != nil {
		t.Fatal(err)
	}
	childID := childTaskID("parent", 3, "call-1", argsHash(call))
	// Simulate a crash right after the parent parked but before the child was
	// released: the child is still paused and acceptance-pending.
	if _, err := f.book.Update(childID, func(t *task.Task) error {
		t.Status = task.StatusPaused
		t.AcceptancePending = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A resumed window must find and release that same child, never create a
	// second one.
	st2 := &taskState{book: f.book, id: "parent", maxSteps: 6, turn: 3}
	installDelegation(st2)
	if out, err := st2.delegate(context.Background(), f.agent, speaker, credentials{}, "web", call, nil); err != nil || out != nil {
		t.Fatalf("resume %v %v", out, err)
	}
	child, err := f.book.Get(childID)
	if err != nil {
		t.Fatal(err)
	}
	if child.AcceptancePending || child.Status != task.StatusQueued {
		t.Fatalf("parked child not released on resume: %+v", child)
	}
	children := 0
	for _, it := range f.book.List("") {
		if it.ParentID == "parent" {
			children++
		}
	}
	if children != 1 {
		t.Fatalf("duplicate children after crash resume: %d", children)
	}
}

func TestDelegateJournalEntryIsNotAnApproval(t *testing.T) {
	f, st, _ := delegateFixture(t, task.Task{})
	speaker, _ := f.agent.Store.GetBot(context.Background(), "main")
	if _, err := st.delegate(context.Background(), f.agent, speaker, credentials{}, "web", delegateCall("call-1"), nil); err != nil {
		t.Fatal(err)
	}
	parent, _ := f.book.Get("parent")
	if parent.Pending.Kind == task.PendingApproval {
		t.Fatal("child job was stored as an approval")
	}
	if len(parent.Approvals) != 0 {
		t.Fatalf("child job created an approval: %+v", parent.Approvals)
	}
	if pendingDecision(parent) != nil {
		t.Fatal("child job treated as an approval decision")
	}
}

var _ = errors.Is
var _ = model.Bot{}

func TestDelegateRefusesWhenParentBudgetExhausted(t *testing.T) {
	f, st, parent := delegateFixture(t, task.Task{})
	if _, err := f.book.Update(parent.ID, func(t *task.Task) error {
		t.TotalSteps = t.MaxSteps
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	speaker, _ := f.agent.Store.GetBot(context.Background(), "main")
	out, err := st.delegate(context.Background(), f.agent, speaker, credentials{}, "web", delegateCall("call-1"), nil)
	if err != nil || out == nil {
		t.Fatalf("expected a refusal result, got %v %v", out, err)
	}
	if !strings.Contains(*out, "预算") {
		t.Fatalf("unexpected budget refusal: %q", *out)
	}
	for _, it := range f.book.List("") {
		if it.ParentID == parent.ID {
			t.Fatal("child created with no budget left")
		}
	}
}

func TestChildBudgetNeverExceedsParentRemaining(t *testing.T) {
	f, st, parent := delegateFixture(t, task.Task{})
	if _, err := f.book.Update(parent.ID, func(t *task.Task) error {
		t.TotalSteps = 21 // 3 steps left of 24
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	speaker, _ := f.agent.Store.GetBot(context.Background(), "main")
	if _, err := st.delegate(context.Background(), f.agent, speaker, credentials{}, "web", delegateCall("call-1"), nil); err != nil {
		t.Fatal(err)
	}
	childID := childTaskID("parent", 3, "call-1", argsHash(delegateCall("call-1")))
	child, _ := f.book.Get(childID)
	if child.MaxSteps != 3 || child.StepBudget > 3 {
		t.Fatalf("child budget exceeds parent remaining: max=%d window=%d", child.MaxSteps, child.StepBudget)
	}
}

func TestAggregateBudgetAcrossMultipleChildrenAndRestart(t *testing.T) {
	f, st, parent := delegateFixture(t, task.Task{})
	ctx := context.Background()
	speaker, _ := f.agent.Store.GetBot(ctx, "main")
	// Two children, each capped by what is still free. The first commits its
	// whole live cap, so the second can only get the rest.
	if _, err := st.delegate(ctx, f.agent, speaker, credentials{}, "web", delegateCall("call-1"), nil); err != nil {
		t.Fatal(err)
	}
	firstID := childTaskID("parent", 3, "call-1", argsHash(delegateCall("call-1")))
	first, _ := f.book.Get(firstID)
	if first.MaxSteps != 24 {
		t.Fatalf("first child cap %d", first.MaxSteps)
	}
	// It actually uses 5, then completes; the rest is released.
	if _, err := f.book.Update(firstID, func(t *task.Task) error {
		t.AcceptancePending = false
		t.Status = task.StatusCompleted
		t.TotalSteps = 5
		t.Result = "ok"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The parent window consumed the finished child and is running again; the
	// dispatcher always sets running before a window executes.
	if _, err := f.book.Update("parent", func(t *task.Task) error {
		t.Status = task.StatusRunning
		t.Pending = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st.turn = 4
	if _, err := st.delegate(ctx, f.agent, speaker, credentials{}, "web", delegateCall("call-2"), nil); err != nil {
		t.Fatal(err)
	}
	secondID := childTaskID("parent", 4, "call-2", argsHash(delegateCall("call-2")))
	second, _ := f.book.Get(secondID)
	if second.MaxSteps != 19 {
		t.Fatalf("second child cap %d, want 19 (24-5)", second.MaxSteps)
	}
	// Exhaust the budget and prove no third child can be created.
	if _, err := f.book.Update(secondID, func(t *task.Task) error {
		t.Status = task.StatusCompleted
		t.TotalSteps = 19
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.book.Update("parent", func(t *task.Task) error {
		t.Status = task.StatusRunning
		t.Pending = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st.turn = 5
	out, err := st.delegate(ctx, f.agent, speaker, credentials{}, "web", delegateCall("call-3"), nil)
	if err != nil || out == nil || !strings.Contains(*out, "预算") {
		t.Fatalf("budget not enforced across children: %v %v", out, err)
	}
	if got := remainingBudget(parent, f.book.List("")); got != 0 {
		t.Fatalf("remaining %d, want 0", got)
	}
}

func TestAggregateBudgetCountsLiveGrandchildren(t *testing.T) {
	f, st, _ := delegateFixture(t, task.Task{})
	ctx := context.Background()
	speaker, _ := f.agent.Store.GetBot(ctx, "main")
	if _, err := st.delegate(ctx, f.agent, speaker, credentials{}, "web", delegateCall("call-1"), nil); err != nil {
		t.Fatal(err)
	}
	firstID := childTaskID("parent", 3, "call-1", argsHash(delegateCall("call-1")))
	// A live grandchild is inside the child's cap, not an extra charge on the
	// parent: the live child already commits its whole MaxSteps, so the parent
	// has no free budget left and the grandchild only narrows the child's.
	now := time.Now().UTC()
	if _, err := f.book.Create(task.Task{
		ID: "grand", OwnerID: f.owner, BotID: "check", SessionID: "grand-s",
		ParentID: firstID, Goal: "孙任务", Status: task.StatusRunning,
		MaxSteps: 4, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	parent, _ := f.book.Get("parent")
	all := f.book.List("")
	if got := remainingBudget(parent, all); got != 0 {
		t.Fatalf("live child did not commit its cap: remaining %d", got)
	}
	child, _ := f.book.Get(firstID)
	if got := remainingBudget(child, all); got != 20 {
		t.Fatalf("grandchild not charged inside the child: remaining %d", got)
	}
}

func TestChildApprovalIsIndependentOfParent(t *testing.T) {
	f, st, _ := delegateFixture(t, task.Task{})
	speaker, _ := f.agent.Store.GetBot(context.Background(), "main")
	if _, err := st.delegate(context.Background(), f.agent, speaker, credentials{}, "web", delegateCall("call-1"), nil); err != nil {
		t.Fatal(err)
	}
	childID := childTaskID("parent", 3, "call-1", argsHash(delegateCall("call-1")))
	// The child parks on its own approval. The parent must stay parked on the
	// child job, not inherit or mirror the approval.
	if _, err := f.book.Update(childID, func(t *task.Task) error {
		t.AcceptancePending = false
		t.Status = task.StatusWaitingApproval
		t.Pending = &task.Pending{Kind: task.PendingApproval, CallID: "child-call", ApprovalID: "apr-child"}
		t.Approvals = append(t.Approvals, task.Approval{ID: "apr-child", CallID: "child-call", Status: task.ApprovalPending})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := st.delegate(context.Background(), f.agent, speaker, credentials{}, "web", delegateCall("call-1"), nil)
	if err != nil || out != nil {
		t.Fatalf("parent should stay parked: %v %v", out, err)
	}
	parent, _ := f.book.Get("parent")
	if parent.Status != task.StatusWaitingJob || len(parent.Approvals) != 0 {
		t.Fatalf("parent inherited the child approval: %+v", parent)
	}
	child, _ := f.book.Get(childID)
	if child.Status != task.StatusWaitingApproval || len(child.Approvals) != 1 {
		t.Fatalf("child approval lost: %+v", child)
	}
}

func TestChildSurvivesRestartAndCompletesParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.enc")
	f := newRuntimeFixture(t, path, preferences.ModeOff, 6)
	now := time.Now().UTC()
	parent := task.Task{
		ID: "parent", OwnerID: f.owner, BotID: "main", SessionID: "sess-parent",
		Status: task.StatusRunning, StepBudget: 6, MaxSteps: 24, MaxAttempts: 3,
		Goal: "让巡检看一下磁盘", CreatedAt: now, UpdatedAt: now,
	}
	if _, err := f.book.Create(parent); err != nil {
		t.Fatal(err)
	}
	speaker, _ := f.agent.Store.GetBot(context.Background(), "main")
	st := &taskState{book: f.book, id: "parent", maxSteps: 6, turn: 3}
	installDelegation(st)
	if _, err := st.delegate(context.Background(), f.agent, speaker, credentials{}, "", delegateCall("call-1"), nil); err != nil {
		t.Fatal(err)
	}
	childID := childTaskID("parent", 3, "call-1", argsHash(delegateCall("call-1")))
	if _, err := f.book.Update(childID, func(t *task.Task) error {
		t.AcceptancePending = false
		t.Status = task.StatusCompleted
		t.Result = "磁盘 40%"
		t.Verified = []string{"df -h"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Reload the registry: both the child and the parent's parked call survive.
	book, err := task.New(path, f.agent.Box)
	if err != nil {
		t.Fatal(err)
	}
	reloadedParent, err := book.Get("parent")
	if err != nil {
		t.Fatal(err)
	}
	if reloadedParent.Status != task.StatusWaitingJob || reloadedParent.Pending == nil || reloadedParent.Pending.JobRef != childID {
		t.Fatalf("parent park lost on restart: %+v", reloadedParent)
	}
	if reloadedChild, err := book.Get(childID); err != nil || reloadedChild.Result != "磁盘 40%" {
		t.Fatalf("child lost on restart: %+v %v", reloadedChild, err)
	}
	// Resuming the same call in the reloaded registry returns the child result
	// and never creates a second child.
	st2 := &taskState{book: book, id: "parent", maxSteps: 6, turn: 3}
	installDelegation(st2)
	out, err := st2.delegate(context.Background(), f.agent, speaker, credentials{}, "", delegateCall("call-1"), nil)
	if err != nil || out == nil || !strings.Contains(*out, "磁盘 40%") {
		t.Fatalf("resume after restart: %v %v", out, err)
	}
	children := 0
	for _, it := range book.List("") {
		if it.ParentID == "parent" {
			children++
		}
	}
	if children != 1 {
		t.Fatalf("duplicate children after restart: %d", children)
	}
}

// TestDelegationEndToEndThroughRunner drives a real parent task through the
// runner: the parent calls ask_agent, the child task runs on its own session
// and finishes with the finish tool, and the parent continues automatically
// with the child's result.
func TestDelegationEndToEndThroughRunner(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4,
		// Parent window: ask the worker, then finish using its result.
		func(n int) string {
			switch n {
			case 1:
				return toolReply("p1", "ask_agent", `{"bot_id":"check","task":"检查磁盘使用率"}`)
			case 2:
				return toolReply("p2", "finish", `{"summary":"磁盘已检查","verification":["巡检返回 40%"]}`)
			default:
				return plainReply("完成")
			}
		},
	)
	// The child runs as its own task and finishes with the finish tool. Its
	// request is the second distinct session, so route by body content.
	f.model.replies = []func(int) string{
		func(n int) string {
			return toolReply("p1", "ask_agent", `{"bot_id":"check","task":"检查磁盘使用率"}`)
		},
		func(n int) string {
			return toolReply("c1", "finish", `{"summary":"磁盘使用 40%","verification":["df -h 返回 40%"]}`)
		},
		func(n int) string {
			return toolReply("p2", "finish", `{"summary":"磁盘已检查","verification":["巡检返回 40%"]}`)
		},
	}
	stop := runBackground(t, f.runner)
	defer stop()
	it := f.submit(t, context.Background(), "sess-e2e", "检查磁盘")

	deadline := time.Now().Add(20 * time.Second)
	var final task.Task
	for time.Now().Before(deadline) {
		cur, err := f.book.Get(it.ID)
		if err == nil && cur.Terminal() {
			final = cur
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if final.Status != task.StatusCompleted {
		cur, _ := f.book.Get(it.ID)
		t.Fatalf("parent did not complete: %+v", cur)
	}
	children := 0
	for _, row := range f.book.List("") {
		if row.ParentID == it.ID {
			children++
			if row.Status != task.StatusCompleted {
				t.Fatalf("child did not complete: %+v", row)
			}
		}
	}
	if children != 1 {
		t.Fatalf("expected exactly one child, got %d", children)
	}
}
