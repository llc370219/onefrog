package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"onefrog/internal/boot"
	"onefrog/internal/bot"
	"onefrog/internal/config"
	"onefrog/internal/model"
	"onefrog/internal/remote"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

// fakeSSH mimics remote.Run's host-key behaviour: it presents fp (default
// SHA256:test) and refuses unknown or mismatching keys.
type fakeSSH struct {
	calls int
	fp    string
}

func (f *fakeSSH) Run(ctx context.Context, host string, port int, user, authType, secret, knownKey, command string) (string, string, error) {
	f.calls++
	fp := f.fp
	if fp == "" {
		fp = "SHA256:test"
	}
	if knownKey == "" {
		return "", fp, remote.ErrHostKeyUnknown
	}
	if knownKey != fp {
		return "", fp, remote.ErrHostKeyMismatch
	}
	return "ok", fp, nil
}

func testClient(t *testing.T) (*http.Client, *httptest.Server, *fakeSSH) {
	t.Helper()
	mem := store.NewMemory()
	box, err := secret.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := boot.Prepare(context.Background(), mem, config.Config{AdminUser: "admin", AdminPassword: "admin", Seed: true}); err != nil {
		t.Fatal(err)
	}
	ssh := &fakeSSH{}
	app := &Server{Store: mem, Box: box, Agent: &bot.Agent{Store: mem, Box: box, SSH: ssh}}
	srv := httptest.NewServer(app.Handler())
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}, srv, ssh
}

func TestAuthAndResources(t *testing.T) {
	client, srv, ssh := testClient(t)
	defer srv.Close()
	res, err := client.Post(srv.URL+"/api/servers", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", res.StatusCode)
	}
	res.Body.Close()
	login(t, client, srv, "admin", "nope", http.StatusUnauthorized)
	login(t, client, srv, "admin", "admin", http.StatusOK)

	var servers []model.Server
	getJSON(t, client, srv.URL+"/api/servers", &servers)
	if len(servers) != 4 {
		t.Fatalf("servers %d", len(servers))
	}
	var created model.Secret
	postJSON(t, client, srv.URL+"/api/secrets", map[string]string{"name": "db-password", "value": "s3cret", "description": "db"}, http.StatusCreated, &created)
	body := getBody(t, client, srv.URL+"/api/secrets")
	if strings.Contains(body, "s3cret") {
		t.Fatal("secret value leaked")
	}
	var enabled model.Module
	postJSON(t, client, srv.URL+"/api/modules/compare/enable", map[string]bool{"enabled": true}, http.StatusOK, &enabled)
	if !enabled.Enabled {
		t.Fatal("module not enabled")
	}
	var history []model.History
	getJSON(t, client, srv.URL+"/api/history", &history)
	var rollbackID string
	for _, item := range history {
		if item.CanRollback {
			rollbackID = item.ID
		}
	}
	if rollbackID == "" {
		t.Fatal("missing rollback row")
	}
	postJSON(t, client, srv.URL+"/api/history/"+rollbackID+"/rollback", map[string]string{}, http.StatusOK, &map[string]any{})
	var chat map[string]any
	postJSON(t, client, srv.URL+"/api/chat", map[string]string{"content": "看一下磁盘", "server_id": "web-01"}, http.StatusOK, &chat)
	if chat["text"] == "" {
		t.Fatal("empty chat reply")
	}
	if ssh.calls != 0 {
		t.Fatal("chat without a model must not run ssh")
	}
	res, err = client.Post(srv.URL+"/api/servers/web-01/test", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || ssh.calls != 1 {
		t.Fatalf("test status %d calls %d", res.StatusCode, ssh.calls)
	}
}

func login(t *testing.T, client *http.Client, srv *httptest.Server, user, pass string, want int) {
	t.Helper()
	res := postRaw(t, client, srv.URL+"/api/auth/login", map[string]string{"username": user, "password": pass})
	defer res.Body.Close()
	if res.StatusCode != want {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("login %d %s", res.StatusCode, b)
	}
}

func postJSON(t *testing.T, client *http.Client, url string, body any, want int, dest any) {
	t.Helper()
	res := postRaw(t, client, url, body)
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != want {
		t.Fatalf("%s %d %s", url, res.StatusCode, raw)
	}
	if dest != nil && len(raw) > 0 && raw[0] != 'N' {
		if err := json.Unmarshal(raw, dest); err != nil {
			t.Fatal(err)
		}
	}
}

func postRaw(t *testing.T, client *http.Client, url string, body any) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	res, err := client.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func getJSON(t *testing.T, client *http.Client, url string, dest any) {
	t.Helper()
	if err := json.Unmarshal([]byte(getBody(t, client, url)), dest); err != nil {
		t.Fatal(err)
	}
}

func getBody(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	res, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s %d %s", url, res.StatusCode, raw)
	}
	return string(raw)
}

func TestSettingsKeepsKeyHidden(t *testing.T) {
	client, srv, _ := testClient(t)
	defer srv.Close()
	login(t, client, srv, "admin", "admin", http.StatusOK)

	var empty settingsView
	getJSON(t, client, srv.URL+"/api/settings", &empty)
	if empty.HasAPIKey || empty.BaseURL != "" {
		t.Fatalf("unexpected seed settings %+v", empty)
	}
	putJSON(t, client, srv.URL+"/api/settings", map[string]string{"base_url": "https://example.test/v1", "model_name": "demo"}, http.StatusBadRequest, nil)

	const key = "sk-test-should-not-leak"
	var saved settingsView
	putJSON(t, client, srv.URL+"/api/settings", map[string]string{"base_url": "https://example.test/v1/", "model_name": "demo", "api_key": key}, http.StatusOK, &saved)
	if saved.BaseURL != "https://example.test/v1" || saved.ModelName != "demo" || !saved.HasAPIKey {
		t.Fatalf("saved %+v", saved)
	}
	body := getBody(t, client, srv.URL+"/api/settings")
	if strings.Contains(body, key) {
		t.Fatal("settings response leaked the key")
	}
	secrets := getBody(t, client, srv.URL+"/api/secrets")
	if strings.Contains(secrets, key) {
		t.Fatal("secret list leaked the key")
	}

	var kept settingsView
	putJSON(t, client, srv.URL+"/api/settings", map[string]string{"base_url": "https://example.test/v1", "model_name": "demo-2"}, http.StatusOK, &kept)
	if kept.ModelName != "demo-2" || !kept.HasAPIKey {
		t.Fatalf("kept %+v", kept)
	}
	var replaced settingsView
	putJSON(t, client, srv.URL+"/api/settings", map[string]string{"base_url": "https://example.test/v1", "model_name": "demo-2", "api_key": "sk-replaced"}, http.StatusOK, &replaced)
	if !replaced.HasAPIKey {
		t.Fatal("replacement dropped the key")
	}
	if strings.Contains(getBody(t, client, srv.URL+"/api/secrets"), "sk-replaced") {
		t.Fatal("replaced key leaked")
	}
}

func putJSON(t *testing.T, client *http.Client, url string, body any, want int, dest any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	payload, _ := io.ReadAll(res.Body)
	if res.StatusCode != want {
		t.Fatalf("PUT %s %d %s", url, res.StatusCode, payload)
	}
	if dest != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, dest); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHostKeyConfirmFlow(t *testing.T) {
	client, srv, ssh := testClient(t)
	defer srv.Close()
	login(t, client, srv, "admin", "admin", http.StatusOK)
	var res map[string]any
	postJSON(t, client, srv.URL+"/api/servers/web-01/test", map[string]string{}, http.StatusOK, &res)
	if res["status"] != "host_key_unknown" || res["fingerprint"] != "SHA256:test" {
		t.Fatalf("first contact must ask for confirmation: %v", res)
	}
	postJSON(t, client, srv.URL+"/api/servers/web-01/test", map[string]string{}, http.StatusOK, &res)
	if res["status"] != "host_key_unknown" {
		t.Fatal("fingerprint must not be stored before confirmation")
	}
	postJSON(t, client, srv.URL+"/api/servers/web-01/test", map[string]string{"fingerprint": "SHA256:other"}, http.StatusOK, &res)
	if res["ok"] == true {
		t.Fatal("wrong confirmed fingerprint must fail")
	}
	postJSON(t, client, srv.URL+"/api/servers/web-01/test", map[string]string{"fingerprint": "SHA256:test"}, http.StatusOK, &res)
	if res["ok"] != true {
		t.Fatalf("confirmed connect failed: %v", res)
	}
	ssh.fp = "SHA256:changed"
	postJSON(t, client, srv.URL+"/api/servers/web-01/test", map[string]string{"fingerprint": "SHA256:changed"}, http.StatusOK, &res)
	if res["status"] != "host_key_mismatch" || res["stored_fingerprint"] != "SHA256:test" {
		t.Fatalf("mismatch must fail and keep stored key: %v", res)
	}
	postJSON(t, client, srv.URL+"/api/servers/web-01/test", map[string]string{}, http.StatusOK, &res)
	if res["status"] != "host_key_mismatch" {
		t.Fatal("stored key was overwritten")
	}
	postJSON(t, client, srv.URL+"/api/servers/web-01/host-key/reset", map[string]string{}, http.StatusOK, &res)
	postJSON(t, client, srv.URL+"/api/servers/web-01/test", map[string]string{}, http.StatusOK, &res)
	if res["status"] != "host_key_unknown" || res["fingerprint"] != "SHA256:changed" {
		t.Fatalf("reset should require re-confirmation: %v", res)
	}
	var history []model.History
	getJSON(t, client, srv.URL+"/api/history", &history)
	seen := map[string]bool{}
	for _, h := range history {
		seen[h.Status] = true
	}
	if !seen["host_key_confirmed"] || !seen["host_key_reset"] {
		t.Fatalf("host key actions missing from history: %v", seen)
	}
}
