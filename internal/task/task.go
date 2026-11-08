// Package task stores durable agent tasks: a goal, a plan, progress and a
// journal of tool calls that survives a restart. Tasks are independent of any
// HTTP request or WebSocket connection; the registry is an encrypted,
// atomically replaced file, so no database driver is required.
package task

import "time"

// Task status. A task is never "completed" just because a model said so; the
// runtime records verification evidence through the finish tool.
const (
	StatusQueued          = "queued"
	StatusRunning         = "running"
	StatusWaitingUser     = "waiting_user"
	StatusWaitingApproval = "waiting_approval"
	StatusWaitingJob      = "waiting_job"
	StatusPaused          = "paused"
	StatusCompleted       = "completed"
	StatusFailed          = "failed"
	StatusCancelled       = "cancelled"
	StatusBudgetExhausted = "budget_exhausted"
)

// Journal states for one tool call.
const (
	JournalStarted     = "started"
	JournalDone        = "done"
	JournalFailed      = "failed"
	JournalInterrupted = "interrupted"
	// JournalWaiting marks a call parked for an operator decision. It is not
	// an unknown outcome: the call has not run, so resuming may execute it.
	JournalWaiting = "waiting"
)

// Pending kinds.
const (
	PendingTool     = "tool"
	PendingApproval = "approval"
	PendingUser     = "user"
	PendingJob      = "job"
)

// Sparse retry schedule for waiting_job: the first checks are quick, later
// ones back off so a long external job does not burn CPU, model calls or the
// operation log. Every wait is bounded by the task deadline.
var jobPollDelays = []time.Duration{
	10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute,
	5 * time.Minute, 10 * time.Minute, 15 * time.Minute,
}

// NextJobPoll returns how long to wait after the given attempt before checking
// an external job again.
func NextJobPoll(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(jobPollDelays) {
		return jobPollDelays[len(jobPollDelays)-1]
	}
	return jobPollDelays[attempt]
}

// Approval states.
const (
	ApprovalPending = "pending"
	ApprovalAllowed = "allowed"
	ApprovalDenied  = "denied"
)

// Limits keep the encrypted registry small. Older finished tasks and older
// journal rows are dropped first; a task that cannot be trimmed is refused.
const (
	MaxTasks              = 100
	MaxJournal            = 200
	MaxCheckpointMessages = 40
	MaxCheckpointBytes    = 256 << 10
	MaxFileBytes          = 16 << 20
	MaxGuidance           = 20
	MaxApprovals          = 50
	MaxPlanSteps          = 40
	MaxProgress           = 80
	MaxResult             = 8000
	MaxGoal               = 8000
	MaxOptionsJSON        = 8000
	MaxLine               = 2000
)

// Checkpoint is the resumable state of one task: the conversation window the
// model last saw, the next step, and short verified facts. It is written
// before and after every tool execution.
type Checkpoint struct {
	Messages     []map[string]any `json:"messages,omitempty"`
	Next         string           `json:"next,omitempty"`
	Facts        []string         `json:"facts,omitempty"`
	Compressions int              `json:"compressions,omitempty"`
}

// JournalEntry records one tool call. started without done/failed means the
// process stopped while the call was in flight; it must be reconciled, never
// replayed. Completed entries are kept so a resumed turn can reuse the result
// of the current call, not to deduplicate future identical commands.
type JournalEntry struct {
	At  time.Time `json:"at"`
	Seq int       `json:"seq"`
	// Turn is the assistant response that issued the call. A resumed turn
	// reuses only entries from its own turn, so an identical command issued
	// later in the task is still allowed to run.
	Turn     int    `json:"turn"`
	CallID   string `json:"call_id"`
	Name     string `json:"name"`
	ArgsHash string `json:"args_hash"`
	State    string `json:"state"`
	Result   string `json:"result,omitempty"`
	// CardMessageID is the exact chat card (ask_user/request_secret) this
	// entry is waiting on. It is persisted so a card answer is bound to the
	// call that produced the card, across restarts.
	CardMessageID string `json:"card_message_id,omitempty"`
	// Executed/Outcome record what actually happened to the call, typed rather
	// than encoded in Result text. Executed is true only when the call reached
	// its side effect; a refusal, a skipped call because it was never approved,
	// and an unknown state after a crash all leave it false. Outcome is a short
	// machine-readable verdict ("done", "failed", "denied", "skipped",
	// "unknown"). A resume must use them, not string matching on Result.
	Executed bool   `json:"executed,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
}

// Journal entry outcomes. They are stored next to Executed so a resumed window
// can tell "this really ran" from "this was refused", "this was skipped" and
// "we do not know". OutcomeUnknown is the important one: a mutating call that
// started and never reported back may or may not have taken effect, so the task
// must be verified before it may claim success.
const (
	OutcomeDone    = "done"
	OutcomeFailed  = "failed"
	OutcomeDenied  = "denied"
	OutcomeSkipped = "skipped"
	OutcomeUnknown = "unknown"
)

// Pending is the call or question the task is suspended on.
type Pending struct {
	CallID     string `json:"call_id,omitempty"`
	Name       string `json:"name,omitempty"`
	ArgsHash   string `json:"args_hash,omitempty"`
	Kind       string `json:"kind,omitempty"`
	ApprovalID string `json:"approval_id,omitempty"`
	// Job describes the external work a waiting_job task is parked on: a
	// reference the runtime can re-check (for example a background command
	// marker) and the next poll time. Polls, not busy loops.
	JobRef   string    `json:"job_ref,omitempty"`
	JobPolls int       `json:"job_polls,omitempty"`
	WakeAt   time.Time `json:"wake_at,omitempty"`
	// Turn is the assistant turn that issued the parked call. Together with
	// CallID and ArgsHash it names the exact call this pending state belongs
	// to, so a later turn that reuses a call id never inherits it.
	Turn int `json:"turn,omitempty"`
	// CardMessageID is the exact chat card message the task is suspended on.
	// An answer must target this message; an older card in the same session
	// is never accepted.
	CardMessageID string `json:"card_message_id,omitempty"`
	// Answer is the operator's reply used as the tool result when the task
	// resumes. It never contains a secret plaintext: secret cards store only
	// the vault acknowledgement.
	Answer string `json:"answer,omitempty"`
}

// Approval is a persisted operator decision. P2 wires the background ASK
// path; P1 stores decisions taken by the runtime.
type Approval struct {
	ID        string    `json:"id"`
	CallID    string    `json:"call_id,omitempty"`
	ArgsHash  string    `json:"args_hash,omitempty"`
	Command   string    `json:"command,omitempty"`
	Reasons   []string  `json:"reasons,omitempty"`
	Status    string    `json:"status"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	DecidedAt time.Time `json:"decided_at,omitempty"`
}

// Task is one durable unit of work.
type Task struct {
	ID        string `json:"id"`
	OwnerID   string `json:"owner_id"`
	BotID     string `json:"bot_id"`
	SessionID string `json:"session_id"`
	ProjectID string `json:"project_id,omitempty"`
	ServerID  string `json:"server_id,omitempty"`
	// ParentID/ParentTurn/ParentCallID link a delegated child task to the
	// parent task and the exact call that asked for it. They are set at
	// creation and never change: a child has its own session, checkpoint,
	// journal and approvals, and is not a second window of its parent.
	ParentID     string `json:"parent_id,omitempty"`
	ParentTurn   int    `json:"parent_turn,omitempty"`
	ParentCallID string `json:"parent_call_id,omitempty"`
	// ParentGoal is the parent's original goal, kept with the child so the
	// reviewer can see the user's authorization even though the child works on
	// a narrower delegated task. It is identity: fixed at creation.
	ParentGoal string `json:"parent_goal,omitempty"`
	// Purpose/Hop/Untrusted persist how a task was submitted. Purpose is the
	// usage actor ("inbox", "automation", ...): a background task must keep
	// its narrower capabilities after a restart. Hop is the bot-message chain
	// depth, so a restart cannot reset the hop limit. Untrusted means the task
	// was started from another bot's message, so its content stays tainted and
	// a command copied from it needs approval. All three are written once by
	// the submission adapter and never change.
	Purpose   string `json:"purpose,omitempty"`
	Hop       int    `json:"hop,omitempty"`
	Untrusted bool   `json:"untrusted,omitempty"`
	// AcceptancePending marks a task whose transcript entry is still being
	// written. It is set at creation and cleared once acceptance completes;
	// Resume must refuse to run it while true. It is lifecycle state, not
	// identity.
	AcceptancePending bool     `json:"acceptance_pending,omitempty"`
	Goal              string   `json:"goal"`
	Plan              []string `json:"plan,omitempty"`
	// PlanCompleted is the number of leading plan steps actually finished. It
	// is persisted so the card's segmented progress strip survives windows and
	// restarts, and it is reconciled whenever the plan is replaced. 0 means
	// nothing completed (or no plan yet); it is never derived from turns,
	// tool-call counts, budget or time.
	PlanCompleted int      `json:"plan_completed,omitempty"`
	Progress      []string `json:"progress,omitempty"`
	Next          string   `json:"next,omitempty"`
	Status        string   `json:"status"`
	Step          int      `json:"step"`
	// Turns counts assistant responses in this task, not retries.
	Turns       int       `json:"turns"`
	StepBudget  int       `json:"step_budget"`
	TotalSteps  int       `json:"total_steps"`
	MaxSteps    int       `json:"max_steps"`
	Attempts    int       `json:"attempts"`
	MaxAttempts int       `json:"max_attempts"`
	Deadline    time.Time `json:"deadline,omitempty"`
	// OptionsJSON is the per-task chat options (no attachment ids, no
	// plaintext secret answers). It is applied when a task resumes, so a
	// background task keeps its approval mode and model route.
	OptionsJSON string         `json:"options_json,omitempty"`
	Checkpoint  Checkpoint     `json:"checkpoint"`
	Journal     []JournalEntry `json:"journal,omitempty"`
	Pending     *Pending       `json:"pending,omitempty"`
	Approvals   []Approval     `json:"approvals,omitempty"`
	Guidance    []string       `json:"guidance,omitempty"`
	Result      string         `json:"result,omitempty"`
	Verified    []string       `json:"verified,omitempty"`
	// ResultMessageID is the exact assistant transcript message that carries
	// Result, recorded when the task writes its closing message. Consumers must
	// use it instead of guessing "the newest assistant message": a later task in
	// the same session would otherwise be mistaken for this task's answer.
	ResultMessageID string    `json:"result_message_id,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
}

// Terminal reports whether a task will not run again.
func (t Task) Terminal() bool {
	switch t.Status {
	case StatusCompleted, StatusFailed, StatusCancelled, StatusBudgetExhausted:
		return true
	}
	return false
}

// Waiting reports whether a task is suspended on an operator action.
func (t Task) Waiting() bool {
	return t.Status == StatusWaitingUser || t.Status == StatusWaitingApproval || t.Status == StatusWaitingJob
}

// Due reports whether a waiting_job task should be polled now.
func (t Task) Due(now time.Time) bool {
	if t.Status != StatusWaitingJob || t.Pending == nil {
		return false
	}
	if t.Pending.WakeAt.IsZero() {
		return true
	}
	return !t.Pending.WakeAt.After(now)
}

// Finished reports whether a task reached a final state, including pause.
func (t Task) Active() bool {
	switch t.Status {
	case StatusQueued, StatusRunning, StatusWaitingUser, StatusWaitingApproval, StatusWaitingJob, StatusPaused:
		return true
	}
	return false
}
