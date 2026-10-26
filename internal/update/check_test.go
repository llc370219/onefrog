package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
