package bot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"onefrog/internal/preferences"
	"onefrog/internal/task"
)

// Durable submission. Every non-interactive entry point (the task API, the bot
// inbox, an automation run) goes through SubmitTask, which wraps the existing
// acceptance pipeline instead of reimplementing it:
//
//	PrepareTaskRequest -> AcceptMessages -> AcceptTask
//
// The pipeline is already two-phase and idempotent: the task row is created
// paused with AcceptancePending set, the transcript entry is written, and only
// then is the task queued. SubmitTask adds nothing to that seam, so a retry can
// never duplicate the transcript or queue a task whose context was never
// accepted.

// SubmitRequest is one durable submission. OwnerID and ClientRequestID together
// are the request identity: a retry with the same pair resolves to the same
// task, and a different payload under the same pair is a conflict.
type SubmitRequest struct {
	OwnerID         string
	BotID           string
	SessionID       string
	ClientRequestID string
	ProjectID       string
	ServerID        string
	Content         string
	Title           string
	Options         Options
	// Purpose/Hop/Untrusted are persisted on the task row at creation. They are
	// written once by the runtime inside the AcceptancePending create, so the
	// dispatcher never sees a task whose provenance is still being written.
	Purpose   string
	Hop       int
	Untrusted bool
	// Ready writes the goal transcript entry. Nil writes the plain goal without
	// attachments. It runs exactly once, between the create and the queue.
	Ready func(ctx context.Context, sessionID string) error
}

// ErrNoRunner means a durable submission was attempted without a task runner.
// Callers that support a synchronous fallback should check TasksRunner first
// and use the ordinary turn-bound loop instead.
var ErrNoRunner = errors.New("当前没有可用的后台任务运行器")

// SubmitTask accepts a durable task and returns it once it is queued. A retried
// request returns the existing task unchanged: no second task, no second
// transcript entry.
func (a *Agent) SubmitTask(ctx context.Context, req SubmitRequest) (task.Task, error) {
	if a.TasksRunner == nil || a.TasksRunner.Book == nil {
		return task.Task{}, ErrNoRunner
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" {
		return task.Task{}, errors.New("任务目标不能为空")
	}
	if strings.TrimSpace(req.OwnerID) == "" {
		return task.Task{}, errors.New("缺少任务所有者")
	}
	// Idempotence is decided before anything is mutated: no session creation,
	// no option rewrite, no history read. The comparison is against the frozen
	// task row only, with fields the retry left unset inherited from that row,
	// so a session whose history, options or model config changed since the
	// original acceptance still resolves to the same task. Only a field the
	// retry explicitly set to a different value is a conflict.
	if req.ClientRequestID != "" {
		if existing, found := a.RequestTask(req.OwnerID, req.ClientRequestID); found {
			if existing.AcceptancePending {
				// The transcript write of the first attempt was never confirmed.
				// Nothing may run on a guess and re-running it could duplicate
				// the goal entry, so the caller must resubmit.
				return task.Task{}, ErrAcceptanceIncomplete
			}
			conflict, err := explicitConflict(existing, req)
			if err != nil {
				return task.Task{}, err
			}
			if conflict {
				return task.Task{}, ErrRequestPayloadChanged
			}
			return existing, nil
		}
	}
	// Resolve the stable session before anything is created, so a retry with a
	// fresh session id still lands on the same session and task.
	sess, project, opt, refs, err := a.PrepareTaskRequest(
		ctx, req.OwnerID, req.BotID, req.SessionID, req.ClientRequestID, req.Title, req.Options,
	)
	if err != nil {
		return task.Task{}, err
	}
	// The project scope is derived from the session and the mode. A client may
	// name it, but naming a different one would let a task read another
	// project's memory, so it is refused instead of silently overriding.
	if v := strings.TrimSpace(req.ProjectID); v != "" && v != project {
		return task.Task{}, errors.New("不能更改任务所属项目，请新建任务")
	}
	opt.TaskID = ""
	// Freeze the accepted conversation now: the system prompt, the history as
	// it exists today, and this request's goal. A later message in the session
	// never becomes part of this task.
	messages, err := a.AcceptMessages(ctx, sess, sess.BotID, req.ServerID, req.Content, refs, opt)
	if err != nil {
		return task.Task{}, err
	}
	appendGoal := req.Ready
	if appendGoal == nil {
		appendGoal = func(ctx context.Context, sessionID string) error {
			return a.AddTaskGoalRefs(ctx, sessionID, req.Content, refs)
		}
	}
	return a.TasksRunner.AcceptTask(ctx, CreateRequest{
		OwnerID: req.OwnerID, BotID: sess.BotID, SessionID: sess.ID,
		ProjectID: project, ServerID: req.ServerID, Content: req.Content,
		ClientRequestID: req.ClientRequestID, Messages: messages,
		Purpose: req.Purpose, Hop: req.Hop, Untrusted: req.Untrusted,
		AppendGoal: appendGoal,
	}, opt)
}

// exactTaskID is the durable id a submission resolves to. It delegates to the
// one shared derivation so a lookup and an acceptance can never disagree.
func exactTaskID(ownerID, clientRequestID string) string {
	return RequestTaskID(ownerID, clientRequestID)
}

// AwaitResult is a durable task's current outcome. Done is false while the task
// is still active; the caller must then show the durable task (status, pending
// card or job), never a success payload.
type AwaitResult struct {
	Task task.Task
	// Result is rebuilt from the task row only. MessageID is empty when the task
	// did not record one, and it is never guessed from the session: a later task
	// in the same session would be mistaken for this one's answer.
	Result Result
	// CardID is the exact card the task is parked on, if any.
	CardID  string
	Done    bool
	Pending *task.Pending
}

// Await returns a task's current outcome without running or waiting for
// anything. It is safe to call in a request handler.
func (r *Runner) Await(ctx context.Context, ownerID, id string) (AwaitResult, error) {
	if r == nil || r.Book == nil {
		return AwaitResult{}, ErrNoRunner
	}
	it, err := r.Book.Get(id)
	if err != nil {
		return AwaitResult{}, err
	}
	if ownerID != "" && it.OwnerID != ownerID {
		return AwaitResult{}, task.ErrNotFound
	}
	out := AwaitResult{
		Task:    it,
		Pending: it.Pending,
		Done:    it.Terminal(),
		Result: Result{
			SessionID: it.SessionID,
			Text:      it.Result,
			MessageID: it.ResultMessageID,
			Steps:     []Step{},
			Tools:     []ToolEvent{},
		},
	}
	if it.Pending != nil && it.Pending.Kind == task.PendingUser {
		out.CardID = strings.TrimSpace(it.Pending.CardMessageID)
	}
	return out, nil
}

// WaitTask blocks until a task stops making progress, so a caller that still
// wants a synchronous answer does not poll forever. It returns as soon as the
// task is terminal, waiting on the operator (a card or an approval), waiting on
// a child job, or paused. It also returns on context expiry, with the last
// observed state and the context error.
func (r *Runner) WaitTask(ctx context.Context, ownerID, id string, poll time.Duration) (AwaitResult, error) {
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	for {
		out, err := r.Await(ctx, ownerID, id)
		if err != nil {
			return out, err
		}
		if out.Done || out.Task.Waiting() || out.Task.Status == task.StatusPaused {
			return out, nil
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return out, ctx.Err()
		case <-timer.C:
		}
	}
}

// explicitConflict reports whether a retry explicitly contradicts the frozen
// task row. It is pure: it reads the stored row and the request, never the
// session, its history or its saved options, so it can run before anything is
// mutated and a retry after the conversation has moved on still matches.
//
// A field the retry left unset (empty string, nil route, nil ids) inherits the
// stored value; it is not a change. Only an explicitly different value is a
// conflict, and the immutable scope and provenance fields are compared too so a
// retry can never widen a task's project, purpose, hop or trust.
func explicitConflict(existing task.Task, req SubmitRequest) (bool, error) {
	if existing.OwnerID != strings.TrimSpace(req.OwnerID) {
		return true, nil
	}
	if existing.Goal != strings.TrimSpace(req.Content) {
		return true, nil
	}
	if v := strings.TrimSpace(req.BotID); v != "" && v != existing.BotID {
		return true, nil
	}
	if v := strings.TrimSpace(req.ServerID); v != "" && v != existing.ServerID {
		return true, nil
	}
	if v := strings.TrimSpace(req.SessionID); v != "" && v != existing.SessionID {
		return true, nil
	}
	if v := strings.TrimSpace(req.ProjectID); v != "" && v != existing.ProjectID {
		return true, nil
	}
	// Provenance is written once at creation. A retry may omit it (the common
	// transport case) but must never change it.
	if v := strings.TrimSpace(req.Purpose); v != "" && v != existing.Purpose {
		return true, nil
	}
	if req.Hop != 0 && req.Hop != existing.Hop {
		return true, nil
	}
	if req.Untrusted && !existing.Untrusted {
		return true, nil
	}
	return optionsConflict(existing.OptionsJSON, req.Options)
}

// optionsConflict compares only the options the retry explicitly set against
// the frozen options row. Unset fields inherit, so a retry is not a conflict
// just because the session's saved options or the model configuration moved.
func optionsConflict(storedJSON string, opt Options) (bool, error) {
	if opt.Mode == "" && opt.ApprovalMode == "" && opt.Workspace == nil &&
		opt.ApprovalRoute == nil && opt.ModelRoute == nil &&
		opt.ExcludedBotIDs == nil && len(opt.AttachmentIDs) == 0 && !hasStoredAttachments(storedJSON) {
		return false, nil
	}
	var stored Options
	if strings.TrimSpace(storedJSON) != "" {
		if err := json.Unmarshal([]byte(storedJSON), &stored); err != nil {
			return false, err
		}
	}
	if opt.Mode != "" && normalizeMode(opt.Mode) != normalizeMode(stored.Mode) {
		return true, nil
	}
	if opt.ApprovalMode != "" && opt.ApprovalMode != "default" {
		mode, ok := preferences.NormalizeMode(opt.ApprovalMode)
		if !ok {
			return false, errors.New("审核方式无效")
		}
		if mode != stored.ApprovalMode {
			return true, nil
		}
	}
	if opt.ModelRoute != nil && !sameRoute(opt.ModelRoute, stored.ModelRoute) {
		return true, nil
	}
	if opt.ApprovalRoute != nil && !sameRoute(opt.ApprovalRoute, stored.ApprovalRoute) {
		return true, nil
	}
	if opt.Workspace != nil && (stored.Workspace == nil || *opt.Workspace != *stored.Workspace) {
		return true, nil
	}
	if opt.ExcludedBotIDs != nil && !sameStrings(sortedCopy(opt.ExcludedBotIDs), sortedCopy(stored.ExcludedBotIDs)) {
		return true, nil
	}
	// Attachments are compared as a set even when the retry names none: dropping
	// the files is a different request, not an omitted default. This is the one
	// option where an empty value is meaningful rather than "inherit".
	if !sameStrings(sortedCopy(opt.AttachmentIDs), sortedCopy(stored.AttachmentIDs)) {
		return true, nil
	}
	return false, nil
}

// hasStoredAttachments reports whether the frozen task carried any file ids.
func hasStoredAttachments(storedJSON string) bool {
	if strings.TrimSpace(storedJSON) == "" {
		return false
	}
	var stored struct {
		AttachmentIDs []string `json:"attachment_ids"`
	}
	if json.Unmarshal([]byte(storedJSON), &stored) != nil {
		return false
	}
	return len(stored.AttachmentIDs) > 0
}
