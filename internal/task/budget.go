package task

import (
	"time"
)

// ReserveModelStep atomically charges one model step to a running task before
// the model call is made, so a crash, a park or a cancellation still counts the
// step that was consumed. It is the only safe way to advance a task's budget:
// it runs under the registry lock, walks the parent chain in memory, and never
// calls back into the Book (no nested lock, no deadlock).
//
// Budget model. A task's MaxSteps caps its own steps *plus* what it commits to
// its children:
//
//	available(T) = MaxSteps
//	             - TotalSteps
//	             - sum(live child MaxSteps)          // reserved, not yet spent
//	             - sum(terminal child actual subtree) // spent, reservation released
//
// A live child holds its whole MaxSteps against the parent, because it may
// still spend all of it. A finished child only charges what it and its
// descendants actually used, so unused budget returns to the parent. Each
// child's MaxSteps was itself bounded by the parent's available budget at
// creation, so the invariant "subtree actual <= root MaxSteps" holds by
// induction. A child spends from its own reserved share: the parent's
// available is already reduced by that reservation, so charging the child
// never depends on the parent's own remaining budget and cannot deadlock.
//
// The returned Task is the updated row. exhausted is true when the task was
// persisted as budget_exhausted, in which case the caller must stop the window
// and must not make another model call. A nil error with exhausted=false means
// the step is durably reserved and the caller may proceed.
func (b *Book) ReserveModelStep(id string, step int) (Task, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cur, ok := b.items[id]
	if !ok {
		return Task{}, false, ErrNotFound
	}
	if cur.Status != StatusRunning {
		return deepCopyTask(cur), false, ErrInvalid
	}
	now := b.now()
	next := deepCopyTask(cur)
	if !next.Deadline.IsZero() && now.After(next.Deadline) {
		next.Status = StatusBudgetExhausted
		next.LastError = "任务时间预算用完"
		next.Pending = nil
		return b.commitReservation(id, cur, next, now, true)
	}
	// Chain from this task up to the root. A cycle or a missing parent stops
	// the walk; a corrupt link must never hang the registry.
	chain, _ := b.ancestryLocked(cur)
	if reason := b.exhaustedChain(chain); reason != "" {
		next.Status = StatusBudgetExhausted
		next.LastError = reason
		next.Pending = nil
		return b.commitReservation(id, cur, next, now, true)
	}
	next.TotalSteps++
	next.Turns++
	next.Step = step
	return b.commitReservation(id, cur, next, now, false)
}

// commitReservation persists a reservation in one write, restoring the previous
// map on failure so a failed save never leaves a half-charged task in memory.
func (b *Book) commitReservation(id string, cur, next Task, now time.Time, exhausted bool) (Task, bool, error) {
	next, err := bound(next)
	if err != nil {
		return Task{}, false, err
	}
	if err := validate(next); err != nil {
		return Task{}, false, err
	}
	next.UpdatedAt = now
	if next.Terminal() && next.FinishedAt.IsZero() {
		next.FinishedAt = next.UpdatedAt
	}
	before := b.items[id]
	b.items[id] = next
	if err := b.saveLocked(); err != nil {
		b.items[id] = before
		return Task{}, false, err
	}
	return deepCopyTask(next), exhausted, nil
}

// checkParentLocked refuses a parent link that points at a missing task or that
// closes a cycle. Must be called with the registry lock held.
func (b *Book) checkParentLocked(it Task) error {
	if it.ParentID == "" {
		return nil
	}
	if it.ParentID == it.ID {
		return ErrInvalid
	}
	seen := map[string]bool{it.ID: true}
	cur, ok := b.items[it.ParentID]
	if !ok {
		return ErrInvalid
	}
	for {
		if seen[cur.ID] {
			return ErrInvalid
		}
		seen[cur.ID] = true
		if cur.ParentID == "" {
			return nil
		}
		next, ok := b.items[cur.ParentID]
		if !ok {
			// A dangling grandparent is corruption, not a valid chain.
			return ErrInvalid
		}
		cur = next
	}
}

// ancestryLocked returns the parent chain of it (nearest first) plus the root.
// It must be called with the registry lock held.
func (b *Book) ancestryLocked(it Task) ([]Task, Task) {
	chain := []Task{it}
	seen := map[string]bool{it.ID: true}
	root := it
	for root.ParentID != "" {
		if seen[root.ParentID] {
			break
		}
		parent, ok := b.items[root.ParentID]
		if !ok {
			break
		}
		seen[parent.ID] = true
		chain = append(chain, parent)
		root = parent
	}
	return chain, root
}

// exhaustedChain reports the first exhausted budget in the chain, from the task
// itself up to the root. It must be called with the registry lock held and
// never calls List.
//
// The task itself is checked against its available budget, so it cannot spend
// what it already reserved for its children. An ancestor is checked against the
// actual steps spent by its whole subtree: the path child has already reserved
// its cap from that ancestor, so spending *inside* the child's reservation must
// never be rejected by the ancestor (that would deadlock the child for no
// reason). The ancestor only stops the subtree once real consumption reaches
// its cap.
func (b *Book) exhaustedChain(chain []Task) string {
	for i, node := range chain {
		if node.MaxSteps <= 0 {
			continue
		}
		var left int
		if i == 0 {
			left = b.availableLocked(node)
		} else {
			left = node.MaxSteps - b.subtreeStepsLocked(node)
		}
		if left <= 0 {
			if i == 0 {
				return "任务的模型步骤总预算已经用完"
			}
			return "任务的模型步骤总预算已经用完（含派工子任务）"
		}
	}
	return ""
}

// availableLocked is a task's own remaining budget after its spent steps and
// the budget it committed to children. Must be called with the registry lock
// held.
func (b *Book) availableLocked(it Task) int {
	if it.MaxSteps <= 0 {
		return 0
	}
	used := it.TotalSteps
	for _, child := range b.items {
		if child.ParentID != it.ID {
			continue
		}
		if child.Terminal() {
			// A finished child releases its unused reservation and charges
			// only what its own subtree actually spent.
			used += b.subtreeStepsLocked(child)
			continue
		}
		// A live child holds its whole cap against the parent.
		used += child.MaxSteps
	}
	return it.MaxSteps - used
}

// subtreeStepsLocked sums TotalSteps over it and every descendant. It walks the
// in-memory items directly, so it is only valid under the registry lock. A
// parent cycle is visited once per node, so a corrupt link can never make the
// walk loop forever or double count a task.
func (b *Book) subtreeStepsLocked(it Task) int {
	children := make(map[string][]Task)
	for _, row := range b.items {
		if row.ParentID != "" {
			children[row.ParentID] = append(children[row.ParentID], row)
		}
	}
	seen := make(map[string]bool, len(b.items))
	var sum func(Task) int
	sum = func(node Task) int {
		if seen[node.ID] {
			return 0
		}
		seen[node.ID] = true
		total := node.TotalSteps
		for _, child := range children[node.ID] {
			total += sum(child)
		}
		return total
	}
	return sum(it)
}
