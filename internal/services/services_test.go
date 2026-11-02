package services

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/remote"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

const fixture = `{"items":[{"kind":"systemd","name":"nginx.service","state":"active","detail":"running"},{"kind":"docker","name":"web","state":"running","detail":"Up 2 hours","image":"nginx:alpine","ports":"0.0.0.0:8080->80/tcp"},{"kind":"ports","name":"nginx","state":"listening","detail":"tcp","ports":"0.0.0.0:80"}],"sources":{"systemd":{"status":"ok"},"docker":{"status":"ok"},"ports":{"status":"ok"}}}`

type execFunc func(context.Context, string, int, string, string, string, string, string) (string, string, error)

func (f execFunc) Run(c context.Context, h string, p int, u, a, s, k, cmd string) (string, string, error) {
	return f(c, h, p, u, a, s, k, cmd)
}
func TestParseStates(t *testing.T) {
	for _, tt := range []struct {
		name, raw, status string
		bad               bool
	}{
		{"complete", fixture, "ok", false},
		{"partial", strings.Replace(fixture, `"docker":{"status":"ok"}`, `"docker":{"status":"unavailable"}`, 1), "partial", false},
		{"unavailable", `{"sources":{"systemd":{"status":"error"},"docker":{"status":"unavailable"},"ports":{"status":"error"}}}`, "failed", false},
		{"no fake success", `{}`, "", true},
		{"shell banner", "login banner\n" + fixture, "", true},
		{"malicious kind", strings.Replace(fixture, `"kind":"docker"`, `"kind":"command"`, 1), "", true},
		{"bad source", strings.Replace(fixture, `"status":"ok"`, `"status":"invented"`, 1), "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v, err := Parse(tt.raw)
			if (err != nil) != tt.bad || (!tt.bad && v.Status != tt.status) {
				t.Fatalf("status=%s err=%v", v.Status, err)
			}
		})
	}
}
func scannerForTest(t *testing.T, fn execFunc) (*Scanner, *store.Memory) {
	t.Helper()
	st := store.NewMemory()
	box, _ := secret.New(bytes.Repeat([]byte{7}, 32))
	cipher, _ := box.Encrypt("login-password")
	_, err := st.CreateServer(context.Background(), store.ServerRec{Server: model.Server{ID: "node", Name: "node", Host: "example.invalid", Username: "root"}, EncryptedKey: cipher, HostKey: "SHA256:confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	return New(st, box, fn, func(_ context.Context, s string) string { return strings.ReplaceAll(s, "login-password", "***") }), st
}
func TestScanGuardsCacheAndFailure(t *testing.T) {
	var calls int
	fail := false
	scanner, st := scannerForTest(t, func(_ context.Context, _ string, _ int, _, _, plain, pin, cmd string) (string, string, error) {
		calls++
		if plain != "login-password" || pin != "SHA256:confirmed" || cmd != Command {
			t.Fatal("credentials, pin or fixed command changed")
		}
		if fail {
			return "login-password", "", remote.ErrHostKeyMismatch
		}
		return strings.Replace(fixture, "nginx.service", "login-password.service", 1), "", nil
	})
	if v := scanner.Views([]string{"node"})[0]; v.Status != "pending" || v.CollectedAt != nil {
		t.Fatal("invented initial data")
	}
	ctx := context.Background()
	v, err := scanner.Scan(ctx, "node")
	if err != nil || v.Status != "ok" || v.CollectedAt == nil || v.Items[0].Name != "***.service" {
		t.Fatalf("invalid scan: %#v %v", v, err)
	}
	v.Items[0].Name = "mutated"
	v.Sources["systemd"] = Source{Status: "error"}
	if scanner.Views([]string{"node"})[0].Items[0].Name == "mutated" {
		t.Fatal("cache exposed mutable slice")
	}
	fail = true
	v, err = scanner.Scan(ctx, "node")
	if err != nil || v.Status != "failed" || v.CollectedAt == nil || len(v.Items) != 3 || !strings.Contains(v.Error, "指纹") {
		t.Fatal("failed scan discarded snapshot or claimed success")
	}
	rec, _ := st.GetServer(ctx, "node")
	rec.ClearHostKey = true
	rec.HostKey = ""
	st.UpdateServer(ctx, rec)
	scanner.Scan(ctx, "node")
	if calls != 2 {
		t.Fatal("unconfirmed SSH key reached executor")
	}
	st.DeleteServer(ctx, "node")
	_, err = scanner.Scan(ctx, "node")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatal("deleted server was scanned")
	}
	if len(scanner.Views(nil)) != 0 {
		t.Fatal("deleted nodes leaked into list")
	}
}
func TestConcurrentScansAreCoalesced(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	scanner, _ := scannerForTest(t, func(context.Context, string, int, string, string, string, string, string) (string, string, error) {
		calls.Add(1)
		close(started)
		<-release
		return fixture, "", nil
	})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); scanner.Scan(context.Background(), "node") }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := scanner.Scan(ctx, "node"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("waiting caller ignored cancellation")
	}
	go func() { defer wg.Done(); scanner.Scan(context.Background(), "node") }()
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("concurrent duplicate SSH scans")
	}
}
