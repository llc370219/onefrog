package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"onefrog/internal/secret"
)

var (
	ErrNotFound = errors.New("任务不存在")
	ErrInvalid  = errors.New("任务数据无效")
	// ErrFull keeps the encrypted file small; finished tasks are dropped first.
	ErrFull = errors.New("任务数量已达上限")
)

const fileVersion = 1

type fileFormat struct {
	Version int    `json:"version"`
	Items   []Task `json:"items"`
}

// Book is the durable task registry. An empty path keeps it in memory, which
// is what tests and embedded use want; a real path requires an encryption box.
type Book struct {
	mu    sync.Mutex
	path  string
	box   *secret.Box
	items map[string]Task
	now   func() time.Time
}

// New loads the registry. A task found in "running" state was interrupted by
// a restart; it is parked for reconciliation instead of being replayed.
func New(path string, box *secret.Box) (*Book, error) {
	if path != "" && box == nil {
		return nil, errors.New("任务存储需要加密密钥")
	}
	b := &Book{path: path, box: box, items: map[string]Task{}, now: func() time.Time { return time.Now().UTC() }}
	if path == "" {
		return b, nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxFileBytes {
		return nil, errors.New("任务文件超过大小上限")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	plain, err := box.Decrypt(string(raw))
	if err != nil {
		return nil, errors.New("任务文件无法解密，请检查主密钥")
	}
	var data fileFormat
	dec := json.NewDecoder(strings.NewReader(plain))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&data); err != nil {
		return nil, errors.New("任务文件无效")
	}
	// A second Decode must hit EOF: dec.More only inspects the next token,
	// so trailing garbage such as a stray "]" can slip past it.
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("任务文件末尾有多余内容")
	}
	if data.Version != fileVersion {
		return nil, fmt.Errorf("不支持的任务文件版本 %d", data.Version)
	}
	if len(data.Items) > MaxTasks {
		return nil, errors.New("任务文件包含过多任务")
	}
	now := b.now()
	for _, it := range data.Items {
		if it.ID == "" {
			return nil, errors.New("任务文件包含空 ID 的任务")
		}
		var err error
		it, err = bound(it)
		if err != nil {
			return nil, fmt.Errorf("任务 %s 无效：%w", it.ID, err)
		}
		if err := validate(it); err != nil {
			return nil, fmt.Errorf("任务 %s 无效：%w", it.ID, err)
		}
		it = recoverTask(it, now)
		b.items[it.ID] = it
	}
	if dup := firstDuplicateID(data.Items); dup != "" {
		return nil, fmt.Errorf("任务文件包含重复 ID %s", dup)
	}
	return b, nil
}

// firstDuplicateID reports the first ID that appears more than once in the
// stored list. A map keyed by ID would silently keep only one row.
func firstDuplicateID(items []Task) string {
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if seen[it.ID] {
			return it.ID
		}
		seen[it.ID] = true
	}
	return ""
}

// recoverTask decides what a task left mid-flight by a previous process may
// do. A task with no in-flight tool call has no unknown side effect, so it is
// queued to continue automatically. A started call whose outcome is unknown
// would be dangerous to re-run: the task is parked as paused and the runtime
// must reconcile with read-only checks.
func recoverTask(it Task, now time.Time) Task {
	if it.Status == StatusWaitingUser || it.Status == StatusWaitingApproval || it.Status == StatusWaitingJob {
		if it.Pending != nil && it.Pending.Kind == PendingTool {
			markInterrupted(&it, now)
			it.Status = StatusPaused
			it.UpdatedAt = now
		}
		return it
	}
	if it.Status == StatusRunning || it.Status == StatusQueued {
		if hasStartedCall(it) || (it.Pending != nil && it.Pending.Kind == PendingTool) {
			markInterrupted(&it, now)
			it.Status = StatusPaused
			it.UpdatedAt = now
			return it
		}
		it.Status = StatusQueued
		it.UpdatedAt = now
		return it
	}
	if it.Pending != nil && it.Pending.Kind == PendingTool {
		// A terminal task cannot have an unfinished call; drop it rather than
		// resume anything.
		it.Pending = nil
		return it
	}
	if it.Status == StatusPaused && hasStartedCall(it) {
		// A paused task left with a started call has an unknown outcome; mark
		// it so a resume reconciles instead of replaying.
		markInterrupted(&it, now)
		it.UpdatedAt = now
	}
	return it
}

func hasStartedCall(it Task) bool {
	for _, e := range it.Journal {
		if e.State == JournalStarted {
			return true
		}
	}
	return false
}

func markInterrupted(it *Task, now time.Time) {
	found := false
	for i := range it.Journal {
		if it.Journal[i].State == JournalStarted {
			it.Journal[i].State = JournalInterrupted
			it.Journal[i].At = now
			found = true
		}
	}
	if it.Pending != nil && it.Pending.Kind == PendingTool {
		it.Pending = nil
		found = true
	}
	if found {
		it.LastError = "服务重启时工具调用正在执行，结果未知，需要先只读核验现状，不要重复执行"
	}
}

// Create stores a new task after validating and bounding it. The caller keeps
// no reference to the stored value: everything returned and kept is a deep
// copy.
func (b *Book) Create(it Task) (Task, error) {
	now := b.now()
	in := deepCopyTask(it)
	in.ID = strings.TrimSpace(in.ID)
	if in.ID == "" {
		return Task{}, ErrInvalid
	}
	if strings.TrimSpace(in.Goal) == "" || strings.TrimSpace(in.OwnerID) == "" || strings.TrimSpace(in.BotID) == "" {
		return Task{}, ErrInvalid
	}
	if in.Status == "" {
		in.Status = StatusQueued
	}
	if in.StepBudget <= 0 {
		in.StepBudget = 8
	}
	if in.MaxSteps <= 0 {
		in.MaxSteps = in.StepBudget * 4
	}
	if in.MaxAttempts <= 0 {
		in.MaxAttempts = 3
	}
	in.CreatedAt, in.UpdatedAt = now, now
	var err error
	in, err = bound(in)
	if err != nil {
		return Task{}, err
	}
	if err := validate(in); err != nil {
		return Task{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.items[in.ID]; ok {
		return Task{}, ErrInvalid
	}
	// The parent link is a DAG edge. It must name a task that already exists
	// (the child cannot be orphaned) and it must not close a cycle, or a later
	// subtree budget walk would never terminate. Checked under the lock so a
	// concurrent delete of the parent cannot race the create.
	if err := b.checkParentLocked(in); err != nil {
		return Task{}, err
	}
	before := cloneItems(b.items)
	b.items[in.ID] = in
	if err := b.enforceRetentionLocked(); err != nil {
		b.items = before
		return Task{}, err
	}
	if err := b.saveLocked(); err != nil {
		b.items = before
		return Task{}, err
	}
	return deepCopyTask(in), nil
}

// Get returns a deep copy of one task by id.
func (b *Book) Get(id string) (Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	it, ok := b.items[id]
	if !ok {
		return Task{}, ErrNotFound
	}
	return deepCopyTask(it), nil
}

// List returns deep copies of tasks, newest update first. ownerID empty
// returns everything (background runtime); API callers pass their own id.
func (b *Book) List(ownerID string) []Task {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Task, 0, len(b.items))
	for _, it := range b.items {
		if ownerID != "" && it.OwnerID != ownerID {
			continue
		}
		out = append(out, deepCopyTask(it))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// Update applies fn to a deep copy and persists it. fn never sees the stored
// value, and any validation, bounding or write error restores the registry
// unchanged, so a failed save never leaves a half-written task in memory.
func (b *Book) Update(id string, fn func(*Task) error) (Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cur, ok := b.items[id]
	if !ok {
		return Task{}, ErrNotFound
	}
	next := deepCopyTask(cur)
	if err := fn(&next); err != nil {
		return Task{}, err
	}
	// Identity, scope and the approved authorization are fixed at creation.
	// A closure may change progress, status or the journal, but a change to
	// any of these is a bug or an attempt to re-scope the task, so refuse it
	// instead of silently discarding the write.
	if next.ID != cur.ID || next.OwnerID != cur.OwnerID || next.CreatedAt != cur.CreatedAt ||
		next.BotID != cur.BotID || next.SessionID != cur.SessionID ||
		next.ProjectID != cur.ProjectID || next.ServerID != cur.ServerID ||
		next.Goal != cur.Goal || next.OptionsJSON != cur.OptionsJSON ||
		next.ParentID != cur.ParentID || next.ParentTurn != cur.ParentTurn ||
		next.ParentCallID != cur.ParentCallID || next.ParentGoal != cur.ParentGoal ||
		next.Purpose != cur.Purpose || next.Hop != cur.Hop ||
		next.Untrusted != cur.Untrusted {
		return Task{}, ErrInvalid
	}
	next, err := bound(next)
	if err != nil {
		return Task{}, err
	}
	if err := validate(next); err != nil {
		return Task{}, err
	}
	next.UpdatedAt = b.now()
	if next.Terminal() && next.FinishedAt.IsZero() {
		next.FinishedAt = next.UpdatedAt
	}
	before := cloneItems(b.items)
	b.items[id] = next
	if err := b.saveLocked(); err != nil {
		b.items = before
		return Task{}, err
	}
	return deepCopyTask(next), nil
}

// Delete removes a finished task. Active tasks are refused.
func (b *Book) Delete(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	it, ok := b.items[id]
	if !ok {
		return ErrNotFound
	}
	if it.Active() {
		return ErrInvalid
	}
	before := cloneItems(b.items)
	delete(b.items, id)
	if err := b.saveLocked(); err != nil {
		b.items = before
		return err
	}
	return nil
}

// ResumeInterrupted parks tasks that were mid-flight (for example after a
// crash) for reconciliation instead of replaying them. It returns how many
// changed, and rolls the registry back if the write fails.
func (b *Book) ResumeInterrupted(now time.Time) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	before := cloneItems(b.items)
	changed := 0
	for id, it := range b.items {
		inflight := it.Status == StatusRunning ||
			(it.Pending != nil && it.Pending.Kind == PendingTool) ||
			(it.Status == StatusPaused && hasStartedCall(it))
		if !inflight {
			continue
		}
		next := deepCopyTask(it)
		if next.Status == StatusRunning {
			next.Status = StatusPaused
		}
		markInterrupted(&next, now)
		next.UpdatedAt = now
		b.items[id] = next
		changed++
	}
	if changed == 0 {
		return 0, nil
	}
	if err := b.saveLocked(); err != nil {
		b.items = before
		return 0, err
	}
	return changed, nil
}

func (b *Book) enforceRetentionLocked() error {
	if len(b.items) <= MaxTasks {
		return nil
	}
	finished := make([]Task, 0, len(b.items))
	for _, it := range b.items {
		if it.Terminal() {
			finished = append(finished, it)
		}
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].UpdatedAt.Before(finished[j].UpdatedAt) })
	remove := len(b.items) - MaxTasks
	for i := 0; i < remove && i < len(finished); i++ {
		delete(b.items, finished[i].ID)
	}
	if len(b.items) > MaxTasks {
		return ErrFull
	}
	return nil
}

// bound enforces every size limit before a task is stored or compared. It
// never cuts the goal or the authorization options (that would change what
// the user approved), and never cuts the checkpoint in the middle of a model
// turn. Oversized values are refused so the caller must decide explicitly.
func bound(it Task) (Task, error) {
	if runeLen(it.Goal) > MaxGoal || runeLen(it.OptionsJSON) > MaxOptionsJSON {
		return Task{}, ErrInvalid
	}
	if it.OptionsJSON != "" && !json.Valid([]byte(it.OptionsJSON)) {
		return Task{}, ErrInvalid
	}
	if runeLen(it.Result) > MaxResult {
		it.Result = clipRunes(it.Result, MaxResult)
	}
	cp, err := compress(it.Checkpoint)
	if err != nil {
		return Task{}, err
	}
	it.Checkpoint = cp
	it.Next = clipRunes(it.Next, MaxLine)
	it.LastError = clipRunes(it.LastError, MaxLine)
	if len(it.Journal) > MaxJournal {
		it.Journal = it.Journal[len(it.Journal)-MaxJournal:]
	}
	if len(it.Guidance) > MaxGuidance {
		it.Guidance = it.Guidance[len(it.Guidance)-MaxGuidance:]
	}
	if len(it.Approvals) > MaxApprovals {
		it.Approvals = it.Approvals[len(it.Approvals)-MaxApprovals:]
	}
	if len(it.Plan) > MaxPlanSteps || len(it.Progress) > MaxProgress || len(it.Verified) > MaxProgress {
		return Task{}, ErrInvalid
	}
	for i := range it.Plan {
		it.Plan[i] = clipRunes(it.Plan[i], MaxLine)
	}
	for i := range it.Progress {
		it.Progress[i] = clipRunes(it.Progress[i], MaxLine)
	}
	for i := range it.Verified {
		it.Verified[i] = clipRunes(it.Verified[i], MaxLine)
	}
	for i := range it.Guidance {
		it.Guidance[i] = clipRunes(it.Guidance[i], MaxLine)
	}
	for i := range it.Journal {
		it.Journal[i].Result = clipRunes(it.Journal[i].Result, MaxResult)
		it.Journal[i].CallID = clipRunes(it.Journal[i].CallID, 200)
		it.Journal[i].ArgsHash = clipRunes(it.Journal[i].ArgsHash, 128)
	}
	for i := range it.Approvals {
		it.Approvals[i].Command = clipRunes(it.Approvals[i].Command, MaxLine)
		if len(it.Approvals[i].Reasons) > 8 {
			it.Approvals[i].Reasons = it.Approvals[i].Reasons[:8]
		}
	}
	if it.Pending != nil {
		it.Pending.JobRef = clipRunes(it.Pending.JobRef, MaxLine)
		it.Pending.ArgsHash = clipRunes(it.Pending.ArgsHash, 128)
	}
	for i := range it.Checkpoint.Facts {
		it.Checkpoint.Facts[i] = clipRunes(it.Checkpoint.Facts[i], MaxLine)
	}
	return it, nil
}

func runeLen(s string) int { return len([]rune(s)) }

// checkpointFits reports whether the window is within both the message count
// and byte limits.
func checkpointFits(msgs []map[string]any) (bool, error) {
	if len(msgs) > MaxCheckpointMessages {
		return false, nil
	}
	raw, err := json.Marshal(msgs)
	if err != nil {
		return false, ErrInvalid
	}
	return len(raw) <= MaxCheckpointBytes, nil
}

// compress keeps whole model turns: it drops the oldest assistant+tool groups
// together, never a lone tool result or a dangling assistant call, and always
// keeps the leading system message, the original user goal and the final
// unfinished assistant batch. If even that protected prefix plus the pending
// batch exceed the limits, it refuses instead of silently dropping the call
// the task is suspended on.
func compress(cp Checkpoint) (Checkpoint, error) {
	if cp.Messages == nil {
		return cp, nil
	}
	if ok, err := checkpointFits(cp.Messages); err != nil {
		return cp, err
	} else if ok {
		return cp, nil
	}
	head, rest := splitHead(cp.Messages)
	tail, droppable := splitPendingBatch(rest)
	for {
		kept := make([]map[string]any, 0, len(head)+len(droppable)+len(tail))
		kept = append(kept, head...)
		kept = append(kept, droppable...)
		kept = append(kept, tail...)
		ok, err := checkpointFits(kept)
		if err != nil {
			return cp, err
		}
		if ok {
			cp.Messages = kept
			cp.Compressions++
			return cp, nil
		}
		if len(droppable) == 0 {
			// Nothing left to drop without cutting the goal or the pending
			// batch: refuse rather than corrupt the window.
			return cp, ErrInvalid
		}
		droppable = dropOldestTurn(droppable)
	}
}

// splitPendingBatch separates the final, possibly unfinished, assistant batch
// (an assistant message with tool calls whose results are still arriving) from
// the history that compression may drop. A completed final batch is not
// protected.
func splitPendingBatch(rest []map[string]any) (tail, droppable []map[string]any) {
	for i := len(rest) - 1; i >= 0; i-- {
		role, _ := rest[i]["role"].(string)
		if role != "assistant" {
			continue
		}
		ids := toolCallIDs(rest[i])
		if len(ids) == 0 {
			continue
		}
		answered := map[string]bool{}
		for _, msg := range rest[i+1:] {
			if r, _ := msg["role"].(string); r != "tool" {
				continue
			}
			if id, _ := msg["tool_call_id"].(string); id != "" {
				answered[id] = true
			}
		}
		for _, id := range ids {
			if !answered[id] {
				return rest[i:], rest[:i]
			}
		}
		return nil, rest
	}
	return nil, rest
}

// toolCallIDs lists the tool call ids an assistant message opened, whatever
// typed struct or decoded map the caller stored them as.
func toolCallIDs(msg map[string]any) []string {
	raw, err := json.Marshal(msg["tool_calls"])
	if err != nil {
		return nil
	}
	var calls []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &calls) != nil {
		return nil
	}
	ids := make([]string, 0, len(calls))
	for _, c := range calls {
		if c.ID != "" {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// splitHead returns the protected prefix: system messages and the first user
// message (the original goal).
func splitHead(msgs []map[string]any) ([]map[string]any, []map[string]any) {
	cut := 0
	for cut < len(msgs) {
		role, _ := msgs[cut]["role"].(string)
		cut++
		if role == "user" {
			break
		}
	}
	return append([]map[string]any{}, msgs[:cut]...), append([]map[string]any{}, msgs[cut:]...)
}

// dropOldestTurn removes one complete group: if it starts with an assistant
// message that has tool calls, the following tool results go with it.
func dropOldestTurn(rest []map[string]any) []map[string]any {
	if len(rest) == 0 {
		return rest
	}
	i := 1
	role, _ := rest[0]["role"].(string)
	if role == "assistant" {
		if _, ok := rest[0]["tool_calls"]; ok {
			for i < len(rest) {
				r, _ := rest[i]["role"].(string)
				if r != "tool" {
					break
				}
				i++
			}
		}
	}
	return rest[i:]
}

func validate(it Task) error {
	if it.ID == "" || it.OwnerID == "" || it.BotID == "" || strings.TrimSpace(it.Goal) == "" {
		return ErrInvalid
	}
	// A plaintext secret answer must never reach the task file. Check the
	// JSON structure, not a substring: a normal goal mentioning the words
	// "secret_answer" must not fail, while a renamed key must still be
	// caught.
	if err := rejectSecretAnswer(it.OptionsJSON); err != nil {
		return err
	}
	switch it.Status {
	case StatusQueued, StatusRunning, StatusWaitingUser, StatusWaitingApproval, StatusWaitingJob, StatusPaused,
		StatusCompleted, StatusFailed, StatusCancelled, StatusBudgetExhausted:
	default:
		return ErrInvalid
	}
	if it.Pending != nil {
		switch it.Pending.Kind {
		case PendingTool, PendingApproval, PendingUser, PendingJob:
		default:
			return ErrInvalid
		}
		if it.Status == StatusWaitingJob && it.Pending.Kind != PendingJob {
			return ErrInvalid
		}
	}
	if it.Status == StatusWaitingJob && it.Pending == nil {
		return ErrInvalid
	}
	// A child link is all-or-nothing: a parent turn and call id without the
	// parent task would be an orphan that could never be collected.
	if (it.ParentTurn != 0 || it.ParentCallID != "") && it.ParentID == "" {
		return ErrInvalid
	}
	for _, entry := range it.Journal {
		switch entry.State {
		case JournalStarted, JournalDone, JournalFailed, JournalInterrupted, JournalWaiting:
		default:
			return ErrInvalid
		}
	}
	if err := checkTurns(it.Checkpoint.Messages); err != nil {
		return err
	}
	return nil
}

// rejectSecretAnswer walks the parsed JSON looking for a field literally
// named "secret_answer" (in objects or arrays), the key the chat Options
// struct uses to carry a plaintext password. Prose that merely contains the
// word is allowed.
func rejectSecretAnswer(optionsJSON string) error {
	if strings.TrimSpace(optionsJSON) == "" {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(optionsJSON))
	dec.UseNumber()
	var walk func(v any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if k == "secret_answer" {
					return true
				}
				if walk(val) {
					return true
				}
			}
		case []any:
			for _, val := range x {
				if walk(val) {
					return true
				}
			}
		}
		return false
	}
	var root any
	if err := dec.Decode(&root); err != nil {
		return ErrInvalid
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	if walk(root) {
		return ErrInvalid
	}
	return nil
}

// checkTurns rejects a checkpoint whose tool messages are not paired with an
// assistant call earlier in the window. An assistant message that carries
// tool calls opens its own call IDs; it does not close the previous batch,
// because a partially answered batch is a legal window (the model saw some
// results already recorded in the journal). Tool results must reference an
// ID opened earlier and never repeat.
func checkTurns(msgs []map[string]any) error {
	open := map[string]bool{}
	used := map[string]bool{}
	for _, msg := range msgs {
		raw, err := json.Marshal(msg)
		if err != nil {
			return errors.New("任务检查点包含无法解析的消息")
		}
		var generic struct {
			Role      string           `json:"role"`
			ToolCalls []map[string]any `json:"tool_calls"`
			ToolID    string           `json:"tool_call_id"`
		}
		if err := json.Unmarshal(raw, &generic); err != nil {
			return errors.New("任务检查点包含无法解析的消息")
		}
		switch generic.Role {
		case "assistant":
			// Only the final batch may be waiting for results. If a new
			// assistant turn starts while earlier calls are unanswered, the
			// window is mismatched in the middle and must be refused.
			if len(open) > 0 {
				return errors.New("任务检查点中上一批工具调用还没有结果")
			}
			for _, c := range generic.ToolCalls {
				if id, ok := c["id"].(string); ok {
					if id == "" || used[id] {
						return errors.New("任务检查点包含重复的工具调用 ID")
					}
					used[id] = true
					open[id] = true
				}
			}
		case "tool":
			if generic.ToolID == "" || !open[generic.ToolID] {
				return errors.New("任务检查点包含未配对的工具结果")
			}
			delete(open, generic.ToolID)
		}
	}
	// Every remaining open ID belongs to the final assistant batch whose
	// results may still be in flight; that is allowed.
	return nil
}

func deepCopyTask(in Task) Task {
	out := in
	out.Plan = append([]string(nil), in.Plan...)
	out.Progress = append([]string(nil), in.Progress...)
	out.Verified = append([]string(nil), in.Verified...)
	out.Guidance = append([]string(nil), in.Guidance...)
	out.Journal = append([]JournalEntry(nil), in.Journal...)
	out.Approvals = make([]Approval, len(in.Approvals))
	for i, a := range in.Approvals {
		out.Approvals[i] = a
		out.Approvals[i].Reasons = append([]string(nil), a.Reasons...)
	}
	out.Checkpoint = Checkpoint{
		Messages:     copyMessages(in.Checkpoint.Messages),
		Next:         in.Checkpoint.Next,
		Facts:        append([]string(nil), in.Checkpoint.Facts...),
		Compressions: in.Checkpoint.Compressions,
	}
	if in.Pending != nil {
		p := *in.Pending
		out.Pending = &p
	}
	return out
}

func cloneItems(in map[string]Task) map[string]Task {
	out := make(map[string]Task, len(in))
	for k, v := range in {
		out[k] = deepCopyTask(v)
	}
	return out
}

func copyMessages(in []map[string]any) []map[string]any {
	if in == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(in))
	for _, msg := range in {
		out = append(out, copyValue(msg).(map[string]any))
	}
	return out
}

func copyValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = copyValue(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = copyValue(val)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(x))
		for i, val := range x {
			out[i] = copyValue(val).(map[string]any)
		}
		return out
	case []string:
		return append([]string(nil), x...)
	case nil, string, bool, json.Number,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return v
	default:
		// Checkpoint messages may hold typed structs (for example a
		// []toolCall slice) rather than decoded maps. Copy them so a caller
		// cannot mutate the stored value through its own slice.
		rv := reflect.ValueOf(v)
		if !rv.IsValid() || !copyableReflect(rv) {
			return v
		}
		return deepCopyReflect(rv).Interface()
	}
}

// copyableReflect reports whether deepCopyReflect can rebuild the value
// without hitting unexported fields it cannot set.
func copyableReflect(rv reflect.Value) bool {
	switch rv.Kind() {
	case reflect.Ptr:
		return rv.IsNil() || copyableReflect(rv.Elem())
	case reflect.Map, reflect.Slice:
		return true
	case reflect.Array:
		return rv.Len() == 0 || copyableReflect(rv.Index(0))
	case reflect.Struct:
		for i := 0; i < rv.NumField(); i++ {
			if !rv.Field(i).CanInterface() {
				return false
			}
		}
		return true
	default:
		return true
	}
}

func deepCopyReflect(rv reflect.Value) reflect.Value {
	switch rv.Kind() {
	case reflect.Ptr:
		if rv.IsNil() {
			return rv
		}
		out := reflect.New(rv.Elem().Type())
		out.Elem().Set(deepCopyReflect(rv.Elem()))
		return out
	case reflect.Map:
		if rv.IsNil() {
			return rv
		}
		out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), deepCopyReflect(iter.Value()))
		}
		return out
	case reflect.Slice:
		if rv.IsNil() {
			return rv
		}
		out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out.Index(i).Set(deepCopyReflect(rv.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(rv.Type()).Elem()
		for i := 0; i < rv.Len(); i++ {
			out.Index(i).Set(deepCopyReflect(rv.Index(i)))
		}
		return out
	case reflect.Struct:
		out := reflect.New(rv.Type()).Elem()
		for i := 0; i < rv.NumField(); i++ {
			out.Field(i).Set(deepCopyReflect(rv.Field(i)))
		}
		return out
	default:
		return rv
	}
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// DueJobTasks returns waiting_job tasks whose next poll time has arrived.
func (b *Book) DueJobTasks(now time.Time) []Task {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Task, 0)
	for _, it := range b.items {
		if it.Due(now) {
			out = append(out, deepCopyTask(it))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pending.WakeAt.Before(out[j].Pending.WakeAt) })
	return out
}

// NextJobWake returns the earliest pending wake time, or zero when none.
func (b *Book) NextJobWake() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	var earliest time.Time
	for _, it := range b.items {
		if it.Status != StatusWaitingJob || it.Pending == nil || it.Pending.WakeAt.IsZero() {
			continue
		}
		if earliest.IsZero() || it.Pending.WakeAt.Before(earliest) {
			earliest = it.Pending.WakeAt
		}
	}
	return earliest
}

func (b *Book) saveLocked() error {
	if b.path == "" || b.box == nil {
		return nil
	}
	data := fileFormat{Version: fileVersion, Items: make([]Task, 0, len(b.items))}
	for _, it := range b.items {
		data.Items = append(data.Items, it)
	}
	sort.Slice(data.Items, func(i, j int) bool { return data.Items[i].ID < data.Items[j].ID })
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(data); err != nil {
		return err
	}
	if buf.Len() > MaxFileBytes {
		return errors.New("任务文件超过大小上限")
	}
	cipher, err := b.box.Encrypt(buf.String())
	if err != nil {
		return err
	}
	dir := filepath.Dir(b.path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tasks-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.WriteString(cipher); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, b.path)
}
