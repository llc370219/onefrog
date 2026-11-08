package task

import (
	"testing"
)

// makeDelegationPair builds a parent parked on childID and a child that is
// acceptance-pending, linked by turn and call id.
func makeDelegationPair(t *testing.T, b *Book, parentStatus, callID string) (string, string) {
	t.Helper()
	put(t, b, "parent", func(it *Task) {
		it.Status = parentStatus
		it.Pending = &Pending{Kind: PendingJob, JobRef: "child", CallID: callID, Turn: 3, ArgsHash: "h"}
	})
	put(t, b, "child", func(it *Task) {
		it.ParentID = "parent"
		it.ParentTurn = 3
		it.ParentCallID = callID
		it.Status = StatusPaused
		it.AcceptancePending = true
	})
	return "parent", "child"
}

// TestActivateChildSucceedsWhileParentParked proves the valid path: a parent
// still waiting on this exact child lets the pending child queue.
func TestActivateChildSucceedsWhileParentParked(t *testing.T) {
	b, err := New("", nil)
	if err != nil {
		t.Fatal(err)
	}
	makeDelegationPair(t, b, StatusWaitingJob, "c1")

	got, err := b.ActivateChild("child")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusQueued || got.AcceptancePending {
		t.Fatalf("child not queued: %+v", got)
	}
	parent, _ := b.Get("parent")
	if parent.Status != StatusWaitingJob {
		t.Fatalf("parent disturbed: %+v", parent)
	}
}

// TestActivateChildReplayIsNoOp proves idempotence: activating an already
// activated child returns it unchanged and never errors, so a replayed window
// re-entering ask_agent cannot fail or rewrite the row.
func TestActivateChildReplayIsNoOp(t *testing.T) {
	b, err := New("", nil)
	if err != nil {
		t.Fatal(err)
	}
	makeDelegationPair(t, b, StatusWaitingJob, "c1")
	if _, err := b.ActivateChild("child"); err != nil {
		t.Fatal(err)
	}
	before, _ := b.Get("child")
	again, err := b.ActivateChild("child")
	if err != nil {
		t.Fatalf("replay must be a no-op, got %v", err)
	}
	if again.Status != StatusQueued || again.AcceptancePending {
		t.Fatalf("replay changed the child: %+v", again)
	}
	if !again.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("replay rewrote the row")
	}
}

// TestActivateChildRefusedAfterParentPauseOrCancel is the race this method
// closes: once the parent is paused or cancelled, a stale window's activation
// must be refused — the child must NOT be re-queued behind the operator's
// back. Same lock means pause-first always wins.
func TestActivateChildRefusedAfterParentPauseOrCancel(t *testing.T) {
	for _, st := range []string{StatusPaused, StatusCancelled, StatusFailed, StatusQueued} {
		b, err := New("", nil)
		if err != nil {
			t.Fatal(err)
		}
		makeDelegationPair(t, b, st, "c1")
		if _, err := b.ActivateChild("child"); err == nil {
			t.Fatalf("activation must be refused while parent is %s", st)
		}
		child, _ := b.Get("child")
		if child.Status != StatusPaused || !child.AcceptancePending {
			t.Fatalf("child changed while parent is %s: %+v", st, child)
		}
	}
}

// TestActivateChildRefusedOnAnchorMismatch proves the parent must name this
// exact child for this exact call: a wrong job ref, call id or turn refuses.
func TestActivateChildRefusedOnAnchorMismatch(t *testing.T) {
	mk := func(mutate func(*Pending)) *Book {
		b, err := New("", nil)
		if err != nil {
			t.Fatal(err)
		}
		put(t, b, "parent", func(it *Task) {
			it.Status = StatusWaitingJob
			p := &Pending{Kind: PendingJob, JobRef: "child", CallID: "c1", Turn: 3, ArgsHash: "h"}
			if mutate != nil {
				mutate(p)
			}
			it.Pending = p
		})
		put(t, b, "child", func(it *Task) {
			it.ParentID = "parent"
			it.ParentTurn = 3
			it.ParentCallID = "c1"
			it.Status = StatusPaused
			it.AcceptancePending = true
		})
		return b
	}
	cases := []struct {
		name   string
		mutate func(*Pending)
	}{
		{"jobref 指向别的子任务", func(p *Pending) { p.JobRef = "other" }},
		{"call id 不匹配", func(p *Pending) { p.CallID = "c9" }},
		{"turn 不匹配", func(p *Pending) { p.Turn = 9 }},
	}
	for _, tc := range cases {
		b := mk(tc.mutate)
		if _, err := b.ActivateChild("child"); err == nil {
			t.Fatalf("%s: activation must be refused", tc.name)
		}
	}
}

// TestActivateChildRefusedForIndependentlyCancelledChild pins the last race
// window: a child the operator cancelled on its own (still acceptance-pending)
// while the parent is still parked on it must stay cancelled — the stale park
// snapshot must not resurrect it.
func TestActivateChildRefusedForIndependentlyCancelledChild(t *testing.T) {
	b, err := New("", nil)
	if err != nil {
		t.Fatal(err)
	}
	parent, child := makeDelegationPair(t, b, StatusWaitingJob, "c1")
	if _, err := b.Update(child, func(it *Task) error {
		it.Status = StatusCancelled
		it.Pending = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ActivateChild(child); err == nil {
		t.Fatal("activation must refuse a cancelled child")
	}
	got, _ := b.Get(child)
	if got.Status != StatusCancelled || !got.AcceptancePending {
		t.Fatalf("cancelled child was resurrected: %+v", got)
	}
	p, _ := b.Get(parent)
	if p.Status != StatusWaitingJob {
		t.Fatalf("parent disturbed: %+v", p)
	}
}
