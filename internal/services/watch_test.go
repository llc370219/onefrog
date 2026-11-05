package services

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onefrog/internal/secret"
	"onefrog/internal/store"
)

func parsedFixture(t *testing.T, raw string) View {
	t.Helper()
	v, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	v.CollectedAt = &now
	return v
}

func TestEvaluateStates(t *testing.T) {
	ok := parsedFixture(t, fixture)
	stopped := parsedFixture(t, strings.Replace(fixture, `"state":"active"`, `"state":"failed"`, 1))
	sick := parsedFixture(t, strings.Replace(fixture, "Up 2 hours", "Up 2 hours (unhealthy)", 1))
	noDocker := parsedFixture(t, strings.Replace(fixture, `"docker":{"status":"ok"}`, `"docker":{"status":"unavailable"}`, 1))
	cut := parsedFixture(t, strings.Replace(fixture, `"systemd":{"status":"ok"}`, `"systemd":{"status":"ok","truncated":true}`, 1))
	failed := ok
	failed.Status, failed.Error = "failed", "SSH 检测失败"
	for _, tt := range []struct {
		name  string
		view  View
		tgt   Target
		state string
	}{
		{"systemd ok", ok, Target{"systemd", "nginx.service"}, StateOK},
		{"docker ok", ok, Target{"docker", "web"}, StateOK},
		{"port by address", ok, Target{"ports", "0.0.0.0:80"}, StateOK},
		{"port name is not address", ok, Target{"ports", "nginx"}, StateMissing},
		{"stopped", stopped, Target{"systemd", "nginx.service"}, StateFault},
		{"unhealthy container", sick, Target{"docker", "web"}, StateFault},
		{"missing", ok, Target{"systemd", "redis.service"}, StateMissing},
		{"source unavailable", noDocker, Target{"docker", "web"}, StateUnknown},
		{"scan failed", failed, Target{"docker", "web"}, StateUnknown},
		{"truncated list", cut, Target{"systemd", "redis.service"}, StateUnknown},
		{"never collected", empty("node"), Target{"docker", "web"}, StateUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got, detail := Evaluate(tt.view, tt.tgt); got != tt.state {
				t.Fatalf("state=%s detail=%s", got, detail)
			}
		})
	}
}

func TestNormalizeValidates(t *testing.T) {
	cfg, err := normalize(WatchConfig{Servers: map[string][]Target{"node": {{" docker ", " web "}, {"docker", "web"}, {"ports", "0.0.0.0:80"}}, "empty": {}}})
	if err != nil || cfg.IntervalSeconds != DefaultInterval || len(cfg.Servers["node"]) != 2 || cfg.Servers["node"][0] != (Target{"docker", "web"}) {
		t.Fatalf("cfg=%#v err=%v", cfg, err)
	}
	if _, ok := cfg.Servers["empty"]; ok {
		t.Fatal("empty selection kept")
	}
	many := make([]Target, maxTargets+1)
	for i := range many {
		many[i] = Target{"systemd", strings.Repeat("a", i+1)}
	}
	for _, bad := range []WatchConfig{
		{IntervalSeconds: 5},
		{IntervalSeconds: MaxInterval + 1},
		{Servers: map[string][]Target{"node": {{"process", "x"}}}},
		{Servers: map[string][]Target{"node": {{"docker", ""}}}},
		{Servers: map[string][]Target{"node": {{"docker", "a\nb"}}}},
		{Servers: map[string][]Target{" ": {{"docker", "web"}}}},
		{Servers: map[string][]Target{"node": many}},
	} {
		if _, err := normalize(bad); err == nil {
			t.Fatalf("accepted %#v", bad)
		}
	}
}

func TestPollRecordsChangesOnce(t *testing.T) {
	raw := fixture
	fail := false
	scanner, st := scannerForTest(t, func(context.Context, string, int, string, string, string, string, string) (string, string, error) {
		if fail {
			return "", "", errors.New("down")
		}
		return raw, "", nil
	})
	ctx := context.Background()
	w, _ := NewWatch("", nil, st)
	w.Attach(scanner)
	if err := w.Set(ctx, WatchConfig{Servers: map[string][]Target{"node": {{"systemd", "nginx.service"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, status, _ := w.Snapshot(ctx); len(status) != 1 || status[0].State != StatePending {
		t.Fatalf("not pending: %#v", status)
	}
	if err := w.Set(ctx, WatchConfig{Servers: map[string][]Target{"ghost": {{"docker", "web"}}}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown server accepted: %v", err)
	}
	state := func() WatchStatus {
		_, status, err := w.Snapshot(ctx)
		if err != nil || len(status) != 1 {
			t.Fatalf("status=%#v err=%v", status, err)
		}
		return status[0]
	}
	history := func() []string {
		list, _ := st.ListHistory(ctx, 50)
		out := []string{}
		for _, h := range list {
			if strings.HasPrefix(h.Command, "service-watch") {
				out = append(out, h.Status)
			}
		}
		return out
	}
	w.Poll(ctx)
	if s := state(); s.State != StateOK || s.CheckedAt == nil || s.ChangedAt == nil || len(history()) != 0 {
		t.Fatalf("first ok: %#v %v", s, history())
	}
	raw = strings.Replace(fixture, `"state":"active"`, `"state":"failed"`, 1)
	w.Poll(ctx)
	w.Poll(ctx)
	fail = true
	w.Poll(ctx)
	if s := state(); s.State != StateUnknown {
		t.Fatalf("ssh failure not unknown: %#v", s)
	}
	fail = false
	w.Poll(ctx)
	if got := history(); len(got) != 1 || got[0] != "failed" {
		t.Fatalf("fault alerted more than once: %v", got)
	}
	raw = fixture
	w.Poll(ctx)
	if s := state(); s.State != StateOK {
		t.Fatalf("not recovered: %#v", s)
	}
	if got := history(); len(got) != 2 {
		t.Fatalf("recovery not recorded: %v", got)
	}
	if err := st.DeleteServer(ctx, "node"); err != nil {
		t.Fatal(err)
	}
	if cfg, status, _ := w.Snapshot(ctx); len(cfg.Servers) != 0 || len(status) != 0 {
		t.Fatal("deleted server still shown")
	}
	w.Poll(ctx)
	w.mu.Lock()
	left := len(w.cfg.Servers) + len(w.entries)
	w.mu.Unlock()
	if left != 0 {
		t.Fatal("deleted server not cleaned")
	}
}

func TestWatchPersistsEncrypted(t *testing.T) {
	scanner, st := scannerForTest(t, func(context.Context, string, int, string, string, string, string, string) (string, string, error) {
		return fixture, "", nil
	})
	box, _ := secret.New(bytes.Repeat([]byte{7}, 32))
	path := filepath.Join(t.TempDir(), "servicewatch.enc")
	ctx := context.Background()
	w, err := NewWatch(path, box, st)
	if err != nil {
		t.Fatal(err)
	}
	w.Attach(scanner)
	if err = w.Set(ctx, WatchConfig{IntervalSeconds: 120, Servers: map[string][]Target{"node": {{"docker", "web"}}}}); err != nil {
		t.Fatal(err)
	}
	w.Poll(ctx)
	again, err := NewWatch(path, box, st)
	if err != nil {
		t.Fatal(err)
	}
	cfg, status, _ := again.Snapshot(ctx)
	if cfg.IntervalSeconds != 120 || len(status) != 1 || status[0].State != StateOK || status[0].CheckedAt == nil {
		t.Fatalf("cfg=%#v status=%#v", cfg, status)
	}
	other, _ := secret.New(bytes.Repeat([]byte{8}, 32))
	if _, err = NewWatch(path, other, st); err == nil {
		t.Fatal("wrong key decrypted watch file")
	}
	if err = again.Remove("node"); err != nil {
		t.Fatal(err)
	}
	third, _ := NewWatch(path, box, st)
	if cfg, _, _ := third.Snapshot(ctx); len(cfg.Servers) != 0 {
		t.Fatal("remove not persisted")
	}
}
