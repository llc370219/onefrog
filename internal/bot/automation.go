package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"onefrog/internal/preferences"
	"onefrog/internal/task"
)

// ErrAutomationWaiting means a durable automation run stopped because it needs
// a human decision (an approval card or a card question), or because it is
// still running. It is deliberately an error: a scheduler must record the run
// as waiting or running, never as success, and must observe the stored task
// instead of submitting the same work again.
var ErrAutomationWaiting = errors.New("自动化任务正在等待处理")

// ErrAutomationRejected marks a run that was refused before anything was
// accepted: missing owner or Bot, a disabled Bot, no model, an empty prompt, a
// missing runner. Retrying the same identity cannot succeed, so a scheduler may
// record it as a failure. Every other error (store, registry, network) is
// uncertain and must be retried with the same identity instead.
var ErrAutomationRejected = errors.New("自动化任务被拒绝")

// rejectedError is a definite local rejection. It exposes Rejected so the
// scheduler can tell "refused, retrying cannot help" apart from "outcome
// unknown, retry the same run" without importing this package.
type rejectedError struct{ err error }

func (e rejectedError) Error() string { return e.err.Error() }
func (e rejectedError) Unwrap() error { return e.err }

// Rejected marks the run as definitely refused.
func (e rejectedError) Rejected() bool { return true }

func automationRejected(format string, args ...any) error {
	return rejectedError{err: fmt.Errorf("%w：%s", ErrAutomationRejected, fmt.Sprintf(format, args...))}
}

// AutomationRequest is one scheduled run. ClientRequestID is the run identity:
// retrying the same run must resolve to the same task, and a fresh scheduled
// firing must use a fresh id.
type AutomationRequest struct {
	OwnerID         string
	BotID           string
	ServerID        string
	SessionID       string
	Title           string
	Prompt          string
	ApprovalMode    string
	ClientRequestID string
}

// AutomationOutcome is what a scheduler persists about a run: the durable task
// id, its session and the current observable state. Text is only ever set for
// a completed task, so a parked or running run can never look successful.
type AutomationOutcome struct {
	TaskID    string
	SessionID string
	Text      string
	Status    string
	Pending   *task.Pending
	CardID    string
}

// Done reports whether the run reached a terminal state.
func (o AutomationOutcome) Done() bool {
	switch o.Status {
	case task.StatusCompleted, task.StatusFailed, task.StatusCancelled, task.StatusBudgetExhausted:
		return true
	}
	return false
}

// automationRun normalises one scheduled run. Both the durable and the
// turn-bound path validate the same things, so a run never executes against a
// missing or disabled Bot, or without a model.
func (a *Agent) automationRun(ctx context.Context, req AutomationRequest) (*Agent, Options, error) {
	if strings.TrimSpace(req.OwnerID) == "" || strings.TrimSpace(req.BotID) == "" {
		return nil, Options{}, automationRejected("缺少所有者或 Bot")
	}
	copy := *a
	agent := &copy
	if a.SettingsLive != nil {
		agent.Settings = a.SettingsLive.Frozen()
	} else if a.Settings != nil {
		agent.Settings = a.Settings.Frozen()
	}
	mode := strings.TrimSpace(req.ApprovalMode)
	if mode == "" {
		mode = "default"
	}
	opt := Options{Mode: "direct", OwnerID: req.OwnerID, RestrictServers: true, ApprovalMode: mode}
	// A background run never executes risky commands unchecked: "off" is
	// raised to smart. An explicitly stricter mode is kept as-is.
	if mode == preferences.ModeOff {
		opt.ApprovalMode = preferences.ModeSmart
	}
	b, err := agent.Store.GetBot(ctx, req.BotID)
	if err != nil {
		return nil, opt, automationRejected("Bot 不存在")
	}
	if !agent.allowedBot(ctx, b.ID) {
		return nil, opt, automationRejected("该 Bot 已停用")
	}
	if agent.Settings != nil {
		resolved, _, err := agent.credentialsFor(ctx, b, false)
		if err != nil {
			return nil, opt, err
		}
		b = resolved
	}
	if strings.TrimSpace(b.BaseURL) == "" {
		return nil, opt, automationRejected("Bot 还没有接上模型")
	}
	return agent, opt, nil
}

// SubmitAutomation accepts one durable automation run and returns its task.
// The work is persisted before it is queued, so a scheduler that loses its
// connection or restarts can observe the same task instead of submitting the
// same run again.
func (a *Agent) SubmitAutomation(ctx context.Context, req AutomationRequest) (task.Task, error) {
	agent, opt, err := a.automationRun(ctx, req)
	if err != nil {
		return task.Task{}, err
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		return task.Task{}, automationRejected("任务内容不能为空")
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "自动化"
	}
	if agent.TasksRunner == nil || agent.TasksRunner.Book == nil {
		return task.Task{}, automationRejected("%v", ErrNoRunner)
	}
	return agent.SubmitTask(ctx, SubmitRequest{
		OwnerID: req.OwnerID, BotID: req.BotID, SessionID: req.SessionID,
		ClientRequestID: req.ClientRequestID, ServerID: req.ServerID,
		Content: req.Prompt, Title: "自动化 · " + title, Options: opt,
		// A scheduled run is background work: it can never ask the user, and
		// the registry records that provenance on the task row itself.
		Purpose: "automation",
	})
}

// ObserveAutomation reads a run's current state without running anything. It
// is what a scheduler calls on a persisted task id: a parked or running run is
// observed, never resubmitted and never reported as success.
func (a *Agent) ObserveAutomation(ctx context.Context, ownerID, taskID string) (AutomationOutcome, error) {
	if a.TasksRunner == nil || a.TasksRunner.Book == nil {
		return AutomationOutcome{}, ErrNoRunner
	}
	got, err := a.TasksRunner.Await(ctx, ownerID, taskID)
	if err != nil {
		return AutomationOutcome{}, err
	}
	out := AutomationOutcome{
		TaskID: got.Task.ID, SessionID: got.Task.SessionID,
		Status: got.Task.Status, Pending: got.Pending, CardID: got.CardID,
	}
	if got.Done && got.Task.Status == task.StatusCompleted {
		out.Text = got.Task.Result
	}
	return out, nil
}

// WaitAutomation blocks until the run stops making progress: terminal, waiting
// for a human, or paused. A run that ends without a human decision returns its
// text; one that is still waiting returns ErrAutomationWaiting with the stored
// state so the scheduler can record it as waiting and attach the card.
func (a *Agent) WaitAutomation(ctx context.Context, ownerID, taskID string) (AutomationOutcome, error) {
	if a.TasksRunner == nil || a.TasksRunner.Book == nil {
		return AutomationOutcome{}, ErrNoRunner
	}
	got, err := a.TasksRunner.WaitTask(ctx, ownerID, taskID, 0)
	if err != nil {
		return AutomationOutcome{}, err
	}
	out := AutomationOutcome{
		TaskID: got.Task.ID, SessionID: got.Task.SessionID,
		Status: got.Task.Status, Pending: got.Pending, CardID: got.CardID,
	}
	if !got.Done {
		return out, ErrAutomationWaiting
	}
	if got.Task.Status == task.StatusCompleted {
		out.Text = got.Task.Result
	}
	return out, nil
}

// isAutomation reports whether this turn is a scheduled automation run.
func isAutomation(ctx context.Context) bool {
	actor, ok := ctx.Value(usageKey{}).(usageActor)
	return ok && actor.Purpose == "automation"
}

// RunAutomation runs one scheduled task in sessionID. Nobody is watching the
// turn, so it behaves like an inbox turn: no ask_user, and "off" approval is
// raised to smart so a background run never executes risky commands
// unchecked.
//
// When a durable task runner is wired, the run is accepted as a task and
// observed through it. A run that parks on an approval or a card returns
// ErrAutomationWaiting together with the task id, so the scheduler records it
// as waiting (never success) and can resume the same task later instead of
// submitting the work again. The turn-bound fallback below exists only for
// contexts without a runner (old unit tests).
func (a *Agent) RunAutomation(ctx context.Context, botID, serverID, sessionID, title, prompt, ownerID, mode string) (Result, error) {
	if a.TasksRunner != nil && a.TasksRunner.Book != nil {
		out, err := a.RunAutomationTask(ctx, AutomationRequest{
			OwnerID: ownerID, BotID: botID, ServerID: serverID, SessionID: sessionID,
			Title: title, Prompt: prompt, ApprovalMode: mode,
		})
		return Result{SessionID: out.SessionID, Text: out.Text}, err
	}
	return a.runAutomationTurn(ctx, botID, serverID, sessionID, title, prompt, ownerID, mode)
}

// RunAutomationTask accepts one durable run, waits until it stops making
// progress, and reports the stored outcome. A waiting or running run is an
// error (ErrAutomationWaiting) with the task id and pending card, so a caller
// never mistakes it for a successful run.
func (a *Agent) RunAutomationTask(ctx context.Context, req AutomationRequest) (AutomationOutcome, error) {
	it, err := a.SubmitAutomation(ctx, req)
	if err != nil {
		return AutomationOutcome{}, err
	}
	out, err := a.WaitAutomation(ctx, req.OwnerID, it.ID)
	if errors.Is(err, ErrAutomationWaiting) {
		return out, err
	}
	if err != nil {
		return out, err
	}
	return out, nil
}

// runAutomationTurn is the turn-bound automation path. It is only correct
// without a durable runner: it cannot survive a restart and it cannot resume a
// parked approval, so production callers always wire TasksRunner.
func (a *Agent) runAutomationTurn(ctx context.Context, botID, serverID, sessionID, title, prompt, ownerID, mode string) (Result, error) {
	copy := *a
	agent := &copy
	if a.SettingsLive != nil {
		agent.Settings = a.SettingsLive.Frozen()
	} else if a.Settings != nil {
		agent.Settings = a.Settings.Frozen()
	}
	if strings.TrimSpace(mode) == "" {
		mode = "default"
	}
	opt := Options{Mode: "direct", OwnerID: ownerID, RestrictServers: true, ApprovalMode: mode}
	ctx = context.WithValue(ctx, optionsKey{}, opt)
	ctx = usageContext(ctx, botID, "automation", "")
	if agent.approvalMode(ctx) == preferences.ModeOff {
		opt.ApprovalMode = preferences.ModeSmart
		ctx = context.WithValue(ctx, optionsKey{}, opt)
	}
	b, err := agent.Store.GetBot(ctx, botID)
	if err != nil {
		return Result{}, errors.New("Bot 不存在")
	}
	if !agent.allowedBot(ctx, b.ID) {
		return Result{}, errors.New("该 Bot 已停用")
	}
	if agent.Settings != nil {
		resolved, _, err := agent.credentialsFor(ctx, b, false)
		if err != nil {
			return Result{}, err
		}
		b = resolved
	}
	if strings.TrimSpace(b.BaseURL) == "" {
		return Result{}, errors.New("Bot 还没有接上模型")
	}
	if title = strings.TrimSpace(title); title == "" {
		title = "自动化"
	}
	sess, err := agent.Store.EnsureSession(ctx, sessionID, b.ID, "自动化 · "+title)
	if err != nil {
		return Result{}, err
	}
	if sess.BotID != b.ID {
		return Result{}, errors.New("自动化对话的 Bot 不一致")
	}
	return agent.ReplyWithOptions(ctx, b.ID, serverID, sess.ID, prompt, opt, nil)
}
