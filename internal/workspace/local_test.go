package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestRoots(t *testing.T) (string, Roots) {
	t.Helper()
	root := t.TempDir()
	allowed, err := NormalizeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, Roots{Enabled: true, CommandExecution: true, Dirs: []string{allowed}}
}

func localWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalReadListSearchEditFlow(t *testing.T) {
	root, roots := newTestRoots(t)
	e := NewLocalEngine(roots, nil)
	ctx := context.Background()

	localWrite(t, filepath.Join(root, "main.go"), "package main\n\nfunc main() {}\n")
	localWrite(t, filepath.Join(root, "notes.md"), "hello world\n")
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	localWrite(t, filepath.Join(root, "sub", "util.go"), "package sub\n")

	// list
	res, err := e.Do(ctx, root, LocalRequest{Action: "list"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 4 {
		t.Fatalf("list: %v", res.Files)
	}

	// read + revision
	res, err = e.Do(ctx, root, LocalRequest{Action: "read", Path: "main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Revision == "" || !strings.Contains(res.Text, "func main()") {
		t.Fatalf("read: %q rev %q", res.Text, res.Revision)
	}
	rev := res.Revision

	// search
	res, err = e.Do(ctx, root, LocalRequest{Action: "search", Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 || res.Files[0] != "notes.md" {
		t.Fatalf("search: %v", res.Files)
	}

	// edit with wrong revision fails
	if _, err := e.Do(ctx, root, LocalRequest{Action: "edit", Path: "main.go", Revision: "deadbeef", Old: "main()", New: "main() {}"}); err == nil {
		t.Fatal("edit with wrong revision succeeded")
	}
	// edit with correct revision
	res, err = e.Do(ctx, root, LocalRequest{Action: "edit", Path: "main.go", Revision: rev, Old: "func main() {}", New: "func main() { println(1) }"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "main.go"))
	if !strings.Contains(string(raw), "println(1)") {
		t.Fatalf("edit not applied: %s", raw)
	}
	// old revision no longer matches
	if _, err := e.Do(ctx, root, LocalRequest{Action: "edit", Path: "main.go", Revision: rev, Old: "func main() {}", New: "x"}); err == nil {
		t.Fatal("stale revision accepted")
	}

	// write new file
	res, err = e.Do(ctx, root, LocalRequest{Action: "write", Path: "new.txt", Content: "created"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(root, "new.txt"))
	if string(raw) != "created" {
		t.Fatalf("write: %s", raw)
	}
	// write over existing without revision refused
	if _, err := e.Do(ctx, root, LocalRequest{Action: "write", Path: "new.txt", Content: "again"}); err == nil {
		t.Fatal("overwrite without revision accepted")
	}
}

func TestLocalPathEscapeAndSymlinkDefense(t *testing.T) {
	root, roots := newTestRoots(t)
	e := NewLocalEngine(roots, nil)
	ctx := context.Background()
	// A secret outside the root.
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	localWrite(t, secret, "topsecret")

	if _, err := e.Do(ctx, root, LocalRequest{Action: "read", Path: "../" + filepath.Base(outside) + "/secret.txt"}); err == nil {
		t.Fatal("../ escape succeeded")
	}
	if _, err := e.Do(ctx, root, LocalRequest{Action: "read", Path: "/etc/passwd"}); err == nil {
		t.Fatal("absolute path succeeded")
	}
	// Symlink inside root pointing outside must be refused.
	if err := os.Symlink(secret, filepath.Join(root, "leak.txt")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := e.Do(ctx, root, LocalRequest{Action: "read", Path: "leak.txt"}); err == nil {
		t.Fatal("symlink escape succeeded")
	}
	if _, err := e.Do(ctx, root, LocalRequest{Action: "write", Path: "leak.txt", Content: "x"}); err == nil {
		t.Fatal("write through symlink succeeded")
	}
	// .git is protected.
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	localWrite(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main")
	if _, err := e.Do(ctx, root, LocalRequest{Action: "read", Path: ".git/HEAD"}); err == nil {
		t.Fatal(".git read succeeded")
	}
	if _, err := e.Do(ctx, root, LocalRequest{Action: "write", Path: "sub/.git/config", Content: "x"}); err == nil {
		t.Fatal(".git write succeeded")
	}
	// .env is protected by name anywhere in the tree.
	localWrite(t, filepath.Join(root, ".env"), "PASSWORD=1")
	if _, err := e.Do(ctx, root, LocalRequest{Action: "read", Path: ".env"}); err == nil {
		t.Fatal(".env read succeeded")
	}
}

func TestLocalRootsAllowlist(t *testing.T) {
	root, roots := newTestRoots(t)
	e := NewLocalEngine(roots, nil)
	ctx := context.Background()

	// Inside the allowlisted root works.
	if _, err := e.Do(ctx, root, LocalRequest{Action: "list"}); err != nil {
		t.Fatal(err)
	}
	// A sibling directory is refused.
	if _, err := e.Do(ctx, root+"/../elsewhere", LocalRequest{Action: "list"}); err == nil {
		t.Fatal("outside root accepted")
	}
	// Disabled feature refuses everything.
	disabled := NewLocalEngine(Roots{Enabled: false, Dirs: roots.Dirs}, nil)
	if _, err := disabled.Do(ctx, root, LocalRequest{Action: "read", Path: "x"}); err == nil {
		t.Fatal("disabled engine accepted a read")
	}
	// Relative or empty root refused.
	if err := (Roots{Enabled: true, Dirs: []string{"relative/path"}}).Validate(); err == nil {
		t.Fatal("relative root accepted by Validate")
	}
	if err := (Roots{Enabled: true, Dirs: []string{"/"}}).Validate(); err == nil {
		t.Fatal("filesystem root accepted by Validate")
	}
}

func TestLocalRunRequiresExplicitOptIn(t *testing.T) {
	root, roots := newTestRoots(t)
	roots.CommandExecution = false
	e := NewLocalEngine(roots, func(ctx context.Context, dir, command string) (string, string, int, error) {
		t.Fatal("runner must not be called when command execution is disabled")
		return "", "", 0, nil
	})
	if _, err := e.Do(context.Background(), root, LocalRequest{Action: "run", Command: "echo hi"}); err == nil {
		t.Fatal("run accepted while disabled")
	}
}

func TestLocalRunExecutesAndReportsExitCode(t *testing.T) {
	root, roots := newTestRoots(t)
	var resolvedRoot string
	if r, err := filepath.EvalSymlinks(root); err == nil {
		resolvedRoot = r
	} else {
		resolvedRoot = root
	}
	e := NewLocalEngine(roots, func(ctx context.Context, dir, command string) (string, string, int, error) {
		// The runner gets the exact canonical requested root (not the
		// allowlist ancestor); the kernel resolves symlinks in cwd.
		got := dir
		if canon, err := filepath.EvalSymlinks(dir); err == nil {
			got = canon
		}
		if got != resolvedRoot {
			t.Fatalf("run dir %q, want root", dir)
		}
		if command == "fail" {
			return "", "boom", 3, nil
		}
		return "ok\n", "", 0, nil
	})
	res, err := e.Do(context.Background(), root, LocalRequest{Action: "run", Command: "echo hi"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 || !strings.Contains(res.Text, "ok") {
		t.Fatalf("run: %+v", res)
	}
	res, err = e.Do(context.Background(), root, LocalRequest{Action: "run", Command: "fail"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == nil || *res.ExitCode != 3 || !strings.Contains(res.Text, "boom") {
		t.Fatalf("failed run: %+v", res)
	}
}

func TestLocalRunNativeExecutor(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	root, roots := newTestRoots(t)
	e := NewLocalEngine(roots, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := e.Do(ctx, root, LocalRequest{Action: "run", Command: "pwd && echo $ONEFROG_SECRET && echo done"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("exit %+v text %s", res.ExitCode, res.Text)
	}
	if !strings.Contains(res.Text, root) {
		t.Fatalf("pwd not in root: %s", res.Text)
	}
	// Sanitized environment: the process's own env does not leak in.
	t.Setenv("ONEFROG_SECRET", "supersecret")
	res, err = e.Do(ctx, root, LocalRequest{Action: "run", Command: "echo [$ONEFROG_SECRET]"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "supersecret") {
		t.Fatalf("environment leaked into local command: %s", res.Text)
	}
	// Failing command reports the real exit code as a result, not an error.
	res, err = e.Do(ctx, root, LocalRequest{Action: "run", Command: "exit 7"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == nil || *res.ExitCode != 7 {
		t.Fatalf("exit code: %+v", res.ExitCode)
	}
}

func TestLocalWalkSkipsProtectedAndSymlinkedDirs(t *testing.T) {
	root, roots := newTestRoots(t)
	e := NewLocalEngine(roots, nil)
	ctx := context.Background()
	localWrite(t, filepath.Join(root, "a.txt"), "x")
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	localWrite(t, filepath.Join(root, ".git", "config"), "secretgit")
	if err := os.Mkdir(filepath.Join(root, "vendor"), 0o750); err != nil {
		t.Fatal(err)
	}
	localWrite(t, filepath.Join(root, "vendor", "v.go"), "package v")
	// symlink loop
	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Skip("symlinks unavailable")
	}
	res, err := e.Do(ctx, root, LocalRequest{Action: "list"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Files, "\n")
	if strings.Contains(joined, "config") {
		t.Fatalf(".git contents listed: %s", joined)
	}
	if !strings.Contains(joined, "vendor/v.go") {
		t.Fatalf("vendor file missing from list: %s", joined)
	}
	if !strings.Contains(joined, "loop ->") {
		t.Fatalf("symlink dir not listed safely: %s", joined)
	}
}

func TestLocalNestedTargetScopesToExactRoot(t *testing.T) {
	// The allowlist contains the parent; a task bound to a nested project
	// must operate on the nested dir only — never on the parent or siblings.
	parent := t.TempDir()
	project := filepath.Join(parent, "project")
	sibling := filepath.Join(parent, "sibling")
	for _, d := range []string{project, sibling} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	localWrite(t, filepath.Join(sibling, "secret.txt"), "sibling data")
	localWrite(t, filepath.Join(parent, "top.txt"), "top data")
	allowedParent, err := NormalizeRoot(parent)
	if err != nil {
		t.Fatal(err)
	}
	e := NewLocalEngine(Roots{Enabled: true, CommandExecution: true, Dirs: []string{allowedParent}}, nil)
	ctx := context.Background()
	// The nested target is authorized and executes against the nested root.
	if _, err := e.Do(ctx, project, LocalRequest{Action: "list"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Do(ctx, project, LocalRequest{Action: "write", Path: "in-project.txt", Content: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project, "in-project.txt")); err != nil {
		t.Fatal("write did not land in the exact target root")
	}
	// The sibling is invisible and unmodifiable through the nested scope.
	if _, err := e.Do(ctx, project, LocalRequest{Action: "read", Path: "../sibling/secret.txt"}); err == nil {
		t.Fatal("sibling read through nested scope accepted")
	}
	if _, err := e.Do(ctx, project, LocalRequest{Action: "write", Path: "../top.txt", Content: "hijack"}); err == nil {
		t.Fatal("parent write through nested scope accepted")
	}
	if raw, err := os.ReadFile(filepath.Join(parent, "top.txt")); err != nil || string(raw) != "top data" {
		t.Fatalf("parent file was modified: %v %q", err, raw)
	}
}

func TestLocalProtectedDirectPathsAndAliases(t *testing.T) {
	root, roots := newTestRoots(t)
	e := NewLocalEngine(roots, nil)
	ctx := context.Background()
	// Direct access to protected names is refused for every action.
	for _, p := range []string{".env", "config/.env", "keys/id_rsa", "cert.pem", "master.key"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o750); err != nil {
			t.Fatal(err)
		}
		localWrite(t, filepath.Join(root, p), "secret")
		for _, action := range []string{"read", "write", "edit"} {
			req := LocalRequest{Action: action, Path: p, Content: "x", Old: "s", New: "y", Revision: "0"}
			if _, err := e.Do(ctx, root, req); err == nil {
				t.Fatalf("%s of protected path %q accepted", action, p)
			}
		}
	}
	// A symlink alias pointing at a protected file is refused too.
	localWrite(t, filepath.Join(root, "app.ini"), "real")
	if err := os.Symlink(filepath.Join(root, ".env"), filepath.Join(root, "alias.cfg")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Do(ctx, root, LocalRequest{Action: "read", Path: "alias.cfg"}); err == nil {
		t.Fatal("symlink alias to protected file accepted")
	}
	// Legitimate example config stays readable.
	localWrite(t, filepath.Join(root, ".env.example"), "PORT=8080")
	if _, err := e.Do(ctx, root, LocalRequest{Action: "read", Path: ".env.example"}); err != nil {
		t.Fatalf("legitimate example config refused: %v", err)
	}
}

func TestLocalWriteRequiresRevisionForExistingFile(t *testing.T) {
	root, roots := newTestRoots(t)
	e := NewLocalEngine(roots, nil)
	ctx := context.Background()
	localWrite(t, filepath.Join(root, "app.txt"), "v1")
	// Overwriting an existing file without revision is refused.
	if _, err := e.Do(ctx, root, LocalRequest{Action: "write", Path: "app.txt", Content: "v2"}); err == nil {
		t.Fatal("existing-file write without revision accepted")
	}
	if raw, _ := os.ReadFile(filepath.Join(root, "app.txt")); string(raw) != "v1" {
		t.Fatal("existing file was modified without revision")
	}
	// A wrong revision is refused.
	if _, err := e.Do(ctx, root, LocalRequest{Action: "write", Path: "app.txt", Content: "v2", Revision: strings.Repeat("a", 64)}); err == nil {
		t.Fatal("write with wrong revision accepted")
	}
	// The correct revision allows the save.
	res, err := e.Do(ctx, root, LocalRequest{Action: "read", Path: "app.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Do(ctx, root, LocalRequest{Action: "write", Path: "app.txt", Content: "v2", Revision: res.Revision}); err != nil {
		t.Fatalf("write with correct revision refused: %v", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(root, "app.txt")); string(raw) != "v2" {
		t.Fatal("revisioned save did not land")
	}
	// Concurrent revision failure: two readers, one save wins, the other is
	// refused.
	r1, _ := e.Do(ctx, root, LocalRequest{Action: "read", Path: "app.txt"})
	r2, _ := e.Do(ctx, root, LocalRequest{Action: "read", Path: "app.txt"})
	if _, err := e.Do(ctx, root, LocalRequest{Action: "write", Path: "app.txt", Content: "v3", Revision: r1.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Do(ctx, root, LocalRequest{Action: "write", Path: "app.txt", Content: "stale", Revision: r2.Revision}); err == nil {
		t.Fatal("stale revision accepted")
	}
}

func TestLocalSymlinkEscapeViaRootConfinedOps(t *testing.T) {
	// os.Root confinement: a symlink swapped in after an earlier check cannot
	// redirect an operation outside the tree, and pre-existing absolute
	// symlinks are refused instead of followed.
	root, roots := newTestRoots(t)
	e := NewLocalEngine(roots, nil)
	ctx := context.Background()
	outside := t.TempDir()
	localWrite(t, filepath.Join(outside, "target.txt"), "outside")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"escape/target.txt", "escape"} {
		if _, err := e.Do(ctx, root, LocalRequest{Action: "read", Path: p}); err == nil {
			t.Fatalf("read through escape symlink %q accepted", p)
		}
		if _, err := e.Do(ctx, root, LocalRequest{Action: "write", Path: p + "/x.txt", Content: "x"}); err == nil {
			t.Fatalf("write through escape symlink %q accepted", p)
		}
	}
	// Walking does not descend into symlinked dirs.
	if _, err := e.Do(ctx, root, LocalRequest{Action: "list"}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalStatusDiffWorksWithGit(t *testing.T) {
	root, roots := newTestRoots(t)
	var got []string
	e := NewLocalEngine(roots, func(ctx context.Context, dir, command string) (string, string, int, error) {
		got = append(got, command)
		if strings.HasPrefix(command, "git status") {
			return "## main\n M a.go\n", "", 0, nil
		}
		return " a.go | 2 +-\n", "", 0, nil
	})
	res, err := e.Do(context.Background(), root, LocalRequest{Action: "status"})
	if err != nil || !strings.Contains(res.Text, "M a.go") {
		t.Fatalf("git status: %v %+v", err, res)
	}
	res, err = e.Do(context.Background(), root, LocalRequest{Action: "diff"})
	if err != nil || !strings.Contains(res.Text, "a.go | 2") {
		t.Fatalf("git diff: %v %+v", err, res)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 git commands, got %d", len(got))
	}
}

func TestLocalBoundedOutputCollectsBoundedly(t *testing.T) {
	// A command that emits far more than the cap must be truncated during
	// collection: memory never balloons and the result says so.
	root, roots := newTestRoots(t)
	e := NewLocalEngine(roots, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := e.Do(ctx, root, LocalRequest{Action: "run", Command: "yes 0123456789 | head -c 5000000"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("unbounded output not reported truncated")
	}
	if len(res.Text) > MaxLocalOutput+2000 {
		t.Fatalf("result too large: %d", len(res.Text))
	}
}

func TestLocalRawRoundTrip(t *testing.T) {
	root, roots := newTestRoots(t)
	e := NewLocalEngine(roots, nil)
	// Create via raw save (new file, no revision needed).
	saved, err := e.RawSave(root, "src/main.go", "package main\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision == "" {
		t.Fatal("save returned no revision")
	}
	// Raw read returns the exact bytes and the same revision.
	got, err := e.RawRead(root, "src/main.go")
	if err != nil || got.Content != "package main\n" || got.Revision != saved.Revision {
		t.Fatalf("raw roundtrip: %v %+v", err, got)
	}
	// A stale revision (another read, then a save) is refused.
	first, _ := e.RawRead(root, "src/main.go")
	second, _ := e.RawRead(root, "src/main.go")
	if _, err := e.RawSave(root, "src/main.go", "package main // v2\n", second.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RawSave(root, "src/main.go", "stale save\n", first.Revision); err == nil {
		t.Fatal("stale revision accepted")
	}
	// The winner's content survived.
	got, _ = e.RawRead(root, "src/main.go")
	if got.Content != "package main // v2\n" {
		t.Fatalf("content: %q", got.Content)
	}
	// Large files are refused for raw editing rather than truncated.
	big := strings.Repeat("x", MaxLocalFile+1)
	localWrite(t, filepath.Join(root, "big.txt"), big)
	if _, err := e.RawRead(root, "big.txt"); err == nil {
		t.Fatal("oversized raw read accepted")
	}
	// Protected names are refused.
	localWrite(t, filepath.Join(root, ".env"), "SECRET=1")
	if _, err := e.RawRead(root, ".env"); err == nil {
		t.Fatal("raw read of .env accepted")
	}
}

func TestLocalSymlinkedRootOutsideAllowlistRefused(t *testing.T) {
	// A symlinked root is only usable when it RESOLVES into an allowed dir;
	// a symlink pointing outside the allowlist must be refused everywhere,
	// including the raw editor API.
	_, allowedRoots := newTestRoots(t)
	e := NewLocalEngine(allowedRoots, nil)
	outside := t.TempDir()
	localWrite(t, filepath.Join(outside, "outside.txt"), "outside data")
	inside := t.TempDir()
	allowedIn, err := NormalizeRoot(inside)
	if err != nil {
		t.Fatal(err)
	}
	_ = allowedIn
	link := filepath.Join(inside, "evil-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := e.Do(context.Background(), link, LocalRequest{Action: "list"}); err == nil {
		t.Fatal("list accepted a symlinked root outside the allowlist")
	}
	if _, err := e.RawRead(link, "outside.txt"); err == nil {
		t.Fatal("RawRead accepted a symlinked root outside the allowlist")
	}
	if _, err := e.RawSave(link, "new.txt", "x", ""); err == nil {
		t.Fatal("RawSave accepted a symlinked root outside the allowlist")
	}
}

func TestLocalSymlinkedParentAliasToProtectedDirRefused(t *testing.T) {
	// A symlinked parent component that aliases a protected directory must
	// not be traversed by read/write/edit/RawRead/RawSave/list.
	root, roots := newTestRoots(t)
	e := NewLocalEngine(roots, nil)
	ctx := context.Background()
	// .ssh is protected; a benign-looking alias to it must be refused.
	if err := os.Mkdir(filepath.Join(root, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	localWrite(t, filepath.Join(root, ".ssh", "id_rsa"), "PRIVATE KEY")
	if err := os.Symlink(filepath.Join(root, ".ssh"), filepath.Join(root, "cfg")); err != nil {
		t.Skip("symlinks unavailable")
	}
	localWrite(t, filepath.Join(root, "ok.txt"), "fine")
	for _, p := range []string{"cfg/id_rsa", "cfg"} {
		if _, err := e.Do(ctx, root, LocalRequest{Action: "read", Path: p}); err == nil {
			t.Fatalf("read via symlinked parent alias %q accepted", p)
		}
		if _, err := e.Do(ctx, root, LocalRequest{Action: "write", Path: p + "/x", Content: "x"}); err == nil {
			t.Fatalf("write via symlinked parent alias %q accepted", p)
		}
		if _, err := e.Do(ctx, root, LocalRequest{Action: "edit", Path: p + "/x", Revision: "0", Old: "a", New: "b"}); err == nil {
			t.Fatalf("edit via symlinked parent alias %q accepted", p)
		}
		if _, err := e.RawRead(root, p); err == nil {
			t.Fatalf("RawRead via symlinked parent alias %q accepted", p)
		}
		if _, err := e.RawSave(root, p+"/x", "x", ""); err == nil {
			t.Fatalf("RawSave via symlinked parent alias %q accepted", p)
		}
		if _, err := e.Do(ctx, root, LocalRequest{Action: "list", Path: p}); err == nil {
			t.Fatalf("list via symlinked parent alias %q accepted", p)
		}
	}
	// The protected content never leaked and nothing was written.
	if raw, err := os.ReadFile(filepath.Join(root, ".ssh", "id_rsa")); err != nil || string(raw) != "PRIVATE KEY" {
		t.Fatalf("protected file disturbed: %v %q", err, raw)
	}
	if _, err := os.Stat(filepath.Join(root, ".ssh", "x")); !os.IsNotExist(err) {
		t.Fatal("a file was written into the protected dir via an alias")
	}
	// A normal file is unaffected.
	if _, err := e.Do(ctx, root, LocalRequest{Action: "read", Path: "ok.txt"}); err != nil {
		t.Fatalf("ordinary read broke: %v", err)
	}
}

func TestLocalNoPredictableTempSentinelSurvivesSave(t *testing.T) {
	// The revisioned save must not use a predictable O_TRUNC temp name: the
	// classic sentinel `file.txt.onefrog-tmp` must neither be created nor
	// survive a save, and pre-planting it must not break or hijack the save.
	root, roots := newTestRoots(t)
	e := NewLocalEngine(roots, nil)
	ctx := context.Background()
	localWrite(t, filepath.Join(root, "app.txt"), "v1")
	res, err := e.Do(ctx, root, LocalRequest{Action: "read", Path: "app.txt"})
	if err != nil {
		t.Fatal(err)
	}
	// An attacker (or a stale leftover) pre-creates the predictable sentinel.
	sentinel := filepath.Join(root, "app.txt.onefrog-tmp")
	localWrite(t, sentinel, "PWNED")
	if _, err := e.Do(ctx, root, LocalRequest{Action: "write", Path: "app.txt", Content: "v2", Revision: res.Revision}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(root, "app.txt")); string(raw) != "v2" {
		t.Fatalf("revisioned save failed: %q", raw)
	}
	// The planted sentinel was never truncated, consumed or renamed into
	// place, and no predictable sentinel is left behind.
	if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "PWNED" {
		t.Fatalf("predictable sentinel was disturbed: %v %q", err, raw)
	}
	// Remove the planted file so the leftover scan below only sees files the
	// engine itself created.
	if err := os.Remove(sentinel); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "onefrog-tmp") {
			t.Fatalf("predictable temp file left behind: %s", entry.Name())
		}
	}
	// Same for edit.
	res, err = e.Do(ctx, root, LocalRequest{Action: "read", Path: "app.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Do(ctx, root, LocalRequest{Action: "edit", Path: "app.txt", Revision: res.Revision, Old: "v2", New: "v3"}); err != nil {
		t.Fatal(err)
	}
	entries, _ = os.ReadDir(root)
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "onefrog-tmp") {
			t.Fatalf("predictable temp file left behind after edit: %s", entry.Name())
		}
	}
}

func TestLocalEditPreservesOriginalPermissions(t *testing.T) {
	// Edit (and revisioned write) must keep the original file's permission
	// bits, since the replacement is created fresh and renamed over.
	root, roots := newTestRoots(t)
	e := NewLocalEngine(roots, nil)
	ctx := context.Background()
	f := filepath.Join(root, "script.sh")
	localWrite(t, f, "echo hi\n")
	if err := os.Chmod(f, 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := e.Do(ctx, root, LocalRequest{Action: "read", Path: "script.sh"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Do(ctx, root, LocalRequest{Action: "edit", Path: "script.sh", Revision: res.Revision, Old: "hi", New: "bye"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o755 {
		t.Fatalf("edit changed permissions: got %o want 755", got)
	}
	// A revisioned write on a 0640 file keeps 0640.
	f2 := filepath.Join(root, "doc.txt")
	localWrite(t, f2, "d1")
	if err := os.Chmod(f2, 0o640); err != nil {
		t.Fatal(err)
	}
	res, err = e.Do(ctx, root, LocalRequest{Action: "read", Path: "doc.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Do(ctx, root, LocalRequest{Action: "write", Path: "doc.txt", Content: "d2", Revision: res.Revision}); err != nil {
		t.Fatal(err)
	}
	st, err = os.Stat(f2)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o640 {
		t.Fatalf("write changed permissions: got %o want 640", got)
	}
}

func TestLocalEngineCopiesRootsSliceInConstructor(t *testing.T) {
	// Mutating the caller's Roots (or its backing array) after construction
	// must not widen or shift the engine's allowlist.
	root, roots := newTestRoots(t)
	dirs := []string{roots.Dirs[0]}
	e := NewLocalEngine(Roots{Enabled: true, CommandExecution: true, Dirs: dirs}, nil)
	ctx := context.Background()
	if _, err := e.Do(ctx, root, LocalRequest{Action: "list"}); err != nil {
		t.Fatal(err)
	}
	// Widen the caller's slice and swap in a directory the operator never
	// allowed: the engine's copy must be untouched.
	dirs[0] = "/etc"
	other := t.TempDir()
	dirs = append(dirs, other)
	if _, err := e.Do(ctx, other, LocalRequest{Action: "list"}); err == nil {
		t.Fatal("engine picked up a root appended to the caller's slice")
	}
	if _, err := e.Do(ctx, "/etc", LocalRequest{Action: "list"}); err == nil {
		t.Fatal("engine picked up a root substituted into the caller's slice")
	}
	// The engine's own reported roots still match the original config.
	got := e.Roots()
	if len(got.Dirs) != 1 || got.Dirs[0] != roots.Dirs[0] {
		t.Fatalf("engine roots mutated: %v", got.Dirs)
	}
}
