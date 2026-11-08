package task

import (
	"path/filepath"
	"testing"
	"time"
)

func budgetBook(t *testing.T) *Book {
	t.Helper()
	b, err := New(filepath.Join(t.TempDir(), "tasks.enc"), testBox(t))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func createRunning(t *testing.T, b *Book, it Task) Task {
	t.Helper()
	if it.Status == "" {
		it.Status = StatusRunning
	}
	if it.CreatedAt.IsZero() {
		it.CreatedAt = time.Now().UTC()
	}
	if it.UpdatedAt.IsZero() {
		it.UpdatedAt = it.CreatedAt
	}
	got, err := b.Create(it)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func stepN(t *testing.T, b *Book, id string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, exhausted, err := b.ReserveModelStep(id, i); err != nil || exhausted {
			t.Fatalf("reserve %s step %d: exhausted=%v err=%v", id, i, exhausted, err)
		}
	}
}

// Root 100 consumes 1, then delegates a child capped at 99. The child may spend
// all 99; the parent may not spend the reserved 99 until the child finishes.
func TestReserveChildSpendsItsReservation(t *testing.T) {
	b := budgetBook(t)
	createRunning(t, b, Task{ID: "root", OwnerID: "u", BotID: "b", Goal: "g", MaxSteps: 100, StepBudget: 5})
	stepN(t, b, "root", 1)
	createRunning(t, b, Task{ID: "child", OwnerID: "u", BotID: "b", Goal: "c",
		ParentID: "root", ParentCallID: "call", MaxSteps: 99, StepBudget: 5})

	// The parent's own capacity is fully reserved to the child.
	if _, exhausted, err := b.ReserveModelStep("root", 1); err != nil || !exhausted {
		t.Fatalf("parent spent reserved child budget: exhausted=%v err=%v", exhausted, err)
	}
	root, _ := b.Get("root")
	if root.Status != StatusBudgetExhausted {
		t.Fatalf("parent not stopped: %s", root.Status)
	}
	// Restore running to prove the child can still spend its own 99.
	if _, err := b.Update("root", func(t *Task) error {
		t.Status = StatusRunning
		t.LastError = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stepN(t, b, "child", 99)
	child, _ := b.Get("child")
	if child.TotalSteps != 99 {
		t.Fatalf("child spent %d", child.TotalSteps)
	}
	// The child is at its cap now.
	if _, exhausted, err := b.ReserveModelStep("child", 99); err != nil || !exhausted {
		t.Fatalf("child overran its cap: exhausted=%v err=%v", exhausted, err)
	}
}

// After the child finishes having used only 5 of its 99, the parent gets 94
// usable steps back (100 - 1 - 5).
func TestReserveTerminalChildReleasesUnusedBudget(t *testing.T) {
	b := budgetBook(t)
	createRunning(t, b, Task{ID: "root", OwnerID: "u", BotID: "b", Goal: "g", MaxSteps: 100, StepBudget: 5})
	stepN(t, b, "root", 1)
	createRunning(t, b, Task{ID: "child", OwnerID: "u", BotID: "b", Goal: "c",
		ParentID: "root", ParentCallID: "call", MaxSteps: 99, StepBudget: 5})
	stepN(t, b, "child", 5)
	if _, err := b.Update("child", func(t *Task) error {
		t.Status = StatusCompleted
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stepN(t, b, "root", 94) // 1 + 94 + 5 == 100
	if _, exhausted, err := b.ReserveModelStep("root", 94); err != nil || !exhausted {
		t.Fatalf("root overran after child released: exhausted=%v err=%v", exhausted, err)
	}
}

// Two live children cannot together exceed what the parent reserved: each child
// is capped at creation, and the parent charges both caps.
func TestReserveConcurrentChildrenCannotExceedReservation(t *testing.T) {
	b := budgetBook(t)
	createRunning(t, b, Task{ID: "root", OwnerID: "u", BotID: "b", Goal: "g", MaxSteps: 10, StepBudget: 5})
	createRunning(t, b, Task{ID: "c1", OwnerID: "u", BotID: "b", Goal: "a",
		ParentID: "root", ParentCallID: "c1", MaxSteps: 5, StepBudget: 5})
	stepN(t, b, "c1", 3)
	// The second child can only be given the parent's remaining capacity (5).
	createRunning(t, b, Task{ID: "c2", OwnerID: "u", BotID: "b", Goal: "b",
		ParentID: "root", ParentCallID: "c2", MaxSteps: 5, StepBudget: 5})
	stepN(t, b, "c1", 2) // c1 reaches its 5
	stepN(t, b, "c2", 5)
	// Total actual subtree steps are 10, the root cap.
	if _, exhausted, err := b.ReserveModelStep("c2", 5); err != nil || !exhausted {
		t.Fatalf("children overran root cap: exhausted=%v err=%v", exhausted, err)
	}
	root, _ := b.Get("root")
	if root.TotalSteps != 0 {
		t.Fatalf("root spent: %d", root.TotalSteps)
	}
}

func TestReserveRequiresRunningAndRespectsDeadline(t *testing.T) {
	b := budgetBook(t)
	createRunning(t, b, Task{ID: "root", OwnerID: "u", BotID: "b", Goal: "g", MaxSteps: 5, StepBudget: 5})
	if _, err := b.Update("root", func(t *Task) error {
		t.Status = StatusPaused
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.ReserveModelStep("root", 0); err == nil {
		t.Fatal("reserve on a paused task succeeded")
	}
	if _, err := b.Update("root", func(t *Task) error {
		t.Status = StatusRunning
		t.Deadline = time.Now().UTC().Add(-time.Minute)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	it, exhausted, err := b.ReserveModelStep("root", 0)
	if err != nil || !exhausted || it.Status != StatusBudgetExhausted {
		t.Fatalf("deadline not enforced: %+v exhausted=%v err=%v", it, exhausted, err)
	}
}

func TestReserveNoDeadlockWithoutChildren(t *testing.T) {
	b := budgetBook(t)
	createRunning(t, b, Task{ID: "root", OwnerID: "u", BotID: "b", Goal: "g", MaxSteps: 2, StepBudget: 2})
	stepN(t, b, "root", 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.ReserveModelStep("root", 2)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ReserveModelStep deadlocked")
	}
}

func TestReserveIsPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.enc")
	b, err := New(path, testBox(t))
	if err != nil {
		t.Fatal(err)
	}
	createRunning(t, b, Task{ID: "root", OwnerID: "u", BotID: "b", Goal: "g", MaxSteps: 4, StepBudget: 4})
	stepN(t, b, "root", 2)
	reloaded, err := New(path, testBox(t))
	if err != nil {
		t.Fatal(err)
	}
	it, err := reloaded.Get("root")
	if err != nil {
		t.Fatal(err)
	}
	if it.TotalSteps != 2 || it.Turns != 2 {
		t.Fatalf("reservation not durable: %+v", it)
	}
}

func TestCreateRejectsMissingSelfAndCyclicParent(t *testing.T) {
	b := budgetBook(t)
	createRunning(t, b, Task{ID: "root", OwnerID: "u", BotID: "b", Goal: "g", MaxSteps: 10, StepBudget: 5})
	// Missing parent.
	if _, err := b.Create(Task{ID: "orphan", OwnerID: "u", BotID: "b", Goal: "g",
		ParentID: "nope", ParentCallID: "c", MaxSteps: 2, StepBudget: 2}); err == nil {
		t.Fatal("orphan child accepted")
	}
	// Self parent.
	if _, err := b.Create(Task{ID: "self", OwnerID: "u", BotID: "b", Goal: "g",
		ParentID: "self", ParentCallID: "c", MaxSteps: 2, StepBudget: 2}); err == nil {
		t.Fatal("self-parent accepted")
	}
	// A child cannot later be reparented to its own descendant: ParentID is
	// immutable, so this is a registry update guard test.
	createRunning(t, b, Task{ID: "kid", OwnerID: "u", BotID: "b", Goal: "k",
		ParentID: "root", ParentCallID: "c1", MaxSteps: 4, StepBudget: 2})
	createRunning(t, b, Task{ID: "grand", OwnerID: "u", BotID: "b", Goal: "h",
		ParentID: "kid", ParentCallID: "c2", MaxSteps: 2, StepBudget: 2})
	if _, err := b.Update("root", func(t *Task) error {
		t.ParentID = "grand"
		t.ParentCallID = "c3"
		return nil
	}); err == nil {
		t.Fatal("reparenting accepted")
	}
}

// A corrupt/cyclic stored dump must not hang the budget walk.
func TestSubtreeWalkTerminatesOnCycle(t *testing.T) {
	b := budgetBook(t)
	createRunning(t, b, Task{ID: "a", OwnerID: "u", BotID: "b", Goal: "g", MaxSteps: 10, StepBudget: 5})
	createRunning(t, b, Task{ID: "c", OwnerID: "u", BotID: "b", Goal: "g", ParentID: "a", ParentCallID: "c", MaxSteps: 4, StepBudget: 2})
	// Force a corrupt cycle directly in memory (bypassing the guard) to prove
	// the walk itself is safe.
	b.mu.Lock()
	cyclic := b.items["a"]
	cyclic.ParentID = "c"
	cyclic.ParentCallID = "x"
	b.items["a"] = cyclic
	b.mu.Unlock()
	done := make(chan int, 1)
	go func() {
		b.mu.Lock()
		n := b.subtreeStepsLocked(b.items["c"])
		b.mu.Unlock()
		done <- n
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("subtree walk hung on a cycle")
	}
}
