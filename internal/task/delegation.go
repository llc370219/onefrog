package task

// ActivateChild atomically clears a delegated child's acceptance gate and
// queues it, but only while the parent still names this exact child as the
// job it is parked on.
//
// The check and the write happen under the same registry lock, which closes
// the activation race: a Pause/Cancel that lands first makes the parent's
// waiting_job state disappear, so the activation is refused and the child
// stays parked; an activation that lands first makes the child queued before
// the propagation pass can see it, so the pause reaches it normally.
// A stale window re-entering ask_agent can therefore never re-queue a child
// the operator has already stopped behind its back.
//
// Replay safety: a child that is not acceptance-pending is already activated
// (or has progressed further), so a replayed activation is a no-op that
// returns the current state without touching the registry.
func (b *Book) ActivateChild(childID string) (Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	child, ok := b.items[childID]
	if !ok {
		return Task{}, ErrNotFound
	}
	if !child.AcceptancePending {
		return deepCopyTask(child), nil
	}
	// The child must still be in the acceptance-parked state. A child the
	// operator cancelled (or whose state moved on) while the parent's park is
	// still visible must never be resurrected from a stale snapshot: only a
	// paused, acceptance-pending child is queued here.
	if child.Status != StatusPaused {
		return Task{}, ErrInvalid
	}
	// The parent must still be parked on exactly this child: same job kind,
	// same reference, and the same call identity (turn + call id) the child
	// was created for. A paused, cancelled, terminal or re-parked parent
	// refuses the activation.
	parent, ok := b.items[child.ParentID]
	if !ok || parent.Status != StatusWaitingJob || parent.Pending == nil ||
		parent.Pending.Kind != PendingJob || parent.Pending.JobRef != child.ID ||
		parent.Pending.Turn != child.ParentTurn || parent.Pending.CallID != child.ParentCallID {
		return Task{}, ErrInvalid
	}
	next := deepCopyTask(child)
	next.AcceptancePending = false
	next.Status = StatusQueued
	next, err := bound(next)
	if err != nil {
		return Task{}, err
	}
	if err := validate(next); err != nil {
		return Task{}, err
	}
	next.UpdatedAt = b.now()
	before := cloneItems(b.items)
	b.items[childID] = next
	if err := b.saveLocked(); err != nil {
		b.items = before
		return Task{}, err
	}
	return deepCopyTask(next), nil
}
