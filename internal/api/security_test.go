package api

import (
	"net/http"
	"net/url"
	"os"

	"github.com/gorilla/websocket"

	"onefrog/internal/model"
	"strings"
	"testing"
	"time"
)

// csrfTransport adds the CSRF header the web client sends, so API tests that
// use plain http.Client exercise the same path as the browser.
type csrfTransport struct{ base http.RoundTripper }

func (t csrfTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("X-Test-No-CSRF") == "" {
		r = r.Clone(r.Context())
		r.Header.Set(CSRFHeader, "1")
	}
	return t.base.RoundTrip(r)
}

func TestMain(m *testing.M) {
	http.DefaultTransport = csrfTransport{base: http.DefaultTransport}
	os.Exit(m.Run())
}

func TestSecurityHeadersAndCSRF(t *testing.T) {
	client, srv, _ := testClient(t)
	defer srv.Close()
	res, err := client.Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	for _, h := range []string{"X-Frame-Options", "X-Content-Type-Options", "Content-Security-Policy", "Referrer-Policy"} {
		if res.Header.Get(h) == "" {
			t.Fatalf("missing %s", h)
		}
	}
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("csp lacks frame-ancestors")
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/auth/login", strings.NewReader(`{"username":"admin","password":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-No-CSRF", "1")
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("missing csrf header got %d", res.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/auth/login", strings.NewReader(`username=admin`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("form body got %d", res.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	client, srv, _ := testClient(t)
	defer srv.Close()
	for i := 0; i < userFailThreshold; i++ {
		res := postRaw(t, client, srv.URL+"/api/auth/login", map[string]string{"username": "admin", "password": "wrong"})
		res.Body.Close()
	}
	res := postRaw(t, client, srv.URL+"/api/auth/login", map[string]string{"username": "admin", "password": "wrong"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") == "" {
		t.Fatalf("expected lockout, got %d", res.StatusCode)
	}
}

func TestLoginLimiterBackoff(t *testing.T) {
	l := newLoginLimiter()
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < userFailThreshold; i++ {
		l.fail("198.51.100.1", "Bob")
	}
	if l.blocked("198.51.100.2", "bob") < 59*time.Second {
		t.Fatal("username lockout should apply from any IP")
	}
	now = now.Add(2 * time.Minute)
	if l.blocked("198.51.100.2", "bob") != 0 {
		t.Fatal("lock should expire")
	}
	l.fail("198.51.100.1", "bob")
	if l.blocked("198.51.100.1", "bob") < 119*time.Second {
		t.Fatal("backoff should grow")
	}
	l.succeed("198.51.100.1", "bob")
	if l.blocked("198.51.100.3", "bob") != 0 {
		t.Fatal("success clears username lock")
	}
}

func TestSameOrigin(t *testing.T) {
	r, _ := http.NewRequest("GET", "http://example.test/ws/chat", nil)
	r.Host = "example.test"
	if !sameOrigin(r, "") {
		t.Fatal("no origin allowed")
	}
	r.Header.Set("Origin", "http://example.test")
	if !sameOrigin(r, "") {
		t.Fatal("same host")
	}
	r.Header.Set("Origin", "https://evil.test")
	if sameOrigin(r, "") {
		t.Fatal("cross origin")
	}
	r.Host = "127.0.0.1:8080"
	r.Header.Set("Origin", "https://of.example.test")
	if !sameOrigin(r, "https://of.example.test") {
		t.Fatal("public url")
	}
}

func TestOAuthStartRequiresRiskAcknowledgement(t *testing.T) {
	t.Setenv("OF_GOOGLE_OAUTH_CLIENT_ID", "")
	client, srv, _ := testClient(t)
	defer srv.Close()
	login(t, client, srv, "admin", "admin", http.StatusOK)
	var view struct {
		Consent     string `json:"consent_version"`
		Definitions []struct {
			Kind       string `json:"kind"`
			Configured bool   `json:"configured"`
			SetupHint  string `json:"setup_hint"`
		} `json:"definitions"`
	}
	getJSON(t, client, srv.URL+"/api/oauth", &view)
	found := false
	for _, d := range view.Definitions {
		if d.Kind == "oauth-antigravity" {
			found = !d.Configured && d.SetupHint != ""
		}
	}
	if !found {
		t.Fatal("unconfigured Google login should be listed with a setup hint")
	}
	res := postRaw(t, client, srv.URL+"/api/oauth/start", map[string]any{"kind": "oauth-antigravity", "consent_version": view.Consent})
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("start without acknowledgement: %d", res.StatusCode)
	}
	var history []model.History
	getJSON(t, client, srv.URL+"/api/history", &history)
	for _, h := range history {
		if h.Status == "risk_acknowledged" {
			t.Fatal("acknowledgement recorded without checkbox")
		}
	}
	res = postRaw(t, client, srv.URL+"/api/oauth/start", map[string]any{"kind": "oauth-antigravity", "consent_version": view.Consent, "risk_acknowledged": true})
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unconfigured provider must not start: %d", res.StatusCode)
	}
	getJSON(t, client, srv.URL+"/api/history", &history)
	acked := false
	for _, h := range history {
		acked = acked || h.Status == "risk_acknowledged"
	}
	if !acked {
		t.Fatal("acknowledgement missing from history")
	}
}

func TestWebSocketOriginChecked(t *testing.T) {
	client, srv, _ := testClient(t)
	defer srv.Close()
	login(t, client, srv, "admin", "admin", http.StatusOK)
	u, _ := url.Parse(srv.URL)
	header := http.Header{}
	for _, c := range client.Jar.Cookies(u) {
		header.Add("Cookie", c.Name+"="+c.Value)
	}
	ws := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/chat"
	header.Set("Origin", "https://evil.example")
	if conn, res, err := websocket.DefaultDialer.Dial(ws, header); err == nil {
		conn.Close()
		t.Fatal("cross-origin websocket accepted")
	} else if res == nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %v", res)
	}
	header.Set("Origin", srv.URL)
	conn, _, err := websocket.DefaultDialer.Dial(ws, header)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}
