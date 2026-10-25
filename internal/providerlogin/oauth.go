// Package providerlogin owns isolated OAuth grants. It never reads the user's Codex/Google credential stores.
package providerlogin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"onefrog/internal/id"
	"onefrog/internal/secret"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ConsentVersion identifies the risk notice the operator must acknowledge
// before any third-party provider login. Bump it when the notice changes.
const ConsentVersion = "2026-10-26-tos-v2"

// GoogleSetupDoc explains how to configure the Google Antigravity OAuth client.
const GoogleSetupDoc = "docs/设置与模型配置.md#google-antigravity-登录需自行配置"

type Definition struct {
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	BaseURL      string `json:"base_url"`
	Experimental bool   `json:"experimental"`
	Scopes       string `json:"scopes"`
	ClientID     string `json:"-"`
	ClientSecret string `json:"-"`
	AuthURL      string `json:"-"`
	TokenURL     string `json:"-"`
	Redirect     string `json:"-"`
	UserInfo     string `json:"-"`
	// Configured is false when the operator still has to supply a client ID
	// (and secret) via environment variables; SetupHint says how.
	Configured bool   `json:"configured"`
	SetupHint  string `json:"setup_hint,omitempty"`
	SetupDoc   string `json:"setup_doc,omitempty"`
}

// The OpenAI entry uses the public Codex desktop client identifier (no secret); operators may override it.
// Google requires the operator to register an OAuth client and supply OF_GOOGLE_OAUTH_CLIENT_ID / OF_GOOGLE_OAUTH_CLIENT_SECRET.
// Providers without a client ID are listed with Configured=false so the UI can
// explain the setup instead of failing.
func Definitions() []Definition {
	all := allDefinitions()
	for i := range all {
		all[i].Configured = all[i].ClientID != ""
		if !all[i].Configured && all[i].Kind == "oauth-antigravity" {
			all[i].SetupHint = "需要配置：在服务端设置环境变量 OF_GOOGLE_OAUTH_CLIENT_ID 和 OF_GOOGLE_OAUTH_CLIENT_SECRET 后重启。Needs configuration: set OF_GOOGLE_OAUTH_CLIENT_ID and OF_GOOGLE_OAUTH_CLIENT_SECRET on the server and restart."
			all[i].SetupDoc = GoogleSetupDoc
		}
	}
	return all
}

func allDefinitions() []Definition {
	return []Definition{{Kind: "oauth-openai", Name: "OpenAI（Codex 登录）", BaseURL: "https://chatgpt.com/backend-api/codex", Scopes: "openid profile email offline_access", ClientID: env("OF_OPENAI_OAUTH_CLIENT_ID", "app_EMoamEEZ73f0CkXaXp7hrann"), AuthURL: "https://auth.openai.com/oauth/authorize", TokenURL: "https://auth.openai.com/oauth/token", Redirect: "http://localhost:1455/auth/callback"}, {Kind: "oauth-antigravity", Name: "Google Antigravity", BaseURL: "https://daily-cloudcode-pa.googleapis.com", Experimental: true, Scopes: "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile", ClientID: os.Getenv("OF_GOOGLE_OAUTH_CLIENT_ID"), ClientSecret: os.Getenv("OF_GOOGLE_OAUTH_CLIENT_SECRET"), AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", Redirect: "http://127.0.0.1:51121/callback", UserInfo: "https://www.googleapis.com/oauth2/v2/userinfo"}}
}
func env(k, v string) string {
	if s := os.Getenv(k); s != "" {
		return s
	}
	return v
}
func DefinitionFor(kind string) (Definition, error) {
	for _, d := range Definitions() {
		if d.Kind == kind {
			return d, nil
		}
	}
	return Definition{}, errors.New("不支持的 OAuth 提供方")
}

type Credential struct {
	Owner     string    `json:"owner"`
	Kind      string    `json:"kind"`
	Access    string    `json:"access"`
	Refresh   string    `json:"refresh"`
	Expires   time.Time `json:"expires"`
	Email     string    `json:"email"`
	AccountID string    `json:"account_id"`
	ProjectID string    `json:"project_id"`
	Consent   string    `json:"consent"`
}

// Acknowledgement records that an operator accepted the third-party login
// risk notice for one provider.
type Acknowledgement struct {
	Owner   string    `json:"owner"`
	Kind    string    `json:"kind"`
	Version string    `json:"version"`
	At      time.Time `json:"at"`
}

// fileV2 is the oauth.enc payload; older files hold only the accounts map.
type fileV2 struct {
	Version          int                        `json:"version"`
	Accounts         map[string]Credential      `json:"accounts"`
	Acknowledgements map[string]Acknowledgement `json:"acknowledgements"`
}

type Account struct {
	Kind    string    `json:"kind"`
	Name    string    `json:"name"`
	Status  string    `json:"status"`
	Email   string    `json:"email"`
	Expires time.Time `json:"expires_at"`
	Message string    `json:"message,omitempty"`
}
type Login struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Status   string    `json:"status"`
	URL      string    `json:"url,omitempty"`
	Redirect string    `json:"redirect_uri"`
	Expires  time.Time `json:"expires_at"`
	Message  string    `json:"message,omitempty"`
}
type pending struct {
	Login
	owner, state, verifier string
	server                 *http.Server
	cancel                 context.CancelFunc
}
type Manager struct {
	mu          sync.Mutex
	path        string
	box         *secret.Box
	accounts    map[string]Credential
	acks        map[string]Acknowledgement
	pending     map[string]*pending
	HTTP        *http.Client
	Definitions []Definition
}

func New(path string, box *secret.Box) (*Manager, error) {
	m := &Manager{path: path, box: box, accounts: map[string]Credential{}, acks: map[string]Acknowledgement{}, pending: map[string]*pending{}, HTTP: &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Definitions: Definitions()}
	if path != "" {
		raw, e := os.ReadFile(path)
		if e == nil {
			plain, e := box.Decrypt(string(raw))
			if e != nil {
				return nil, e
			}
			var v2 fileV2
			if json.Unmarshal([]byte(plain), &v2) == nil && v2.Version == 2 {
				if v2.Accounts != nil {
					m.accounts = v2.Accounts
				}
				if v2.Acknowledgements != nil {
					m.acks = v2.Acknowledgements
				}
			} else if e = json.Unmarshal([]byte(plain), &m.accounts); e != nil {
				return nil, e
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		}
	}
	return m, nil
}
func (m *Manager) definition(kind string) (Definition, error) {
	for _, d := range m.Definitions {
		if d.Kind == kind {
			return d, nil
		}
	}
	return Definition{}, errors.New("不支持的 OAuth 提供方")
}
func key(owner, kind string) string { return owner + "/" + kind }
func mask(email string) string {
	p := strings.SplitN(email, "@", 2)
	if len(p) != 2 {
		return "已授权账号"
	}
	r := []rune(p[0])
	if len(r) == 0 {
		return "***@" + p[1]
	}
	return string(r[:1]) + "***@" + p[1]
}
func (m *Manager) Accounts(owner string) []Account {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Account{}
	for _, d := range m.Definitions {
		v := Account{Kind: d.Kind, Name: d.Name, Status: "disconnected"}
		if c, ok := m.accounts[key(owner, d.Kind)]; ok {
			v.Status = "connected"
			v.Email = mask(c.Email)
			v.Expires = c.Expires
			if d.Kind == "oauth-antigravity" && c.ProjectID == "" {
				v.Status = "setup_required"
				v.Message = "已授权，请在 Antigravity 官方应用完成账号初始化后重新连接"
			}
			if time.Now().After(c.Expires) && c.Refresh == "" {
				v.Status = "reauth_required"
				v.Message = "授权已过期，请重新登录"
			}
		}
		out = append(out, v)
	}
	return out
}
func (m *Manager) persistLocked() error {
	if m.path == "" {
		return nil
	}
	raw, e := json.Marshal(fileV2{Version: 2, Accounts: m.accounts, Acknowledgements: m.acks})
	if e != nil {
		return e
	}
	enc, e := m.box.Encrypt(string(raw))
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(m.path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(m.path), ".oauth-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.WriteString(enc); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), m.path)
}
func random() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Acknowledge stores (encrypted, with a timestamp) that owner accepted the
// current risk notice for kind.
func (m *Manager) Acknowledge(owner, kind string) (Acknowledgement, error) {
	if _, e := m.definition(kind); e != nil {
		return Acknowledgement{}, e
	}
	a := Acknowledgement{Owner: owner, Kind: kind, Version: ConsentVersion, At: time.Now().UTC()}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(owner, kind)
	old, existed := m.acks[k]
	m.acks[k] = a
	if e := m.persistLocked(); e != nil {
		if existed {
			m.acks[k] = old
		} else {
			delete(m.acks, k)
		}
		return Acknowledgement{}, e
	}
	return a, nil
}

// Acknowledgements returns owner's current acknowledgements by provider kind.
func (m *Manager) Acknowledgements(owner string) map[string]Acknowledgement {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Acknowledgement{}
	for _, a := range m.acks {
		if a.Owner == owner && a.Version == ConsentVersion {
			out[a.Kind] = a
		}
	}
	return out
}

func (m *Manager) Start(owner, kind, consent string) (Login, error) {
	if consent != ConsentVersion {
		return Login{}, errors.New("请先阅读第三方登录风险说明并勾选确认")
	}
	d, e := m.definition(kind)
	if e != nil {
		return Login{}, e
	}
	if d.ClientID == "" {
		return Login{}, errors.New("该登录方式需要配置：请在服务端设置 OF_GOOGLE_OAUTH_CLIENT_ID / OF_GOOGLE_OAUTH_CLIENT_SECRET（见 " + GoogleSetupDoc + "）")
	}
	m.mu.Lock()
	ack, ok := m.acks[key(owner, kind)]
	m.mu.Unlock()
	if !ok || ack.Version != ConsentVersion {
		return Login{}, errors.New("请先阅读第三方登录风险说明并勾选确认")
	}
	verifier, state := random(), random()
	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {d.ClientID}, "redirect_uri": {d.Redirect}, "scope": {d.Scopes}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}, "state": {state}}
	if kind == "oauth-openai" {
		q.Set("codex_cli_simplified_flow", "true")
		q.Set("id_token_add_organizations", "true")
		q.Set("originator", "onefrog")
	} else {
		q.Set("access_type", "offline")
		q.Set("prompt", "consent select_account")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	p := &pending{Login: Login{ID: id.New(), Kind: kind, Status: "pending", URL: d.AuthURL + "?" + q.Encode(), Redirect: d.Redirect, Expires: time.Now().Add(10 * time.Minute)}, owner: owner, state: state, verifier: verifier, cancel: cancel}
	m.mu.Lock()
	for _, old := range m.pending {
		if old.owner == owner && old.Kind == kind && old.Status == "pending" {
			old.Status = "cancelled"
			old.cancel()
			if old.server != nil {
				_ = old.server.Close()
			}
		}
	}
	m.pending[p.ID] = p
	m.mu.Unlock()
	u, _ := url.Parse(d.Redirect)
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", u.Port()))
	if err == nil {
		mux := http.NewServeMux()
		p.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		mux.HandleFunc(u.Path, func(w http.ResponseWriter, r *http.Request) {
			full := d.Redirect + "?" + r.URL.RawQuery
			err := m.Complete(owner, p.ID, full)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			if err != nil {
				http.Error(w, "OneFrog: authorization could not be completed. Return to the application.", 400)
				return
			}
			http.Redirect(w, r, "/onefrog-success", http.StatusSeeOther)
		})
		mux.HandleFunc("/onefrog-success", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "default-src 'none'")
			_, _ = io.WriteString(w, "<!doctype html><title>OneFrog</title><h2>授权已完成</h2><p>请返回 OneFrog，可关闭本页。</p>")
		})
		go func() { _ = p.server.Serve(listener) }()
	} else {
		m.mu.Lock()
		p.Message = "本机回调端口被占用，可完成登录后粘贴完整回调 URL"
		m.mu.Unlock()
	}
	go func() {
		<-ctx.Done()
		m.mu.Lock()
		if p.Status == "pending" || p.Status == "exchanging" {
			p.Status = "expired"
			p.Message = "登录已过期，请重试"
		}
		m.mu.Unlock()
		if p.server != nil {
			_ = p.server.Close()
		}
	}()
	return m.Status(owner, p.ID)
}
func (m *Manager) Status(owner, id string) (Login, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.pending[id]
	if p == nil || p.owner != owner {
		return Login{}, errors.New("登录请求不存在")
	}
	v := p.Login
	if v.Status != "pending" {
		v.URL = ""
	}
	return v, nil
}
func (m *Manager) Cancel(owner, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.pending[id]
	if p == nil || p.owner != owner {
		return errors.New("登录请求不存在")
	}
	p.Status = "cancelled"
	p.verifier = ""
	p.cancel()
	if p.server != nil {
		_ = p.server.Close()
	}
	return nil
}
func (m *Manager) Complete(owner, id, callback string) error {
	m.mu.Lock()
	p := m.pending[id]
	if p == nil || p.owner != owner || p.Status != "pending" || time.Now().After(p.Expires) {
		m.mu.Unlock()
		return errors.New("登录请求无效或已过期")
	}
	u, e := url.Parse(callback)
	expected, _ := url.Parse(p.Redirect)
	if e != nil || u.Scheme != expected.Scheme || u.Host != expected.Host || u.Path != expected.Path || u.Query().Get("state") != p.state || u.Query().Get("code") == "" {
		m.mu.Unlock()
		return errors.New("回调 URL 或 state 无效，请粘贴完整的浏览器地址")
	}
	d, e := m.definition(p.Kind)
	if e != nil {
		m.mu.Unlock()
		return e
	}
	p.Status = "exchanging"
	code, verifier := u.Query().Get("code"), p.verifier
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	c, e := m.exchange(ctx, d, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {d.ClientID}, "redirect_uri": {d.Redirect}, "code_verifier": {verifier}}, Credential{Owner: owner, Kind: d.Kind, Consent: ConsentVersion})
	if e == nil && d.Kind == "oauth-antigravity" {
		c.ProjectID, _ = m.project(ctx, c.Access)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.Status != "exchanging" {
		return errors.New("登录已取消")
	}
	p.verifier = ""
	if e != nil {
		p.Status = "failed"
		p.Message = e.Error()
		return e
	}
	k := key(owner, d.Kind)
	old, existed := m.accounts[k]
	m.accounts[k] = c
	if e = m.persistLocked(); e != nil {
		if existed {
			m.accounts[k] = old
		} else {
			delete(m.accounts, k)
		}
		p.Status = "failed"
		p.Message = "授权信息无法加密保存"
		return errors.New(p.Message)
	}
	p.Status = "completed"
	p.Message = "授权信息已加密保存"
	return nil
}
func (m *Manager) exchange(ctx context.Context, d Definition, form url.Values, old Credential) (Credential, error) {
	if d.ClientSecret != "" {
		form.Set("client_secret", d.ClientSecret)
	}
	req, e := http.NewRequestWithContext(ctx, "POST", d.TokenURL, strings.NewReader(form.Encode()))
	if e != nil {
		return old, e
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, e := m.HTTP.Do(req)
	if e != nil {
		return old, errors.New("无法连接授权服务，请检查网络后重试")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return old, fmt.Errorf("授权服务返回 %d，请重新登录", res.StatusCode)
	}
	var v struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		ID      string `json:"id_token"`
		Expires int    `json:"expires_in"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 128*1024)).Decode(&v) != nil || v.Access == "" {
		return old, errors.New("授权服务未返回有效令牌")
	}
	old.Access = v.Access
	if v.Refresh != "" {
		old.Refresh = v.Refresh
	}
	if v.Expires <= 0 {
		v.Expires = 3600
	}
	old.Expires = time.Now().Add(time.Duration(v.Expires) * time.Second)
	for _, token := range []string{v.ID, v.Access} {
		parts := strings.Split(token, ".")
		if len(parts) > 1 {
			raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims map[string]any
			_ = json.Unmarshal(raw, &claims)
			if email, ok := claims["email"].(string); ok {
				old.Email = email
			}
			if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
				old.AccountID, _ = auth["chatgpt_account_id"].(string)
			}
		}
	}
	if d.UserInfo != "" {
		req, _ = http.NewRequestWithContext(ctx, "GET", d.UserInfo, nil)
		req.Header.Set("Authorization", "Bearer "+old.Access)
		if res, e = m.HTTP.Do(req); e == nil {
			var data struct {
				Email string `json:"email"`
			}
			if res.StatusCode == 200 {
				_ = json.NewDecoder(io.LimitReader(res.Body, 64*1024)).Decode(&data)
				old.Email = data.Email
			}
			res.Body.Close()
		}
	}
	return old, nil
}
func (m *Manager) Access(ctx context.Context, owner, kind string) (Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.accounts[key(owner, kind)]
	if !ok {
		return c, errors.New("请先登录该 OAuth 提供方")
	}
	if time.Until(c.Expires) < time.Minute {
		if c.Refresh == "" {
			return c, errors.New("授权已过期，请重新登录")
		}
		d, e := m.definition(kind)
		if e != nil {
			return c, e
		}
		next, e := m.exchange(ctx, d, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.Refresh}, "client_id": {d.ClientID}}, c)
		if e != nil {
			return c, e
		}
		m.accounts[key(owner, kind)] = next
		if e = m.persistLocked(); e != nil {
			return c, errors.New("刷新授权保存失败")
		}
		c = next
	}
	return c, nil
}
func (m *Manager) Disconnect(owner, kind string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.pending {
		if p.owner == owner && p.Kind == kind && (p.Status == "pending" || p.Status == "exchanging") {
			p.Status = "cancelled"
			p.verifier = ""
			p.cancel()
			if p.server != nil {
				_ = p.server.Close()
			}
		}
	}
	k := key(owner, kind)
	old, ok := m.accounts[k]
	delete(m.accounts, k)
	if e := m.persistLocked(); e != nil {
		if ok {
			m.accounts[k] = old
		}
		return e
	}
	return nil
}
func (m *Manager) project(ctx context.Context, token string) (string, error) {
	d, _ := m.definition("oauth-antigravity")
	base := strings.Replace(d.BaseURL, "daily-cloudcode-pa", "cloudcode-pa", 1)
	raw, e := m.request(ctx, "POST", base+"/v1internal:loadCodeAssist", token, "", map[string]any{"metadata": map[string]string{"ideType": "ANTIGRAVITY"}})
	if e != nil {
		return "", e
	}
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	if s, ok := v["cloudaicompanionProject"].(string); ok {
		return s, nil
	}
	if o, ok := v["cloudaicompanionProject"].(map[string]any); ok {
		if s, ok := o["id"].(string); ok {
			return s, nil
		}
	}
	return "", errors.New("请在官方 Antigravity 应用完成账号初始化")
}
