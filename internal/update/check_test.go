package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeGitHub(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/repos/llc370219/onefrog/releases/latest" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "OneFrog/") {
			t.Errorf("missing User-Agent")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

const releaseJSON = `{"tag_name":"v0.2.0","html_url":"https://github.com/llc370219/onefrog/releases/tag/v0.2.0","body":"notes","published_at":"2026-10-26T01:00:00Z"}`

func TestCheckFindsNewerRelease(t *testing.T) {
	srv, hits := fakeGitHub(t, 200, releaseJSON)
	c := &Checker{Current: "v0.1.1", API: srv.URL}
	s, err := c.CheckNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !s.Available || s.Latest != "v0.2.0" || s.Notes != "notes" || s.URL != "https://github.com/llc370219/onefrog/releases/tag/v0.2.0" || s.CheckedAt == nil || !s.Enabled {
		t.Fatalf("unexpected status %+v", s)
	}
	// Status is served from the cache.
	_ = c.Status()
	if hits.Load() != 1 {
		t.Fatalf("expected one request, got %d", hits.Load())
	}
}

func TestCheckSameVersionNotAvailable(t *testing.T) {
	srv, _ := fakeGitHub(t, 200, `{"tag_name":"v0.1.1","html_url":"https://github.com/llc370219/onefrog/releases/tag/v0.1.1"}`)
	c := &Checker{Current: "v0.1.1", API: srv.URL}
	s, err := c.CheckNow(context.Background())
	if err != nil || s.Available || s.Latest != "v0.1.1" {
		t.Fatalf("status %+v err %v", s, err)
	}
}

func TestDisabledChecksMakeNoRequests(t *testing.T) {
	srv, hits := fakeGitHub(t, 200, releaseJSON)
	for name, c := range map[string]*Checker{
		"env":      {Current: "v0.1.1", API: srv.URL, EnvDisabled: true},
		"settings": {Current: "v0.1.1", API: srv.URL, SettingsDisabled: func() bool { return true }},
		"dev":      {Current: "dev", API: srv.URL},
		"repo":     {Current: "v0.1.1", API: srv.URL, Repo: "not a repo"},
	} {
		s, err := c.CheckNow(context.Background())
		if err != nil || s.Enabled || s.Available || s.DisabledBy != name {
			t.Errorf("%s: status %+v err %v", name, s, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("disabled checker made %d requests", hits.Load())
	}
}

func TestDisablingHidesCachedUpdate(t *testing.T) {
	srv, _ := fakeGitHub(t, 200, releaseJSON)
	off := false
	c := &Checker{Current: "v0.1.1", API: srv.URL, SettingsDisabled: func() bool { return off }}
	if s, _ := c.CheckNow(context.Background()); !s.Available {
		t.Fatal("expected update")
	}
	off = true
	if s := c.Status(); s.Available || s.Enabled {
		t.Fatalf("disabled checker still reports update: %+v", s)
	}
}

func TestManualCooldown(t *testing.T) {
	srv, hits := fakeGitHub(t, 200, releaseJSON)
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	c := &Checker{Current: "v0.1.1", API: srv.URL, Now: func() time.Time { return now }}
	if _, err := c.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CheckNow(context.Background()); !errors.Is(err, ErrTooSoon) {
		t.Fatalf("expected ErrTooSoon, got %v", err)
	}
	now = now.Add(ManualCooldown + time.Second)
	if _, err := c.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("expected 2 requests, got %d", hits.Load())
	}
}

func TestCheckErrorsKeepPreviousResult(t *testing.T) {
	body := releaseJSON
	code := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	now := time.Now()
	c := &Checker{Current: "v0.1.1", API: srv.URL, Now: func() time.Time { return now }}
	if _, err := c.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		code int
		body string
	}{
		{500, ""},
		{404, `{"message":"Not Found"}`},
		{200, `{"tag_name":"latest"}`},
		{200, `{"tag_name":"v9.0.0","prerelease":true}`},
		{200, `not json`},
	} {
		code, body = tc.code, tc.body
		now = now.Add(time.Minute)
		s, err := c.CheckNow(context.Background())
		if err == nil || s.Error == "" {
			t.Errorf("%d %q: expected error", tc.code, tc.body)
		}
		if s.Latest != "v0.2.0" || !s.Available {
			t.Errorf("%d %q: lost cached release: %+v", tc.code, tc.body, s)
		}
	}
}

func TestReleaseURLAndNotesAreSanitized(t *testing.T) {
	long := strings.Repeat("新", maxNotes)
	srv, _ := fakeGitHub(t, 200, `{"tag_name":"v0.2.0","html_url":"https://evil.example/x","body":"`+long+`"}`)
	c := &Checker{Current: "v0.1.1", API: srv.URL}
	s, err := c.CheckNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.URL != "https://github.com/llc370219/onefrog/releases/tag/v0.2.0" {
		t.Fatalf("url not pinned to repo: %s", s.URL)
	}
	if len(s.Notes) > maxNotes+8 || !strings.HasSuffix(s.Notes, "…") || !strings.HasPrefix(s.Notes, "新") {
		t.Fatalf("notes not truncated cleanly: %d bytes", len(s.Notes))
	}
	for _, r := range strings.TrimSuffix(s.Notes, "\n…") {
		if r == '\uFFFD' {
			t.Fatal("notes cut inside a UTF-8 sequence")
		}
	}
}

func TestRunChecksAndStops(t *testing.T) {
	srv, hits := fakeGitHub(t, 200, releaseJSON)
	c := &Checker{Current: "v0.1.1", API: srv.URL, Interval: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	cancel()
	<-done
	if hits.Load() != 0 {
		t.Fatalf("cancelled before first check, got %d requests", hits.Load())
	}
}

func cacheRelease(c *Checker, tag string) {
	c.mu.Lock()
	c.latest = &release{Tag: tag, URL: "https://github.com/llc370219/onefrog/releases/tag/" + tag}
	c.mu.Unlock()
}

func TestApplyWritesCachedTag(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OF_UPGRADE_DIR", dir)
	c := &Checker{Current: "v0.1.7"}
	cacheRelease(c, "v0.1.8")
	if s := c.Status(); !s.Available || !s.CanApply || s.Latest != "v0.1.8" {
		t.Fatalf("status %+v", s)
	}
	got, err := c.Apply()
	if err != nil || got != "v0.1.8" {
		t.Fatalf("apply %q %v", got, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "upgrade"))
	if err != nil || string(body) != "v0.1.8\n" {
		t.Fatalf("trigger %q err %v", body, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "upgrade" {
		t.Fatalf("dir %+v err %v", entries, err)
	}
}

func TestApplyRefusesBetaWhenHelperCannotSwitch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OF_UPGRADE_DIR", dir)
	c := &Checker{Current: "v0.1.31", Channel: ChannelStable}
	cacheRelease(c, "v0.1.32-beta.1")
	if s := c.Status(); !s.HelperStale {
		t.Fatalf("expected stale helper, %+v", s)
	}
	if _, err := c.Apply(); !errors.Is(err, ErrHelperStale) {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "upgrade")); !os.IsNotExist(err) {
		t.Fatal("stale helper still wrote a trigger")
	}
	if err := os.WriteFile(filepath.Join(dir, "helper-version"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := c.Apply()
	if err != nil || got != "v0.1.32-beta.1" {
		t.Fatalf("apply %q %v", got, err)
	}
}

func TestApplyRefusesMissingDir(t *testing.T) {
	t.Setenv("OF_UPGRADE_DIR", filepath.Join(t.TempDir(), "missing"))
	c := &Checker{Current: "v0.1.7"}
	cacheRelease(c, "v0.1.8")
	if s := c.Status(); s.CanApply {
		t.Fatal("missing dir reported can_apply")
	}
	if _, err := c.Apply(); !errors.Is(err, ErrUpgradeUnavailable) {
		t.Fatal(err)
	}
}

func TestApplyRefusesWhenNotNewer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OF_UPGRADE_DIR", dir)
	c := &Checker{Current: "v0.1.8"}
	cacheRelease(c, "v0.1.8")
	if _, err := c.Apply(); !errors.Is(err, ErrNoUpdate) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "upgrade")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestApplyRefusesUnsafeOrDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OF_UPGRADE_DIR", dir)
	unsafe := &Checker{Current: "v0.1.0"}
	cacheRelease(unsafe, "v0.1.8-rc.1")
	if s := unsafe.Status(); !s.Available {
		t.Fatalf("expected a higher core version to count as available: %+v", s)
	}
	if _, err := unsafe.Apply(); !errors.Is(err, ErrNoUpdate) {
		t.Fatal(err)
	}
	disabled := &Checker{Current: "v0.1.7", EnvDisabled: true}
	cacheRelease(disabled, "v0.1.8")
	if _, err := disabled.Apply(); !errors.Is(err, ErrNoUpdate) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "upgrade")); !os.IsNotExist(err) {
		t.Fatal("wrote a trigger", err)
	}
}

func TestApplyRejectsUnsafeUpgradeDir(t *testing.T) {
	for _, dir := range []string{"relative/dir", "/tmp/onefrog/../escape"} {
		t.Setenv("OF_UPGRADE_DIR", dir)
		c := &Checker{Current: "v0.1.7"}
		cacheRelease(c, "v0.1.8")
		if s := c.Status(); s.CanApply {
			t.Fatalf("%s reported can_apply", dir)
		}
		if _, err := c.Apply(); !errors.Is(err, ErrUpgradeUnavailable) {
			t.Fatalf("%s: %v", dir, err)
		}
	}
}
