package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"onefrog/internal/boot"
	"onefrog/internal/bot"
	"onefrog/internal/config"
	"onefrog/internal/preferences"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"onefrog/internal/update"
)

func TestUpdateStatusAndToggle(t *testing.T) {
	var hits int
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if strings.Contains(r.URL.RawQuery, "per_page") {
			_, _ = io.WriteString(w, "[{\"tag_name\":\"v0.1.31-beta.1\",\"prerelease\":true,\"html_url\":\"https://github.com/llc370219/onefrog/releases/tag/v0.1.31-beta.1\"}]")
			return
		}
		_, _ = io.WriteString(w, "{\"tag_name\":\"v0.1.1\",\"html_url\":\"https://github.com/llc370219/onefrog/releases/tag/v0.1.1\",\"body\":\"notes\"}")
	}))
	defer gh.Close()
	mem := store.NewMemory()
	box, _ := secret.New(bytes.Repeat([]byte{7}, 32))
	if err := boot.Prepare(context.Background(), mem, config.Config{AdminUser: "admin", AdminPassword: "admin", Seed: true}); err != nil {
		t.Fatal(err)
	}
	settings, _ := preferences.New("", box)
	web := t.TempDir()
	_ = os.WriteFile(filepath.Join(web, "index.html"), []byte("<html>index</html>"), 0o600)
	_ = os.WriteFile(filepath.Join(web, "favicon.ico"), []byte("ICO"), 0o600)
	_ = os.WriteFile(filepath.Join(web, "manifest.webmanifest"), []byte(`{"name":"OneFrog"}`), 0o600)
	checker := &update.Checker{Current: "v0.1.1", API: gh.URL, SettingsDisabled: func() bool { return settings.Snapshot().UpdateCheckDisabled }, SettingsChannel: func() string { return settings.Snapshot().UpdateChannel }, SettingsAutoUpdate: func() bool { return settings.Snapshot().AutoUpdate }}
	app := &Server{Store: mem, Box: box, Settings: settings, Updates: checker, WebDir: web, Agent: &bot.Agent{Store: mem, Box: box, SSH: &fakeSSH{}}}
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	if res, err := client.Get(srv.URL + "/api/update"); err != nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("update status must require login: %v %v", res, err)
	}
	login(t, client, srv, "admin", "admin", 200)
	var s update.Status
	getJSON(t, client, srv.URL+"/api/update", &s)
	if s.Current != "v0.1.1" || !s.Enabled || s.Available || s.CheckedAt != nil {
		t.Fatalf("initial status %+v", s)
	}
	postJSON(t, client, srv.URL+"/api/update/check", map[string]any{}, 200, &s)
	if s.Latest != "v0.1.1" || s.Available || s.URL == "" || hits != 1 {
		t.Fatalf("after check %+v hits %d", s, hits)
	}
	postJSON(t, client, srv.URL+"/api/update/check", map[string]any{}, 429, nil)

	putJSON(t, client, srv.URL+"/api/settings/update", map[string]any{}, 400, nil)
	putJSON(t, client, srv.URL+"/api/settings/update", map[string]bool{"enabled": false}, 200, &s)
	if s.Enabled || s.DisabledBy != "settings" || !settings.Snapshot().UpdateCheckDisabled {
		t.Fatalf("toggle off %+v", s)
	}
	// Saving the main settings form keeps the toggle.
	var state preferences.State
	getJSON(t, client, srv.URL+"/api/settings", &state)
	putJSON(t, client, srv.URL+"/api/settings", state, 200, nil)
	if !settings.Snapshot().UpdateCheckDisabled {
		t.Fatal("saving settings reset the update toggle")
	}
	putJSON(t, client, srv.URL+"/api/settings/update", map[string]bool{"enabled": true}, 200, &s)
	if !s.Enabled {
		t.Fatalf("toggle on %+v", s)
	}
	putJSON(t, client, srv.URL+"/api/settings/update", map[string]string{"channel": "nope"}, 400, nil)
	putJSON(t, client, srv.URL+"/api/settings/update", map[string]string{"channel": "beta"}, 200, &s)
	if s.Channel != "beta" || s.Latest != "v0.1.31-beta.1" || !s.Available || settings.Snapshot().UpdateChannel != "beta" {
		t.Fatalf("beta channel %+v", s)
	}
	putJSON(t, client, srv.URL+"/api/settings/update", map[string]bool{"auto_update": true}, 200, &s)
	if !s.AutoUpdate || !settings.Snapshot().AutoUpdate {
		t.Fatalf("auto update toggle on %+v", s)
	}
	getJSON(t, client, srv.URL+"/api/settings", &state)
	putJSON(t, client, srv.URL+"/api/settings", state, 200, nil)
	if !settings.Snapshot().AutoUpdate {
		t.Fatal("saving settings reset auto update")
	}
	putJSON(t, client, srv.URL+"/api/settings/update", map[string]bool{"auto_update": false}, 200, &s)
	if s.AutoUpdate || settings.Snapshot().AutoUpdate {
		t.Fatalf("auto update toggle off %+v", s)
	}
	getJSON(t, client, srv.URL+"/api/settings", &state)
	putJSON(t, client, srv.URL+"/api/settings", state, 200, nil)
	if settings.Snapshot().UpdateChannel != "beta" {
		t.Fatal("saving settings reset the update channel")
	}

	for path, want := range map[string]string{"/favicon.ico": "ICO", "/manifest.webmanifest": "OneFrog", "/servers": "index"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(string(body), want) {
			t.Fatalf("%s: %d %q", path, res.StatusCode, body)
		}
		if path == "/manifest.webmanifest" && !strings.HasPrefix(res.Header.Get("Content-Type"), "application/manifest+json") {
			t.Fatalf("manifest content type %q", res.Header.Get("Content-Type"))
		}
	}
}

func TestUpdateApplyUsesCachedReleaseOnly(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"tag_name":"v0.2.0","html_url":"https://github.com/llc370219/onefrog/releases/tag/v0.2.0","body":"notes"}`)
	}))
	defer gh.Close()
	mem := store.NewMemory()
	box, _ := secret.New(bytes.Repeat([]byte{7}, 32))
	if err := boot.Prepare(context.Background(), mem, config.Config{AdminUser: "admin", AdminPassword: "admin", Seed: true}); err != nil {
		t.Fatal(err)
	}
	settings, _ := preferences.New("", box)
	dir := t.TempDir()
	t.Setenv("OF_UPGRADE_DIR", dir)
	checker := &update.Checker{Current: "v0.1.1", API: gh.URL, SettingsDisabled: func() bool { return settings.Snapshot().UpdateCheckDisabled }}
	app := &Server{Store: mem, Box: box, Settings: settings, Updates: checker, WebDir: t.TempDir(), Agent: &bot.Agent{Store: mem, Box: box, SSH: &fakeSSH{}}}
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	res := postRaw(t, client, srv.URL+"/api/update/apply", map[string]any{"version": "v9.9.9"})
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("apply must require login: %d", res.StatusCode)
	}
	login(t, client, srv, "admin", "admin", 200)

	var denied struct {
		Error string `json:"error"`
	}
	postJSON(t, client, srv.URL+"/api/update/apply", map[string]any{"version": "v9.9.9"}, http.StatusConflict, &denied)
	if denied.Error == "" || strings.Contains(denied.Error, "v9.9.9") {
		t.Fatalf("unexpected deny %q", denied.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, "upgrade")); !os.IsNotExist(err) {
		t.Fatal("wrote a trigger before a release was cached", err)
	}

	var status update.Status
	postJSON(t, client, srv.URL+"/api/update/check", map[string]any{}, 200, &status)
	if !status.Available || !status.CanApply || status.Latest != "v0.2.0" {
		t.Fatalf("status %+v", status)
	}
	var applied struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
		Message string `json:"message"`
	}
	postJSON(t, client, srv.URL+"/api/update/apply", map[string]any{"version": "v9.9.9"}, http.StatusAccepted, &applied)
	if !applied.OK || applied.Version != "v0.2.0" || !strings.Contains(applied.Message, "正在升级") {
		t.Fatalf("applied %+v", applied)
	}
	body, err := os.ReadFile(filepath.Join(dir, "upgrade"))
	if err != nil || string(body) != "v0.2.0\n" {
		t.Fatalf("trigger %q err %v", body, err)
	}
}
