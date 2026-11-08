package bot

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"onefrog/internal/task"
)

// Task activity: a bounded, safe event trail rendered on the task card.
// Events record tool start/end/error and model/plan/verification boundaries
// with generic labels — never raw args, output, file contents or secrets.
// The trail lives on the Runner (process memory) and is rebuilt from the
// durable journal when missing, so a page refresh reconnects to the card.

// ActivityEvent is one safe activity record.
type ActivityEvent struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	Name       string `json:"name"`
	Label      string `json:"label"`
	Status     string `json:"status"` // running|completed|failed|interrupted
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// Activity statuses.
const (
	ActivityRunning     = "running"
	ActivityCompleted   = "completed"
	ActivityFailed      = "failed"
	ActivityInterrupted = "interrupted"
)

// maxTaskActivities bounds the per-task trail kept in memory.
const maxTaskActivities = 40

// activityStore keeps a per-task ring of safe events. Every event carries a
// stable task-scoped sequence number (durable across restarts via the
// journal), so React keys never collide and reconstruction is deterministic.
type activityStore struct {
	mu    sync.Mutex
	tasks map[string][]ActivityEvent
	seq   map[string]int
}

func newActivityStore() *activityStore {
	return &activityStore{tasks: map[string][]ActivityEvent{}, seq: map[string]int{}}
}

func (s *activityStore) add(ev ActivityEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq[ev.TaskID]++
	ev.ID = "a-" + itoaActivity(s.seq[ev.TaskID])
	rows := append(s.tasks[ev.TaskID], ev)
	if len(rows) > maxTaskActivities {
		rows = rows[len(rows)-maxTaskActivities:]
	}
	s.tasks[ev.TaskID] = rows
}

// seedSeq aligns the in-memory sequence with the durable journal so
// reconstructed events and live events never share an ID.
func (s *activityStore) seedSeq(taskID string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seq[taskID] < n {
		s.seq[taskID] = n
	}
}

// close updates the matching running event (same task, name and label).
func (s *activityStore) close(taskID, name, label, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.tasks[taskID]
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Status == ActivityRunning && rows[i].Name == name && rows[i].Label == label {
			rows[i].Status = status
			rows[i].FinishedAt = timeNowUTC().Format(time.RFC3339)
			if started, err := time.Parse(time.RFC3339, rows[i].StartedAt); err == nil {
				rows[i].DurationMS = time.Since(started).Milliseconds()
			}
			break
		}
	}
}

// list returns the task's events, oldest first.
func (s *activityStore) list(taskID string) []ActivityEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.tasks[taskID]
	out := make([]ActivityEvent, len(rows))
	copy(out, rows)
	return out
}

func timeNowUTC() time.Time { return time.Now().UTC() }

// ActivityTracker records safe events for one task.
type ActivityTracker struct {
	store  *activityStore
	taskID string
}

func (r *Runner) tracker(taskID string) *ActivityTracker {
	r.actMu.Lock()
	if r.activities == nil {
		r.activities = newActivityStore()
	}
	store := r.activities
	r.actMu.Unlock()
	return &ActivityTracker{store: store, taskID: taskID}
}

// toolStart records a tool execution start with a safe label.
func (t *ActivityTracker) toolStart(name, label string) {
	if t == nil {
		return
	}
	t.store.add(ActivityEvent{
		TaskID: t.taskID, Name: name, Label: safeLabel(label),
		Status: ActivityRunning, StartedAt: timeNowUTC().Format(time.RFC3339),
	})
}

// toolEnd closes the most recent matching running event.
func (t *ActivityTracker) toolEnd(name, label, status string) {
	if t == nil {
		return
	}
	if status != ActivityCompleted && status != ActivityFailed && status != ActivityInterrupted {
		status = ActivityCompleted
	}
	t.store.close(t.taskID, name, safeLabel(label), status)
}

// modelStep records a model turn boundary (no duration tracking needed: it
// closes immediately).
func (t *ActivityTracker) modelStep(label string) {
	if t == nil {
		return
	}
	now := timeNowUTC().Format(time.RFC3339)
	t.store.add(ActivityEvent{TaskID: t.taskID, Name: "model", Label: safeLabel(label), Status: ActivityCompleted, StartedAt: now, FinishedAt: now})
}

// note records a one-shot boundary event (plan saved, verification, etc.).
func (t *ActivityTracker) note(name, label string) {
	if t == nil {
		return
	}
	now := timeNowUTC().Format(time.RFC3339)
	t.store.add(ActivityEvent{TaskID: t.taskID, Name: name, Label: safeLabel(label), Status: ActivityCompleted, StartedAt: now, FinishedAt: now})
}

// safeLabel clips a label and strips anything that looks like content: only
// the first line survives, and length is bounded.
func safeLabel(label string) string {
	label = strings.TrimSpace(label)
	if i := strings.IndexAny(label, "\r\n"); i >= 0 {
		label = label[:i]
	}
	return clip(label, 120)
}

// safeToolLabel builds the generic label for one tool call: tool name plus a
// small safe target extracted from the arguments. It never includes full
// argument JSON, commands or file contents.
func safeToolLabel(call toolCall) string {
	name := call.Function.Name
	var args map[string]any
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return name
	}
	target := ""
	for _, key := range []string{"action", "path", "server_id", "id", "job_id", "bot_id", "name"} {
		if s, ok := args[key].(string); ok && strings.TrimSpace(s) != "" {
			target = strings.TrimSpace(s)
			break
		}
	}
	if target == "" {
		return name
	}
	return name + " " + clip(strings.Join(strings.Fields(target), " "), 60)
}

// activitiesFor returns the in-memory trail, rebuilding missing entries from
// the durable journal so a restart (or a fresh runner) still shows history.
func activitiesFor(r *Runner, it task.Task) []ActivityEvent {
	if r == nil {
		return nil
	}
	r.actMu.Lock()
	if r.activities == nil {
		r.activities = newActivityStore()
	}
	store := r.activities
	r.actMu.Unlock()
	live := store.list(it.ID)
	// Determine which journal rows are already covered: journal rows have
	// (turn, call id); the in-memory events do not carry them, so coverage is
	// approximated by (name, label, status) of running/closed pairs. Simplest
	// reliable rule: if the in-memory trail is empty, rebuild the whole thing
	// from the journal; otherwise trust memory (it survives within a process
	// and the journal covers cross-restart history).
	if len(live) == 0 {
		return journalActivities(it)
	}
	// Align future in-memory IDs past the journal's sequence space.
	store.seedSeq(it.ID, len(it.Journal)+len(live))
	return append(journalActivitiesCovering(it, live), live...)
}

// journalActivities converts finished journal rows into safe events.
func journalActivities(it task.Task) []ActivityEvent {
	out := make([]ActivityEvent, 0, len(it.Journal))
	for _, e := range it.Journal {
		if e.State != task.JournalDone && e.State != task.JournalFailed && e.State != task.JournalInterrupted {
			continue
		}
		ev := ActivityEvent{
			ID:        itoaActivity(e.Seq),
			Name:      clip(e.Name, 60),
			Label:     clip(e.Name, 120),
			StartedAt: e.At.UTC().Format(time.RFC3339),
		}
		switch {
		case e.State == task.JournalInterrupted:
			ev.Status = ActivityInterrupted
		case e.Executed && e.Outcome == task.OutcomeDone:
			ev.Status = ActivityCompleted
		case e.Outcome == task.OutcomeFailed:
			ev.Status = ActivityFailed
		case e.Outcome == task.OutcomeDenied || e.Outcome == task.OutcomeSkipped:
			ev.Status = ActivityCompleted
			ev.Label = clip(e.Name+"（未执行）", 120)
		default:
			ev.Status = ActivityCompleted
		}
		out = append(out, ev)
	}
	if len(out) > maxTaskActivities {
		out = out[len(out)-maxTaskActivities:]
	}
	return out
}

// journalActivitiesCovering adds only journal rows whose sequence numbers are
// not plausibly already represented in the live trail (the live trail holds
// at most the newest events; the journal is the durable superset).
func journalActivitiesCovering(it task.Task, live []ActivityEvent) []ActivityEvent {
	if len(live) >= maxTaskActivities {
		return nil
	}
	// The live trail records recent events; the journal rows from before the
	// current process started are the ones missing. Without a stored marker we
	// conservatively expose only rows that finished before the oldest live
	// event started.
	cutoff := timeNowUTC()
	if len(live) > 0 {
		if t, err := time.Parse(time.RFC3339, live[0].StartedAt); err == nil {
			cutoff = t
		}
	}
	out := []ActivityEvent{}
	for _, ev := range journalActivities(it) {
		if t, err := time.Parse(time.RFC3339, ev.StartedAt); err == nil && t.Before(cutoff) {
			out = append(out, ev)
		}
	}
	return out
}

func itoaActivity(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// ActivitiesFor is the API-facing activity trail for one task: the live trail
// plus the durable journal, oldest first, all labels safe.
func ActivitiesFor(r *Runner, it task.Task) []ActivityEvent {
	return activitiesFor(r, it)
}

// TaskModelLabel reports the model routed for the task's bot, read from the
// live settings snapshot. It is a display label only; nothing here widens the
// task's authority.
func TaskModelLabel(r *Runner, it task.Task) string {
	if r == nil || r.Agent == nil || r.Agent.Settings == nil {
		return ""
	}
	// The stored options may pin a per-task model route.
	if it.OptionsJSON != "" {
		var opts struct {
			ModelRoute *struct {
				ProviderID string `json:"provider_id"`
				Model      string `json:"model"`
			} `json:"model_route"`
		}
		if json.Unmarshal([]byte(it.OptionsJSON), &opts) == nil && opts.ModelRoute != nil && opts.ModelRoute.Model != "" {
			return clip(opts.ModelRoute.Model, 120)
		}
	}
	b, err := r.Agent.Store.GetBot(context.Background(), it.BotID)
	if err != nil {
		return ""
	}
	route := r.Agent.Settings.RouteFor(b, it.ParentID != "")
	if route.Model == "" {
		return ""
	}
	return clip(route.Model, 120)
}
