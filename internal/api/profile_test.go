package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"onefrog/internal/model"
	"strings"
	"testing"

	"onefrog/internal/boot"
	"onefrog/internal/bot"
	"onefrog/internal/config"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

func putProfile(t *testing.T, c *http.Client, url string, body any, want int) string {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode != want {
		t.Fatalf("profile got %d want %d: %s", res.StatusCode, want, data)
	}
	return string(data)
}
func TestProfileCredentials(t *testing.T) {
	c, s, _ := testClient(t)
	defer s.Close()
	url := s.URL + "/api/auth/profile"
	putProfile(t, c, url, map[string]string{"username": "admin"}, 401)
	login(t, c, s, "admin", "admin", 200)
	raw := putProfile(t, c, url, map[string]string{"username": "admin", "display_name": "我的名字", "avatar": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Y9Zl1sAAAAASUVORK5CYII="}, 200)
	if !strings.Contains(raw, "我的名字") || strings.Contains(raw, "Hash") {
		t.Fatal(raw)
	}
	putProfile(t, c, url, map[string]string{"username": "renamed", "current_password": "wrong"}, 400)
	putProfile(t, c, url, map[string]string{"username": "admin", "avatar": "data:image/svg+xml;base64,PHN2Zz4="}, 400)
	putProfile(t, c, url, map[string]string{"username": "admin", "current_password": "admin", "new_password": "short"}, 400)
	// A second device's existing session must be invalidated after a credential change.
	second := &http.Client{}
	res := postRaw(t, second, s.URL+"/api/auth/login", map[string]string{"username": "admin", "password": "admin"})
	cookies := res.Cookies()
	res.Body.Close()
	putProfile(t, c, url, map[string]string{"username": "renamed", "display_name": "我的名字", "current_password": "admin", "new_password": "new-password-123"}, 200)
	var me model.User
	getJSON(t, c, s.URL+"/api/auth/me", &me)
	if me.Username != "renamed" {
		t.Fatal(me)
	}
	req, _ := http.NewRequest("GET", s.URL+"/api/auth/me", nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	res, err := second.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("old device remained signed in")
	}
	login(t, c, s, "admin", "admin", 401)
	login(t, c, s, "renamed", "admin", 401)
	login(t, c, s, "renamed", "new-password-123", 200)
}

func TestInstallLoginIsLockedUntilCredentialsChange(t *testing.T) {
	mem := store.NewMemory()
	box, err := secret.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err = boot.Prepare(context.Background(), mem, config.Config{AdminUser: "admin", AdminPassword: "temp-pass-123", ForceCredentialSetup: true}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&Server{Store: mem, Box: box, Agent: &bot.Agent{Store: mem, Box: box}}).Handler())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	login(t, client, srv, "admin", "temp-pass-123", http.StatusOK)
	res, err := client.Get(srv.URL + "/api/servers")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("locked api %d", res.StatusCode)
	}
	putProfile(t, client, srv.URL+"/api/auth/profile", map[string]string{"username": "admin", "current_password": "temp-pass-123", "new_password": "another-password"}, 400)
	putProfile(t, client, srv.URL+"/api/auth/profile", map[string]string{"username": "owner", "display_name": "Owner", "current_password": "temp-pass-123", "new_password": "another-password"}, 200)
	var me model.User
	getJSON(t, client, srv.URL+"/api/auth/me", &me)
	if me.MustChangeCredentials || me.Username != "owner" {
		t.Fatalf("%+v", me)
	}
	res, err = client.Get(srv.URL + "/api/servers")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unlocked api %d", res.StatusCode)
	}
}
func TestConversationDirectory(t *testing.T) {
	c, s, _ := testClient(t)
	defer s.Close()
	login(t, c, s, "admin", "admin", 200)
	var reply struct {
		SessionID string `json:"session_id"`
	}
	postJSON(t, c, s.URL+"/api/chat", map[string]string{"content": "检查磁盘"}, 200, &reply)
	var sessions []model.SessionSummary
	getJSON(t, c, s.URL+"/api/sessions", &sessions)
	if len(sessions) != 1 || sessions[0].ID != reply.SessionID || sessions[0].Title != "检查磁盘" || sessions[0].Preview == "" || sessions[0].BotID == "" {
		t.Fatalf("directory %+v", sessions)
	}
	var messages []model.Message
	getJSON(t, c, s.URL+"/api/sessions/"+reply.SessionID+"/messages", &messages)
	if len(messages) != 2 || messages[0].Content != "检查磁盘" {
		t.Fatal(messages)
	}
}
