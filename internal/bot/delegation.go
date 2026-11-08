package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/task"
)

// Durable delegation. ask_agent inside a durable task does not run a child
// turn inline: it creates a child *task* with its own session, checkpoint,
// journal and approvals, parks the parent on that child, and resumes the
// parent with the child's stored result. The child identity is deterministic
// from the parent call, so a restart or a resumed window finds the same child
// instead of creating a second one.

// delegateJobKind marks a waiting_job pending that is really a child task.
// It is never an approval: JobRef names the child task id.
const delegateJobKind = task.PendingJob

// delegateWake is how soon a parent re-checks a child that is still running.
// The child's own completion wakes the runner, so this is only a backstop.
const delegateWake = 15 * time.Second

// installDelegation wires durable child tasks into one window. The runtime
// calls it right after building the taskState; without it ask_agent stays an
// ordinary tool call.
func installDelegation(st *taskState) {
	if st == nil {
		return
	}
	st.delegateCall = st.delegate
}

// childTaskID derives the one child task a parent call may ever create.
func childTaskID(parentID string, turn int, callID, hash string) string {
	sum := sha256.Sum256([]byte("child\x00" + parentID + "\x00" + fmt.Sprint(turn) + "\x00" + callID + "\x00" + hash))
	return "task-child-" + hex.EncodeToString(sum[:12])
}

// delegate implements taskState.delegateCall for ask_agent. It returns
// (nil, nil) when the parent is parked on the child, a tool result when the
// child finished, and an error only for real failures.
func (st *taskState) delegate(ctx context.Context, a *Agent, speaker model.Bot, creds credentials, serverID string, call toolCall, tr *trace) (*string, error) {
	it, err := st.book.Get(st.id)
	if err != nil {
		st.halted = true
		return nil, err
	}
	if it.ParentID != "" {
		// A child never delegates again: that would let one ask_agent fan out
		// without bound and hide work from the parent's budget.
		out := "这是协作子任务，不能再派工。请把需要别人做的事写进结论，由主控决定。"
		return &out, nil
	}
	var args struct {
		BotID string `json:"bot_id"`
		Task  string `json:"task"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		out := "派工参数不是有效的 JSON"
		return &out, nil
	}
	args.Task = strings.TrimSpace(a.redact(ctx, args.Task))
	if args.Task == "" {
		out := "派工需要写清要做什么"
		return &out, nil
	}
	hash := argsHash(call)
	childID := childTaskID(st.id, st.turn, call.ID, hash)
	// The child's reviewer must see the user's original authorization, not
	// only the narrower delegated task.
	parentGoal := it.Goal
	prior := findEntry(it.Journal, st.turn, call.ID, hash)
	parked := prior != nil && prior.State == task.JournalWaiting && it.Pending != nil &&
		it.Pending.Kind == delegateJobKind && it.Pending.JobRef == childID &&
		it.Pending.Turn == st.turn && it.Pending.CallID == call.ID

	child, childErr := st.book.Get(childID)
	if childErr != nil && !errors.Is(childErr, task.ErrNotFound) {
		st.halted = true
		return nil, childErr
	}
	if errors.Is(childErr, task.ErrNotFound) {
		// First attempt: persist the child (the relationship) before the
		// parent records any intent, so a crash can never leave a parent
		// pointing at a child that does not exist.
		if !parked && prior != nil && prior.State == task.JournalDone {
			// The call already produced a result in this window; never
			// re-delegate a finished call.
			out := plainResult(prior.Result)
			return &out, nil
		}
		created, err := st.createChild(ctx, a, speaker, serverID, parentGoal, args, childID, call.ID)
		if err != nil {
			out := "派工失败：" + a.safe(ctx, err.Error())
			return &out, nil
		}
		child = created
	}
	if child.ParentID != st.id || child.ParentCallID != call.ID || child.ParentTurn != st.turn {
		out := "派工失败：子任务关联不一致，未执行"
		return &out, nil
	}

	if !child.Terminal() {
		// The child is still running (or was created but not queued before a
		// crash). Keep the parent parked and let the child's runner finish.
		if err := st.parkOnChild(call, hash, childID); err != nil {
			return nil, err
		}
		if child.Status == task.StatusPaused && child.AcceptancePending {
			if _, err := st.book.Update(childID, func(t *task.Task) error {
				if t.AcceptancePending && t.Status == task.StatusPaused {
					t.AcceptancePending = false
					t.Status = task.StatusQueued
				}
				return nil
			}); err != nil {
				st.halted = true
				return nil, err
			}
		}
		return nil, nil
	}

	out := childOutcome(child)
	if prior == nil {
		if err := st.recordDone(call, hash, out); err != nil {
			return nil, err
		}
	} else if err := st.markEntry(prior, task.JournalDone, out); err != nil {
		return nil, err
	}
	if st.parked != nil {
		st.parked = nil
	}
	return &out, nil
}

// childOutcome turns a finished child into the parent's tool result. A child
// that did not complete says so explicitly instead of looking like success.
func childOutcome(child task.Task) string {
	name := child.BotID
	switch child.Status {
	case task.StatusCompleted:
		text := strings.TrimSpace(child.Result)
		if len(child.Verified) > 0 {
			text += "\n\n验证：\n- " + strings.Join(child.Verified, "\n- ")
		}
		if text == "" {
			text = "（子任务已完成，但没有返回结论）"
		}
		return name + "：" + text
	case task.StatusFailed:
		reason := strings.TrimSpace(child.LastError)
		if reason == "" {
			reason = "没有记录原因"
		}
		return "派工失败：" + name + " 的子任务执行失败（" + reason + "）。不要假设已经完成。"
	case task.StatusCancelled:
		return "派工已取消：" + name + " 的子任务被取消，结果未知。"
	case task.StatusBudgetExhausted:
		return "派工未完成：" + name + " 的子任务预算用完，结果不确定。请缩小范围后再派工。"
	default:
		return "派工未完成：" + name + " 的子任务停在 " + child.Status + "，结果未知。"
	}
}

// createChild persists the child task in the acceptance gate: paused and
// marked pending, so the runner cannot execute it before the parent records
// that it is waiting on it.
func (st *taskState) createChild(ctx context.Context, a *Agent, speaker model.Bot, serverID, parentGoal string, args struct {
	BotID string `json:"bot_id"`
	Task  string `json:"task"`
}, childID, callID string) (task.Task, error) {
	target, err := a.findBot(ctx, strings.TrimSpace(args.BotID))
	if err != nil {
		return task.Task{}, errors.New("没有这个 Bot")
	}
	if !a.allowedBot(ctx, target.ID) {
		return task.Task{}, errors.New("该 Bot 已停用或被用户排除，禁止派工")
	}
	if target.ID == speaker.ID {
		return task.Task{}, errors.New("不能把任务派给自己")
	}
	parent, err := st.book.Get(st.id)
	if err != nil {
		return task.Task{}, err
	}
	// The child has its own session so it never shares the parent's transcript,
	// its runner slot or its pending cards.
	sessionID := childID + "-s"
	if _, err := a.Store.EnsureSession(ctx, sessionID, target.ID, "协作子任务"); err != nil {
		return task.Task{}, err
	}
	// The child gets its own accepted snapshot: a worker system prompt and the
	// delegated task as its first user message. It never sees the parent's
	// conversation, transcript or private memory.
	// Build the prompt as a worker turn: no leader mailbox, no delegation.
	promptCtx := usageContext(ctx, target.ID, "worker", "")
	system := a.workerPrompt(promptCtx, target, serverID)
	goal := args.Task
	messages := []map[string]any{
		{"role": "system", "content": system},
		{"role": "user", "content": goal},
	}
	// Strict aggregate budget: MaxSteps is a cap on this task's steps *plus*
	// every descendant it delegates. The remaining budget is computed from the
	// live registry every time, so an unfinished child holds its whole cap
	// while a finished child only charges what it actually used. No
	// reservation field can drift, and a crash cannot leak budget.
	exec := a.execution()
	windowSteps := exec.MaxSteps
	if windowSteps <= 0 {
		windowSteps = 6
	}
	remaining := remainingBudget(parent, st.book.List(""))
	if remaining <= 0 {
		return task.Task{}, errors.New("父任务步骤预算已经用完，不能再派工")
	}
	if windowSteps > remaining {
		windowSteps = remaining
	}
	deadline := parent.Deadline
	return st.book.Create(task.Task{
		ID: childID, OwnerID: parent.OwnerID, BotID: target.ID, SessionID: sessionID,
		ProjectID: parent.ProjectID, ServerID: parent.ServerID,
		ParentID: parent.ID, ParentTurn: st.turn, ParentCallID: callID,
		ParentGoal: parentGoal,
		Goal:       goal, Status: task.StatusPaused, AcceptancePending: true,
		// Provenance is inherited, not recomputed: a child of an inbox task is
		// still a background task, and a child of an untrusted message stays
		// untrusted with the same hop depth. The runtime still forces the
		// worker capability by ParentID, which is the narrower of the two.
		Purpose: parent.Purpose, Hop: parent.Hop, Untrusted: parent.Untrusted,
		Checkpoint: task.Checkpoint{Messages: messages},
		// StepBudget is this window's model calls; MaxSteps is the child's
		// whole-task cap, bounded by what the parent has left.
		StepBudget: windowSteps, MaxSteps: remaining, MaxAttempts: 2,
		Deadline: deadline,
		// The child inherits the parent's frozen authorization options
		// (approval mode, routes, workspace, excluded bots) but keeps its own
		// approvals and journal.
		OptionsJSON: parent.OptionsJSON,
	})
}

// spentSubtree is the actual steps a finished task and its descendants used.
func spentSubtree(it task.Task, children map[string][]task.Task) int {
	used := it.TotalSteps
	for _, child := range children[it.ID] {
		used += spentSubtree(child, children)
	}
	return used
}

// remainingBudget is the capacity a task can still delegate: its MaxSteps minus
// its own steps, every live child's whole reserved cap, and every finished
// child's actual subtree cost. It matches Book.ReserveModelStep's accounting,
// so the child cap the delegation writes is exactly what the registry will let
// that child spend. A live child therefore never has to compete with its
// parent for the same steps.
func remainingBudget(parent task.Task, all []task.Task) int {
	if parent.MaxSteps <= 0 {
		return 0
	}
	byParent := make(map[string][]task.Task)
	for _, it := range all {
		if it.ParentID != "" {
			byParent[it.ParentID] = append(byParent[it.ParentID], it)
		}
	}
	used := parent.TotalSteps
	for _, child := range byParent[parent.ID] {
		if child.Terminal() {
			used += spentSubtree(child, byParent)
			continue
		}
		used += child.MaxSteps
	}
	return parent.MaxSteps - used
}

// parkOnChild records the parent's waiting state for this exact call. It is
// idempotent: a resumed window re-parks the same call without a second entry.
func (st *taskState) parkOnChild(call toolCall, hash, childID string) error {
	_, err := st.book.Update(st.id, func(it *task.Task) error {
		if !windowOwns(it) {
			// Parking is a window outcome. The one exception is the window
			// re-recording the very same park it already wrote (a resumed
			// window re-entering ask_agent): that is idempotent. A task the
			// operator paused, cancelled or re-queued is never revived as
			// waiting_job.
			if it.Status == task.StatusWaitingJob && it.Pending != nil &&
				it.Pending.Kind == delegateJobKind && it.Pending.JobRef == childID &&
				it.Pending.CallID == call.ID && it.Pending.Turn == st.turn &&
				it.Pending.ArgsHash == hash {
				return nil
			}
			return stopSignal(it.Status)
		}
		it.Status = task.StatusWaitingJob
		it.Pending = &task.Pending{
			Kind: delegateJobKind, CallID: call.ID, Name: call.Function.Name,
			ArgsHash: hash, Turn: st.turn, JobRef: childID,
			WakeAt: time.Now().UTC().Add(delegateWake),
		}
		entry := task.JournalEntry{
			At: time.Now().UTC(), Seq: len(it.Journal) + 1, Turn: st.turn,
			CallID: call.ID, Name: call.Function.Name, ArgsHash: hash, State: task.JournalWaiting,
		}
		for i := len(it.Journal) - 1; i >= 0; i-- {
			if it.Journal[i].Turn == st.turn && it.Journal[i].CallID == call.ID && it.Journal[i].ArgsHash == hash {
				it.Journal[i].State = task.JournalWaiting
				it.Journal[i].At = entry.At
				it.Journal[i].Name = entry.Name
				return nil
			}
		}
		it.Journal = append(it.Journal, entry)
		return nil
	})
	if err != nil {
		st.halted = true
	}
	return err
}

// ParentOf returns the task id a delegated child belongs to, or "" for a
// top-level task. The runtime uses it to skip child tasks in user-facing lists
// and to propagate pause/cancel.
func ParentOf(it task.Task) string { return it.ParentID }

// ChildTasks returns the children a parent task has created, oldest first.
// The parent and child link is immutable, so this is a stable view.
func (r *Runner) ChildTasks(parentID string) []task.Task {
	var out []task.Task
	for _, it := range r.Book.List("") {
		if it.ParentID == parentID {
			out = append(out, it)
		}
	}
	return out
}

// propagateToChildren applies fn to every non-terminal child of a parent and
// stops the child's in-flight goroutine. It is how pause and cancel reach a
// child whose parent stopped: the child has its own runner slot, so it will
// not stop by itself. Errors are returned, never swallowed into a success.
func (r *Runner) propagateToChildren(parentID string, fn func(*task.Task) error) error {
	for _, child := range r.ChildTasks(parentID) {
		if child.Terminal() {
			continue
		}
		if _, err := r.Book.Update(child.ID, fn); err != nil && !errors.Is(err, task.ErrNotFound) {
			return err
		}
		// Stopping the status is not enough: a child already inside a model
		// or tool call keeps running until its context is cancelled.
		r.cancelHandle(child.ID)
	}
	return nil
}

// PropagatePause pauses every child of a paused parent.
func (r *Runner) PropagatePause(parentID string) error {
	return r.propagateToChildren(parentID, func(t *task.Task) error {
		if t.Terminal() {
			return nil
		}
		t.Status = task.StatusPaused
		return nil
	})
}

// PropagateCancel cancels every child of a cancelled parent. A cancelled
// child's runner sees the status at its next checkpoint and stops.
func (r *Runner) PropagateCancel(parentID string) error {
	return r.propagateToChildren(parentID, func(t *task.Task) error {
		if t.Terminal() {
			return nil
		}
		t.Status = task.StatusCancelled
		t.Pending = nil
		return nil
	})
}
