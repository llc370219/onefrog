package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"onefrog/internal/model"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

func TestCheckoutReadEditDiffAndRun(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("runner intentionally refuses root")
	}
	for _, name := range []string{"python3", "git"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " unavailable")
		}
	}
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"app.txt": "hello world\n", ".env": "SECRET=example-only", "build.sh": "grep -q 'hello frog' app.txt\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(root, "hardlink.txt")); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "app.txt", "build.sh")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "initial")
	// A checkout's own Git filter must not run during a read-only status/diff.
	git("config", "filter.sideeffect.clean", "touch ran-filter; cat")
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("app.txt filter=sideeffect\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	box, _ := secret.New(make([]byte, 32))
	key, _ := box.Encrypt("fixture")
	db := store.NewMemory()
	if _, err := db.CreateServer(ctx, store.ServerRec{Server: model.Server{ID: "host", Username: "dev"}, HostKey: "pinned", EncryptedKey: key}); err != nil {
		t.Fatal(err)
	}
	// Exercise the exact command and JSON stdin protocol used over SSH.
	svc := &Service{Store: db, Box: box, Run: func(ctx context.Context, host string, port int, user, auth, key, pin, command string, input []byte) (string, string, error) {
		if user != "dev" || key != "fixture" || pin != "pinned" {
			t.Fatal("wrong SSH identity")
		}
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
		cmd.Stdin = strings.NewReader(string(input))
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("runner stderr: %s", raw)
		}
		return string(raw), "", err
	}}
	target := Target{ServerID: "host", Root: root}
	call := func(req Request) Result {
		t.Helper()
		result, err := svc.Do(ctx, target, req)
		if err != nil {
			t.Fatalf("%s %s: %v", req.Action, req.Path, err)
		}
		return result
	}
	list := call(Request{Action: "list"})
	if !slices.Contains(list.Files, "app.txt") || slices.Contains(list.Files, ".env") {
		t.Fatal(list.Files)
	}
	for _, name := range []string{".env", "../outside.txt", "link.txt", "hardlink.txt", ".git/config"} {
		if _, err := svc.Do(ctx, target, Request{Action: "read", Path: name}); err == nil {
			t.Fatalf("unsafe read succeeded: %s", name)
		}
	}
	file := call(Request{Action: "read", Path: "app.txt", Lines: 10})
	if file.Text != "hello world\n" || file.Revision == "" {
		t.Fatal(file)
	}
	updated := call(Request{Action: "edit", Path: "app.txt", Revision: file.Revision, Old: "world", New: "frog"})
	if updated.Revision == file.Revision {
		t.Fatal("file version not changed")
	}
	if _, err := svc.Do(ctx, target, Request{Action: "write", Path: "app.txt", Revision: file.Revision, Content: "stale"}); err == nil {
		t.Fatal("stale edit overwrote a newer file")
	}
	if got := call(Request{Action: "diff"}).Text; !strings.Contains(got, "+hello frog") {
		t.Fatal(got)
	}
	if _, err := os.Stat(filepath.Join(root, "ran-filter")); !os.IsNotExist(err) {
		t.Fatalf("diff executed checkout Git filter: %v", err)
	}
	if run := call(Request{Action: "run", Command: "sh build.sh && printf verified"}); run.ExitCode == nil || *run.ExitCode != 0 || run.Text != "verified" {
		t.Fatalf("wrong test result: %+v", run)
	}
	if run := call(Request{Action: "run", Command: "printf failed; exit 12"}); run.ExitCode == nil || *run.ExitCode != 12 || run.Text != "failed" {
		t.Fatalf("nonzero command result lost: %+v", run)
	}
}
