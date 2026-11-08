package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func testEngine(t *testing.T, root string) *LocalEngine {
	t.Helper()
	allowed, err := NormalizeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	return NewLocalEngine(Roots{Enabled: true, CommandExecution: true, Dirs: []string{allowed}}, nil)
}

func TestJobStartStatusWaitCancelLifecycle(t *testing.T) {
	root := t.TempDir()
	e := testEngine(t, root)
	table := NewJobTable()
	// Short retention so finished jobs expire immediately: sequential jobs
	// beyond the table cap keep working.
	table.retention = time.Millisecond
	ctx := context.Background()
	job, err := table.Start(ctx, e, root, "echo hello; sleep 2", "u1", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobRunning || job.PID == 0 {
		t.Fatalf("unexpected start state: %+v", job)
	}
	// Only the owning task can see it.
	if _, err := table.Status(job.ID, "u1", "task-other"); err == nil {
		t.Fatal("another task read the job")
	}
	if _, err := table.Status(job.ID, "u2", "task-1"); err == nil {
		t.Fatal("another owner read the job")
	}
	// Wait for real completion.
	deadline := time.Now().Add(15 * time.Second)
	for {
		j, err := table.Status(job.ID, "u1", "task-1")
		if err != nil {
			t.Fatal(err)
		}
		if j.Finished() {
			if j.Status != JobDone || j.ExitCode == nil || *j.ExitCode != 0 {
				t.Fatalf("bad final job: %+v", j)
			}
			if !strings.Contains(j.Log, "hello") {
				t.Fatalf("log missing output: %q", j.Log)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Cancel of a finished job reports its stored outcome.
	c, err := table.Cancel(job.ID, "u1", "task-1")
	if err != nil || c.Status != JobDone {
		t.Fatalf("cancel after finish: %v %+v", err, c)
	}
}

func TestJobCancelKillsProcess(t *testing.T) {
	root := t.TempDir()
	e := testEngine(t, root)
	table := NewJobTable()
	job, err := table.Start(context.Background(), e, root, "sleep 60", "u1", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan Job, 1)
	go func() {
		j, err := table.Cancel(job.ID, "u1", "task-1")
		if err != nil {
			t.Error(err)
			return
		}
		done <- j
	}()
	select {
	case j := <-done:
		if j.Status != JobCancelled {
			t.Fatalf("cancel status %s", j.Status)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cancel did not return")
	}
}

func TestJobOwnershipIsEnforced(t *testing.T) {
	root := t.TempDir()
	e := testEngine(t, root)
	table := NewJobTable()
	job, err := table.Start(context.Background(), e, root, "sleep 1", "u1", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.Cancel(job.ID, "u1", "task-2"); err == nil {
		t.Fatal("foreign task cancelled the job")
	}
	if _, err := table.Wait(context.Background(), job.ID, "u2", "task-1"); err == nil {
		t.Fatal("foreign owner waited on the job")
	}
}

func TestJobPerOwnerLimitWithSequentialCompletion(t *testing.T) {
	// More than MaxJobsTotal sequential jobs must work: finished jobs expire,
	// so a long-running task never deadlocks on the limit.
	root := t.TempDir()
	e := testEngine(t, root)
	table := NewJobTable()
	for i := 0; i < MaxJobsTotal+4; i++ {
		job, err := table.Start(context.Background(), e, root, "true", "u1", "task-1")
		if err != nil {
			t.Fatalf("job %d refused: %v", i, err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			j, _ := table.Status(job.ID, "u1", "task-1")
			if j.Finished() {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("job never finished")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestJobPerOwnerConcurrencyLimit(t *testing.T) {
	root := t.TempDir()
	e := testEngine(t, root)
	table := NewJobTable()
	var last error
	for i := 0; i < MaxJobsPerOwner+2; i++ {
		_, err := table.Start(context.Background(), e, root, "sleep 30", "u1", "task-1")
		if err != nil {
			last = err
		}
	}
	if last == nil {
		t.Fatal("per-owner limit not enforced")
	}
}

func TestJobConcurrentStartStatusRace(t *testing.T) {
	root := t.TempDir()
	e := testEngine(t, root)
	table := NewJobTable()
	job, err := table.Start(context.Background(), e, root, "for i in 1 2 3 4 5; do echo line$i; done", "u1", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for k := 0; k < 8; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				j, err := table.Status(job.ID, "u1", "task-1")
				if err != nil {
					t.Error(err)
					return
				}
				if j.Finished() {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	wg.Wait()
	j, err := table.Status(job.ID, "u1", "task-1")
	if err != nil || !j.Finished() || j.Status != JobDone {
		t.Fatalf("concurrent readers ended wrong: %v %+v", err, j)
	}
	if !strings.Contains(j.Log, "line5") {
		t.Fatalf("log lost output: %q", j.Log)
	}
}

func TestJobStartRefusesOutsideRoot(t *testing.T) {
	root := t.TempDir()
	e := testEngine(t, root)
	table := NewJobTable()
	outside := t.TempDir()
	if _, err := table.Start(context.Background(), e, outside, "true", "u1", "task-1"); err == nil {
		t.Fatal("job started outside allowlist")
	}
	_ = filepath.Join // keep filepath imported
}

// TestJobCancelKillsProcessTreeRegression guards against orphaned
// grandchildren: a sh that exits while a background grandchild holds the
// pipes must not keep the job alive after cancel. Before the tree-kill fix
// the grandchild kept the log pipes open, cmd.Wait hung and the job never
// resolved. The MaxJobRuntime timeout routes through the same
// cmd.Cancel → killProcessTree path, so this also covers the timeout case.
func TestJobCancelKillsProcessTreeRegression(t *testing.T) {
	if !canInspectProcesses {
		t.Skip("process inspection unavailable on this platform")
	}
	root := t.TempDir()
	e := testEngine(t, root)
	table := NewJobTable()
	// The grandchild inherits stdout/stderr and sleeps long enough to hold
	// the pipes open if it survives; sh exits immediately either way.
	job, err := table.Start(context.Background(), e, root,
		"sleep 30 & echo started", "u1", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	c, err := table.Cancel(job.ID, "u1", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != JobCancelled {
		t.Fatalf("cancel status %s", c.Status)
	}
	// The cancelled job must actually finish, not hang in running: cancel
	// already waited on rj.done, so reaching here means the pipes closed.
}

// TestJobCancelTreeRegression checks the shared tree-kill path: cancel and
// the MaxJobRuntime timeout both route through cmd.Cancel → killProcessTree,
// so a pipe-holding grandchild must not keep the job alive past the call.
func TestJobCancelTreeRegression(t *testing.T) {
	if !canInspectProcesses {
		t.Skip("process inspection unavailable on this platform")
	}
	root := t.TempDir()
	e := testEngine(t, root)
	table := NewJobTable()
	// A grandchild inheriting stdout/stderr sleeps long enough to hold the
	// pipes open if it survives; sh exits immediately either way.
	job, err := table.Start(context.Background(), e, root,
		"sleep 30 & echo started", "u1", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	c, err := table.Cancel(job.ID, "u1", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != JobCancelled {
		t.Fatalf("cancel status %s", c.Status)
	}
}

// TestJobGrandchildDiesWithJob verifies the actual OS-level guarantee: after
// cancel the spawned grandchild is gone, not orphaned.
func TestJobGrandchildDiesWithJob(t *testing.T) {
	if !canInspectProcesses {
		t.Skip("process inspection unavailable on this platform")
	}
	root := t.TempDir()
	e := testEngine(t, root)
	table := NewJobTable()
	// Write the grandchild pid to a file inside the workspace root, then
	// wait for cancel to reap it.
	job, err := table.Start(context.Background(), e, root,
		"sleep 30 & echo $! > pid.txt; sleep 60", "u1", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	// Wait until the grandchild pid is on disk before cancelling, so the
	// echo is not killed before it runs.
	var pidBytes []byte
	waitDeadline := time.Now().Add(10 * time.Second)
	for {
		pidBytes, err = os.ReadFile(filepath.Join(root, "pid.txt"))
		if err == nil {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatalf("grandchild pid never written: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := table.Cancel(job.ID, "u1", "task-1"); err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("bad pid file: %v", err)
	}
	// Give the kernel a moment to deliver the group signal, then confirm
	// the grandchild is not alive. A leftover process would outlive the job.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if !pidAlive(pid) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived cancel", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestJobFinishedTableCapRegression runs maxFinishedJobs+5 sequential jobs
// and verifies the table never holds more than maxFinishedJobs finished
// jobs (none reaches the retention deadline), the newest survive and the
// oldest are evicted. Since watch gc's as soon as a job finishes, the cap
// must hold immediately after each Wait, not just at the next Start.
func TestJobFinishedTableCapRegression(t *testing.T) {
	root := t.TempDir()
	e := testEngine(t, root)
	table := NewJobTable()
	// Long retention: eviction must come from the hard cap, not expiry.
	table.retention = time.Hour
	ctx := context.Background()
	const extra = 5
	// started[i] holds the first maxFinishedJobs job ids in start order;
	// anything earlier must have been evicted by the cap.
	started := make([]string, 0, maxFinishedJobs+extra)
	for i := 0; i < maxFinishedJobs+extra; i++ {
		job, err := table.Start(ctx, e, root, "true", "u1", "task-1")
		if err != nil {
			t.Fatalf("sequential job %d refused: %v", i, err)
		}
		started = append(started, job.ID)
		done, err := table.Wait(ctx, job.ID, "u1", "task-1")
		if err != nil {
			t.Fatalf("job %d wait: %v", i, err)
		}
		if !done.Finished() || done.ExitCode == nil || *done.ExitCode != 0 {
			t.Fatalf("job %d did not finish cleanly: %+v", i, done)
		}
		// The cap must be enforced immediately after a job finishes, without
		// waiting for the next Start's gc.
		table.mu.Lock()
		n := len(table.jobs)
		table.mu.Unlock()
		if n > maxFinishedJobs {
			t.Fatalf("after job %d: table holds %d finished jobs, cap is %d", i, n, maxFinishedJobs)
		}
	}
	table.mu.Lock()
	n := len(table.jobs)
	// The newest maxFinishedJobs jobs must be present.
	kept := started[len(started)-maxFinishedJobs:]
	for _, id := range kept {
		if _, ok := table.jobs[id]; !ok {
			t.Errorf("newest job %s was evicted", id)
		}
	}
	// Everything older must have been evicted.
	for _, id := range started[:len(started)-maxFinishedJobs] {
		if _, ok := table.jobs[id]; ok {
			t.Errorf("oldest job %s survived past the cap", id)
		}
	}
	table.mu.Unlock()
	if n != maxFinishedJobs {
		t.Fatalf("table holds %d jobs, want exactly %d", n, maxFinishedJobs)
	}
}

// TestJobConcurrentCancelStatusRace hammers Cancel and Status concurrently
// on the same job; run under -race to catch state transitions racing.
func TestJobConcurrentCancelStatusRace(t *testing.T) {
	root := t.TempDir()
	e := testEngine(t, root)
	table := NewJobTable()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		job, err := table.Start(context.Background(), e, root, "echo hi", "u1", "task-1")
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(3)
		go func(id string) {
			defer wg.Done()
			_, _ = table.Cancel(id, "u1", "task-1")
		}(job.ID)
		go func(id string) {
			defer wg.Done()
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				j, err := table.Status(id, "u1", "task-1")
				if err != nil {
					t.Error(err)
					return
				}
				if j.Finished() {
					return
				}
				time.Sleep(2 * time.Millisecond)
			}
			t.Error("job never finished")
		}(job.ID)
		go func(id string) {
			defer wg.Done()
			_, _ = table.Wait(context.Background(), id, "u1", "task-1")
		}(job.ID)
	}
	wg.Wait()
}

// TestJobConcurrentStartRace hammers Start from several goroutines; run
// under -race to catch seq/id/limit bookkeeping racing.
func TestJobConcurrentStartRace(t *testing.T) {
	root := t.TempDir()
	e := testEngine(t, root)
	table := NewJobTable()
	table.retention = time.Millisecond
	ids := make(chan string, 32)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 4; i++ {
				job, err := table.Start(context.Background(), e, root, "true", "u1", "task-1")
				if err != nil {
					continue // limits may legitimately refuse under churn
				}
				ids <- job.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate job id %s", id)
		}
		seen[id] = true
	}
}
