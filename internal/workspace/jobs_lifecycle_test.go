package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func lifecycleEngine(t *testing.T, root string) *LocalEngine {
	t.Helper()
	allowed, err := NormalizeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	return NewLocalEngine(Roots{Enabled: true, CommandExecution: true, Dirs: []string{allowed}}, nil)
}

// waitJobTerminal polls until the job reaches a terminal state, then checks
// the exact status (and exit code, when one is expected).
func waitJobTerminal(t *testing.T, table *JobTable, id, owner, task, want string, wantCode *int) Job {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		j, err := table.Status(id, owner, task)
		if err != nil {
			t.Fatal(err)
		}
		if j.Finished() {
			if j.Status != want {
				t.Fatalf("job %s status %s, want %s (job %+v)", id, j.Status, want, j)
			}
			if wantCode != nil {
				if j.ExitCode == nil || *j.ExitCode != *wantCode {
					t.Fatalf("job %s exit code %v, want %d", id, j.ExitCode, *wantCode)
				}
			}
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never finished", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitJobRunning polls until the job reports running (i.e. it registered).
func waitJobRunning(t *testing.T, table *JobTable, id, owner, task string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		j, err := table.Status(id, owner, task)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status == JobRunning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never reported running", id)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startTracked wraps Start and registers cleanup for the job immediately
// after a successful start, so a Fatal mid-test cannot leak sleep processes.
func startTracked(t *testing.T, table *JobTable, ctx context.Context, e *LocalEngine, root, command, owner, task string) Job {
	t.Helper()
	job, err := table.Start(ctx, e, root, command, owner, task)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		table.mu.Lock()
		alive := table.jobs[job.ID] != nil && table.jobs[job.ID].job.Status == JobRunning
		table.mu.Unlock()
		if !alive {
			return
		}
		table.mu.Lock()
		if rj := table.jobs[job.ID]; rj != nil {
			signalJob(rj)
		}
		table.mu.Unlock()
		// Best-effort bounded wait; never fatal in cleanup.
		if rj := table.jobs[job.ID]; rj != nil {
			select {
			case <-rj.done:
			case <-time.After(2 * time.Second):
			}
		}
	})
	return job
}

// TestCancelTaskKillsOwnedJobTree verifies the scoped cancellation contract:
// CancelTask(owner, taskID) kills the owned job's whole process tree, the
// job resolves to cancelled, and — critically — jobs belonging to OTHER
// tasks and OTHER owners in the same table keep running untouched.
func TestCancelTaskKillsOwnedJobTree(t *testing.T) {
	if !canInspectProcesses {
		t.Skip("process inspection unavailable on this platform")
	}
	root := t.TempDir()
	e := lifecycleEngine(t, root)
	table := NewJobTable()

	// The owned job spawns a grandchild that writes its pid; if the tree
	// kill misses it, the grandchild survives past cancel.
	owned := startTracked(t, table, context.Background(), e, root,
		"sleep 30 & echo $! > owned_pid.txt; sleep 60", "bot-a", "task-owned")
	otherTask := startTracked(t, table, context.Background(), e, root, "sleep 30", "bot-a", "task-other")
	otherOwner := startTracked(t, table, context.Background(), e, root, "sleep 30", "bot-b", "task-owned")
	foreign := startTracked(t, table, context.Background(), e, root, "sleep 30", "bot-c", "task-foreign")
	waitJobRunning(t, table, owned.ID, "bot-a", "task-owned")

	// Wait for the grandchild pid to land on disk before cancelling.
	var pidBytes []byte
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for {
		pidBytes, err = os.ReadFile(filepath.Join(root, "owned_pid.txt"))
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild pid never written: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := table.CancelTask("bot-a", "task-owned"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	// The owned job must be cancelled. Cleanup handles the probes, so no
	// sleeper outlives the test even on a Fatal regression.
	waitJobTerminal(t, table, owned.ID, "bot-a", "task-owned", JobCancelled, nil)

	// The grandchild must actually be dead, not orphaned.
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("bad pid file: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		if !pidAlive(pid) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived CancelTask", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Every other task/owner's job must still be running.
	probes := []struct{ id, owner, task string }{
		{otherTask.ID, "bot-a", "task-other"},
		{otherOwner.ID, "bot-b", "task-owned"},
		{foreign.ID, "bot-c", "task-foreign"},
	}
	for _, p := range probes {
		j, err := table.Status(p.id, p.owner, p.task)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status != JobRunning {
			t.Fatalf("job %s (owner %s task %s) was %s, must still be running", p.id, p.owner, p.task, j.Status)
		}
	}
}

// TestCancelTaskIsExactOwnerTaskScope verifies CancelTask matches on BOTH
// owner and taskID: a job sharing only the owner or only the task survives.
func TestCancelTaskIsExactOwnerTaskScope(t *testing.T) {
	root := t.TempDir()
	e := lifecycleEngine(t, root)
	table := NewJobTable()
	sameOwnerDiffTask := startTracked(t, table, context.Background(), e, root, "sleep 30", "bot-a", "task-x")
	diffOwnerSameTask := startTracked(t, table, context.Background(), e, root, "sleep 30", "bot-b", "task-y")
	if err := table.CancelTask("bot-a", "task-y"); err != nil {
		t.Fatal(err)
	}
	probes := []struct{ id, owner, task string }{
		{sameOwnerDiffTask.ID, "bot-a", "task-x"},
		{diffOwnerSameTask.ID, "bot-b", "task-y"},
	}
	for _, p := range probes {
		j, err := table.Status(p.id, p.owner, p.task)
		running := err == nil && j.Status == JobRunning
		if !running {
			t.Fatalf("job %s (owner %s task %s) no longer running after out-of-scope CancelTask: %v %+v", p.id, p.owner, p.task, err, j)
		}
	}
}

// TestCancelTaskHasNoTombstone verifies no permanent tombstone is left: a
// cancelled job resolves through the normal retention/cap path, and a
// cancelled task can start a new job immediately.
func TestCancelTaskHasNoTombstone(t *testing.T) {
	root := t.TempDir()
	e := lifecycleEngine(t, root)
	table := NewJobTable()
	job := startTracked(t, table, context.Background(), e, root, "sleep 30", "bot-a", "task-a")
	if err := table.CancelTask("bot-a", "task-a"); err != nil {
		t.Fatal(err)
	}
	waitJobTerminal(t, table, job.ID, "bot-a", "task-a", JobCancelled, nil)
	// A cancelled task can start a fresh job right away: no tombstone.
	again := startTracked(t, table, context.Background(), e, root, "true", "bot-a", "task-a")
	waitJobTerminal(t, table, again.ID, "bot-a", "task-a", JobDone, func() *int { z := 0; return &z }())
}

// TestStartRejectsCanceledContext verifies Start refuses to spawn a job when
// the caller's context is already canceled, and that the rejection surfaces
// context.Canceled.
func TestStartRejectsCanceledContext(t *testing.T) {
	root := t.TempDir()
	e := lifecycleEngine(t, root)
	table := NewJobTable()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	j, err := table.Start(ctx, e, root, "true", "bot-a", "task-a")
	if err == nil {
		_ = table.CancelTask("bot-a", "task-a")
		t.Fatalf("Start accepted a canceled ctx: %+v", j)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v, want context.Canceled", err)
	}
}

// TestStartCanceledContextRaceRegistration is the race-semantics regression:
// a Start with an ALREADY-canceled ctx must never win the registration race
// against CancelTask. Either it rejects, or — in the interleaving where the
// ctx is canceled after the lock check but before the snapshot — the job
// gets registered and is then guaranteed to be killed by CancelTask (the
// proc handle is stored under the same table lock, so no half-registered
// job can be missed). No job may survive as running for this task.
func TestStartCanceledContextRaceRegistration(t *testing.T) {
	if !canInspectProcesses {
		t.Skip("process inspection unavailable on this platform")
	}
	root := t.TempDir()
	e := lifecycleEngine(t, root)
	table := NewJobTable()
	table.retention = time.Millisecond
	var wg sync.WaitGroup
	// Hammer the table with starts whose ctx is already canceled and with
	// CancelTask for the same owner/task, from opposite goroutines.
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			job, err := table.Start(ctx, e, root, "sleep 30", "bot-a", "task-r")
			if err == nil {
				t.Errorf("Start succeeded with an already-canceled ctx: %+v", job)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			_ = table.CancelTask("bot-a", "task-r")
			time.Sleep(2 * time.Millisecond)
		}
	}()
	wg.Wait()
	// Whatever the interleaving, no job may survive as running for this
	// task: any registered job was either cancelled or never registered.
	table.mu.Lock()
	defer table.mu.Unlock()
	for id, rj := range table.jobs {
		if rj.job.Owner == "bot-a" && rj.job.TaskID == "task-r" && rj.job.Status == JobRunning {
			t.Errorf("job %s leaked as running after canceled-Start race", id)
		}
	}
}

// gateCtx wraps a cancelable context and intercepts Start's two ctx.Err()
// checks: the first (lock-free) call passes through untouched, the second —
// the under-registration-lock gate — blocks until the test releases it, so
// the test can cancel the underlying ctx while Start is parked between the
// two gates.
type gateCtx struct {
	context.Context
	entered chan struct{} // closed when the second Err call begins
	release chan struct{} // closed to let the second Err return
	once    sync.Once
	calls   atomic.Int32
}

func (c *gateCtx) Err() error {
	if c.calls.Add(1) >= 2 {
		c.once.Do(func() { close(c.entered) })
		<-c.release
	}
	return c.Context.Err()
}

// TestStartUnderLockGateRejectsCanceledCtx uses the gateCtx barrier to park
// Start deterministically between its two ctx checks, then cancels the
// underlying ctx: the under-lock gate must reject, so no job spawns even
// though the first (lock-free) check saw a live context.
func TestStartUnderLockGateRejectsCanceledCtx(t *testing.T) {
	root := t.TempDir()
	e := lifecycleEngine(t, root)
	table := NewJobTable()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := &gateCtx{Context: ctx, entered: make(chan struct{}), release: make(chan struct{})}
	type result struct {
		job Job
		err error
	}
	done := make(chan result, 1)
	go func() {
		job, err := table.Start(g, e, root, "sleep 30", "bot-a", "task-gate")
		done <- result{job, err}
	}()

	// Wait until Start is parked at the second gate (holding t.mu).
	select {
	case <-g.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("Start never reached the under-lock ctx gate")
	}
	// Cancel while Start sits between the gates, then let the gate return.
	cancel()
	close(g.release)

	select {
	case r := <-done:
		if r.err == nil {
			t.Cleanup(func() { _ = table.CancelTask("bot-a", "task-gate") })
			t.Fatalf("Start with ctx canceled under the lock succeeded: %+v", r.job)
		}
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("error %v, want context.Canceled", r.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Start never returned")
	}
}

// TestCancelTaskTrueRaceLiveCtx runs independent (Start, cancel, CancelTask)
// pairs, each with its own taskID and a shared live ctx: goroutine A starts
// the job, goroutine B cancels the window ctx and THEN cancels that exact
// task. Because cancel() strictly precedes the pair's CancelTask snapshot,
// the invariant is: a successful Start registered before the snapshot (and
// is cancelled by it); a rejected Start never registered. A started job may
// never remain running.
func TestCancelTaskTrueRaceLiveCtx(t *testing.T) {
	root := t.TempDir()
	e := lifecycleEngine(t, root)
	table := NewJobTable()
	const pairs = 16
	var (
		mu  sync.Mutex
		ids []struct{ id, task string }
		wg  sync.WaitGroup
	)
	for i := 0; i < 16; i++ {
		taskID := fmt.Sprintf("task-race-%d", i)
		ctx, cancel := context.WithCancel(context.Background())
		wg.Add(2)
		go func() { // A: start with a live ctx
			defer wg.Done()
			job, err := table.Start(ctx, e, root, "sleep 30", "bot-a", taskID)
			if err != nil {
				return
			}
			mu.Lock()
			ids = append(ids, struct{ id, task string }{job.ID, taskID})
			mu.Unlock()
			t.Cleanup(func() { _ = table.CancelTask("bot-a", taskID) })
		}()
		go func() { // B: cancel the window ctx, then that exact task
			defer wg.Done()
			cancel()
			_ = table.CancelTask("bot-a", taskID)
		}()
	}
	wg.Wait()
	mu.Lock()
	local := append([]struct{ id, task string }(nil), ids...)
	mu.Unlock()
	// CancelTask awaited the terminal state of every job in its snapshot,
	// so each started job must already be cancelled here — never running.
	for _, p := range local {
		j, err := table.Status(p.id, "bot-a", p.task)
		if err != nil {
			t.Fatalf("job %s vanished: %v", p.id, err)
		}
		if j.Status != JobCancelled {
			t.Fatalf("job %s (task %s) status %s, want cancelled", p.id, p.task, j.Status)
		}
	}
}

// TestNormalWindowContextEndPreservesJob proves the detached-background
// contract: the caller ctx going through its normal end (window done) must
// NOT kill an already-registered job. Only an explicit CancelTask/Cancel
// may terminate it.
func TestNormalWindowContextEndPreservesJob(t *testing.T) {
	root := t.TempDir()
	e := lifecycleEngine(t, root)
	table := NewJobTable()
	ctx, cancel := context.WithCancel(context.Background())
	job := startTracked(t, table, ctx, e, root, "sleep 1; echo done", "bot-a", "task-w")
	// The model window ends right after Start returns.
	cancel()
	// The job must still finish on its own as done, not cancelled.
	j := waitJobTerminal(t, table, job.ID, "bot-a", "task-w", JobDone, func() *int { z := 0; return &z }())
	if !strings.Contains(j.Log, "done") {
		t.Fatalf("job log lost output: %q", j.Log)
	}
}
