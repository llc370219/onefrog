package workspace

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Native background command jobs. A job is a real OS process started inside an
// exact allowlisted local root: it has a pid, streaming bounded logs, a cancel
// handle and a final exit code. Jobs belong to the (owner, task) that started
// them; other tasks and other owners cannot see or touch them. The table is
// process-local by design: a job that outlives a restart has an unknown
// outcome, and the task layer must treat it that way rather than replay it.

// JobLimits bounds each job and the job table.
const (
	MaxJobsPerOwner = 8
	MaxJobsTotal    = 32
	MaxJobLogBytes  = 64 << 10
	MaxJobRuntime   = 60 * time.Minute
	jobLogChunk     = 8 << 10
	// finishedJobRetention is how long a finished job stays queryable, so a
	// task can read its exit code; after that it is garbage-collected.
	finishedJobRetention = 30 * time.Minute
	// maxFinishedJobs is a hard cap on finished jobs kept in the table. The
	// retention window alone cannot bound the table when short jobs finish
	// faster than they expire; this cap can, and always spares the newest.
	maxFinishedJobs = MaxJobsTotal
)

// Job status values.
const (
	JobRunning   = "running"
	JobDone      = "done"
	JobFailed    = "failed" // nonzero exit
	JobCancelled = "cancelled"
)

// Job is one background command. Owner and TaskID scope every table lookup.
type Job struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	Root      string    `json:"root"`
	Command   string    `json:"command"`
	PID       int       `json:"pid,omitempty"`
	Status    string    `json:"status"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Log       string    `json:"log"`
	Truncated bool      `json:"truncated,omitempty"`
}

// runningJob carries one job's live handles. job's state is guarded by the
// TABLE lock (t.mu): there is exactly one lock for job state, the id map, seq
// and limits — no nested or split locks, so no deadlock and no torn reads.
// The collector goroutine also takes t.mu (never logMu around it) for every
// state transition, so readers cannot observe half-written final states.
type runningJob struct {
	job    Job
	cancel context.CancelFunc
	done   chan struct{}
	logMu  sync.Mutex // serializes collector writes into job.Log only
	proc   atomic.Pointer[os.Process]
}

// JobTable tracks background jobs started by tasks.
type JobTable struct {
	mu        sync.Mutex
	seq       int
	jobs      map[string]*runningJob
	retention time.Duration // finished-job retention; 0 = default
}

// NewJobTable builds an empty job table.
func NewJobTable() *JobTable {
	return &JobTable{jobs: map[string]*runningJob{}}
}

var errNoJob = errors.New("后台作业不存在（可能已结束并被清理，或服务重启过）")
var errNotOwner = errors.New("后台作业不属于当前任务，无法访问")

// Start runs command in the exact requested root as a background process.
// owner and taskID scope the job: only they can read or cancel it.
func (t *JobTable) Start(ctx context.Context, engine *LocalEngine, root, command, owner, taskID string) (Job, error) {
	if engine == nil {
		return Job{}, errors.New("本地工作区未启用")
	}
	// Authorization uses the allowlist; execution uses the exact requested
	// root (same rule as LocalEngine.Do).
	if _, err := engine.allowed(root); err != nil {
		return Job{}, err
	}
	target, err := NormalizeRoot(root)
	if err != nil {
		return Job{}, err
	}
	if st, serr := os.Stat(target); serr != nil || !st.IsDir() {
		return Job{}, errors.New("工作区根目录不存在或不是目录")
	}
	if resolved, rerr := filepath.EvalSymlinks(target); rerr == nil {
		if err := engine.assertCovered(resolved); err != nil {
			return Job{}, err
		}
	}
	if !engine.Roots().CommandExecution {
		return Job{}, errors.New("本地命令执行未启用")
	}
	t.gc()
	t.mu.Lock()
	running := 0
	for _, j := range t.jobs {
		if j.job.Status == JobRunning {
			running++
		}
	}
	if running >= MaxJobsTotal {
		t.mu.Unlock()
		return Job{}, errors.New("后台作业数量已达上限，请稍后重试")
	}
	perOwner := 0
	for _, j := range t.jobs {
		if j.job.Owner == owner && j.job.Status == JobRunning {
			perOwner++
		}
	}
	if perOwner >= MaxJobsPerOwner {
		t.mu.Unlock()
		return Job{}, errors.New("这个任务的后台作业已达上限，请先等待或取消现有作业")
	}
	runCtx, cancel := context.WithTimeout(context.Background(), MaxJobRuntime)
	cmd := exec.CommandContext(runCtx, "sh", "-c", command)
	cmd.Dir = target
	// New process group: cancel/timeout kills the whole tree, children
	// included, so no descendant holds the log pipes. The group kill happens
	// via cmd.Cancel, because CommandContext itself only signals the direct
	// child. WaitDelay forces the pipes closed if descendants survive.
	cmd.SysProcAttr = procGroupAttr()
	cmd.Cancel = func() error { return killProcessTree(cmd.Process) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = sanitizedEnv()
	logPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.mu.Unlock()
		return Job{}, err
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		t.mu.Unlock()
		return Job{}, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.mu.Unlock()
		return Job{}, err
	}
	t.seq++
	id := time.Now().UTC().Format("20060102T150405") + "-" + itoa(t.seq)
	rj := &runningJob{
		job: Job{
			ID: id, Owner: owner, TaskID: taskID, Root: target, Command: command,
			PID: cmd.Process.Pid, Status: JobRunning, StartedAt: time.Now().UTC(),
		},
		cancel: cancel,
		done:   make(chan struct{}),
	}
	t.jobs[id] = rj
	t.mu.Unlock()
	rj.proc.Store(cmd.Process)
	go t.watch(rj, cmd, runCtx, logPipe, errPipe)
	return t.snapshot(rj), nil
}

// watch collects the job's output, waits for the process and records the
// final state. It is the only goroutine that mutates the job after Start.
func (t *JobTable) watch(rj *runningJob, cmd *exec.Cmd, runCtx context.Context, logPipe, errPipe io.ReadCloser) {
	defer close(rj.done)
	defer logPipe.Close()
	defer errPipe.Close()
	collect := func(r interface{ Read([]byte) (int, error) }) {
		buf := make([]byte, jobLogChunk)
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				rj.logMu.Lock()
				appendJobLog(&rj.job, string(buf[:n]))
				rj.logMu.Unlock()
			}
			if rerr != nil {
				return
			}
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); collect(logPipe) }()
	go func() { defer wg.Done(); collect(errPipe) }()
	wg.Wait()
	runErr := cmd.Wait()
	t.mu.Lock()
	rj.job.EndedAt = time.Now().UTC()
	if ctxErr := runCtx.Err(); ctxErr != nil && (errors.Is(ctxErr, context.Canceled) || errors.Is(ctxErr, context.DeadlineExceeded)) {
		rj.job.Status = JobCancelled
	} else if runErr != nil {
		rj.job.Status = JobFailed
		if code := exitCodeOf(runErr); code >= 0 {
			c := code
			rj.job.ExitCode = &c
		}
	} else {
		rj.job.Status = JobDone
		zero := 0
		rj.job.ExitCode = &zero
	}
	t.mu.Unlock()
	// Enforce the finished-job cap right away: a fast job sequence must not
	// have to wait for the next Start's gc to trim the table. Safe to call
	// here because t.mu is released; gc only takes t.mu, matching the single
	// lock order used everywhere else.
	t.gc()
}

// lookup finds the job and verifies ownership. t.mu must be held.
func (t *JobTable) lookup(id, owner, taskID string) (*runningJob, error) {
	rj, ok := t.jobs[id]
	if !ok {
		return nil, errNoJob
	}
	if owner != "" && rj.job.Owner != owner {
		return nil, errNotOwner
	}
	if taskID != "" && rj.job.TaskID != taskID {
		return nil, errNotOwner
	}
	return rj, nil
}

// Status returns a copy of the job's current state, only to its owner task.
func (t *JobTable) Status(id, owner, taskID string) (Job, error) {
	t.mu.Lock()
	rj, err := t.lookup(id, owner, taskID)
	if err != nil {
		t.mu.Unlock()
		return Job{}, err
	}
	snap := t.snapshotLocked(rj)
	t.mu.Unlock()
	return snap, nil
}

// Finished reports whether the job reached a terminal state.
func (j Job) Finished() bool { return j.Status != JobRunning }

// Cancel stops a running job, only for its owner task. Cancelling an
// already-finished job reports its stored outcome.
func (t *JobTable) Cancel(id, owner, taskID string) (Job, error) {
	t.mu.Lock()
	rj, err := t.lookup(id, owner, taskID)
	t.mu.Unlock()
	if err != nil {
		return Job{}, err
	}
	t.mu.Lock()
	alive := rj.job.Status == JobRunning
	t.mu.Unlock()
	if alive {
		if ap := rj.proc.Load(); ap != nil {
			p := ap
			rj.cancel()
			// Verify the tree actually died: cancel signals the group, but the
			// kernel delivers the signal asynchronously. Poll briefly and
			// re-kill so a racing grandchild cannot keep the pipes open.
			if canInspectProcesses {
				for i := 0; i < 20; i++ {
					if !pidAlive(p.Pid) {
						break
					}
					_ = killProcessTree(p)
					time.Sleep(50 * time.Millisecond)
				}
			}
		} else {
			rj.cancel()
		}
	}
	select {
	case <-rj.done:
	case <-time.After(10 * time.Second):
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked(rj), nil
}

// Wait blocks until the job finishes or the context ends, only for its owner
// task.
func (t *JobTable) Wait(ctx context.Context, id, owner, taskID string) (Job, error) {
	t.mu.Lock()
	rj, err := t.lookup(id, owner, taskID)
	t.mu.Unlock()
	if err != nil {
		return Job{}, err
	}
	select {
	case <-rj.done:
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.snapshotLocked(rj), nil
	case <-ctx.Done():
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.snapshotLocked(rj), ctx.Err()
	}
}

// gc drops finished jobs past their retention, then enforces a hard cap on
// how many finished jobs stay in the table (newest kept, oldest evicted), so
// the table cannot grow without bound even when jobs churn faster than the
// retention window. More than MaxJobsTotal sequential jobs still work.
func (t *JobTable) gc() {
	t.mu.Lock()
	defer t.mu.Unlock()
	retention := t.retention
	if retention <= 0 {
		retention = finishedJobRetention
	}
	drop := time.Now().Add(-retention)
	var finished []string // finished job ids, oldest first
	for id, rj := range t.jobs {
		if rj.job.Status != JobRunning && !rj.job.EndedAt.IsZero() {
			if rj.job.EndedAt.Before(drop) {
				delete(t.jobs, id)
				continue
			}
			finished = append(finished, id)
		}
	}
	// Sort oldest-ended first so eviction spares the newest entries.
	sort.Slice(finished, func(i, j int) bool {
		return t.jobs[finished[i]].job.EndedAt.Before(t.jobs[finished[j]].job.EndedAt)
	})
	for len(finished) > maxFinishedJobs {
		delete(t.jobs, finished[0])
		finished = finished[1:]
	}
}

// Count reports live job count (for tests and limits).
func (t *JobTable) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.jobs)
}

// snapshotLocked copies the job state. The table lock guards job fields; the
// collector's logMu only serializes log appends, so hold it for the log.
func (t *JobTable) snapshotLocked(rj *runningJob) Job {
	rj.logMu.Lock()
	out := rj.job
	out.Log = clipJobLog(out.Log)
	rj.logMu.Unlock()
	return out
}

func (t *JobTable) snapshot(rj *runningJob) Job {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked(rj)
}

func appendJobLog(j *Job, chunk string) {
	if len(j.Log)+len(chunk) > MaxJobLogBytes {
		j.Truncated = true
		keep := MaxJobLogBytes - len(chunk)
		if keep < 0 {
			keep = 0
		}
		// Keep the tail; the head is what drops.
		j.Log = j.Log[len(j.Log)-keep:] + chunk
		return
	}
	j.Log += chunk
}

func clipJobLog(s string) string {
	if len(s) <= MaxJobLogBytes {
		return s
	}
	return s[len(s)-MaxJobLogBytes:]
}

func exitCodeOf(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// sanitizedEnv is shared with LocalEngine.exec.
func sanitizedEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TERM=" + firstNonEmptyLocal(os.Getenv("TERM"), "dumb"),
		"LANG=" + firstNonEmptyLocal(os.Getenv("LANG"), "C.UTF-8"),
	}
}
