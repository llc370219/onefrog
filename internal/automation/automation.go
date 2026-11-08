// Package automation runs user-defined Bot tasks on a schedule, similar to
// recurring "tasks" in chat assistants: a saved prompt plus a schedule. Each run
// talks to the chosen Bot in a dedicated session and leaves a history entry.
package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"onefrog/internal/id"
	"onefrog/internal/maintenance"
	"onefrog/internal/model"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

const (
	MaxItems      = 50
	MaxName       = 40
	MaxPrompt     = 4000
	MinInterval   = 15          // minutes
	MaxInterval   = 7 * 24 * 60 // minutes
	RunTimeout    = 10 * time.Minute
	maxParallel   = 2
	SessionPrefix = "auto-"
	summaryLimit  = 300
)

const (
	KindInterval = "interval"
	KindDaily    = "daily"
	KindWeekly   = "weekly"
	KindOnce     = "once"
)

const (
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusFailed  = "failed"
	// StatusWaiting means the run stopped for a human decision (an approval
	// card or a card question). It is not a success and it is not a failure:
	// the stored task can still be resumed.
	StatusWaiting = "waiting"
)

var (
	ErrRunning = errors.New("任务正在运行")
	ErrFull    = fmt.Errorf("自动化任务最多 %d 个", MaxItems)
)

// ValidationError marks bad user input so the API can answer 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// Schedule describes when an automation fires. Time and Weekday use the
// server's local time zone.
type Schedule struct {
	Kind    string     `json:"kind"`
	Minutes int        `json:"minutes,omitempty"`
	Time    string     `json:"time,omitempty"`
	Weekday int        `json:"weekday,omitempty"`
	At      *time.Time `json:"at,omitempty"`
}

type Automation struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Prompt       string     `json:"prompt"`
	BotID        string     `json:"bot_id"`
	ServerID     string     `json:"server_id"`
	Schedule     Schedule   `json:"schedule"`
	ApprovalMode string     `json:"approval_mode"`
	Enabled      bool       `json:"enabled"`
	OwnerID      string     `json:"owner_id"`
	NextRunAt    *time.Time `json:"next_run_at,omitempty"`
	LastRunAt    *time.Time `json:"last_run_at,omitempty"`
	LastStatus   string     `json:"last_status,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	LastSummary  string     `json:"last_summary,omitempty"`
	SessionID    string     `json:"session_id,omitempty"`
	// TaskID is the durable task of the most recent run. It is persisted when
	// the run is accepted, before anything executes, so a restart observes the
	// same task instead of failing it or submitting the work again.
	TaskID string `json:"task_id,omitempty"`
	// RunID identifies one scheduled firing. A retry of that firing reuses it,
	// so the runtime resolves to the same task.
	RunID     string    `json:"run_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Runner performs one run and returns the session used and the Bot's reply.
type Runner func(ctx context.Context, it Automation) (sessionID, text string, err error)

// Observation is what a durable run currently looks like. Text is only set for
// a completed run, so a waiting or running run can never be recorded as a
// success.
type Observation struct {
	SessionID string
	Text      string
	Status    string
	// Waiting means the run stopped for a human decision; Done means it reached
	// a terminal state. Neither is implied by the other.
	Waiting bool
	Done    bool
}

// DurableRunner executes a run through the shared task runtime. Submit accepts
// the run and returns its durable task id; it must persist before returning so
// the scheduler can record the id before any work happens. Wait observes that
// task until it stops making progress.
type DurableRunner struct {
	Submit func(ctx context.Context, it Automation, runID string) (taskID, sessionID string, err error)
	Wait   func(ctx context.Context, it Automation, taskID string) (Observation, error)
	// Observe reads the current state without waiting; the scheduler uses it on
	// restart so an interrupted run is inspected instead of failed.
	Observe func(ctx context.Context, it Automation, taskID string) (Observation, error)
}

// SessionID is the chat session an automation writes into.
func SessionID(it Automation) string { return SessionPrefix + it.ID + "-" + it.BotID }

// IsSession reports whether a session id belongs to an automation.
func IsSession(id string) bool { return strings.HasPrefix(id, SessionPrefix) }

type bookFile struct {
	Items []Automation `json:"items"`
}

// Book stores automations (encrypted on disk) and runs them when due.
type Book struct {
	Gate  *maintenance.Gate
	path  string
	box   *secret.Box
	store store.Store

	mu      sync.Mutex
	items   map[string]Automation
	running map[string]bool
	runner  Runner
	durable *DurableRunner
	base    context.Context

	slots chan struct{}
	wake  chan struct{}
	wg    sync.WaitGroup
	now   func() time.Time
	loc   *time.Location
}

// New loads the book from path. An empty path or nil box keeps it in memory.
func New(path string, box *secret.Box, st store.Store) (*Book, error) {
	b := &Book{
		path: path, box: box, store: st,
		items: map[string]Automation{}, running: map[string]bool{},
		base:  context.Background(),
		slots: make(chan struct{}, maxParallel), wake: make(chan struct{}, 1),
		now: func() time.Time { return time.Now().UTC() }, loc: time.Local,
	}
	if path == "" || box == nil {
		return b, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := box.Decrypt(string(raw))
	if err != nil {
		return nil, err
	}
	var data bookFile
	if err = json.Unmarshal([]byte(plain), &data); err != nil {
		return nil, err
	}
	for _, it := range data.Items {
		if it.ID == "" {
			continue
		}
		// A run that was in flight when the process stopped is not a failure:
		// its durable task may still be parked on a human decision or still
		// running in the registry. Keep both the status and the task id, and let
		// the startup reconcile observe the stored task. Only the turn-bound
		// path (no task id) has nothing to observe, so that one is failed.
		if it.LastStatus == StatusRunning {
			// RunID alone is enough to recover: submission is idempotent per run
			// identity, so a crash between submit and the TaskID write re-submits
			// the same run and adopts the same task instead of starting a new
			// run. Only a run with no durable identity at all is a failure.
			if it.RunID != "" || it.TaskID != "" {
				it.LastStatus = StatusWaiting
				it.LastError = "服务重启，正在核对任务状态"
			} else {
				it.LastStatus = StatusFailed
				it.LastError = "服务重启，运行被中断"
			}
		}
		b.items[it.ID] = it
	}
	return b, nil
}

// SetRunner installs the function that talks to the Bot.
func (b *Book) SetRunner(r Runner) {
	b.mu.Lock()
	b.runner = r
	b.mu.Unlock()
}

// SetDurableRunner installs the durable task-backed runner. When it is set, a
// run is submitted as a task, its id is persisted before anything executes, and
// a restart observes that task instead of failing it or resubmitting the work.
func (b *Book) SetDurableRunner(r *DurableRunner) {
	b.mu.Lock()
	b.durable = r
	b.mu.Unlock()
}

// Wake makes the scheduler re-check due items without blocking.
func (b *Book) Wake() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *Book) List() []Automation {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Automation, 0, len(b.items))
	for _, it := range b.items {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (b *Book) Get(id string) (Automation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	it, ok := b.items[id]
	if !ok {
		return Automation{}, store.ErrNotFound
	}
	return it, nil
}

func (b *Book) Create(in Automation, ownerID string) (Automation, error) {
	now := b.now()
	it, err := clean(in, now)
	if err != nil {
		return Automation{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) >= MaxItems {
		return Automation{}, ErrFull
	}
	it.ID = id.New()
	it.OwnerID = ownerID
	it.CreatedAt, it.UpdatedAt = now, now
	it.LastStatus, it.LastError, it.LastSummary, it.SessionID, it.LastRunAt = "", "", "", "", nil
	it.NextRunAt = b.nextFor(it, now)
	b.items[it.ID] = it
	if err = b.saveLocked(); err != nil {
		delete(b.items, it.ID)
		return Automation{}, err
	}
	b.Wake()
	return it, nil
}

func (b *Book) Update(id string, in Automation) (Automation, error) {
	now := b.now()
	c, err := clean(in, now)
	if err != nil {
		return Automation{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	old, ok := b.items[id]
	if !ok {
		return Automation{}, store.ErrNotFound
	}
	it := old
	it.Name, it.Prompt, it.BotID, it.ServerID = c.Name, c.Prompt, c.BotID, c.ServerID
	it.Schedule, it.ApprovalMode, it.Enabled = c.Schedule, c.ApprovalMode, c.Enabled
	it.UpdatedAt = now
	it.NextRunAt = b.nextFor(it, now)
	b.items[id] = it
	if err = b.saveLocked(); err != nil {
		b.items[id] = old
		return Automation{}, err
	}
	b.Wake()
	return it, nil
}

func (b *Book) Delete(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	old, ok := b.items[id]
	if !ok {
		return store.ErrNotFound
	}
	delete(b.items, id)
	if err := b.saveLocked(); err != nil {
		b.items[id] = old
		return err
	}
	return nil
}

// ForgetServer detaches automations from a deleted server.
func (b *Book) ForgetServer(serverID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	dirty := false
	for k, it := range b.items {
		if it.ServerID == serverID {
			it.ServerID = ""
			b.items[k] = it
			dirty = true
		}
	}
	if !dirty {
		return nil
	}
	return b.saveLocked()
}

// RunNow starts one run immediately, regardless of the schedule.
func (b *Book) RunNow(id string) (Automation, error) {
	if b.Gate.Paused() {
		return Automation{}, maintenance.ErrPaused
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	it, ok := b.items[id]
	if !ok {
		return Automation{}, store.ErrNotFound
	}
	if b.running[id] {
		return Automation{}, ErrRunning
	}
	// A firing whose durable run is still unresolved also blocks RunNow: the
	// caller must see "already running" instead of a silent no-op that looks
	// like a fresh run was started.
	if b.pendingLocked(it) {
		return Automation{}, ErrRunning
	}
	return b.startLocked(it, false), nil
}

// Run dispatches due automations until ctx ends, then waits for running ones.
func (b *Book) Run(ctx context.Context) {
	b.mu.Lock()
	b.base = ctx
	b.mu.Unlock()
	b.reconcile()
	defer b.wg.Wait()
	tick := time.NewTicker(reconcileEvery)
	defer tick.Stop()
	for {
		wait := b.dispatchDue()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-tick.C:
			timer.Stop()
			// A parked run whose approval was granted elsewhere resumes in the
			// task runtime; observing periodically keeps its scheduler status
			// truthful without waiting for a restart.
			b.reconcile()
		case <-b.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// reconcileEvery bounds how long a scheduler status can lag the durable task
// state (for example a run approved while the process kept running).
const reconcileEvery = 30 * time.Second

// reconcile observes runs that were in flight when the process stopped. A run
// whose durable task still exists is re-read: a completed task records its
// result, a parked or running one stays waiting. Nothing is resubmitted and
// nothing is marked failed just because the process restarted.
func (b *Book) reconcile() {
	b.mu.Lock()
	durable := b.durable
	var pending []Automation
	for _, it := range b.items {
		if it.TaskID == "" || (it.LastStatus != StatusRunning && it.LastStatus != StatusWaiting) {
			continue
		}
		pending = append(pending, it)
	}
	b.mu.Unlock()
	if durable == nil {
		return
	}
	if durable.Observe != nil {
		for _, it := range pending {
			ctx, cancel := context.WithTimeout(b.background(), 15*time.Second)
			obs, err := durable.Observe(ctx, it, it.TaskID)
			cancel()
			if err != nil {
				log.Printf("automation: reconcile %s: %v", it.ID, err)
				continue
			}
			b.applyObservation(it.ID, it.RunID, it.TaskID, obs)
		}
	}
	// A run that was submitted but whose task id was never written is recovered
	// through its persisted run identity: re-submitting the same identity
	// adopts the same task, so the work is observed instead of run twice.
	for _, it := range b.unidentifiedRuns() {
		ctx, cancel := context.WithTimeout(b.background(), 15*time.Second)
		sid, text, err := b.executeDurable(ctx, it, durable)
		cancel()
		if err != nil && !errors.Is(err, errWaiting) {
			log.Printf("automation: recover run %s: %v", it.ID, err)
		}
		// Record the recovered outcome exactly like a live run, so a recovered
		// task does not leave the automation stuck in waiting forever.
		b.finish(it, sid, text, err)
	}
}

// unidentifiedRuns returns runs whose durable task id is still unknown, so
// startup can recover them by re-submitting the same run identity. New marks a
// running run with a RunID as waiting, so both statuses are covered; the
// running case is kept for a run that started after New.
func (b *Book) unidentifiedRuns() []Automation {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Automation
	for _, it := range b.items {
		if it.RunID == "" || it.TaskID != "" {
			continue
		}
		if it.LastStatus != StatusWaiting && it.LastStatus != StatusRunning {
			continue
		}
		out = append(out, it)
	}
	return out
}

// observedStatus maps a durable task status to the scheduler's status. Only a
// completed task is a success; a parked or paused one keeps waiting; every
// other terminal state is a failure, never a silent success.
func observedStatus(obs Observation) string {
	switch {
	case obs.Status == "completed" || (obs.Done && obs.Status == "" && !obs.Waiting):
		return StatusSuccess
	case obs.Waiting:
		return StatusWaiting
	case obs.Status == "paused":
		return StatusWaiting
	case obs.Done:
		// failed, cancelled, budget_exhausted: a terminal run that did not
		// finish its goal. Reporting it as success would hide the failure.
		return StatusFailed
	default:
		// queued, running, waiting_job: still active.
		return StatusRunning
	}
}

// applyObservation records an observed run state truthfully. Only a completed
// run becomes a success, a parked one stays waiting, and a failed or cancelled
// terminal run stays failed. The observation must still belong to the run the
// automation is currently on: a stale one is discarded.
func (b *Book) applyObservation(id, runID, taskID string, obs Observation) {
	b.mu.Lock()
	defer b.mu.Unlock()
	it, ok := b.items[id]
	if !ok {
		return
	}
	if runID != "" && it.RunID != "" && it.RunID != runID {
		return
	}
	if taskID != "" && it.TaskID != "" && it.TaskID != taskID {
		return
	}
	prev := it
	switch status := observedStatus(obs); status {
	case StatusSuccess:
		it.LastStatus = StatusSuccess
		it.LastError = ""
		it.LastSummary = clip(strings.TrimSpace(obs.Text), summaryLimit)
	case StatusWaiting:
		it.LastStatus = StatusWaiting
		it.LastError = "等待审批或用户回答后才能继续"
		it.LastSummary = ""
	case StatusFailed:
		it.LastStatus = StatusFailed
		it.LastError = clip(firstNonEmptyText(obs.Text, "任务没有完成："+obs.Status), summaryLimit)
		it.LastSummary = ""
	default:
		it.LastStatus = StatusRunning
		it.LastError = ""
	}
	if obs.SessionID != "" {
		it.SessionID = obs.SessionID
	}
	b.items[id] = it
	if err := b.saveLocked(); err != nil {
		log.Printf("automation: save: %v", err)
		b.items[id] = prev
	}
}

// firstNonEmptyText returns the first non-blank candidate.
func firstNonEmptyText(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func (b *Book) background() context.Context {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.base == nil {
		return context.Background()
	}
	return b.base
}

func (b *Book) dispatchDue() time.Duration {
	done, err := b.Gate.Enter()
	if err != nil {
		return time.Second
	}
	defer done()
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	wait := time.Minute
	for _, it := range b.items {
		if !it.Enabled || it.NextRunAt == nil {
			continue
		}
		if !it.NextRunAt.After(now) {
			if b.running[it.ID] || b.pendingLocked(it) {
				// Still busy with the previous run: skip this slot.
				it.NextRunAt = b.nextFor(it, now)
				b.items[it.ID] = it
				if err := b.saveLocked(); err != nil {
					log.Printf("automation: save: %v", err)
				}
			} else {
				it = b.startLocked(it, true)
			}
		}
		if it.NextRunAt != nil {
			if d := it.NextRunAt.Sub(now); d < wait {
				wait = d
			}
		}
	}
	if wait < time.Second {
		wait = time.Second
	}
	return wait
}

// pendingLocked reports whether a firing still owns an unresolved durable run.
// The run identity is written before anything executes and survives until the
// run reaches a terminal state, so RunID plus a running/waiting status is the
// durable marker of "this automation still has one run in flight" — including
// the recovery window where a RunID exists but the TaskID write did not land,
// and the window where an observation turned the run back to running.
func (b *Book) pendingLocked(it Automation) bool {
	if b.durable == nil || strings.TrimSpace(it.RunID) == "" {
		return false
	}
	return it.LastStatus == StatusRunning || it.LastStatus == StatusWaiting
}

func (b *Book) startLocked(it Automation, scheduled bool) Automation {
	done, err := b.Gate.Enter()
	if err != nil {
		return it
	}
	// No overlap: while the previous durable run is still unresolved (running,
	// parked on a human decision, or awaiting recovery) a new firing must not
	// replace its task id or start a second task.
	if b.pendingLocked(it) {
		done()
		return it
	}
	now := b.now()
	prev := it
	// Every firing gets its own run identity. Reusing the previous one would
	// resolve to the previous task and silently reuse its result.
	it.RunID = id.New()
	it.TaskID = ""
	it.LastStatus = StatusRunning
	it.LastError = ""
	it.LastRunAt = &now
	if scheduled || it.Schedule.Kind != KindOnce {
		it.NextRunAt = b.nextFor(it, now)
	}
	b.items[it.ID] = it
	if err := b.saveLocked(); err != nil {
		// The run identity must be durable before anything executes, otherwise
		// a retry could not prove it is the same firing. Roll back and fail
		// closed instead of running an unidentified run.
		log.Printf("automation: save: %v", err)
		b.items[it.ID] = prev
		done()
		return prev
	}
	b.running[it.ID] = true
	b.wg.Add(1)
	go func() { defer done(); b.execute(b.base, it) }()
	return it
}

func (b *Book) execute(parent context.Context, it Automation) {
	defer b.wg.Done()
	var (
		sid, out string
		err      error
	)
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("运行异常：%v", r)
		}
		b.finish(it, sid, out, err)
	}()
	select {
	case b.slots <- struct{}{}:
	case <-parent.Done():
		err = errors.New("服务正在停止")
		return
	}
	defer func() { <-b.slots }()
	b.mu.Lock()
	runner := b.runner
	durable := b.durable
	b.mu.Unlock()
	if durable != nil {
		sid, out, err = b.executeDurable(parent, it, durable)
		return
	}
	if runner == nil {
		err = errors.New("Bot 服务未配置")
		return
	}
	ctx, cancel := context.WithTimeout(parent, RunTimeout)
	defer cancel()
	sid, out, err = runner(ctx, it)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = errors.New("运行超时")
	}
}

// errWaiting marks a run that stopped for a human decision or is still active.
// finish maps it to StatusWaiting instead of success or failure.
var errWaiting = errors.New("运行等待处理")

// isRejected reports whether an error definitely means "this run was refused
// before anything was accepted". Such a run may be recorded as failed; every
// other submission error is uncertain and must be retried with the same run
// identity.
func isRejected(err error) bool {
	var r interface{ Rejected() bool }
	return errors.As(err, &r) && r.Rejected()
}

// executeDurable submits the run as a durable task, persists its id before any
// work happens, and then observes the task. A run that parks on a human
// decision is recorded as waiting, never success, and its task id is kept so
// the decision resumes that task instead of starting another run.
func (b *Book) executeDurable(parent context.Context, it Automation, d *DurableRunner) (sid, out string, err error) {
	runID := strings.TrimSpace(it.RunID)
	if runID == "" {
		// Without a persisted identity this run cannot be proven to be the same
		// firing on a retry, so it must not execute. startLocked always mints one.
		return "", "", errors.New("运行标识缺失，未执行")
	}
	ctx, cancel := context.WithTimeout(parent, RunTimeout)
	defer cancel()
	// Submission is idempotent per run identity: after a crash between submit
	// and the TaskID write, this re-submits the same run and adopts the same
	// task instead of creating a second one. An adopted TaskID is kept, so no
	// new identity is ever invented for it.
	taskID := strings.TrimSpace(it.TaskID)
	sessionID := strings.TrimSpace(it.SessionID)
	submitted, submittedSession, submitErr := d.Submit(ctx, it, runID)
	if submitErr != nil {
		// A definite local rejection can never succeed on retry, so it is a
		// failure. Anything else (store, registry, network, timeout) is an
		// uncertain submission: the task may have been accepted, so the run
		// keeps its identity and stays waiting for the same run to be retried
		// instead of being marked failed and replaced by a new firing.
		if isRejected(submitErr) {
			return "", "", submitErr
		}
		return "", "", fmt.Errorf("%w：提交结果未知（%v）", errWaiting, submitErr)
	}
	if taskID != "" && submitted != "" && submitted != taskID {
		return "", "", errors.New("运行对应的任务不一致，未执行")
	}
	if taskID == "" {
		taskID = submitted
	}
	if submittedSession != "" {
		sessionID = submittedSession
	}
	if taskID == "" {
		return sessionID, "", errors.New("运行没有生成任务，未执行")
	}
	sid = sessionID
	b.mu.Lock()
	// The observation must target this firing only: a stale observation (the
	// automation already moved on to another run) is discarded.
	cur, ok := b.items[it.ID]
	stale := !ok || (cur.RunID != "" && cur.RunID != runID)
	if !stale {
		prev := cur
		cur.TaskID = taskID
		if sessionID != "" {
			cur.SessionID = sessionID
		}
		b.items[it.ID] = cur
		if saveErr := b.saveLocked(); saveErr != nil {
			log.Printf("automation: save task id: %v", saveErr)
			b.items[it.ID] = prev
			b.mu.Unlock()
			// The task was accepted, but its id could not be persisted. The run
			// must stay unresolved with the same identity so a retry adopts the
			// same task: reporting a failure here would free the automation for
			// a new firing while the accepted task is still running.
			return sid, "", fmt.Errorf("%w：任务标识保存失败，等待恢复", errWaiting)
		}
	}
	b.mu.Unlock()
	if stale {
		return sid, "", nil
	}
	if d.Wait == nil {
		// Nothing can observe the task, so its outcome is unknown. Reporting
		// success here would claim a result nobody read.
		return sid, "", errWaiting
	}
	obs, waitErr := d.Wait(ctx, it, taskID)
	if obs.SessionID != "" {
		sid = obs.SessionID
	}
	// The status mapping is the single source of truth: only a completed run is
	// a success. A failed, cancelled or budget-exhausted run is a failure, and a
	// parked, paused or still-active one keeps waiting.
	switch observedStatus(obs) {
	case StatusSuccess:
		return sid, obs.Text, nil
	case StatusWaiting:
		return sid, "", errWaiting
	case StatusFailed:
		if waitErr != nil {
			return sid, "", waitErr
		}
		return sid, "", errors.New(firstNonEmptyText(obs.Text, "运行没有完成："+obs.Status))
	default:
		if waitErr != nil {
			return sid, "", waitErr
		}
		return sid, "", errWaiting
	}
}

func (b *Book) finish(ran Automation, sid, out string, runErr error) {
	status, detail := StatusSuccess, strings.TrimSpace(out)
	switch {
	case errors.Is(runErr, errWaiting):
		// A parked run needs a human decision or is still active. It is not a
		// success and not a failure: the durable task stays resumable.
		status, detail = StatusWaiting, "等待处理"
	case runErr != nil:
		status, detail = StatusFailed, runErr.Error()
	}
	if sid == "" {
		sid = SessionID(ran)
	}
	b.mu.Lock()
	it, ok := b.items[ran.ID]
	// Only the current firing may write the final status. A late result from a
	// previous firing (the automation already moved on) must not overwrite the
	// newer run's state, its task id, or the busy marker of the current run.
	if ok && ran.RunID != "" && it.RunID != "" && it.RunID != ran.RunID {
		b.mu.Unlock()
		return
	}
	delete(b.running, ran.ID)
	if ok {
		prev := it
		it.LastStatus = status
		switch {
		case status == StatusWaiting:
			it.LastError = "等待审批或用户回答后才能继续"
			it.LastSummary = ""
		case runErr != nil:
			it.LastError = clip(detail, summaryLimit)
			it.LastSummary = ""
		default:
			it.LastError = ""
			it.LastSummary = clip(detail, summaryLimit)
		}
		it.SessionID = sid
		if it.Schedule.Kind == KindOnce && it.NextRunAt == nil {
			it.Enabled = false
		}
		b.items[it.ID] = it
		if err := b.saveLocked(); err != nil {
			log.Printf("automation: save: %v", err)
			// Never keep an unpersisted success. Disk still holds the pre-run
			// state, which carries the run identity, so the run stays unresolved
			// and a retry reconciles the same task; in memory it is marked
			// waiting too, so no new firing can start while disk is stale.
			status = StatusWaiting
			detail = "运行结果保存失败，等待恢复"
			prev.LastStatus = status
			prev.LastError = detail
			prev.LastSummary = ""
			b.items[it.ID] = prev
		}
	}
	st := b.store
	b.mu.Unlock()
	if st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := model.History{BotID: ran.BotID, ServerID: ran.ServerID, Action: "自动化 " + ran.Name, Command: "自动化任务：" + ran.Name + "\n" + clip(detail, 2000), Status: status}
	if bot, err := st.GetBot(ctx, ran.BotID); err == nil {
		h.BotName = bot.Name
	}
	if ran.ServerID != "" {
		if rec, err := st.GetServer(ctx, ran.ServerID); err == nil {
			h.ServerName = rec.Name
		}
	}
	if _, err := st.AddHistory(ctx, store.HistoryRec{History: h}); err != nil {
		log.Printf("automation: history: %v", err)
	}
}

func (b *Book) nextFor(it Automation, now time.Time) *time.Time {
	if !it.Enabled {
		return nil
	}
	return Next(it.Schedule, now, b.loc)
}

// Next returns the first fire time strictly after "after", or nil if none.
func Next(s Schedule, after time.Time, loc *time.Location) *time.Time {
	if loc == nil {
		loc = time.Local
	}
	var t time.Time
	switch s.Kind {
	case KindInterval:
		if s.Minutes <= 0 {
			return nil
		}
		t = after.Add(time.Duration(s.Minutes) * time.Minute)
	case KindDaily, KindWeekly:
		h, m, ok := parseClock(s.Time)
		if !ok {
			return nil
		}
		local := after.In(loc)
		t = time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, loc)
		step := 1
		if s.Kind == KindWeekly {
			step = 7
			t = t.AddDate(0, 0, (s.Weekday-int(t.Weekday())+7)%7)
		}
		for !t.After(after) {
			t = t.AddDate(0, 0, step)
		}
	case KindOnce:
		if s.At == nil || !s.At.After(after) {
			return nil
		}
		t = *s.At
	default:
		return nil
	}
	t = t.UTC()
	return &t
}

func parseClock(v string) (int, int, bool) {
	hs, ms, ok := strings.Cut(strings.TrimSpace(v), ":")
	if !ok {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(hs)
	m, err2 := strconv.Atoi(ms)
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

func clean(in Automation, now time.Time) (Automation, error) {
	out := Automation{
		Name:         strings.TrimSpace(in.Name),
		Prompt:       strings.TrimSpace(in.Prompt),
		BotID:        strings.TrimSpace(in.BotID),
		ServerID:     strings.TrimSpace(in.ServerID),
		ApprovalMode: strings.TrimSpace(in.ApprovalMode),
		Enabled:      in.Enabled,
	}
	if n := utf8.RuneCountInString(out.Name); n == 0 || n > MaxName {
		return out, invalid("名称需要 1-%d 个字", MaxName)
	}
	for _, r := range out.Name {
		if unicode.IsControl(r) {
			return out, invalid("名称不能包含控制字符")
		}
	}
	if n := utf8.RuneCountInString(out.Prompt); n == 0 || n > MaxPrompt {
		return out, invalid("任务内容需要 1-%d 个字", MaxPrompt)
	}
	if out.BotID == "" {
		return out, invalid("请选择 Bot")
	}
	switch out.ApprovalMode {
	case "", "smart", "model", "plan":
	default:
		return out, invalid("审批模式无效")
	}
	s := Schedule{Kind: strings.TrimSpace(in.Schedule.Kind)}
	switch s.Kind {
	case KindInterval:
		if in.Schedule.Minutes < MinInterval || in.Schedule.Minutes > MaxInterval {
			return out, invalid("间隔需要 %d 分钟到 7 天", MinInterval)
		}
		s.Minutes = in.Schedule.Minutes
	case KindDaily, KindWeekly:
		h, m, ok := parseClock(in.Schedule.Time)
		if !ok {
			return out, invalid("时间格式应为 HH:MM")
		}
		s.Time = fmt.Sprintf("%02d:%02d", h, m)
		if s.Kind == KindWeekly {
			if in.Schedule.Weekday < 0 || in.Schedule.Weekday > 6 {
				return out, invalid("星期无效")
			}
			s.Weekday = in.Schedule.Weekday
		}
	case KindOnce:
		if in.Schedule.At == nil || in.Schedule.At.IsZero() {
			return out, invalid("请选择运行时间")
		}
		at := in.Schedule.At.UTC().Truncate(time.Minute)
		if out.Enabled && !at.After(now) {
			return out, invalid("运行时间需要晚于现在")
		}
		s.At = &at
	default:
		return out, invalid("计划类型无效")
	}
	out.Schedule = s
	return out, nil
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

func (b *Book) saveLocked() error {
	if b.path == "" || b.box == nil {
		return nil
	}
	data := bookFile{Items: make([]Automation, 0, len(b.items))}
	for _, it := range b.items {
		data.Items = append(data.Items, it)
	}
	sort.Slice(data.Items, func(i, j int) bool { return data.Items[i].ID < data.Items[j].ID })
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	enc, err := b.box.Encrypt(string(raw))
	if err != nil {
		return err
	}
	dir := filepath.Dir(b.path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".automations-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.WriteString(enc); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, b.path)
}
