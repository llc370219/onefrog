package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"onefrog/internal/id"
	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/task"
)

// acceptID derives a stable task id from the owner and the client request id
// only. A retried acceptance lands on the same task even when the transport
// assigned a fresh session id, and a different request never collides because
// it carries a different request id. The goal is deliberately not part of the
// id: it is compared for conflict instead.
func acceptID(ownerID, clientRequestID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{ownerID, clientRequestID}, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// Runner executes durable tasks in the background. It is independent of any
// HTTP request or WebSocket connection: transports only create the task,
// answer its pending question, or read its status.
type Runner struct {
	Agent *Agent
	Book  *task.Book

	mu      sync.Mutex
	running map[string]bool
	cancel  map[string]context.CancelFunc
	wg      sync.WaitGroup
	wake    chan struct{}
	base    context.Context
	// finalMu serialises "check the transcript, then append the closing
	// message". The deterministic id alone is not enough: Memory.AddMessage
	// appends blindly, so two writers that both see the message missing would
	// both append it. Every final write (a window's own, and the repair scan)
	// goes through this lock.
	finalMu sync.Mutex
	// activities keeps the per-task safe activity trail rendered on the task
	// card. Process memory; a fresh process rebuilds the visible trail from
	// the durable journal.
	activities *activityStore
	// actMu guards lazy creation of activities; the store itself is
	// internally synchronized.
	actMu sync.Mutex
	// stepHook replaces Step when set. It exists only so tests can hold a
	// window open at a known point and drive operator actions against it
	// deterministically, instead of racing a real model call.
	stepHook func(ctx context.Context, a *Agent, it task.Task) (taskOutcome, error)
}

// NewRunner wires a durable task runner.
func NewRunner(agent *Agent, book *task.Book) *Runner {
	return &Runner{Agent: agent, Book: book, running: map[string]bool{}, cancel: map[string]context.CancelFunc{}, wake: make(chan struct{}, 1)}
}

// Wake asks the runner to look for queued work without blocking.
func (r *Runner) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Wait blocks until every task goroutine has stopped.
//
// It must not run concurrently with dispatch: a WaitGroup may not start a Wait
// while the counter can still go from zero to one. Run already waits for its
// own windows before returning, so callers that own the Run loop should wait
// for Run itself rather than calling Wait while it dispatches.
func (r *Runner) Wait() { r.wg.Wait() }

// Run dispatches queued tasks until ctx ends. It is started once per process,
// next to the automation scheduler. Due waiting_job tasks are woken by the same
// loop, so a parked job is re-checked without a busy loop.
func (r *Runner) Run(ctx context.Context) {
	r.mu.Lock()
	r.base = ctx
	r.mu.Unlock()
	if _, err := r.Book.ResumeInterrupted(time.Now().UTC()); err != nil {
		log.Printf("tasks: recovery: %v", err)
	}
	r.CleanupIncompleteAcceptance(time.Now().UTC())
	defer r.wg.Wait()
	for {
		// Order matters for liveness: a pass may make work dispatchable (a due
		// job or a finished child wakes its parent), and the same pass must
		// then start it. Expiry comes first so a task whose deadline passed is
		// ended instead of being woken into a new window, then due jobs are
		// re-queued, then dispatch starts everything queued. The wake token
		// from wakeParent may arrive while this pass runs, so it must not be
		// the only thing that can turn a parked task into a running one.
		r.ExpireOverdue(time.Now().UTC())
		r.wakeDueJobs()
		r.dispatch()
		r.repairFinals()
		// An acceptance that died before its transcript write must not linger
		// as a task that looks accepted. This runs on every tick, not just at
		// startup, so a submission that crashed a minute ago is cleaned up too.
		if n := r.CleanupIncompleteAcceptance(time.Now().UTC()); n > 0 {
			log.Printf("tasks: cleaned up %d incomplete acceptance(s)", n)
		}
		wait := 30 * time.Second
		if next := r.Book.NextJobWake(); !next.IsZero() {
			if d := time.Until(next); d < wait {
				wait = d
			}
		}
		if wait < time.Second {
			wait = time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-r.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// ExpireOverdue ends every task whose persisted deadline has passed, whatever
// state it is parked in. A task waiting on an approval, a card or an external
// job has no window to notice the deadline, so without this scan it would stay
// active forever and keep its card, approval or child reservation alive.
//
// AcceptancePending tasks are owned by CleanupIncompleteAcceptance: their
// acceptance has not been confirmed, so "expired" is not the right failure.
// Completed and other terminal tasks are never touched. It returns how many
// tasks were ended.
func (r *Runner) ExpireOverdue(now time.Time) int {
	changed := 0
	for _, it := range r.Book.List("") {
		if it.Deadline.IsZero() || now.Before(it.Deadline) {
			continue
		}
		if it.Terminal() || it.AcceptancePending {
			continue
		}
		_, err := r.Book.Update(it.ID, func(t *task.Task) error {
			// Recheck under the registry lock: the task may have finished, been
			// accepted or been re-queued since the scan read it.
			if t.Terminal() || t.AcceptancePending || t.Deadline.IsZero() || now.Before(t.Deadline) {
				return errNotQueued
			}
			t.Status = task.StatusBudgetExhausted
			t.LastError = "任务整体时限已到（" + t.Deadline.UTC().Format(time.RFC3339) + "），已停止；结果和进度保留在此，未执行的步骤没有运行"
			t.Pending = nil
			return nil
		})
		if err != nil {
			continue
		}
		changed++
		// A parked window may still be running: stop it so it cannot keep
		// working for a task that is over.
		r.cancelHandle(it.ID)
		// A parent waiting on this child must learn it is done instead of
		// polling until its own deadline.
		r.wakeParent(it.ID)
	}
	if changed > 0 {
		r.Wake()
	}
	return changed
}

// wakeDueJobs re-queues waiting_job tasks whose poll time has arrived. The
// pending job reference stays part of the window the model resumes with.
// A task parked on a native background job (JobRef "job:<id>") only resumes
// when the job has actually finished; a still-running job re-parks with the
// next sparse poll, so the task never invents a wait result.
func (r *Runner) wakeDueJobs() {
	now := time.Now().UTC()
	for _, it := range r.Book.DueJobTasks(now) {
		resume, result := r.consumeJobWait(context.Background(), it)
		if !resume {
			continue
		}
		// A native background job (JobRef "job:<id>") only resumes with a real
		// result, and the backfill must not be followed by the generic
		// re-queue: if any anchor does not match, the task stays parked.
		if result != "" && it.Pending != nil && strings.HasPrefix(it.Pending.JobRef, "job:") {
			if _, err := r.Book.Update(it.ID, func(t *task.Task) error {
				if t.Status != task.StatusWaitingJob || t.Pending == nil ||
					t.Pending.JobRef != it.Pending.JobRef || t.Pending.Turn != it.Pending.Turn ||
					t.Pending.CallID != it.Pending.CallID || t.Pending.ArgsHash != it.Pending.ArgsHash {
					return errNotQueued
				}
				// Fill in the real outcome: the journal entry for the parked
				// job_wait call (JournalDone was set when the task parked) and the
				// trailing tool message of that same call in the checkpoint, so
				// the model resumes with the genuine result instead of a stub.
				foundJournal := false
				for i := len(t.Journal) - 1; i >= 0; i-- {
					e := &t.Journal[i]
					if e.State == task.JournalDone && e.Name == "job_wait" &&
						e.Turn == t.Pending.Turn && e.CallID == t.Pending.CallID &&
						e.ArgsHash == t.Pending.ArgsHash {
						e.Result = result
						foundJournal = true
						break
					}
				}
				foundTool := false
				for i := len(t.Checkpoint.Messages) - 1; i >= 0; i-- {
					m := t.Checkpoint.Messages[i]
					if role, _ := m["role"].(string); role == "tool" &&
						m["tool_call_id"] == t.Pending.CallID {
						m["content"] = result
						foundTool = true
						break
					}
				}
				// Both anchors must exist: without them the parked call has no
				// place for the real result, so the task must not be queued.
				if !foundJournal || !foundTool {
					return errNotQueued
				}
				t.Status = task.StatusQueued
				t.Pending = nil
				return nil
			}); err != nil {
				log.Printf("tasks: finish job wait %s: %v", it.ID, err)
			}
			continue
		}
		if result != "" {
			// The external job is finished (or its state was lost): record the real
			// outcome as the tool result of the parked job_wait call, then queue.
			if _, err := r.Book.Update(it.ID, func(t *task.Task) error {
				if t.Status != task.StatusWaitingJob || t.Pending == nil {
					return nil
				}
				t.Status = task.StatusQueued
				// Fill in the real outcome: the journal entry for the parked
				// job_wait call (JournalDone was set when the task parked) and the
				// trailing tool message of that same call in the checkpoint, so
				// the model resumes with the genuine result instead of a stub.
				for i := len(t.Journal) - 1; i >= 0; i-- {
					e := &t.Journal[i]
					if e.State == task.JournalDone && e.Name == "job_wait" &&
						e.Turn == t.Pending.Turn && e.CallID == t.Pending.CallID &&
						e.ArgsHash == t.Pending.ArgsHash {
						e.Result = result
						break
					}
				}
				for i := len(t.Checkpoint.Messages) - 1; i >= 0; i-- {
					m := t.Checkpoint.Messages[i]
					if role, _ := m["role"].(string); role == "tool" &&
						m["tool_call_id"] == t.Pending.CallID {
						m["content"] = result
						break
					}
				}
				t.Pending = nil
				return nil
			}); err != nil {
				log.Printf("tasks: finish job wait %s: %v", it.ID, err)
				continue
			}
		}
		_, err := r.Book.Update(it.ID, func(t *task.Task) error {
			if t.Status != task.StatusWaitingJob || t.Pending == nil || t.Pending.Kind != task.PendingJob {
				return nil
			}
			if !t.Deadline.IsZero() && now.After(t.Deadline) {
				t.Status = task.StatusBudgetExhausted
				t.LastError = "等待外部任务超时"
				t.Pending = nil
				return nil
			}
			// A due job becomes queued work; the poll count backs off.
			t.Pending.JobPolls++
			t.Pending.WakeAt = now.Add(task.NextJobPoll(t.Pending.JobPolls))
			t.Status = task.StatusQueued
			return nil
		})
		if err != nil {
			log.Printf("tasks: wake job %s: %v", it.ID, err)
		}
	}
}

// dispatch starts every queued task whose session has no other task running.
func (r *Runner) dispatch() {
	for _, it := range r.Book.List("") {
		// AcceptancePending is a lifecycle gate, not an identity: a task whose
		// transcript write was never confirmed must never be dispatched.
		if it.Status != task.StatusQueued || it.AcceptancePending {
			continue
		}
		if !r.acquire(it.SessionID) {
			continue
		}
		// Claim under the registry lock: a concurrent pause/cancel/duplicate
		// dispatch loses the race and does nothing.
		if _, err := r.Book.Update(it.ID, func(t *task.Task) error {
			if t.Status != task.StatusQueued {
				return errNotQueued
			}
			t.Status = task.StatusRunning
			// StartedAt is the moment the task first began, not the start of
			// the latest window: a task that continues across windows must not
			// look like it was created anew.
			if t.StartedAt.IsZero() {
				t.StartedAt = time.Now().UTC()
			}
			return nil
		}); err != nil {
			r.release(it.SessionID)
			continue
		}
		runCtx, cancel := context.WithCancel(r.baseOrBackground())
		r.mu.Lock()
		r.cancel[it.ID] = cancel
		r.mu.Unlock()
		r.wg.Add(1)
		go func(id, session string) {
			defer r.wg.Done()
			defer r.release(session)
			// Drop this window's cancel handle when the window ends. The map
			// is keyed by task, so a stale entry would leak one closure per
			// window and a later cancel could fire an old, already-finished
			// window's context.
			defer func() {
				r.mu.Lock()
				delete(r.cancel, id)
				r.mu.Unlock()
				cancel()
			}()
			r.execute(runCtx, id)
		}(it.ID, it.SessionID)
	}
}

var errNotQueued = errors.New("任务不再是排队状态")

func (r *Runner) acquire(session string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running[session] {
		return false
	}
	r.running[session] = true
	return true
}

func (r *Runner) release(session string) {
	r.mu.Lock()
	delete(r.running, session)
	r.mu.Unlock()
	r.Wake()
}

// cancelHandle stops the goroutine of a running task, if any.
func (r *Runner) cancelHandle(id string) {
	r.mu.Lock()
	cancel := r.cancel[id]
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// step runs one window, through the test hook when one is installed.
func (r *Runner) step(ctx context.Context, it task.Task) (taskOutcome, error) {
	if hook := r.stepHook; hook != nil {
		return hook(ctx, r.Agent, it)
	}
	return r.Step(ctx, r.Agent, it)
}

// windowOwns reports whether a finishing window may still write a window-level
// outcome (failed, budget_exhausted, cancelled, completed, next window).
//
// Only a task that is still running belongs to the window that dispatched it.
// Every other status is a newer durable decision that must survive a late
// return: paused/cancelled by the operator, waiting on a card, approval or job,
// terminal, or queued because the operator paused and immediately resumed while
// this window was still unwinding. That last case matters even though the old
// window still holds the session slot and the new one cannot be dispatched yet:
// the operator already re-queued the task, so the stale window must not turn it
// into failed or completed behind their back.
func windowOwns(t *task.Task) bool {
	return t.Status == task.StatusRunning
}

// windowActive is windowOwns plus the endpoints this window may legitimately
// write itself: the parks it just recorded (approval, card, job, child) and the
// completion its own finish produced. A window that parked or finished still
// has to close its pending tool batch and checkpoint before it exits, so those
// writes are its own. Anything else — paused, cancelled, failed or expired, and
// a task re-queued by a pause/resume — belongs to a newer decision and must not
// be written by the old window.
func windowActive(t *task.Task) bool {
	return t.Status == task.StatusRunning || t.Waiting() || t.Status == task.StatusCompleted
}

// execute runs one task window to its next stopping point.
func (r *Runner) execute(ctx context.Context, id string) {
	it, err := r.Book.Get(id)
	if err != nil {
		return
	}
	defer r.wakeParent(id)
	if !it.Deadline.IsZero() {
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, it.Deadline)
		defer stop()
	}
	out, err := r.step(ctx, it)
	if errors.Is(err, errCancelled) {
		_, _ = r.Book.Update(id, func(t *task.Task) error {
			// A late outcome from a window must not overwrite what the
			// operator (or a park) has already made durable.
			if !windowOwns(t) {
				return nil
			}
			t.Status = task.StatusCancelled
			t.Pending = nil
			return nil
		})
		return
	}
	if errors.Is(err, errSuspended) || errors.Is(err, errPaused) {
		// Paused and waiting tasks keep the status the operator or the park set.
		return
	}
	if errors.Is(err, errBudget) {
		// countStep already stored budget_exhausted; keep the text it produced.
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// A cancelled context is either an operator cancel (status already set)
		// or the process stopping. Never overwrite an operator-set status.
		if cur, e := r.Book.Get(id); e == nil && (cur.Status == task.StatusCancelled || cur.Status == task.StatusPaused) {
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			_, _ = r.Book.Update(id, func(t *task.Task) error {
				if !windowOwns(t) {
					return nil
				}
				t.Status = task.StatusBudgetExhausted
				t.LastError = "任务时间预算用完"
				t.Pending = nil
				return nil
			})
		}
		return
	}
	if err != nil {
		log.Printf("task %s: %s", id, err)
		_, _ = r.Book.Update(id, func(t *task.Task) error {
			// A window error is not authoritative: an operator pause, cancel or
			// resume that landed while the window was unwinding wins, and so
			// does a park. Only a task that is still ours may become failed.
			if !windowOwns(t) {
				return nil
			}
			t.Status = task.StatusFailed
			t.LastError = clip(err.Error(), task.MaxLine)
			t.Pending = nil
			return nil
		})
		return
	}
	// The window ended normally. Inside the overall budget the task continues
	// in a fresh window; only the total step or time budget ends it.
	_, _ = r.Book.Update(id, func(t *task.Task) error {
		if !windowOwns(t) {
			return nil
		}
		if out.Text != "" {
			t.Result = clip(out.Text, task.MaxResult)
			t.Next = clip(out.Text, task.MaxLine)
			t.Checkpoint.Next = t.Next
		}
		// TotalSteps is reserved by countStep before each model call, so a
		// parked or crashed window still counts what it consumed.
		if out.Final {
			t.Status = task.StatusCompleted
			t.Pending = nil
			return nil
		}
		if !out.WindowFull {
			if !out.Mutated {
				// A plain conversation did all it needed without changing
				// anything: that is a finished task.
				t.Status = task.StatusCompleted
				t.Pending = nil
				return nil
			}
			// Work was done but the model stopped without finish. Continue in
			// another window a bounded number of times so the model can verify
			// and finish; only then ask the operator.
			t.Attempts++
			if t.Attempts >= t.MaxAttempts {
				t.Status = task.StatusWaitingUser
				t.Pending = &task.Pending{Kind: task.PendingUser, JobRef: "多轮之后模型仍没有调用 finish，请确认是否完成"}
				return nil
			}
			t.Status = task.StatusQueued
			t.Step = 0
			return nil
		}
		if t.TotalSteps >= t.MaxSteps || (!t.Deadline.IsZero() && time.Now().UTC().After(t.Deadline)) {
			t.Status = task.StatusBudgetExhausted
			if t.LastError == "" {
				t.LastError = "总步骤或时间预算用完，任务还没有完成"
			}
			t.Pending = nil
			return nil
		}
		// Continue in a new window: the step counter resets, the task keeps
		// its goal, plan, progress and checkpoint.
		t.Status = task.StatusQueued
		t.Step = 0
		return nil
	})
	r.Wake()
}

// wakeParent makes a parent parked on this child re-check it immediately when
// the child reaches a terminal state, instead of waiting for the sparse poll
// backstop. A parent parked on anything else is left untouched.
func (r *Runner) wakeParent(childID string) {
	child, err := r.Book.Get(childID)
	if err != nil || child.ParentID == "" || !child.Terminal() {
		return
	}
	_, err = r.Book.Update(child.ParentID, func(t *task.Task) error {
		if t.Terminal() || t.Pending == nil || t.Pending.Kind != task.PendingJob || t.Pending.JobRef != childID {
			return nil
		}
		// Due now: the runner turns this into queued work on its next pass.
		t.Pending.WakeAt = time.Now().UTC()
		t.Pending.JobPolls = 0
		return nil
	})
	if err != nil {
		log.Printf("tasks: wake parent of %s: %v", childID, err)
	}
	r.Wake()
}

func (r *Runner) baseOrBackground() context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.base == nil {
		return context.Background()
	}
	return r.base
}

// Step runs one window of a task and reports how the window ended.
func (r *Runner) Step(ctx context.Context, a *Agent, it task.Task) (taskOutcome, error) {
	if it.Status == task.StatusCancelled {
		return taskOutcome{}, errCancelled
	}
	finishGate, err := a.Gate.Enter()
	if err != nil {
		return taskOutcome{}, err
	}
	defer finishGate()

	track := r.tracker(it.ID)
	ctx = context.WithValue(ctx, optionsKey{}, taskOptions(it))
	ctx = usageContext(ctx, it.BotID, "task", "")
	// The reviewer must see the authorization the user actually gave. For a
	// delegated child that is the parent's original goal, not the narrower
	// instruction the leader generated: a delegated task must never look like
	// a fresh authorization.
	authorization := it.Goal
	if it.ParentID != "" && strings.TrimSpace(it.ParentGoal) != "" {
		authorization = it.ParentGoal
	}
	ctx = context.WithValue(ctx, taskKey{}, authorization)
	ctx = context.WithValue(ctx, taskSessionKey{}, it.SessionID)
	ctx = withTaint(ctx)
	ctx = withGrants(ctx)
	ctx = withProject(ctx, it.ProjectID)

	b, err := a.Store.GetBot(ctx, it.BotID)
	if err != nil {
		return taskOutcome{}, errors.New("Bot 不存在")
	}
	if !a.allowedBot(ctx, b.ID) {
		return taskOutcome{}, errors.New("该 Bot 已停用")
	}
	b, _, err = a.credentialsFor(ctx, b, false)
	if err != nil {
		return taskOutcome{}, err
	}
	if strings.TrimSpace(b.BaseURL) == "" {
		return taskOutcome{}, errors.New("Bot 还没有接上模型")
	}
	st := &taskState{book: r.Book, id: it.ID, maxSteps: it.StepBudget, finalMu: &r.finalMu, track: track}
	if st.maxSteps <= 0 {
		st.maxSteps = a.execution().MaxSteps
	}
	// A resumed window keeps the turn counter it was stored with, so an
	// unfinished assistant batch keeps its journal identity, and side effects
	// recorded in an earlier window still count as mutations.
	st.turn = it.Turns
	st.mutated = journalMutated(it.Journal)
	st.unknown = journalUnknown(it.Journal)
	// Verification evidence is sequence-based: a resumed window inherits the
	// last mutation's journal position, so evidence from before it never
	// satisfies finish.
	st.lastMutationSeq = journalMutationSeq(it.Journal)
	// Durable delegation: ask_agent inside this window creates a child task
	// instead of running a worker turn inline.
	installDelegation(st)
	// Apply the decision that belongs to the call this task is suspended on.
	st.decision = pendingDecision(it)
	ctx = withTaskState(ctx, st)
	ctx = context.WithValue(ctx, optionsKey{}, taskOptionsWithApprover(it, st.taskApprover()))
	if it.ParentID != "" {
		// A delegated child runs as a worker: nobody is watching it live, so it
		// cannot ask the user, and it must never re-delegate.
		ctx = usageContext(ctx, it.BotID, "worker", "")
	} else if it.Purpose != "" {
		// A background submission keeps the narrower capabilities it was
		// accepted with: an inbox or automation task is not a live chat and
		// cannot ask the user. The purpose is rehydrated from the stored task,
		// never inferred from the current context.
		ctx = usageContext(ctx, it.BotID, it.Purpose, "")
	}
	// The hop count is part of the accepted provenance: a restart must not
	// reset the bot-message chain depth. An untrusted origin is recorded by
	// marking the task's own goal as untrusted content, so a command copied
	// from it still needs approval after a restart.
	if it.Hop > 0 {
		ctx = context.WithValue(ctx, hopKey{}, it.Hop)
	}
	if it.Untrusted {
		addUntrusted(ctx, it.Goal)
	}

	tr := &trace{agent: a, ctx: ctx}
	result, err := a.leadWithTrace(ctx, b, it.ServerID, it.SessionID, tr)
	if err != nil && !errors.Is(err, errSuspended) {
		return st.outcome, err
	}
	// The loop records how it ended (finish tool, plain answer, or a full
	// window). Do not re-derive that from status: a parked or paused task also
	// leaves this function normally.
	out := st.outcome
	if out.Text == "" {
		out.Text = result.Text
	}
	out.Mutated = out.Mutated || st.mutated
	// A card (ask_user / request_secret) ends the window. The card message was
	// just written to the transcript, so its exact id is persisted on Pending
	// before the task parks: an answer is then accepted only for that card.
	if result.Choice != nil || result.SecretRequest != nil {
		kind := task.PendingUser
		question := result.Text
		if result.Choice != nil {
			question = result.Choice.Prompt
		} else if result.SecretRequest != nil {
			question = result.SecretRequest.Label
		}
		if err := st.parkCard(kind, it.BotID, it.SessionID, question, result.Choice, result.SecretRequest, result.MessageID); err != nil {
			return out, err
		}
	}
	return out, err
}

// pendingDecision returns the operator's answer for the exact call the task is
// suspended on: same approval id, call id and args hash. It is consumed once,
// because the journal entry it targets stops being "waiting" after the run.
func pendingDecision(it task.Task) *task.Approval {
	if it.Pending == nil || it.Pending.Kind != task.PendingApproval {
		return nil
	}
	waiting := false
	for _, e := range it.Journal {
		if e.CallID == it.Pending.CallID && e.State == task.JournalWaiting &&
			e.ArgsHash == it.Pending.ArgsHash && e.Turn == it.Pending.Turn {
			waiting = true
			break
		}
	}
	if !waiting {
		return nil
	}
	for i := len(it.Approvals) - 1; i >= 0; i-- {
		a := it.Approvals[i]
		if a.ID != it.Pending.ApprovalID || a.CallID != it.Pending.CallID || a.ArgsHash != it.Pending.ArgsHash {
			continue
		}
		if a.Status == task.ApprovalAllowed || a.Status == task.ApprovalDenied {
			return &a
		}
	}
	return nil
}

// taskOptions rebuilds the per-task chat options. Attachments and plaintext
// secret answers are never stored, so they are absent here.
func taskOptions(it task.Task) Options {
	opt := Options{OwnerID: it.OwnerID, RestrictServers: true, TaskID: it.ID}
	if it.OptionsJSON != "" {
		_ = json.Unmarshal([]byte(it.OptionsJSON), &opt)
	}
	opt.OwnerID = it.OwnerID
	opt.TaskID = it.ID
	return opt
}

func taskOptionsWithApprover(it task.Task, approver Approver) Options {
	opt := taskOptions(it)
	opt.Approver = approver
	return opt
}

// withProject sets the project scope used by memory and collaboration.
func withProject(ctx context.Context, projectID string) context.Context {
	if strings.TrimSpace(projectID) == "" {
		return ctx
	}
	return context.WithValue(ctx, projectKey{}, projectID)
}

// AcceptTask registers a durable task in phases so a failed acceptance never
// leaves a runnable ghost and a retry never creates a second task:
//
//  1. create the task with AcceptancePending set (never dispatchable) with the
//     frozen goal, frozen options and the accepted conversation snapshot;
//  2. write the user's transcript entry (with attachments) through the
//     callback, then clear AcceptancePending and queue the task.
//
// If step 2 fails the task is removed, and nothing runs. A process that dies
// between the two steps leaves an AcceptancePending task that is never
// dispatched and never resumable; Run cleans it up as failed. The stored
// checkpoint is the accepted context: later user messages in the same session
// never become part of this task's authority.
func (r *Runner) AcceptTask(ctx context.Context, req CreateRequest, opt Options) (task.Task, error) {
	goal := strings.TrimSpace(req.Content)
	if goal == "" {
		return task.Task{}, errors.New("任务目标不能为空")
	}
	if req.SessionID == "" {
		return task.Task{}, errors.New("任务需要先建立对话")
	}
	if strings.TrimSpace(req.OwnerID) == "" || strings.TrimSpace(req.BotID) == "" {
		return task.Task{}, errors.New("缺少任务所有者或 Bot")
	}
	opt.OwnerID = req.OwnerID
	opt.TaskID = ""
	opt.ChoiceAnswer = nil
	opt.SecretAnswer = nil
	opt.ProjectTitle = ""
	// Attachments are part of the accepted authorization: the exact files the
	// user submitted are frozen with the task, and a retry that names different
	// files is a different request, not a replay of this one. The serialization
	// is shared with the retry comparison so both sides agree byte for byte.
	raw := AcceptanceOptionsJSON(opt)
	if raw == "" {
		return task.Task{}, errors.New("无法保存任务设置")
	}
	snapshot := cloneMessages(req.Messages)
	if len(snapshot) == 0 {
		return task.Task{}, errors.New("任务缺少已接受的对话上下文")
	}
	// The task id comes from the owner and the client request id only, so a
	// retry with a fresh session still lands on the same task.
	taskID := strings.TrimSpace(req.TaskID)
	if taskID == "" && req.ClientRequestID != "" {
		taskID = "task-" + acceptID(req.OwnerID, req.ClientRequestID)
	}
	if taskID == "" {
		taskID = "task-" + id.New()
	}
	want := task.Task{
		OwnerID: req.OwnerID, BotID: req.BotID, SessionID: req.SessionID,
		ProjectID: req.ProjectID, ServerID: req.ServerID, Goal: goal, OptionsJSON: string(raw),
		Purpose: req.Purpose, Hop: req.Hop, Untrusted: req.Untrusted,
		Checkpoint: task.Checkpoint{Messages: snapshot},
	}
	if existing, getErr := r.Book.Get(taskID); getErr == nil {
		// A retry of the same acceptance returns the same task; a different
		// request that collides on the id is a conflict, not a silent reuse.
		if !sameAcceptance(existing, want) {
			return task.Task{}, errors.New("任务标识已被另一个请求占用")
		}
		if existing.AcceptancePending {
			// The transcript write was never confirmed. Nothing may run on a
			// guess, and re-running the write could duplicate it.
			return task.Task{}, ErrAcceptanceIncomplete
		}
		return existing, nil
	}
	it, err := r.Book.Create(task.Task{
		ID: taskID, OwnerID: req.OwnerID, BotID: req.BotID, SessionID: req.SessionID,
		ProjectID: req.ProjectID, ServerID: req.ServerID, Goal: goal,
		Purpose: req.Purpose, Hop: req.Hop, Untrusted: req.Untrusted,
		// Acceptance is not complete yet: the task exists but is never
		// dispatched and Resume refuses it until the transcript is written.
		Status: task.StatusPaused, AcceptancePending: true, StepBudget: r.Agent.execution().MaxSteps,
		// The task-wide model-step budget is a multiple of the per-window
		// budget, so several automatic windows fit inside one task.
		MaxSteps:    taskStepBudget(r.Agent.execution().MaxSteps),
		MaxAttempts: maxAttempts,
		// A durable task always has a bounded lifetime: a task that can never
		// finish (a dead external job, a model that loops) must not sit in the
		// registry forever. It is persisted so a restart cannot extend it.
		Deadline:    time.Now().UTC().Add(taskLifetime),
		OptionsJSON: string(raw),
		Checkpoint:  task.Checkpoint{Messages: snapshot},
	})
	if err != nil {
		existing, getErr := r.Book.Get(taskID)
		if getErr == nil {
			// Lost a create race: same request may join the winner, a
			// different one must conflict.
			if !sameAcceptance(existing, want) {
				return task.Task{}, errors.New("任务标识已被另一个请求占用")
			}
			if existing.AcceptancePending {
				return task.Task{}, ErrAcceptanceIncomplete
			}
			return existing, nil
		}
		return task.Task{}, err
	}
	if req.AppendGoal != nil {
		if err := req.AppendGoal(ctx, it.SessionID); err != nil {
			// Roll back: no transcript, so nothing may run and no ghost stays.
			r.discardAcceptance(taskID, err)
			return task.Task{}, err
		}
	}
	it, err = r.Book.Update(taskID, func(t *task.Task) error {
		if t.Status != task.StatusPaused || !t.AcceptancePending {
			return errors.New("任务在创建过程中被修改")
		}
		t.AcceptancePending = false
		t.Status = task.StatusQueued
		return nil
	})
	if err != nil {
		r.discardAcceptance(taskID, err)
		return task.Task{}, err
	}
	r.Wake()
	return it, nil
}

// ErrAcceptanceIncomplete means an earlier attempt stopped between creating
// the task and confirming its transcript entry.
var ErrAcceptanceIncomplete = errors.New("上一次任务创建没有完成，请重新提交")

// discardAcceptance removes a task whose transcript write did not happen. A
// task that never ran has no history worth keeping, and leaving it behind
// would look accepted.
func (r *Runner) discardAcceptance(taskID string, cause error) {
	_, _ = r.Book.Update(taskID, func(t *task.Task) error {
		if t.Status == task.StatusQueued || t.Status == task.StatusRunning {
			return errNotQueued
		}
		t.AcceptancePending = true
		t.Status = task.StatusPaused
		t.Pending = nil
		t.LastError = "任务创建失败：" + cause.Error()
		return nil
	})
}

// CleanupIncompleteAcceptance fails tasks whose acceptance never completed, so
// a crash between the two acceptance steps cannot leave a task that looks
// accepted but has no transcript. It is called once when the runner starts.
func (r *Runner) CleanupIncompleteAcceptance(now time.Time) int {
	changed := 0
	for _, it := range r.Book.List("") {
		if !it.AcceptancePending {
			continue
		}
		if now.Sub(it.UpdatedAt) < acceptanceGrace {
			// A live acceptance may still be in progress in this process.
			continue
		}
		if _, err := r.Book.Update(it.ID, func(t *task.Task) error {
			if !t.AcceptancePending {
				return nil
			}
			t.AcceptancePending = false
			t.Status = task.StatusFailed
			t.LastError = "任务创建没有完成，未执行"
			t.Pending = nil
			return nil
		}); err == nil {
			changed++
		}
	}
	return changed
}

// acceptanceGrace is how long an unfinished acceptance is assumed to be live.
const acceptanceGrace = 2 * time.Minute

// maxAttempts bounds how many windows a task may spend trying to finish after
// it has already changed something.
const maxAttempts = 3

// taskLifetime bounds how long a durable task may stay active. It is generous
// enough for a long deployment but short enough that a stuck task cannot keep
// a runner slot, a pending card or an external job reservation forever. The
// value is persisted on the task, so a restart never extends it.
const taskLifetime = 12 * time.Hour

// taskStepBudget is the task-wide model-step budget derived from the per-window
// step budget: a task may span a few windows, but not run without bound.
func taskStepBudget(windowSteps int) int {
	if windowSteps <= 0 {
		windowSteps = 8
	}
	return windowSteps * 4
}

// taskOptionsSnapshot is the durable subset of Options. Attachments and secret
// answers are deliberately absent.
type taskOptionsSnapshot struct {
	Workspace      any                `json:"workspace,omitempty"`
	ApprovalRoute  *preferences.Route `json:"approval_route,omitempty"`
	ModelRoute     *preferences.Route `json:"model_route,omitempty"`
	ExcludedBotIDs []string           `json:"excluded_bot_ids,omitempty"`
	Mode           string             `json:"mode,omitempty"`
	ApprovalMode   string             `json:"approval_mode,omitempty"`
	// AttachmentIDs are the validated file ids the user submitted. They are
	// part of the frozen authorization and are persisted so a retry with
	// different files cannot adopt this task. There is never a session-wide or
	// live lookup here: only what acceptance already validated.
	AttachmentIDs []string `json:"attachment_ids,omitempty"`
}

// sameAcceptance reports whether an existing task is the same accepted request.
func sameAcceptance(existing, want task.Task) bool {
	return existing.OwnerID == want.OwnerID && existing.BotID == want.BotID &&
		existing.SessionID == want.SessionID && existing.Goal == want.Goal &&
		existing.ServerID == want.ServerID && existing.ProjectID == want.ProjectID &&
		existing.OptionsJSON == want.OptionsJSON &&
		existing.Purpose == want.Purpose && existing.Hop == want.Hop &&
		existing.Untrusted == want.Untrusted
}

// cloneMessages deep-copies the accepted conversation so later session writes
// cannot change what a queued task runs with.
func cloneMessages(in []map[string]any) []map[string]any {
	if len(in) == 0 {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// CreateRequest is what a transport asks the runtime to run as a task.
type CreateRequest struct {
	// TaskID reuses an existing task (resuming a waiting card) or is empty for
	// a new one.
	TaskID    string
	OwnerID   string
	BotID     string
	SessionID string
	ProjectID string
	ServerID  string
	Content   string
	// ClientRequestID makes acceptance idempotent: a retried request resolves
	// to the same task instead of creating a second one.
	ClientRequestID string
	// AppendGoal writes the user's transcript entry (with attachments) after
	// the task row exists but before it is queued. It is supplied by the API
	// layer so transcript storage stays out of the runtime.
	AppendGoal func(ctx context.Context, sessionID string) error
	// Messages is the accepted conversation window: system prompt plus the
	// user's message and any already-resolved history the API wants this task
	// to start from. It is deep-copied at acceptance, so later messages in the
	// same session never widen this task's authority.
	Messages []map[string]any
	// Purpose/Hop/Untrusted are the submission provenance the registry persists
	// on the task row at creation. They are written once, inside the
	// AcceptancePending create, so no window can ever run with a provenance the
	// dispatcher has not seen yet. A background submission sets Purpose to
	// "inbox"/"automation"; a message-driven one sets Hop and Untrusted.
	Purpose   string
	Hop       int
	Untrusted bool
}

// CreateTask is kept for transports that accept a task in one step. New code
// should use AcceptTask, which only queues after the transcript write.
func (r *Runner) CreateTask(ctx context.Context, req CreateRequest, opt Options) (task.Task, error) {
	return r.AcceptTask(ctx, req, opt)
}

// Pause marks a task paused. A running window stops at its next checkpoint.
func (r *Runner) Pause(ctx context.Context, ownerID, id string) (task.Task, error) {
	it, err := r.Book.Update(id, func(t *task.Task) error {
		if t.OwnerID != ownerID {
			return task.ErrNotFound
		}
		if t.Terminal() {
			return errors.New("任务已经结束")
		}
		t.Status = task.StatusPaused
		return nil
	})
	if err != nil {
		return task.Task{}, err
	}
	// Stop the in-flight window so pause takes effect now instead of after the
	// current model or tool call finishes.
	r.cancelHandle(id)
	// A child has its own runner slot, so pausing the parent must pause it too
	// and stop its window; otherwise it keeps working for a paused task.
	for _, child := range r.ChildTasks(id) {
		r.cancelHandle(child.ID)
	}
	if err := r.PropagatePause(id); err != nil {
		log.Printf("tasks: pause children of %s: %v", id, err)
	}
	return it, nil
}

// Resume re-queues a paused task, or answers a waiting one.
func (r *Runner) Resume(ctx context.Context, ownerID, id string) (task.Task, error) {
	it, err := r.Book.Update(id, func(t *task.Task) error {
		if t.OwnerID != ownerID {
			return task.ErrNotFound
		}
		if t.AcceptancePending {
			// The transcript was never confirmed: running it would execute a
			// task the user never saw accepted.
			return ErrAcceptanceIncomplete
		}
		switch t.Status {
		case task.StatusPaused:
			// A paused task may still be holding an undecided approval or an
			// unanswered card. Resuming must restore that waiting state, not
			// silently run past it.
			return resumeChild(t)
		case task.StatusWaitingUser, task.StatusWaitingApproval, task.StatusWaitingJob:
			return errors.New("请先回答等待中的问题")
		case task.StatusQueued:
			return nil
		default:
			return errors.New("任务已经结束")
		}
	})
	if err != nil {
		return task.Task{}, err
	}
	// Resume only the parent. A child paused by propagation is re-queued by the
	// delegation call when the parent replays the same ask_agent; children that
	// are waiting on the user or on an approval stay waiting, so resuming the
	// parent never bypasses an unanswered card or an undecided approval.
	for _, child := range r.ChildTasks(id) {
		if _, err := r.Book.Update(child.ID, func(t *task.Task) error {
			return resumeChild(t)
		}); err != nil {
			log.Printf("tasks: resume child %s: %v", child.ID, err)
		}
	}
	r.Wake()
	return it, nil
}

// resumeChild re-queues a paused child only when it has nothing left to wait
// for. A child still holding an undecided approval or an unanswered card is
// restored to that waiting state: resuming the parent must never turn an
// unanswered question into a silent run.
func resumeChild(t *task.Task) error {
	if t.AcceptancePending || t.Status != task.StatusPaused {
		return nil
	}
	if t.Pending != nil {
		switch t.Pending.Kind {
		case task.PendingApproval:
			t.Status = task.StatusWaitingApproval
			return nil
		case task.PendingUser:
			t.Status = task.StatusWaitingUser
			return nil
		}
	}
	t.Status = task.StatusQueued
	return nil
}

// Cancel stops a task for good. A running window sees the status at its next
// checkpoint; a paused one is cancelled immediately.
func (r *Runner) Cancel(ctx context.Context, ownerID, id string) (task.Task, error) {
	it, err := r.Book.Update(id, func(t *task.Task) error {
		if t.OwnerID != ownerID {
			return task.ErrNotFound
		}
		if t.Terminal() {
			return nil
		}
		t.Status = task.StatusCancelled
		t.Pending = nil
		return nil
	})
	if err != nil {
		return task.Task{}, err
	}
	// Stop the in-flight window now; the status check above already made the
	// cancellation durable, so the goroutine cannot overwrite it.
	r.cancelHandle(id)
	for _, child := range r.ChildTasks(id) {
		r.cancelHandle(child.ID)
	}
	if err := r.PropagateCancel(id); err != nil {
		log.Printf("tasks: cancel children of %s: %v", id, err)
	}
	r.Wake()
	return it, nil
}

// Guide appends an operator instruction without interrupting a running window.
func (r *Runner) Guide(ctx context.Context, ownerID, id, content string) (task.Task, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return task.Task{}, errors.New("引导内容不能为空")
	}
	it, err := r.Book.Update(id, func(t *task.Task) error {
		if t.OwnerID != ownerID {
			return task.ErrNotFound
		}
		if t.Terminal() {
			return errors.New("任务已经结束")
		}
		t.Guidance = append(t.Guidance, clip(content, task.MaxLine))
		return nil
	})
	if err != nil {
		return task.Task{}, err
	}
	r.Wake()
	return it, nil
}

// Decide records an operator decision for a pending approval and re-queues the
// task. The parked call resumes with this decision; the model is not asked to
// produce another action.
func (r *Runner) Decide(ctx context.Context, ownerID, id, approvalID string, allow bool, reason string) (task.Task, error) {
	it, err := r.Book.Update(id, func(t *task.Task) error {
		if t.OwnerID != ownerID {
			return task.ErrNotFound
		}
		// A decision must target the exact call the task is suspended on. The
		// pending record carries the call identity, so a decision for an older
		// approval in the same task can never requeue a different call.
		p := t.Pending
		if t.Status != task.StatusWaitingApproval || p == nil || p.Kind != task.PendingApproval {
			return errors.New("任务不在等待审批")
		}
		if p.ApprovalID == "" || p.ApprovalID != approvalID {
			return errors.New("这项审批不是任务当前等待的审批")
		}
		idx := -1
		for i := range t.Approvals {
			if t.Approvals[i].ID != approvalID {
				continue
			}
			if t.Approvals[i].CallID != p.CallID || t.Approvals[i].ArgsHash != p.ArgsHash {
				return errors.New("这项审批与挂起的调用不一致")
			}
			idx = i
			break
		}
		if idx < 0 {
			return task.ErrNotFound
		}
		if t.Approvals[idx].Status != task.ApprovalPending {
			if t.Approvals[idx].Status == decisionStatus(allow) {
				// An idempotent retry of the same decision is not an error and
				// must not execute the call again.
				return nil
			}
			return errors.New("这项审批已经处理过")
		}
		t.Approvals[idx].DecidedAt = time.Now().UTC()
		t.Approvals[idx].Note = clip(reason, task.MaxLine)
		t.Approvals[idx].Status = decisionStatus(allow)
		t.Status = task.StatusQueued
		return nil
	})
	if err != nil {
		return task.Task{}, err
	}
	r.Wake()
	return it, nil
}

// decisionStatus maps an allow/deny decision to the stored approval status.
func decisionStatus(allow bool) string {
	if allow {
		return task.ApprovalAllowed
	}
	return task.ApprovalDenied
}

var _ = model.Bot{}
