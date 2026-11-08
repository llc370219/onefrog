package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/task"
)

// Durable task execution. A task owns its conversation window, journal and
// pending state in the task registry; the HTTP request that created it is not
// part of the loop, so a dropped connection never cancels the work.

type taskStateKey struct{}

func taskFrom(ctx context.Context) *taskState {
	st, _ := ctx.Value(taskStateKey{}).(*taskState)
	return st
}

func withTaskState(ctx context.Context, st *taskState) context.Context {
	return context.WithValue(ctx, taskStateKey{}, st)
}

// errSuspended stops the loop without executing anything further: the task is
// left in its stored waiting state.
var errSuspended = errors.New("任务已挂起，等待处理")

// ErrTaskSuspended lets a transport tell a parked task from a failure.
var ErrTaskSuspended = errSuspended

// errCancelled is returned when the operator cancelled the task.
var errCancelled = errors.New("任务已取消")

// errPaused is returned when the operator paused the task at a checkpoint.
var errPaused = errors.New("任务已暂停")

// errBudget reports that the task-wide model-step budget is used up. The
// terminal status is already committed when it is returned.
var errBudget = errors.New("任务的模型步骤总预算已经用完")

// mutatingTools change state outside the task. They are what makes a bare
// closing answer unverified rather than a completed goal.
var mutatingTools = map[string]bool{
	"run_command": true, "connect_server": true, "install_probe": true, "panel": true,
	"workspace": true, "develop_module": true, "manage_module_record": true,
	"manage_modules": true, "generate_secret": true, "request_secret": true,
	"message_agent": true,
	"job_start":     true, "job_cancel": true,
}

// toolMutates reports whether a call may change remote or panel state. The
// workspace tool only mutates on write/edit/run: a read/list/search/status/
// diff is an observation, and treating it as a mutation would both demand a
// pointless verification after a pure read and make "verified" evidence
// impossible to establish.
func toolMutates(call toolCall) bool {
	if call.Function.Name == "workspace" {
		var args struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
		switch strings.ToLower(strings.TrimSpace(args.Action)) {
		case "write", "edit", "run":
			return true
		}
		return false
	}
	return toolMutatesName(call.Function.Name)
}

// toolMutatesName is the name-only form used when scanning a stored journal.
// It is conservative for the journal (a workspace row may be a read): the
// executed row's action is recovered from the stored result text when it
// matters, see workspaceEntryReadOnly.
func toolMutatesName(name string) bool { return mutatingTools[name] }

// workspaceEntryReadOnly reports whether a stored workspace journal row was a
// read-only action. The row's arguments are not stored, so the decision reads
// the tool result: read actions return revision/text/lines headers, writes and
// edits start with 已写入/已编辑, and runs with stdout/stderr/退出码.
func workspaceEntryReadOnly(result string) bool {
	head := result
	if i := strings.IndexByte(head, '\n'); i >= 0 {
		head = head[:i]
	}
	switch {
	case strings.HasPrefix(head, "已写入 "), strings.HasPrefix(head, "已编辑 "):
		return false
	case strings.HasPrefix(head, "stdout:"), strings.HasPrefix(head, "stderr:"), strings.HasPrefix(head, "命令执行完成"):
		return false
	case strings.HasPrefix(head, "文件 "), strings.HasPrefix(head, "工作区根目录共列出"), strings.HasPrefix(head, "包含 "):
		return true
	}
	// Unrecognized shape: be conservative and treat it as a mutation.
	return false
}

// journalMutated reports whether an earlier window already changed something.
// A resumed window must not count as side-effect free just because its own
// journal is empty.
//
// Only an entry the registry marks as executed counts. A refusal, a skipped
// batch call and an unknown (interrupted) outcome all store a result but none
// of them proves anything ran. The flag is typed metadata
// (task.JournalEntry.Executed), never a marker inside the result text: tool
// output is untrusted and could contain any marker we invented.
func journalMutated(rows []task.JournalEntry) bool {
	for _, e := range rows {
		if e.State == task.JournalDone && mutatingTools[e.Name] && e.Executed {
			return true
		}
	}
	return false
}

// journalUnknown reports whether a mutating call may have taken effect without
// reporting. It stays true for the rest of the task, across windows and
// restarts, so a later window cannot quietly claim the goal was verified.
func journalUnknown(rows []task.JournalEntry) bool {
	for _, e := range rows {
		if mutatingTools[e.Name] && (e.Outcome == task.OutcomeUnknown ||
			e.State == task.JournalStarted || e.State == task.JournalInterrupted) {
			return true
		}
	}
	return false
}

// journalMutationSeq returns the highest journal Seq of an executed mutating
// call. A resumed window uses it so verification evidence recorded before
// that mutation cannot satisfy finish.
func journalMutationSeq(rows []task.JournalEntry) int {
	last := 0
	for _, e := range rows {
		if e.State != task.JournalDone || !e.Executed {
			continue
		}
		if toolMutatesName(e.Name) && !(e.Name == "workspace" && workspaceEntryReadOnly(e.Result)) {
			last = e.Seq
		}
	}
	return last
}

// plainResult returns a stored result unchanged. It exists so call sites read
// naturally while the executed state lives in typed metadata.
func plainResult(out string) string { return out }

type taskState struct {
	book     *task.Book
	id       string
	step     int
	maxSteps int
	halted   bool
	finished bool
	result   string
	// track records safe activity events for the task card. Nil disables it.
	track *ActivityTracker
	// jobParked marks that execCall parked this task on a native background
	// job (job_wait): execute() must not close the started entry again, which
	// would flip the parked task back to running.
	jobParked bool
	// mutated records that this window changed something, so a bare closing
	// answer cannot be reported as a completed goal.
	mutated bool
	// unknown records that a mutating call may have taken effect without
	// reporting (started, then the process died). The task may not claim
	// success until the effect is verified.
	unknown bool
	// checked records that a read-only call ran after an unknown outcome, so
	// the model has actually looked at the state instead of assuming.
	checked bool
	// lastMutationSeq is the journal Seq of the most recent executed mutating
	// call (this window or an earlier one). Verification evidence only counts
	// when recorded after this sequence, so a later mutation always
	// invalidates earlier "verified" claims.
	lastMutationSeq int
	// verifiedSeq is the journal Seq of the read-only observation that
	// verified the state after lastMutationSeq. finish requires
	// verifiedSeq > lastMutationSeq when the task mutated anything.
	verifiedSeq int
	// lastCallID is the journal identity of the call execute() just closed;
	// it locates the row whose Seq anchors mutation/verification evidence.
	lastCallID string
	// finalMu serialises the read-then-append of the closing transcript
	// message against the repair scan. It is the runner's lock, so a window
	// finishing a task and the periodic repair can never write at once.
	finalMu *sync.Mutex
	// turn is the assistant response currently being executed. A journal entry
	// is identified by (turn, call id, args hash), never by call id alone, so a
	// later turn that reuses a call id cannot inherit an old result.
	turn int
	// decision is the operator's answer for the pending approval on resume.
	decision *task.Approval
	// parked is the request that stopped the loop, kept so the transport can
	// show the same card it asked about.
	parked *ApprovalRequest
	// current is the call being authorized, so a parked approval records the
	// real call id and argument hash instead of the question text.
	current *toolCall
	// outcome is how the window ended, kept so the runner sees WindowFull and
	// Final instead of re-deriving them from the stored status.
	outcome taskOutcome
	// approvingTool is the tool name of the call the operator approved, used
	// together with the decision's call id and argument hash so only that
	// exact call inherits the approval.
	approvingTool string
	// delegateCall runs a delegated child task for ask_agent. It is installed
	// by the delegation layer; until then ask_agent is treated as an ordinary
	// tool call so a child job reference is never mistaken for an approval.
	delegateCall func(ctx context.Context, a *Agent, speaker model.Bot, creds credentials, serverID string, call toolCall, tr *trace) (*string, error)
}

func (st *taskState) stopped() bool { return st.halted || st.finished }

// countStep reserves one model step durably before the call is made, so a
// crash or a park still counts the step that was consumed. It also advances
// the turn counter journal entries are bound to.
func (st *taskState) countStep(ctx context.Context) error {
	// The registry reserves the step atomically under its own lock and walks
	// the parent chain in memory. Doing this here with Book.Update plus
	// Book.List would deadlock, and computing the budget outside the lock
	// would let parent and children overspend the shared cap.
	it, exhausted, err := st.book.ReserveModelStep(st.id, st.step)
	if errors.Is(err, task.ErrInvalid) {
		// The task is not running any more: paused or cancelled while the
		// window was between calls.
		st.halted = true
		return errPaused
	}
	if err != nil {
		st.halted = true
		return err
	}
	if exhausted {
		st.halted = true
		return errBudget
	}
	st.turn = it.Turns
	return nil
}

// saveFinal records the closing assistant message durably, then writes it to
// the transcript.
//
// Everything that makes the answer durable happens in ONE registry write: the
// checkpoint, the final text, the deterministic message id and the terminal
// status. A crash before it leaves the task running, so the window is simply
// redone; a crash after it leaves a completed task that knows exactly what to
// say and which transcript id to use. The transcript write is the only step
// left, it is idempotent, and a periodic scan repairs it if it never happened.
// There is no window in which a task is completed but its answer is lost.
func (st *taskState) saveFinal(ctx context.Context, a *Agent, messages []map[string]any, text string) error {
	messageID := st.finalMessageID()
	if st.mutated || st.unknown {
		// The task changed something (or may have), so a bare closing reply is
		// not an answer: it must be confirmed with finish. Record the text as
		// progress and let the window logic continue or ask the operator.
		return st.saveProgress(messages, text)
	}
	if messages != nil {
		withReply := append(append([]map[string]any{}, messages...), map[string]any{"role": "assistant", "content": text})
		messages = withReply
	}
	_, err := st.book.Update(st.id, func(it *task.Task) error {
		// An operator action or a park wins: never complete a task the
		// operator paused, cancelled or re-queued, and never overwrite a
		// waiting state.
		if !windowOwns(it) {
			return errFinalBlocked
		}
		if messages != nil {
			it.Checkpoint.Messages = messages
		}
		it.Checkpoint.Next = clip(text, task.MaxLine)
		it.Next = clip(text, task.MaxLine)
		if text != "" {
			it.Progress = append(it.Progress, clip(text, task.MaxLine))
		}
		it.Result = clip(text, task.MaxResult)
		it.ResultMessageID = messageID
		it.Status = task.StatusCompleted
		it.FinishedAt = time.Now().UTC()
		it.Pending = nil
		return nil
	})
	if errors.Is(err, errFinalBlocked) {
		st.halted = true
		return errSuspended
	}
	if err != nil {
		st.halted = true
		return err
	}
	st.finished = true
	// The task is complete either way; a failed transcript write is repaired
	// by a later scan, so it must not turn a finished task into a failed one.
	if werr := st.writeFinal(ctx, a, text, messageID); werr != nil {
		log.Printf("task %s: write final message: %v", st.id, werr)
	}
	return nil
}

// errFinalBlocked means the task stopped being runnable while its closing
// message was being written.
var errFinalBlocked = errors.New("任务已不再运行")

// saveProgress records a non-final assistant note. It deliberately does NOT
// set ResultMessageID: the text is a progress step of a task that will keep
// going, so it is not the answer a consumer should read.
func (st *taskState) saveProgress(messages []map[string]any, text string) error {
	if messages != nil {
		messages = append(append([]map[string]any{}, messages...), map[string]any{"role": "assistant", "content": text})
	}
	_, err := st.book.Update(st.id, func(it *task.Task) error {
		// A window that no longer owns the task must not write progress over
		// the state the operator just set (paused, cancelled, re-queued after
		// a quick pause/resume, or parked).
		if !windowOwns(it) {
			return errFinalBlocked
		}
		if messages != nil {
			it.Checkpoint.Messages = messages
		}
		it.Checkpoint.Next = clip(text, task.MaxLine)
		it.Next = clip(text, task.MaxLine)
		return nil
	})
	if errors.Is(err, errFinalBlocked) {
		st.halted = true
		return errSuspended
	}
	if err != nil {
		st.halted = true
	}
	return err
}

// finalMessageID is a stable id for a task's closing transcript message. It is
// derived, not random, so a retried write can detect the message it already
// appended.
func (st *taskState) finalMessageID() string {
	sum := sha256.Sum256([]byte("final\x00" + st.id + "\x00" + strconv.Itoa(st.turn)))
	return "task-final-" + hex.EncodeToString(sum[:12])
}

// writeFinal appends the closing message to the transcript exactly once. The
// session is read first: the deterministic id makes the check cheap and safe
// even after a crash between the task write and the transcript write.
func (st *taskState) writeFinal(ctx context.Context, a *Agent, text, messageID string) error {
	session := taskSession(ctx)
	if session == "" || text == "" {
		return nil
	}
	if st.finalMu != nil {
		st.finalMu.Lock()
		defer st.finalMu.Unlock()
	}
	if msgs, err := a.Store.ListMessages(ctx, session); err == nil {
		for _, m := range msgs {
			if m.ID == messageID {
				return nil
			}
		}
	} else {
		// Cannot prove the message is absent; do not risk a duplicate.
		return err
	}
	if _, err := a.Store.AddMessage(ctx, model.Message{ID: messageID, SessionID: session, Role: "assistant", Content: text}); err != nil {
		// A duplicate id from another writer is the only benign conflict: the
		// message is already there.
		if msgs, listErr := a.Store.ListMessages(ctx, session); listErr == nil {
			for _, m := range msgs {
				if m.ID == messageID {
					return nil
				}
			}
		}
		return err
	}
	return nil
}

// repairFinals writes the closing message of every completed task whose
// transcript entry is missing. It runs on each scan, not just at startup: a
// transient transcript error, a crash between the task write and the transcript
// write, and a process that died before its first scan are all repaired the
// same way. The deterministic id makes the repair idempotent.
func (r *Runner) repairFinals() {
	for _, it := range r.Book.List("") {
		if it.Status == task.StatusCompleted {
			r.repairFinal(it)
		}
	}
}

// repairFinal writes a stored closing message whose transcript entry is
// missing.
func (r *Runner) repairFinal(it task.Task) {
	if it.ResultMessageID == "" || strings.TrimSpace(it.Result) == "" {
		return
	}
	ctx := context.WithValue(r.baseOrBackground(), taskSessionKey{}, it.SessionID)
	st := &taskState{book: r.Book, id: it.ID, turn: it.Turns, finalMu: &r.finalMu}
	if err := st.writeFinal(ctx, r.Agent, it.Result, it.ResultMessageID); err != nil {
		log.Printf("tasks: repair final %s: %v", it.ID, err)
	}
}

type taskSessionKey struct{}

func taskSession(ctx context.Context) string {
	s, _ := ctx.Value(taskSessionKey{}).(string)
	return s
}

// taskApprover is the Options.Approver used inside a durable task. It never
// blocks on a socket: it records the question and asks the runtime to park.
// The only allow it gives itself is for the exact call the operator approved.
func (st *taskState) taskApprover() Approver {
	return func(ctx context.Context, req ApprovalRequest) (bool, string) {
		// The approval only covers the exact call the operator decided on:
		// same call id, same tool and same argument hash. A display label or a
		// reused call id is never enough, so a nested or re-issued call in the
		// same window still needs its own decision.
		if cur, ok := callIdentityOf(ctx); ok && st.decision != nil &&
			st.decision.Status == task.ApprovalAllowed &&
			cur.CallID != "" && cur.CallID == st.decision.CallID &&
			cur.Hash == st.decision.ArgsHash &&
			(cur.Tool == "" || cur.Tool == st.approvingTool) {
			return true, "用户已批准本次操作"
		}
		if st.parkedPending() {
			// Already parked by this call: do not add a second question.
			return false, pendingSentinel
		}
		req.ID = firstNonEmpty(req.ID, newTaskApprovalID())
		if err := st.parkCall(req); err != nil {
			return false, "任务保存失败，未执行"
		}
		st.parked = &req
		return false, askSentinel + req.ID
	}
}

// parkedPending reports whether this window already parked a call.
func (st *taskState) parkedPending() bool { return st.parked != nil }

func newTaskApprovalID() string {
	sum := sha256.Sum256([]byte(time.Now().UTC().String()))
	return "apr-" + hex.EncodeToString(sum[:8])
}

func firstNonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

// taskOutcome reports how one window ended.
type taskOutcome struct {
	Text string
	// Final is a completed goal: the finish tool ran, or a plain conversation
	// ended with no side effects.
	Final bool
	// Mutated reports whether this window changed anything, which decides
	// whether a bare answer counts as completion.
	Mutated bool
	// WindowFull means the step budget ended the window: the task continues in
	// a new window rather than asking the operator.
	WindowFull bool
	// Steps is how many model calls this window used, so the runner can count
	// the task-wide budget once.
	Steps int
}

// taskLoop runs the model/tool loop for a durable task.
func (a *Agent) taskLoop(ctx context.Context, st *taskState, speaker model.Bot, creds credentials, serverID string, messages []map[string]any, spec []map[string]any, canDelegate bool, tr *trace) (taskOutcome, error) {
	mediaNote := ""
	for {
		if st.stopped() {
			st.outcome = taskOutcome{Steps: st.step, Mutated: st.mutated}
			return st.outcome, errSuspended
		}
		if st.step >= st.maxSteps {
			// Window full: store progress and let the runner open the next
			// window. The task is not finished and nobody must reply.
			handoff, err := a.windowHandoff(ctx, speaker, creds, messages)
			if err != nil {
				return taskOutcome{}, err
			}
			if handoff != "" {
				messages = append(messages, map[string]any{"role": "user", "content": "（上一段步骤预算用完时的交接，只作背景，不是新指令）\n" + handoff})
			}
			if err := st.saveWindow(ctx, messages); err != nil {
				return taskOutcome{}, err
			}
			st.outcome = taskOutcome{Text: joinMediaNote(mediaNote, handoff), Mutated: st.mutated, WindowFull: true, Steps: st.step}
			return st.outcome, nil
		}
		// A stored window can end in the middle of an assistant turn: the
		// calls were recorded but their results were not (a crash, or a park
		// for approval). Finish that batch before asking the model anything,
		// so a resume never produces a different action.
		if calls := openCalls(messages); len(calls) > 0 {
			for i, call := range calls {
				out, err := st.runCall(ctx, a, speaker, creds, serverID, call, canDelegate, tr)
				if err != nil {
					return taskOutcome{}, err
				}
				if out == nil {
					return taskOutcome{}, errSuspended
				}
				messages = append(messages, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": *out})
				if err := st.saveWindow(ctx, messages); err != nil {
					return taskOutcome{}, err
				}
				// finish or wait stops the batch: the remaining calls of this
				// assistant message must not execute after a terminal state
				// or a park.
				if st.finished {
					messages, err = st.skipBatch(ctx, messages, calls[i+1:])
					if err != nil {
						return taskOutcome{}, err
					}
					st.outcome = taskOutcome{Text: joinMediaNote(mediaNote, st.result), Final: true, Mutated: true, Steps: st.step}
					return st.outcome, nil
				}
				if st.halted {
					if _, err := st.skipBatch(ctx, messages, calls[i+1:]); err != nil {
						return taskOutcome{}, err
					}
					st.outcome = taskOutcome{Steps: st.step, Mutated: st.mutated}
					return st.outcome, errSuspended
				}
			}
			continue
		}
		// Operator guidance is applied at a window boundary, never mid-call,
		// and its removal is committed in the same write as the window.
		next, err := st.takeGuidance(ctx, messages)
		if err != nil {
			return taskOutcome{}, err
		}
		messages = next
		// A card answer is the operator's next turn. It is appended once, as a
		// user message, and cleared in the same write so a restart cannot
		// apply it twice.
		answered, err := st.takeAnswer(ctx, messages)
		if err != nil {
			return taskOutcome{}, err
		}
		messages = answered
		st.step++
		if err := st.countStep(ctx); err != nil {
			return taskOutcome{}, err
		}
		st.track.modelStep("模型步骤")
		msg, err := a.nextModelTurn(ctx, speaker, creds, messages, spec)
		if err != nil {
			return taskOutcome{}, err
		}
		if note, rest := peelMediaNote(msg.Content); note != "" {
			mediaNote = note
			msg.Content = rest
		}
		if len(msg.ToolCalls) == 0 {
			text := strings.TrimSpace(a.redact(ctx, msg.Content))
			// Store the final assistant reply before the task leaves running.
			if err := st.saveFinal(ctx, a, messages, text); err != nil {
				return taskOutcome{}, err
			}
			st.outcome = taskOutcome{Text: joinMediaNote(mediaNote, text), Mutated: st.mutated, Steps: st.step}
			return st.outcome, nil
		}
		if tr != nil && !isWorker(ctx) && !hasInteractiveCardCall(msg.ToolCalls) {
			if say := strings.TrimSpace(a.redact(ctx, msg.Content)); say != "" {
				tr.emit(Step{Kind: "say", Bot: speaker.Name, Output: say})
			}
		}
		entry := map[string]any{"role": "assistant", "content": msg.Content, "tool_calls": msg.ToolCalls}
		if len(msg.ResponseItems) > 0 {
			entry["response_items"] = msg.ResponseItems
		}
		messages = append(messages, entry)
		if err := st.saveWindow(ctx, messages); err != nil {
			return taskOutcome{}, err
		}
		for i, call := range msg.ToolCalls {
			out, err := st.runCall(ctx, a, speaker, creds, serverID, call, canDelegate, tr)
			if err != nil {
				return taskOutcome{}, err
			}
			if out == nil {
				return taskOutcome{}, errSuspended
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": *out})
			if err := st.saveWindow(ctx, messages); err != nil {
				return taskOutcome{}, err
			}
			// finish ends the task and wait parks it. Any further call in the
			// same assistant batch would be a side effect after the decision,
			// so the batch stops here with a recorded result for this call.
			if st.finished {
				messages, err = st.skipBatch(ctx, messages, msg.ToolCalls[i+1:])
				if err != nil {
					return taskOutcome{}, err
				}
				st.outcome = taskOutcome{Text: joinMediaNote(mediaNote, st.result), Final: true, Mutated: true, Steps: st.step}
				return st.outcome, nil
			}
			if st.halted {
				if _, err := st.skipBatch(ctx, messages, msg.ToolCalls[i+1:]); err != nil {
					return taskOutcome{}, err
				}
				st.outcome = taskOutcome{Steps: st.step, Mutated: st.mutated}
				return st.outcome, errSuspended
			}
		}
		if st.finished {
			st.outcome = taskOutcome{Text: joinMediaNote(mediaNote, st.result), Final: true, Mutated: true, Steps: st.step}
			return st.outcome, nil
		}
		if st.stopped() {
			st.outcome = taskOutcome{Steps: st.step, Mutated: st.mutated}
			return st.outcome, errSuspended
		}
		if tr != nil && (tr.choice != nil || tr.secret != nil) && !isWorker(ctx) {
			st.outcome = taskOutcome{Text: joinMediaNote(mediaNote, msg.Content), Mutated: st.mutated, Steps: st.step}
			return st.outcome, nil
		}
	}
}

// skipBatch records a deterministic "not executed" result for the calls of a
// batch that stopped early (finish, wait, pause or a park). Without it the
// checkpoint would keep unanswered tool calls and a restart would execute
// them after the task had already ended or parked.
func (st *taskState) skipBatch(ctx context.Context, messages []map[string]any, rest []toolCall) ([]map[string]any, error) {
	if len(rest) == 0 {
		return messages, nil
	}
	for _, call := range rest {
		out := "未执行：同一批次的 " + call.Function.Name + " 没有运行（上一步已经结束或挂起这批调用）。如果要继续，请在下一步重新发起。"
		// The skipped call gets its own journal row but must not touch status or
		// Pending: the task is already completed or parked on a different call.
		if err := st.markSkipped(call, out); err != nil {
			return nil, err
		}
		messages = append(messages, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": out})
	}
	if err := st.saveWindow(ctx, messages); err != nil {
		return nil, err
	}
	return messages, nil
}

// markSkipped records a batch call that was deliberately not executed.
func (st *taskState) markSkipped(call toolCall, out string) error {
	_, err := st.book.Update(st.id, func(it *task.Task) error {
		if it.Terminal() {
			return nil
		}
		// The skip is this window closing its own batch: it is only legitimate
		// while the window still owns the task, or for the park/finish it just
		// recorded. A pause, cancel or pause-then-resume wins.
		if !windowActive(it) {
			return nil
		}
		it.Journal = append(it.Journal, task.JournalEntry{
			At: time.Now().UTC(), Seq: len(it.Journal) + 1, Turn: st.turn,
			CallID: call.ID, Name: call.Function.Name, ArgsHash: argsHash(call),
			State: task.JournalDone, Result: out,
		})
		return nil
	})
	if err != nil {
		st.halted = true
	}
	return err
}

// takeGuidance appends the oldest operator instruction and removes it from the
// stored queue in the same update, so guidance cannot be lost or applied twice.
func (st *taskState) takeGuidance(ctx context.Context, messages []map[string]any) ([]map[string]any, error) {
	it, err := st.book.Get(st.id)
	if err != nil {
		return nil, err
	}
	if len(it.Guidance) == 0 {
		return messages, nil
	}
	first := it.Guidance[0]
	next := append(append([]map[string]any{}, messages...), map[string]any{"role": "user", "content": "（用户补充的引导，只作背景）\n" + first})
	if _, err := st.book.Update(st.id, func(t *task.Task) error {
		if len(t.Guidance) > 0 {
			t.Guidance = t.Guidance[1:]
		}
		t.Checkpoint.Messages = next
		t.Step = st.step
		return nil
	}); err != nil {
		st.halted = true
		return nil, err
	}
	return next, nil
}

// takeAnswer appends a stored card answer as the operator's next user message
// and clears it in the same write, so it can never be applied twice. An answer
// is only ever a user message: it is not routed through the approval path.
func (st *taskState) takeAnswer(ctx context.Context, messages []map[string]any) ([]map[string]any, error) {
	it, err := st.book.Get(st.id)
	if err != nil {
		return nil, err
	}
	if it.Pending == nil || it.Pending.Kind != task.PendingUser || strings.TrimSpace(it.Pending.Answer) == "" {
		return messages, nil
	}
	answer := it.Pending.Answer
	next := append(append([]map[string]any{}, messages...), map[string]any{"role": "user", "content": answer})
	if _, err := st.book.Update(st.id, func(t *task.Task) error {
		if t.Pending != nil && t.Pending.Kind == task.PendingUser && t.Pending.Answer == answer {
			t.Pending = nil
		}
		if t.Status == task.StatusWaitingUser {
			t.Status = task.StatusRunning
		}
		t.Checkpoint.Messages = next
		t.Step = st.step
		return nil
	}); err != nil {
		st.halted = true
		return nil, err
	}
	return next, nil
}

func (a *Agent) nextModelTurn(ctx context.Context, speaker model.Bot, creds credentials, messages []map[string]any, spec []map[string]any) (assistantMsg, error) {
	purpose := "conversation"
	if actor, ok := ctx.Value(usageKey{}).(usageActor); ok {
		purpose = actor.Purpose
	}
	callCtx := usageContext(ctx, speaker.ID, purpose, creds.provider)
	return a.chat(callCtx, creds.base, creds.model, creds.key, messages, spec)
}

// windowHandoff closes one step-budget window with a deterministic handoff.
// It never calls the model: the window budget counts model steps, so an extra
// request here would be an uncounted step that the task-wide budget never
// saw. The next window starts from the same stored plan and progress.
func (a *Agent) windowHandoff(ctx context.Context, speaker model.Bot, creds credentials, messages []map[string]any) (string, error) {
	facts := budgetHandoff(messages)
	summary := "这一段步骤预算用完，任务会在新的一段继续。"
	if facts != "" {
		summary += "\n\n已完成：\n" + facts
	}
	if err := a.setHandoff(ctx, summary); err != nil {
		return summary, err
	}
	return summary, nil
}

func (st *taskState) saveWindow(ctx context.Context, messages []map[string]any) error {
	_, err := st.book.Update(st.id, func(it *task.Task) error {
		// A window that parked or finished still closes its own checkpoint, but
		// one the operator paused, cancelled or re-queued must not overwrite
		// that state with a stale checkpoint.
		if !windowActive(it) {
			if it.Status == task.StatusCancelled {
				return errCancelled
			}
			return errPaused
		}
		it.Checkpoint.Messages = messages
		it.Step = st.step
		return nil
	})
	if errors.Is(err, errCancelled) {
		st.finished = true
		return err
	}
	if errors.Is(err, errPaused) {
		st.halted = true
		return err
	}
	if err != nil {
		st.halted = true
	}
	return err
}

// runCall executes one tool call durably.
//
// Identity is (turn, call id, args hash). A journal entry from an earlier turn
// that happens to reuse a call id is never treated as this call.
func (st *taskState) runCall(ctx context.Context, a *Agent, speaker model.Bot, creds credentials, serverID string, call toolCall, canDelegate bool, tr *trace) (*string, error) {
	it, err := st.book.Get(st.id)
	if err != nil {
		st.halted = true
		return nil, err
	}
	switch it.Status {
	case task.StatusCancelled:
		st.finished = true
		return nil, errCancelled
	case task.StatusPaused:
		st.halted = true
		return nil, errPaused
	case task.StatusQueued:
		// The operator paused and immediately resumed while this window was
		// still unwinding: the task was re-queued for a new window, so this one
		// must not execute anything else.
		st.halted = true
		return nil, errPaused
	}
	if !windowOwns(&it) {
		// Terminal (finished or expired) or parked: no new side effect belongs
		// to a window that no longer owns the task.
		st.halted = true
		st.finished = it.Terminal()
		return nil, errPaused
	}
	hash := argsHash(call)
	// A delegated child is not an approval and not a stored tool result: the
	// parent parks on the child job, so it must be intercepted before the
	// journal switch would read a child reference as a parked call.
	if st.delegateCall != nil && (call.Function.Name == "ask_agent" || call.Function.Name == "hand_off") {
		return st.delegateCall(ctx, a, speaker, creds, serverID, call, tr)
	}
	prior := findEntry(it.Journal, st.turn, call.ID, hash)
	if prior != nil {
		switch prior.State {
		case task.JournalDone, task.JournalFailed:
			out := plainResult(prior.Result)
			return &out, nil
		case task.JournalStarted, task.JournalInterrupted:
			// Outcome unknown: the call started and the process stopped before
			// it reported. Report that, and record it as an unknown outcome so
			// the task must verify the effect before it can claim completion.
			out := interruptedResult(call, *prior)
			if toolMutates(call) {
				st.unknown = true
			}
			if err := st.markEntryUnknown(prior, out); err != nil {
				return nil, err
			}
			return &out, nil
		case task.JournalWaiting:
			return st.resumeParked(ctx, a, speaker, creds, serverID, call, canDelegate, tr, prior)
		}
	}
	return st.execute(ctx, a, speaker, creds, serverID, call, canDelegate, tr)
}

// resumeParked handles a call that was stored as waiting for the operator.
// A recorded allow runs it exactly once; anything else becomes the result.
func (st *taskState) resumeParked(ctx context.Context, a *Agent, speaker model.Bot, creds credentials, serverID string, call toolCall, canDelegate bool, tr *trace, prior *task.JournalEntry) (*string, error) {
	decision := st.decision
	st.decision = nil
	if decision == nil {
		// No decision is stored for this parked call. That is not a refusal:
		// re-park so the operator still sees the same question, and never
		// record a denial the operator did not give.
		st.halted = true
		return nil, nil
	}
	// An approval is bound to the exact call that asked: same call id, same
	// argument hash. A decision recorded for different arguments is not an
	// approval of this call, so it is parked again instead of executed.
	if decision.CallID != call.ID || decision.ArgsHash != argsHash(call) {
		st.halted = true
		if _, err := st.book.Update(st.id, func(t *task.Task) error {
			// Re-parking is a window outcome; a task the operator already
			// paused, cancelled or re-queued keeps that state.
			if !windowOwns(t) {
				return nil
			}
			t.Status = task.StatusWaitingApproval
			if t.Pending != nil {
				t.Pending.ApprovalID = ""
			}
			return nil
		}); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if decision.Status != task.ApprovalAllowed {
		out := "用户没有批准这次操作，已跳过。不要换一种写法重试同一操作；请说明情况并询问下一步。"
		if decision.Note != "" {
			out += "（" + a.redact(ctx, decision.Note) + "）"
		}
		if err := st.markEntry(prior, task.JournalDone, out); err != nil {
			return nil, err
		}
		return &out, nil
	}
	// Move the entry back to started (not appended), then run the approved
	// call with a permit bound to this exact call only.
	if err := st.markEntry(prior, task.JournalStarted, ""); err != nil {
		return nil, err
	}
	// The permit must carry the target the shared hook resolves for this call;
	// using the fallback server would re-ask forever when the call targets
	// another machine than the one selected this turn.
	target := serverID
	if resolved, ok := a.effectiveServer(ctx, serverID, call); ok {
		target = resolved
	}
	ctx = withApprovedCall(withCurrentCall(ctx, call.ID), call.ID)
	ctx = withCallPermit(ctx, PermitForApprovedCall(speaker.ID, target, call))
	// The recorded decision was verified against this exact call above; hand
	// the handler the same exact-call grant the shared hook would have made,
	// so a handler that requires a grant (native local write/run, job
	// start/cancel) accepts an operator-approved call.
	ctx = withCallGrant(ctx, newCallGrant(speaker.ID, target, call, call.Function.Arguments))
	return st.executeApproved(ctx, a, speaker, creds, serverID, call, canDelegate, tr, prior)
}

// executeApproved runs a call whose approval is already recorded.
func (st *taskState) executeApproved(ctx context.Context, a *Agent, speaker model.Bot, creds credentials, serverID string, call toolCall, canDelegate bool, tr *trace, prior *task.JournalEntry) (*string, error) {
	st.current = &call
	defer func() { st.current = nil }()
	// The context already carries the approved-call identity and the one-shot
	// permit for exactly this call. Re-marking it here would drop the permit.
	out := a.execCall(ctx, speaker, creds, serverID, call, canDelegate, tr)
	if st.parked != nil {
		st.halted = true
		return nil, nil
	}
	// The call really ran: mark the stored result as executed so a later
	// window knows the task has changed something.
	if err := st.markEntryExecuted(prior, out); err != nil {
		return nil, err
	}
	if toolMutates(call) {
		st.mutated = true
	}
	return &out, nil
}

func (st *taskState) execute(ctx context.Context, a *Agent, speaker model.Bot, creds credentials, serverID string, call toolCall, canDelegate bool, tr *trace) (*string, error) {
	label := safeToolLabel(call)
	// Task-only tools (plan, checkpoint, wait, finish) are pure state changes.
	if out, handled, err := st.runTaskTool(ctx, a, call); handled {
		if err != nil {
			return nil, err
		}
		return &out, nil
	}
	st.current = &call
	defer func() { st.current = nil }()
	hash := argsHash(call)
	st.lastCallID = call.ID
	callCtx := withCallIdentity(withCurrentCall(ctx, call.ID), callIdentity{CallID: call.ID, Tool: call.Function.Name, Hash: hash})

	// Shared authorization runs before any intent is recorded. A park leaves
	// the task in waiting_approval and the call unexecuted; a refusal becomes
	// the tool result.
	if !hookAlreadyRan(callCtx, speaker.ID, call) {
		authorize := a.AuthorizeCall
		if authorize == nil {
			authorize = a.authorizeCall
		}
		next, blocked := authorize(callCtx, speaker, serverID, call)
		if st.parkedPending() {
			st.halted = true
			return nil, nil
		}
		if blocked != "" {
			if err := st.recordDone(call, hash, blocked); err != nil {
				return nil, err
			}
			return &blocked, nil
		}
		callCtx = next
	}
	if err := st.recordIntent(call, hash); err != nil {
		return nil, err
	}
	st.track.toolStart(call.Function.Name, label)
	out := a.execCall(MarkAuthorizedCall(callCtx, speaker.ID, serverID, call), speaker, creds, serverID, call, canDelegate, tr)
	st.track.toolEnd(call.Function.Name, label, ActivityCompleted)
	if st.jobParked {
		// job_wait parked the task on a real job; the park update already
		// closed the journal entry and set waiting_job. Just stop.
		st.jobParked = false
		st.halted = true
		return &out, nil
	}
	// The approver parked this call: the task is already waiting_approval and
	// must not be flipped back to running.
	if st.parked != nil {
		st.halted = true
		return nil, nil
	}
	// The call ran: mark its journal entry executed, so a resumed window can
	// tell a real side effect from a refusal or a skipped call.
	if err := st.closeOpenEntryExecuted(call.ID, hash, out); err != nil {
		return nil, err
	}
	// Only an executed call counts as a change; a refused or denied call did
	// not touch anything, so a bare closing answer after it is still a plain
	// conversation.
	if toolMutates(call) {
		st.mutated = true
		if seq := st.lastExecutedSeq(); seq > st.lastMutationSeq {
			st.lastMutationSeq = seq
			// A new mutation invalidates earlier verification evidence.
			st.verifiedSeq = 0
		}
	}
	if st.unknown && verificationLook(call, out) {
		// After an unknown outcome the model must actually look at the state
		// before it may close the task. Only an explicit read-only inspection
		// that really ran counts: a second mutating call is not a check, and
		// neither is a refused or failed one.
		st.checked = true
	}
	if !toolMutates(call) && verificationLook(call, out) {
		// A real read-only observation after the last mutation is verification
		// evidence for finish.
		if seq := st.lastExecutedSeq(); seq > st.verifiedSeq {
			st.verifiedSeq = seq
		}
	}
	return &out, nil
}

// lastExecutedSeq returns the journal sequence of the call this state just
// closed, looked up from the durable registry.
func (st *taskState) lastExecutedSeq() int {
	it, err := st.book.Get(st.id)
	if err != nil || len(it.Journal) == 0 {
		return 0
	}
	for i := len(it.Journal) - 1; i >= 0; i-- {
		if it.Journal[i].Turn == st.turn && it.Journal[i].CallID == st.lastCallID {
			return it.Journal[i].Seq
		}
	}
	return 0
}

// verificationLook reports whether a call is a read-only inspection whose
// result the model can use to reconcile an unknown outcome.
//
// It uses the same explicit allowlist the approval layer treats as read-only
// (plus memory recall), never a default of "not mutating". run_command is
// deliberately excluded: a shell command is only read-only by argument
// inspection, which is not strong enough evidence to call a failed restart
// verified. This is a conservative gate on which tools may satisfy it, not a
// proof that the reported state is correct.
func verificationLook(call toolCall, out string) bool {
	if strings.TrimSpace(out) == "" {
		return false
	}
	// A refusal or an execution failure is not an observation.
	if strings.Contains(out, pendingSentinel) || strings.Contains(out, askSentinel) ||
		strings.Contains(out, "未执行") || strings.Contains(out, "没有批准") {
		return false
	}
	switch call.Function.Name {
	case "recall", "inspect_services", "panel", "workspace", "manage_modules",
		"develop_module", "manage_module_record", "job_status":
		return readOnlyCall(call.Function.Name, call.Function.Arguments)
	}
	return false
}

// findEntry returns the journal entry matching (turn, call id, args hash).
//
// A started, interrupted or waiting entry for the same turn and call id is
// returned even when the stored hash differs: an entry written by an older
// process may not carry the same hash, and treating it as "not this call"
// would re-run a side effect whose outcome is unknown.
func findEntry(rows []task.JournalEntry, turn int, callID, hash string) *task.JournalEntry {
	var loose *task.JournalEntry
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Turn != turn || rows[i].CallID != callID {
			continue
		}
		if rows[i].ArgsHash == hash {
			e := rows[i]
			return &e
		}
		// Only an entry whose outcome is unknown may be matched by call id
		// alone: it must block a replay even though the stored hash differs.
		// A waiting entry with different arguments belongs to a different
		// call, and matching it would let changed arguments ride an old
		// approval.
		switch rows[i].State {
		case task.JournalStarted, task.JournalInterrupted:
			e := rows[i]
			loose = &e
		}
	}
	return loose
}

// markEntry rewrites one entry identified by its full identity, keeping the
// execution metadata it already carries.
func (st *taskState) markEntry(entry *task.JournalEntry, state, out string) error {
	return st.markEntryAt(entry, state, out, entry.Executed, entry.Outcome)
}

// markEntryExecuted records that the call really ran.
func (st *taskState) markEntryExecuted(entry *task.JournalEntry, out string) error {
	return st.markEntryAt(entry, task.JournalDone, out, true, task.OutcomeDone)
}

// markEntryUnknown records a call whose outcome can never be known: it started
// and the process stopped before it reported. The task may not claim success
// until the effect is verified.
func (st *taskState) markEntryUnknown(entry *task.JournalEntry, out string) error {
	return st.markEntryAt(entry, task.JournalDone, out, false, task.OutcomeUnknown)
}

// markEntryRefused records a call that was not executed because it was refused
// or skipped.
func (st *taskState) markEntryRefused(entry *task.JournalEntry, out, outcome string) error {
	return st.markEntryAt(entry, task.JournalDone, out, false, outcome)
}

func (st *taskState) markEntryAt(entry *task.JournalEntry, state, out string, executed bool, outcome string) error {
	// A window that no longer owns the task is stopping, not rolling back: a
	// call that really ran keeps its evidence, and the stop signal is returned
	// after the write. An error from Update would discard the row that proves
	// it, so the evidence is committed with a nil error and the stop is raised
	// outside the callback.
	var stopErr error
	_, err := st.book.Update(st.id, func(it *task.Task) error {
		if state == task.JournalStarted && !windowOwns(it) {
			// Starting a call is a window action. A call whose approval was just
			// resolved must not begin a side effect for a task the operator
			// already paused, cancelled, re-queued or parked.
			stopErr = stopSignal(it.Status)
			return nil
		}
		recordEntry(it, entry, state, out, executed, outcome)
		// The journal row is evidence; the status and Pending belong to the
		// window only while it still owns the task. A pause, cancel, park or
		// pause-then-resume that landed first must survive a late return.
		switch {
		case windowOwns(it):
			if state == task.JournalDone || state == task.JournalStarted {
				it.Pending = nil
			}
		case it.Status == task.StatusWaitingJob:
			// This window's own park on a child: consuming the finished child
			// resumes the parent. An operator park (approval/card) is never
			// cleared this way.
			it.Pending = nil
			it.Status = task.StatusRunning
		default:
			stopErr = stopSignal(it.Status)
		}
		return nil
	})
	if err != nil {
		st.halted = true
		return err
	}
	if stopErr != nil {
		st.halted = true
		st.finished = errors.Is(stopErr, errCancelled)
		return stopErr
	}
	return nil
}

// stopSignal is the error a late write reports for a task the window no longer
// owns.
func stopSignal(status string) error {
	if status == task.StatusCancelled {
		return errCancelled
	}
	return errPaused
}

// recordEntry writes one journal row's execution metadata, keeping the row's
// identity. It never touches task status.
func recordEntry(it *task.Task, entry *task.JournalEntry, state, out string, executed bool, outcome string) {
	for i := range it.Journal {
		if it.Journal[i].Turn == entry.Turn && it.Journal[i].CallID == entry.CallID && it.Journal[i].ArgsHash == entry.ArgsHash {
			it.Journal[i].State = state
			it.Journal[i].Result = out
			it.Journal[i].Executed = executed
			it.Journal[i].Outcome = outcome
			return
		}
	}
}

// closeOpenEntry closes the started entry of the call just executed.
func (st *taskState) closeOpenEntry(callID, hash, state, out string) error {
	return st.closeOpenEntryAt(callID, hash, state, out, false, task.OutcomeSkipped)
}

// closeOpenEntryExecuted closes the entry and records that the call really ran.
func (st *taskState) closeOpenEntryExecuted(callID, hash, out string) error {
	return st.closeOpenEntryAt(callID, hash, task.JournalDone, out, true, task.OutcomeDone)
}

func (st *taskState) closeOpenEntryAt(callID, hash, state, out string, executed bool, outcome string) error {
	// Same as markEntryAt: an executed call keeps its evidence even when the
	// task was cancelled while it was running.
	var stopErr error
	_, err := st.book.Update(st.id, func(it *task.Task) error {
		closeOpenEntry(it, st.turn, callID, hash, state, out, executed, outcome)
		switch {
		case windowOwns(it):
			it.Pending = nil
		case it.Status == task.StatusWaitingJob:
			it.Pending = nil
			it.Status = task.StatusRunning
		default:
			stopErr = stopSignal(it.Status)
		}
		return nil
	})
	if err != nil {
		st.halted = true
		return err
	}
	if stopErr != nil {
		st.halted = true
		st.finished = errors.Is(stopErr, errCancelled)
		return stopErr
	}
	return nil
}

// closeOpenEntry closes the matching started row. The row is always updated so
// an executed call keeps its evidence even when the window already lost the
// task; status and Pending are the caller's business.
func closeOpenEntry(it *task.Task, turn int, callID, hash, state, out string, executed bool, outcome string) {
	for i := len(it.Journal) - 1; i >= 0; i-- {
		if it.Journal[i].Turn == turn && it.Journal[i].CallID == callID && it.Journal[i].ArgsHash == hash && it.Journal[i].State == task.JournalStarted {
			it.Journal[i].State = state
			it.Journal[i].Result = out
			it.Journal[i].Executed = executed
			it.Journal[i].Outcome = outcome
			return
		}
	}
}

func (st *taskState) recordIntent(call toolCall, hash string) error {
	_, err := st.book.Update(st.id, func(it *task.Task) error {
		// The intent is written before the side effect, so it may only be
		// written while this window still owns the task. A pause, cancel or
		// pause-then-resume that already moved the task must stop the call
		// before it runs instead of reviving the task as running.
		if !windowOwns(it) {
			if it.Status == task.StatusCancelled {
				return errCancelled
			}
			return errPaused
		}
		it.Pending = &task.Pending{Kind: task.PendingTool, CallID: call.ID, Name: call.Function.Name, ArgsHash: hash}
		it.Journal = append(it.Journal, task.JournalEntry{
			At: time.Now().UTC(), Seq: len(it.Journal) + 1, Turn: st.turn,
			CallID: call.ID, Name: call.Function.Name, ArgsHash: hash, State: task.JournalStarted,
		})
		return nil
	})
	if errors.Is(err, errPaused) {
		st.halted = true
		return err
	}
	if err != nil {
		st.halted = true
	}
	return err
}

// recordDone stores a call result when no intent was recorded (a refusal or a
// task-state tool), keeping one journal entry per call.
func (st *taskState) recordDone(call toolCall, hash, out string) error {
	_, err := st.book.Update(st.id, func(it *task.Task) error {
		// A refusal result belongs to this window too: once the operator moved
		// the task, the window must not clear Pending or revive it.
		if !windowOwns(it) {
			if it.Status == task.StatusCancelled {
				return errCancelled
			}
			return errPaused
		}
		it.Journal = append(it.Journal, task.JournalEntry{
			At: time.Now().UTC(), Seq: len(it.Journal) + 1, Turn: st.turn,
			CallID: call.ID, Name: call.Function.Name, ArgsHash: hash, State: task.JournalDone, Result: out,
		})
		it.Pending = nil
		return nil
	})
	if errors.Is(err, errPaused) || errors.Is(err, errCancelled) {
		st.halted = true
		return err
	}
	if err != nil {
		st.halted = true
	}
	return err
}

// isTaskStateTool reports tools handled by the durable task itself.
func isTaskStateTool(name string) bool {
	switch name {
	case "plan", "plan_progress", "checkpoint", "wait", "finish":
		return true
	}
	return false
}

// parkCall persists the approval question and marks the call waiting. The
// journal entry moves from started to waiting: the call has not run, so a
// restart resumes it rather than reconciling an unknown effect.
func (st *taskState) parkCall(req ApprovalRequest) error {
	callID, hash := req.ID, argsHashFromReq(req)
	if st.current != nil {
		callID, hash = st.current.ID, argsHash(*st.current)
	}
	_, err := st.book.Update(st.id, func(it *task.Task) error {
		// Parking is a window outcome: a task the operator paused, cancelled,
		// resumed or already parked keeps that state. The call did not run, so
		// there is nothing to reconcile.
		if !windowOwns(it) {
			return errFinalBlocked
		}
		it.Status = task.StatusWaitingApproval
		// Turn is part of the pending identity: together with the call id and
		// the argument hash it names exactly one call, so a later turn that
		// reuses a call id can never inherit this approval.
		it.Pending = &task.Pending{Kind: task.PendingApproval, CallID: callID, ArgsHash: hash, ApprovalID: req.ID, Turn: st.turn}
		// Re-parking the same call must reuse its pending approval instead of
		// appending a second one with a new id.
		reused := false
		for i := range it.Approvals {
			a := it.Approvals[i]
			if a.CallID == callID && a.ArgsHash == hash && a.Status == task.ApprovalPending {
				it.Pending.ApprovalID = a.ID
				reused = true
				break
			}
		}
		if !reused {
			it.Approvals = append(it.Approvals, task.Approval{
				ID: req.ID, CallID: callID, ArgsHash: hash, Command: clip(req.Command, task.MaxLine),
				Reasons: append([]string(nil), req.Reasons...), Status: task.ApprovalPending, CreatedAt: time.Now().UTC(),
			})
		}
		// The call must have a journal entry even when the park happened before
		// any intent was recorded: resume identifies the parked call by
		// (turn, call id, args hash), and without an entry it would authorize
		// again and create a second question.
		moved := false
		for i := len(it.Journal) - 1; i >= 0; i-- {
			if it.Journal[i].CallID == callID && it.Journal[i].State == task.JournalStarted {
				it.Journal[i].State = task.JournalWaiting
				it.Journal[i].ArgsHash = hash
				it.Journal[i].Turn = st.turn
				moved = true
				break
			}
		}
		if !moved {
			name := ""
			if st.current != nil {
				name = st.current.Function.Name
			}
			it.Journal = append(it.Journal, task.JournalEntry{
				At: time.Now().UTC(), Seq: len(it.Journal) + 1, Turn: st.turn,
				CallID: callID, Name: name, ArgsHash: hash, State: task.JournalWaiting,
			})
		}
		return nil
	})
	if err != nil {
		st.halted = true
	}
	return err
}

// snapshot returns this task's current stored row, or a bare row when it cannot
// be read. It is only used to locate the delegation root outside a registry
// lock.
func (st *taskState) snapshot(all []task.Task) task.Task {
	for _, it := range all {
		if it.ID == st.id {
			return it
		}
	}
	return task.Task{ID: st.id}
}

// rootOfSnapshot finds the task with the given root id in an already fetched
// snapshot, without touching the registry.
func rootOfSnapshot(rootID string, all []task.Task) task.Task {
	for _, it := range all {
		if it.ID == rootID {
			return it
		}
	}
	return task.Task{ID: rootID}
}

// rootOf walks a delegated task up to the top-level task whose accepted budget
// the subtree shares. A cycle or a missing parent stops the walk so a corrupt
// link cannot hang the runtime.
func rootOf(it task.Task, all []task.Task) task.Task {
	byID := make(map[string]task.Task, len(all))
	for _, row := range all {
		byID[row.ID] = row
	}
	seen := map[string]bool{it.ID: true}
	for it.ParentID != "" {
		if seen[it.ParentID] {
			return it
		}
		parent, ok := byID[it.ParentID]
		if !ok {
			return it
		}
		seen[parent.ID] = true
		it = parent
	}
	return it
}

func argsHashFromReq(req ApprovalRequest) string {
	sum := sha256.Sum256([]byte(req.Command))
	return hex.EncodeToString(sum[:])
}

func argsHash(call toolCall) string {
	sum := sha256.Sum256([]byte(call.Function.Name + "\n" + call.Function.Arguments))
	return hex.EncodeToString(sum[:])
}

func interruptedResult(call toolCall, e task.JournalEntry) string {
	out := "执行结果未知，未重复执行：上一条 " + call.Function.Name + " 调用在中断前已经开始，可能已经生效，也可能没有。先只用只读命令核对现状，再决定下一步。"
	if strings.TrimSpace(e.Result) != "" {
		out += "（此前记录：" + clip(e.Result, 300) + "）"
	}
	return out
}

func copyMessages(in []map[string]any) []map[string]any {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out []map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// openCalls returns the tool calls of the last assistant turn that have no
// matching tool result yet, in their original order. A resumed window must
// finish these before any new model turn.
func openCalls(messages []map[string]any) []toolCall {
	answered := map[string]bool{}
	var pending []toolCall
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		switch role {
		case "assistant":
			pending = nil
			raw, _ := json.Marshal(msg["tool_calls"])
			var calls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			}
			if json.Unmarshal(raw, &calls) == nil {
				for _, c := range calls {
					if c.ID == "" || answered[c.ID] {
						continue
					}
					call := toolCall{ID: c.ID, Type: c.Type}
					call.Function.Name = c.Function.Name
					call.Function.Arguments = c.Function.Arguments
					pending = append(pending, call)
				}
			}
		case "tool":
			id, _ := msg["tool_call_id"].(string)
			if id != "" {
				answered[id] = true
				for i, c := range pending {
					if c.ID == id {
						pending = append(pending[:i], pending[i+1:]...)
						break
					}
				}
			}
		}
	}
	return pending
}

func (a *Agent) setHandoff(ctx context.Context, summary string) error {
	st := taskFrom(ctx)
	if st == nil {
		return nil
	}
	_, err := st.book.Update(st.id, func(it *task.Task) error {
		it.Next = summary
		it.Checkpoint.Next = summary
		return nil
	})
	if err != nil {
		st.halted = true
	}
	return err
}

var _ = preferences.ModeSmart
