package providerlogin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"onefrog/internal/secret"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testLogin(t *testing.T, kind string) (*Manager, *httptest.Server, *int) {
	t.Helper()
	refreshes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("grant_type") == "refresh_token" {
				refreshes++
			}
			payload, _ := json.Marshal(map[string]any{"email": "person@example.com", "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "account-1"}})
			jwt := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": jwt, "refresh_token": "private-refresh", "id_token": jwt, "expires_in": 3600})
		case "/userinfo":
			_, _ = io.WriteString(w, `{"email":"person@example.com"}`)
		case "/api/v1internal:loadCodeAssist":
			_, _ = io.WriteString(w, `{"cloudaicompanionProject":"project-1"}`)
		case "/api/v1internal:fetchAvailableModels":
			_, _ = io.WriteString(w, `{"models":{"gemini-test":{},"claude-test":{}}}`)
		case "/api/models":
			_, _ = io.WriteString(w, `{"models":[{"slug":"codex-test"}]}`)
		case "/api/responses":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":4,\"output_tokens\":2,\"total_tokens\":6}}}\n\n")
		case "/api/v1internal:generateContent":
			_, _ = io.WriteString(w, `{"response":{"candidates":[{"content":{"parts":[{"text":"OK"}]}}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}}}`)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	box, _ := secret.New(bytes.Repeat([]byte{5}, 32))
	m, e := New(filepath.Join(t.TempDir(), "oauth.enc"), box)
	if e != nil {
		t.Fatal(e)
	}
	m.Definitions = []Definition{{Kind: kind, Name: "test", AuthURL: server.URL + "/authorize", TokenURL: server.URL + "/token", BaseURL: server.URL + "/api", Redirect: "http://127.0.0.1:0/callback", ClientID: "public-test", UserInfo: server.URL + "/userinfo", Scopes: "scope"}}
	m.HTTP = server.Client()
	return m, server, &refreshes
}
func TestPKCEOwnerStateEncryptedRefresh(t *testing.T) {
	m, s, refreshes := testLogin(t, "oauth-openai")
	defer s.Close()
	if _, e := m.Start("owner", "oauth-openai", ""); e == nil {
		t.Fatal("consent not enforced")
	}
	if _, e := m.Start("owner", "oauth-openai", ConsentVersion); e == nil {
		t.Fatal("started without acknowledgement")
	}
	if _, e := m.Acknowledge("owner", "oauth-openai"); e != nil {
		t.Fatal(e)
	}
	login, e := m.Start("owner", "oauth-openai", ConsentVersion)
	if e != nil {
		t.Fatal(e)
	}
	defer m.Cancel("owner", login.ID)
	auth, _ := url.Parse(login.URL)
	p := m.pending[login.ID]
	challenge := sha256.Sum256([]byte(p.verifier))
	if auth.Query().Get("code_challenge") != base64.RawURLEncoding.EncodeToString(challenge[:]) {
		t.Fatal("invalid PKCE")
	}
	callback := login.Redirect + "?code=private-code&state=" + auth.Query().Get("state")
	if e = m.Complete("other", login.ID, callback); e == nil {
		t.Fatal("owner bypass")
	}
	if e = m.Complete("owner", login.ID, strings.Replace(callback, "state=", "state=wrong", 1)); e == nil {
		t.Fatal("state bypass")
	}
	if e = m.Complete("owner", login.ID, callback); e != nil {
		t.Fatal(e)
	}
	if e = m.Complete("owner", login.ID, callback); e == nil {
		t.Fatal("replay")
	}
	raw, _ := os.ReadFile(m.path)
	if bytes.Contains(raw, []byte("private-refresh")) || bytes.Contains(raw, []byte("person@example.com")) {
		t.Fatal("plaintext OAuth persisted")
	}
	view, _ := json.Marshal(m.Accounts("owner"))
	if bytes.Contains(view, []byte("private-refresh")) || bytes.Contains(view, []byte("person@example.com")) {
		t.Fatal("credential exposed")
	}
	m.mu.Lock()
	c := m.accounts[key("owner", "oauth-openai")]
	c.Expires = time.Now().Add(-time.Second)
	m.accounts[key("owner", "oauth-openai")] = c
	m.mu.Unlock()
	if _, e = m.Access(context.Background(), "owner", "oauth-openai"); e != nil || *refreshes != 1 {
		t.Fatal("refresh", e)
	}
	models, e := m.Models(context.Background(), "owner", "oauth-openai")
	if e != nil || len(models) != 1 {
		t.Fatal(models, e)
	}
	out, e := m.CompleteChat(context.Background(), "owner", "oauth-openai", "codex-test", []map[string]any{{"role": "user", "content": "hello"}}, nil)
	if e != nil || !bytes.Contains(out, []byte("OK")) {
		t.Fatal(string(out), e)
	}
	if e = m.Disconnect("owner", "oauth-openai"); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Access(context.Background(), "owner", "oauth-openai"); e == nil {
		t.Fatal("disconnect failed")
	}
}
func TestGoogleGrantModelsAndInference(t *testing.T) {
	m, s, _ := testLogin(t, "oauth-antigravity")
	defer s.Close()
	if _, e := m.Acknowledge("owner", "oauth-antigravity"); e != nil {
		t.Fatal(e)
	}
	login, e := m.Start("owner", "oauth-antigravity", ConsentVersion)
	if e != nil {
		t.Fatal(e)
	}
	defer m.Cancel("owner", login.ID)
	u, _ := url.Parse(login.URL)
	if e = m.Complete("owner", login.ID, login.Redirect+"?code=test&state="+u.Query().Get("state")); e != nil {
		t.Fatal(e)
	}
	models, e := m.Models(context.Background(), "owner", "oauth-antigravity")
	if e != nil || len(models) != 2 {
		t.Fatal(models, e)
	}
	out, e := m.CompleteChat(context.Background(), "owner", "oauth-antigravity", "gemini-test", []map[string]any{{"role": "user", "content": "hello"}}, nil)
	if e != nil || !bytes.Contains(out, []byte("OK")) {
		t.Fatal(string(out), e)
	}
}
func TestAdapterToolRoundTrip(t *testing.T) {
	messages := []map[string]any{{"role": "system", "content": "instructions"}, {"role": "user", "content": []map[string]any{{"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64,AAAA"}}}}, {"role": "assistant", "tool_calls": []map[string]any{{"id": "call-1", "thought_signature": "sig", "function": map[string]string{"name": "run_command", "arguments": "{\"command\":\"df -h\"}"}}}}, {"role": "tool", "tool_call_id": "call-1", "content": "result"}}
	raw, _ := json.Marshal(googleRequest("gemini", "project", messages, nil))
	for _, want := range []string{"inlineData", "functionResponse", "run_command", "thoughtSignature", "sig"} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatal("missing", want)
		}
	}
	raw, _ = json.Marshal(responsesRequest("model", messages, nil))
	if !bytes.Contains(raw, []byte("function_call_output")) {
		t.Fatal("missing tool result")
	}
	out, e := normalizeGoogle([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"run_command","args":{"command":"df"}},"thoughtSignature":"sig"}]}}]}`))
	if e != nil || !bytes.Contains(out, []byte("thought_signature")) {
		t.Fatal(string(out), e)
	}
	if _, e = normalizeResponses([]byte("data: {\"type\":\"response.failed\"}\n")); e == nil {
		t.Fatal("accepted failed stream")
	}
}
