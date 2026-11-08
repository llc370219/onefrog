package bot

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"onefrog/internal/preferences"
	"onefrog/internal/task"
	"onefrog/internal/workspace"
)

// jobsFixture is a runtime fixture with a real JobTable bound, for tests
// that verify pause/cancel/deadline stop a task tree's background jobs.
type jobsFixture struct {
	*runtimeFixture
	jobs   *workspace.JobTable
	root   string
	engine *workspace.LocalEngine
}

func newJobsFixture(t *testing.T) *jobsFixture {
	t.Helper()
	f := &jobsFixture{runtimeFixture: newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 4)}
	root := t.TempDir()
	allowed, err := workspace.NormalizeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	f.root = root
	f.engine = workspace.NewLocalEngine(workspace.Roots{Enabled: true, CommandExecution: true, Dirs: []string{allowed}}, nil)
	f.jobs = workspace.NewJobTable()
	f.agent.LocalWorkspace = f.engine
	f.agent.Jobs = f.jobs
	return f
}

// createTree creates a running root task and its direct running child.
func (f *jobsFixture) createTree(t *testing.T, rootID, childID string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := f.book.Create(task.Task{
		ID: rootID, OwnerID: f.owner, BotID: "main", SessionID: "s-" + rootID,
		Goal: "根任务", Status: task.StatusRunning, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.book.Create(task.Task{
		ID: childID, OwnerID: f.owner, BotID: "main", SessionID: "s-" + childID,
		Goal: "子任务", ParentID: rootID, Status: task.StatusRunning, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

// startJob starts a real sleeping job owned by (owner, taskID) and cleans it
// up when the test ends.
func (f *jobsFixture) startJob(t *testing.T, owner, taskID string) string {
	t.Helper()
	job, err := f.jobs.Start(context.Background(), f.engine, f.root, "sleep 30", owner, taskID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.jobs.Cancel(job.ID, owner, taskID) })
	return job.ID
}

func waitJobDone(t *testing.T, f *jobsFixture, id, owner, taskID string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		j, err := f.jobs.Status(id, owner, taskID)
		if err == nil && j.Finished() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s never finished", id)
}

func waitTaskStatus(t *testing.T, f *jobsFixture, id, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		it, err := f.book.Get(id)
		if err == nil && it.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	it, _ := f.book.Get(id)
	t.Fatalf("task %s status %s, want %s", id, it.Status, want)
}

// TestTaskPauseStopsOwnAndChildJobs proves Pause stops the root's and its
// direct child's background jobs (owner = BotID) while an unrelated bot's
// job keeps running.
func TestTaskPauseStopsOwnAndChildJobs(t *testing.T) {
	f := newJobsFixture(t)
	f.createTree(t, "root-j", "child-j")
	rootJob := f.startJob(t, "main", "root-j")
	childJob := f.startJob(t, "main", "child-j")
	otherJob := f.startJob(t, "check", "unrelated")

	if _, err := f.runner.Pause(context.Background(), f.owner, "root-j"); err != nil {
		t.Fatal(err)
	}
	waitTaskStatus(t, f, "root-j", task.StatusPaused)
	waitTaskStatus(t, f, "child-j", task.StatusPaused)
	waitJobDone(t, f, rootJob, "main", "root-j")
	waitJobDone(t, f, childJob, "main", "child-j")

	j, err := f.jobs.Status(otherJob, "check", "unrelated")
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != workspace.JobRunning {
		t.Fatalf("unrelated bot job was %s, must keep running", j.Status)
	}
}

// TestTaskCancelStopsOwnAndChildJobs proves Cancel durably cancels the tree
// and kills the root's and child's background jobs; an unrelated bot's job
// keeps running.
func TestTaskCancelStopsOwnAndChildJobs(t *testing.T) {
	f := newJobsFixture(t)
	f.createTree(t, "root-c", "child-c")
	rootJob := f.startJob(t, "main", "root-c")
	childJob := f.startJob(t, "main", "child-c")
	otherJob := f.startJob(t, "check", "unrelated")

	if _, err := f.runner.Cancel(context.Background(), f.owner, "root-c"); err != nil {
		t.Fatal(err)
	}
	waitTaskStatus(t, f, "root-c", task.StatusCancelled)
	waitTaskStatus(t, f, "child-c", task.StatusCancelled)
	waitJobDone(t, f, rootJob, "main", "root-c")
	waitJobDone(t, f, childJob, "main", "child-c")

	j, err := f.jobs.Status(otherJob, "check", "unrelated")
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != workspace.JobRunning {
		t.Fatalf("unrelated bot job was %s, must keep running", j.Status)
	}
}

// TestTaskDeadlineStopsOwnAndChildJobs proves the window's
// DeadlineExceeded branch durably cancels the children and stops the tree's
// background jobs; an unrelated bot's job keeps running.
func TestTaskDeadlineStopsOwnAndChildJobs(t *testing.T) {
	f := newJobsFixture(t)
	f.createTree(t, "root-d", "child-d")
	rootJob := f.startJob(t, "main", "root-d")
	childJob := f.startJob(t, "main", "child-d")
	otherJob := f.startJob(t, "check", "unrelated")
	f.runner.stepHook = func(ctx context.Context, a *Agent, it task.Task) (taskOutcome, error) {
		return taskOutcome{}, context.DeadlineExceeded
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.runner.execute(ctx, "root-d")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		cancel()
		t.Fatal("execute never returned")
	}
	cancel()

	waitTaskStatus(t, f, "root-d", task.StatusBudgetExhausted)
	waitTaskStatus(t, f, "child-d", task.StatusCancelled)
	waitJobDone(t, f, rootJob, "main", "root-d")
	waitJobDone(t, f, childJob, "main", "child-d")

	j, err := f.jobs.Status(otherJob, "check", "unrelated")
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != workspace.JobRunning {
		t.Fatalf("unrelated bot job was %s, must keep running", j.Status)
	}
}

// TestTaskProcessShutdownCanceledKeepsTreeAndJobs proves a pure
// context.Canceled from the window (e.g. process shutdown) does NOT durably
// cancel delegated children and does NOT kill the tree's background jobs:
// jobs must span windows and survive a restart-shaped cancellation.
func TestTaskProcessShutdownCanceledKeepsTreeAndJobs(t *testing.T) {
	f := newJobsFixture(t)
	f.createTree(t, "root-x", "child-x")
	rootJob := f.startJob(t, "main", "root-x")
	childJob := f.startJob(t, "main", "child-x")
	f.runner.stepHook = func(ctx context.Context, a *Agent, it task.Task) (taskOutcome, error) {
		return taskOutcome{}, context.Canceled
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.runner.execute(ctx, "root-x")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		cancel()
		t.Fatal("execute never returned")
	}
	cancel()

	// The statuses must be untouched: root still running, child still
	// running, no durable cancellation happened.
	root, err := f.book.Get("root-x")
	if err != nil {
		t.Fatal(err)
	}
	if root.Status != task.StatusRunning {
		t.Fatalf("root task status %s, want running after process-shutdown cancel", root.Status)
	}
	child, err := f.book.Get("child-x")
	if err != nil {
		t.Fatal(err)
	}
	if child.Status != task.StatusRunning {
		t.Fatalf("child task status %s, want running after process-shutdown cancel", child.Status)
	}
	// The detached jobs must still be running: a restart must not kill them.
	for _, probe := range []struct{ id, taskID string }{
		{rootJob, "root-x"}, {childJob, "child-x"},
	} {
		j, err := f.jobs.Status(probe.id, "main", probe.taskID)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status != workspace.JobRunning {
			t.Fatalf("job %s was %s, must survive process-shutdown cancel", probe.id, j.Status)
		}
	}
}
