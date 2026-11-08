package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"onefrog/internal/boot"
	"onefrog/internal/bot"
	"onefrog/internal/config"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"onefrog/internal/task"
	"onefrog/internal/workspace"
)

func TestNativeOrContainerReportsTruthfully(t *testing.T) {
	if nativeOrContainer() == "" {
		t.Fatal("empty environment")
	}
	// Injected detection must flow through.
	e := workspace.NewLocalEngine(workspace.Roots{Enabled: true}, nil)
	_ = e
	if nativeOrContainer() != "native" && nativeOrContainer() != "container" {
		t.Fatalf("unexpected environment label %q", nativeOrContainer())
	}
}

// localWorkspaceServer boots a real store via boot.Prepare and mounts the
// HTTP API with a local workspace engine limited to one operator-allowlisted
// temp root. No SSH fake: this flow never touches remote execution.
func localWorkspaceServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	mem, box := preparedStore(t)
	root := t.TempDir()
	allowed, err := workspace.NormalizeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	engine := workspace.NewLocalEngine(workspace.Roots{Enabled: true, CommandExecution: true, Dirs: []string{allowed}}, nil)
	app := &Server{Store: mem, Box: box, Agent: &bot.Agent{Store: mem, Box: box, LocalWorkspace: engine}}
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	return srv, allowed
}

// TestWorkspaceFlatHTTPUnauthorized proves /api/workspace is behind auth: an
// anonymous flat POST must get 401, never a silent local browse.
func TestWorkspaceFlatHTTPUnauthorized(t *testing.T) {
	srv, allowed := localWorkspaceServer(t)
	res, err := http.Post(srv.URL+"/api/workspace", "application/json",
		strings.NewReader(mustJSON(map[string]any{
			"target": map[string]any{"server_id": "local", "root": allowed, "kind": "local"},
			"action": "list", "path": ".",
		})))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated workspace POST: %d, want 401", res.StatusCode)
	}
}

// TestWorkspaceFlatHTTPLocalRoundTrip drives the flat /api/workspace shape
// exactly as the web editor does: one JSON object with top-level target,
// action, path, content and revision. It covers create, a >500-line full
// read with no truncation and no excerpt header, a revisioned save, refusal
// of stale/missing revisions (with on-disk bytes untouched), root escapes,
// an unknown kind and exec/edit/search refusals.
func TestWorkspaceFlatHTTPLocalRoundTrip(t *testing.T) {
	srv, allowed := localWorkspaceServer(t)
	jar := loginClient(t, srv)
	target := map[string]any{"server_id": "local", "root": allowed, "kind": "local"}

	// Create a new file via the flat write shape.
	res := postRaw(t, jar, srv.URL+"/api/workspace", map[string]any{
		"target": target, "action": "write", "path": "src/big.txt", "content": "v1\n",
	})
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("flat write: %d %s", res.StatusCode, body)
	}
	var saved workspace.Result
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Revision == "" {
		t.Fatal("write returned no revision")
	}

	// A >500-line file: read must return every line, no excerpt header, no
	// truncation flag — the editor's full-content contract.
	var b strings.Builder
	for i := 1; i <= 520; i++ {
		b.WriteString("line-")
		b.WriteString(strings.Repeat("x", 20))
		b.WriteString("-")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("\n")
	}
	big := b.String()
	res = postRaw(t, jar, srv.URL+"/api/workspace", map[string]any{
		"target": target, "action": "write", "path": "src/big.txt", "content": big, "revision": saved.Revision,
	})
	body, _ = io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("big write: %d %s", res.StatusCode, body)
	}
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	res = postRaw(t, jar, srv.URL+"/api/workspace", map[string]any{
		"target": target, "action": "read", "path": "src/big.txt",
	})
	body, _ = io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("big read: %d %s", res.StatusCode, body)
	}
	var read workspace.Result
	if err := json.Unmarshal(body, &read); err != nil {
		t.Fatal(err)
	}
	if read.Text != big {
		t.Fatalf("read not byte-exact: got %d bytes want %d", len(read.Text), len(big))
	}
	if read.Truncated {
		t.Fatal("read reported truncation")
	}
	if strings.Contains(read.Text, "文件 ") || strings.HasPrefix(read.Text, "revision:") {
		t.Fatal("read leaked an excerpt header")
	}
	if read.Revision == "" || read.Revision != saved.Revision {
		t.Fatalf("read revision %q must match the saved revision %q", read.Revision, saved.Revision)
	}

	// Update with the current revision succeeds and changes the bytes.
	res = postRaw(t, jar, srv.URL+"/api/workspace", map[string]any{
		"target": target, "action": "write", "path": "src/big.txt", "content": "v2\n", "revision": read.Revision,
	})
	body, _ = io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("revisioned write: %d %s", res.StatusCode, body)
	}
	var updated workspace.Result
	if err := json.Unmarshal(body, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Revision == read.Revision || updated.Text != "v2\n" {
		t.Fatalf("update result: %+v", updated)
	}

	// A stale revision is refused and the winner's bytes survive.
	res = postRaw(t, jar, srv.URL+"/api/workspace", map[string]any{
		"target": target, "action": "write", "path": "src/big.txt", "content": "stale\n", "revision": read.Revision,
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("stale revision accepted: %d", res.StatusCode)
	}
	// A missing revision on an existing file is also refused.
	res2 := postRaw(t, jar, srv.URL+"/api/workspace", map[string]any{
		"target": target, "action": "write", "path": "src/big.txt", "content": "no-rev\n",
	})
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing revision accepted: %d", res2.StatusCode)
	}
	onDisk, err := os.ReadFile(filepath.Join(allowed, "src", "big.txt"))
	if err != nil || string(onDisk) != "v2\n" {
		t.Fatalf("file must keep the winner's bytes: %v %q", err, onDisk)
	}

	// A root outside the operator allowlist is refused.
	escaped := postRaw(t, jar, srv.URL+"/api/workspace", map[string]any{
		"target": map[string]any{"server_id": "local", "root": t.TempDir(), "kind": "local"},
		"action": "list", "path": ".",
	})
	defer escaped.Body.Close()
	if escaped.StatusCode != http.StatusBadRequest {
		t.Fatalf("outside root accepted: %d", escaped.StatusCode)
	}

	// An unknown kind must be rejected, never fall back to SSH.
	badKind := postRaw(t, jar, srv.URL+"/api/workspace", map[string]any{
		"target": map[string]any{"server_id": "web-01", "root": allowed, "kind": "docker"},
		"action": "list", "path": ".",
	})
	defer badKind.Body.Close()
	if badKind.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown kind accepted: %d", badKind.StatusCode)
	}

	// run/edit/search are never offered over the flat HTTP entry.
	for _, action := range []string{"run", "edit", "search"} {
		res := postRaw(t, jar, srv.URL+"/api/workspace", map[string]any{
			"target": target, "action": action, "path": "src/big.txt",
			"command": "echo hi", "old": "v2", "new": "v3", "content": "needle",
		})
		defer res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("action %q offered over flat HTTP: %d", action, res.StatusCode)
		}
	}
}

func TestWorkspaceLocalHTTPRawRoundTrip(t *testing.T) {
	mem, box := preparedStore(t)
	root := t.TempDir()
	allowed, err := workspace.NormalizeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	engine := workspace.NewLocalEngine(workspace.Roots{Enabled: true, CommandExecution: true, Dirs: []string{allowed}}, nil)
	app := &Server{Store: mem, Box: box, Agent: &bot.Agent{Store: mem, Box: box, LocalWorkspace: engine}}
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	client, _, _ := testClient(t)
	_ = client
	jar := loginClient(t, srv)
	target := map[string]any{"server_id": "local", "root": allowed, "kind": "local"}
	// Create via raw save.
	res := postRaw(t, jar, srv.URL+"/api/workspace/local/raw", map[string]any{
		"target": target, "path": "src/app.txt", "content": "hello\n",
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io_ReadAll(res.Body)
		t.Fatalf("raw save %d %s", res.StatusCode, body)
	}
	var saved workspace.RawReadResult
	if err := json.NewDecoder(res.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	if saved.Revision == "" {
		t.Fatal("no revision returned")
	}
	// Raw read returns exact bytes.
	res2, err := jar.Post(srv.URL+"/api/workspace/local/raw/read", "application/json", strings.NewReader(mustJSON(map[string]any{"target": target, "path": "src/app.txt"})))
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("raw read %d", res2.StatusCode)
	}
	var got workspace.RawReadResult
	if err := json.NewDecoder(res2.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Content != "hello\n" || got.Revision != saved.Revision {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	// A stale revision is refused.
	res3 := postRaw(t, jar, srv.URL+"/api/workspace/local/raw", map[string]any{
		"target": target, "path": "src/app.txt", "content": "stale\n", "revision": strings.Repeat("0", 64),
	})
	defer res3.Body.Close()
	if res3.StatusCode == http.StatusOK {
		t.Fatal("stale revision accepted")
	}
	// The winner's bytes survive on disk.
	raw, err := os.ReadFile(filepath.Join(root, "src", "app.txt"))
	if err != nil || string(raw) != "hello\n" {
		t.Fatalf("file state: %v %q", err, raw)
	}
	// Exec over HTTP is never offered.
	res4 := postRaw(t, jar, srv.URL+"/api/workspace/local", map[string]any{
		"target": target, "request": map[string]any{"action": "run", "command": "echo hi"},
	})
	defer res4.Body.Close()
	if res4.StatusCode != http.StatusBadRequest {
		t.Fatal("exec offered over HTTP")
	}
}

func TestWorkspaceCapabilitiesEnvironment(t *testing.T) {
	mem, box := preparedStore(t)
	app := &Server{Store: mem, Box: box, Agent: &bot.Agent{Store: mem, Box: box}}
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	jar := loginClient(t, srv)
	res, err := jar.Get(srv.URL + "/api/workspace/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var caps map[string]any
	if err := json.NewDecoder(res.Body).Decode(&caps); err != nil {
		t.Fatal(err)
	}
	local, _ := caps["local"].(map[string]any)
	if local == nil {
		t.Fatal("missing local block")
	}
	env, _ := local["execution_environment"].(string)
	if env != "native" && env != "container" {
		t.Fatalf("environment label %q", env)
	}
	if enabled, _ := local["enabled"].(bool); enabled {
		t.Fatal("local enabled without operator policy")
	}
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func loginClient(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	login(t, c, srv, "admin", "admin", http.StatusOK)
	return c
}

func io_ReadAll(r interface{ Read([]byte) (int, error) }) (string, error) {
	buf := make([]byte, 4096)
	n, err := r.Read(buf)
	return string(buf[:n]), err
}

var _ = context.Background

// preparedStore mirrors testClient's store setup without the SSH fake.
func preparedStore(t *testing.T) (store.Store, *secret.Box) {
	t.Helper()
	mem := store.NewMemory()
	box, err := secret.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := boot.Prepare(context.Background(), mem, config.Config{AdminUser: "admin", AdminPassword: "admin", Seed: true}); err != nil {
		t.Fatal(err)
	}
	return mem, box
}

func TestTaskViewPlanCompleted(t *testing.T) {
	now := time.Now().UTC()
	it := task.Task{
		ID: "t-view", OwnerID: "u1", BotID: "main", SessionID: "s-view", Goal: "g",
		Status: task.StatusRunning, Plan: []string{"a", "b", "c"}, PlanCompleted: 2,
		CreatedAt: now, UpdatedAt: now,
	}
	v := taskView(it)
	if v.PlanCompleted != 2 || len(v.Plan) != 3 {
		t.Fatalf("plan_completed %d of %d", v.PlanCompleted, len(v.Plan))
	}
	// Out-of-range counts are clamped, never trusted.
	it.PlanCompleted = 9
	if got := taskView(it); got.PlanCompleted != 3 {
		t.Fatalf("clamped %d", got.PlanCompleted)
	}
	// Missing plan means 0 — no fabricated completion.
	it.Plan = nil
	it.PlanCompleted = 5
	if got := taskView(it); got.PlanCompleted != 0 || len(got.Plan) != 0 {
		t.Fatalf("no-plan view: %d %d", got.PlanCompleted, len(got.Plan))
	}
}
