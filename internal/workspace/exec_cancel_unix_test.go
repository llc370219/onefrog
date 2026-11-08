//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly || solaris || aix

package workspace

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Regression: cancelling the exec context must tear down the whole foreground
// process tree promptly. Go's CommandContext kills only the direct child, and
// grandchildren that outlive it keep the output pipes open — without a
// process-group Cancel wired up, exec blocks until the descendants exit on
// their own. This goes through the real LocalEngine.exec path (no injected
// runner) so a regression in the production wiring is caught here.
func TestExecContextCancelKillsProcessTree(t *testing.T) {
	if !canInspectProcesses {
		t.Skip("platform cannot inspect other processes")
	}

	root, roots := newTestRoots(t)
	e := NewLocalEngine(roots, nil)

	pidFile := "sleep.pid"
	// The shell spawns sleep as a background child and records its pid, then
	// waits on it — the tree is shell -> sleep, and cancellation must kill both.
	command := "sleep 60 & echo $! > " + pidFile + "; wait $!"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type execResult struct {
		stdout, stderr string
		code           int
		truncated      bool
		err            error
	}
	done := make(chan execResult, 1)
	go func() {
		stdout, stderr, code, truncated, err := e.exec(ctx, root, command)
		done <- execResult{stdout, stderr, code, truncated, err}
	}()

	// Bounded wait for the child pid to appear on disk.
	deadline := time.Now().Add(5 * time.Second)
	var pid int
	for {
		raw, err := os.ReadFile(root + "/" + pidFile)
		if err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && n > 0 {
				pid = n
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("child pid file never appeared")
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("command finished before cancellation")
	default:
	}

	start := time.Now()
	cancel()

	select {
	case res := <-done:
		if res.err == nil && res.code == 0 {
			t.Fatal("expected the command to report failure after cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("exec did not return promptly after cancel (waited %v)", time.Since(start))
	}

	// The sleep child runs in the shell's new process group (Setpgid), so its
	// death proves the whole group was signalled, not just the shell. Wait
	// briefly for the reparented child to be reaped before asserting.
	gone := false
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); {
		if !pidAlive(pid) {
			gone = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !gone {
		t.Fatalf("spawned sleep child %d is still alive after cancellation", pid)
	}
}
