// Package preferences owns saved model routing and execution policy.
package preferences

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"onefrog/internal/model"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

type Provider struct {
	AuthKind     string   `json:"auth_kind,omitempty"`
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	BaseURL      string   `json:"base_url"`
	Enabled      bool     `json:"enabled"`
	AuthRequired bool     `json:"auth_required"`
	Models       []string `json:"models"`
	HiddenModels []string `json:"hidden_models"`
	HasAPIKey    bool     `json:"has_api_key"`
}
type ProviderInput struct {
	Provider
	APIKey      string `json:"api_key,omitempty"`
	ClearAPIKey bool   `json:"clear_api_key,omitempty"`
}
type Route struct {
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
}

// Approval modes. ModeUnset means the operator has not chosen yet; the agent
// then behaves like ModeSmart and the web UI asks for a choice.
const (
	ModeUnset  = ""
	ModeOff    = "off"    // run commands without asking (catastrophic keywords still blocked)
	ModeSmart  = "smart"  // read-only commands run, risky ones wait for the operator
	ModeManual = "manual" // advanced: confirm every command
	ModeModel  = "model"  // advanced: an independent model approves or rejects
	ModePlan   = "plan"   // advanced: plan only, never run remote commands
)

// NormalizeMode maps legacy names ("rules" was the original no-prompt mode)
// and reports whether the mode is known.
func NormalizeMode(mode string) (string, bool) {
	mode = strings.TrimSpace(mode)
	if mode == "rules" {
		mode = ModeOff
	}
	switch mode {
	case ModeUnset, ModeOff, ModeSmart, ModeManual, ModeModel, ModePlan:
		return mode, true
	}
	return mode, false
}

type Approval struct {
	Mode         string `json:"mode"`
	Route        Route  `json:"route"`
	Instructions string `json:"instructions"`
}
type Execution struct {
	TimeoutSeconds        int `json:"timeout_seconds"`
	CommandTimeoutSeconds int `json:"command_timeout_seconds"`
	MaxSteps              int `json:"max_steps"`
	ContextMessages       int `json:"context_messages"`
}
type State struct {
	DisabledBots []string         `json:"disabled_bots"`
	Providers    []Provider       `json:"providers"`
	Default      Route            `json:"default"`
	Worker       Route            `json:"worker"`
	BotRoutes    map[string]Route `json:"bot_routes"`
	Approval     Approval         `json:"approval"`
	Execution    Execution        `json:"execution"`
}
type Input struct {
	DisabledBots []string         `json:"disabled_bots"`
	Providers    []ProviderInput  `json:"providers"`
	Default      Route            `json:"default"`
	Worker       Route            `json:"worker"`
	BotRoutes    map[string]Route `json:"bot_routes"`
	Approval     Approval         `json:"approval"`
	Execution    Execution        `json:"execution"`
}
type diskState struct {
	State State             `json:"settings"`
	Keys  map[string]string `json:"keys"`
}
type Manager struct {
	mu   sync.RWMutex
	box  *secret.Box
	path string
	data diskState
}

func New(path string, box *secret.Box) (*Manager, error) {
	m := &Manager{box: box, path: path, data: diskState{State: Defaults(), Keys: map[string]string{}}}
	if path == "" {
		return m, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := box.Decrypt(string(raw))
	if err != nil {
		return nil, errors.New("设置无法解密，请检查主密钥")
	}
	if err = json.Unmarshal([]byte(plain), &m.data); err != nil {
		return nil, errors.New("设置文件无效")
	}
	if m.data.Keys == nil {
		m.data.Keys = map[string]string{}
	}
	m.data.State.Approval.Mode, _ = NormalizeMode(m.data.State.Approval.Mode)
	return m, nil
}
func Defaults() State {
	return State{Providers: []Provider{}, BotRoutes: map[string]Route{}, Approval: Approval{Mode: ModeUnset}, Execution: Execution{TimeoutSeconds: 60, CommandTimeoutSeconds: 30, MaxSteps: 6, ContextMessages: 24}}
}
func clone(s State) State {
	raw, _ := json.Marshal(s)
	var out State
	_ = json.Unmarshal(raw, &out)
	return out
}
func (m *Manager) Snapshot() State { m.mu.RLock(); defer m.mu.RUnlock(); return clone(m.data.State) }
func (m *Manager) Key(id string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	enc := m.data.Keys[id]
	if enc == "" {
		return "", nil
	}
	return m.box.Decrypt(enc)
}
func ValidateURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("接口地址须为 HTTP(S) 基础地址，不能包含账号、查询参数或片段")
	}
	return raw, nil
}
func (m *Manager) Save(in Input) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(in.Providers) > 30 {
		return State{}, errors.New("最多配置 30 个渠道")
	}
	disabled := in.DisabledBots
	if disabled == nil {
		disabled = m.data.State.DisabledBots
	}
	next := diskState{State: State{DisabledBots: disabled, Default: in.Default, Worker: in.Worker, BotRoutes: in.BotRoutes, Approval: in.Approval, Execution: in.Execution, Providers: []Provider{}}, Keys: map[string]string{}}
	seen := map[string]bool{}
	for _, req := range in.Providers {
		p := req.Provider
		p.ID = strings.TrimSpace(p.ID)
		p.Name = strings.TrimSpace(p.Name)
		if p.ID == "" || len(p.ID) > 80 || p.Name == "" || seen[p.ID] {
			return State{}, errors.New("渠道 ID 不能为空或重复，名称必填")
		}
		seen[p.ID] = true
		base, err := ValidateURL(p.BaseURL)
		if err != nil {
			return State{}, err
		}
		p.BaseURL = base
		if p.AuthKind != "" && p.AuthKind != "api_key" {
			expected := ""
			if p.AuthKind == "oauth-openai" {
				expected = "https://chatgpt.com/backend-api/codex"
			} else if p.AuthKind == "oauth-antigravity" {
				expected = "https://daily-cloudcode-pa.googleapis.com"
			} else {
				return State{}, errors.New("不支持的认证方式")
			}
			if base != expected {
				return State{}, errors.New("OAuth 地址不可修改，避免授权令牌发送到其他服务")
			}
			if strings.TrimSpace(req.APIKey) != "" {
				return State{}, errors.New("OAuth 渠道不使用手动 API Key")
			}
			p.AuthRequired = false
			next.Keys[p.ID] = ""
		}
		for _, old := range m.data.State.Providers {
			if old.ID == p.ID && old.BaseURL != p.BaseURL && old.HasAPIKey && strings.TrimSpace(req.APIKey) == "" && !req.ClearAPIKey {
				return State{}, errors.New("修改接口地址时请重新输入或清除原密钥")
			}
		}
		if req.ClearAPIKey {
			next.Keys[p.ID] = ""
		} else {
			next.Keys[p.ID] = m.data.Keys[p.ID]
		}
		if key := strings.TrimSpace(req.APIKey); key != "" {
			enc, err := m.box.Encrypt(key)
			if err != nil {
				return State{}, err
			}
			next.Keys[p.ID] = enc
		}
		if strings.HasPrefix(p.AuthKind, "oauth-") {
			next.Keys[p.ID] = ""
		}
		p.HasAPIKey = next.Keys[p.ID] != ""
		if p.AuthRequired && !p.HasAPIKey {
			return State{}, fmt.Errorf("渠道 %s 需要 API Key", p.Name)
		}
		models := []string{}
		ids := map[string]bool{}
		for _, v := range p.Models {
			v = strings.TrimSpace(v)
			if v != "" && !ids[v] {
				models = append(models, v)
				ids[v] = true
			}
		}
		p.Models = models
		hidden := []string{}
		hiddenSeen := map[string]bool{}
		for _, v := range p.HiddenModels {
			if ids[v] && !hiddenSeen[v] {
				hidden = append(hidden, v)
				hiddenSeen[v] = true
			}
		}
		p.HiddenModels = hidden
		next.State.Providers = append(next.State.Providers, p)
	}
	if next.State.BotRoutes == nil {
		next.State.BotRoutes = map[string]Route{}
	}
	for label, route := range map[string]Route{"主控": in.Default, "协作": in.Worker, "审批": in.Approval.Route} {
		required := label == "主控" && len(in.Providers) > 0 || label == "审批" && in.Approval.Mode == "model"
		if err := validateRoute(next.State, route, required); err != nil {
			return State{}, fmt.Errorf("%s：%w", label, err)
		}
	}
	for _, r := range next.State.BotRoutes {
		if err := validateRoute(next.State, r, false); err != nil {
			return State{}, err
		}
	}
	mode, ok := NormalizeMode(in.Approval.Mode)
	if !ok {
		return State{}, errors.New("无效的审批模式")
	}
	next.State.Approval.Mode = mode
	e := in.Execution
	if e.TimeoutSeconds < 5 || e.TimeoutSeconds > 180 || e.CommandTimeoutSeconds < 5 || e.CommandTimeoutSeconds > 300 || e.MaxSteps < 1 || e.MaxSteps > 12 || e.ContextMessages < 2 || e.ContextMessages > 100 {
		return State{}, errors.New("执行参数超出范围")
	}
	if err := m.persist(next); err != nil {
		return State{}, err
	}
	m.data = next
	return clone(next.State), nil
}

// SetApprovalMode stores only the approval mode, keeping every other setting.
// It is used by the first-login choice, the API and OF_APPROVAL_MODE.
func (m *Manager) SetApprovalMode(mode string) (State, error) {
	mode, ok := NormalizeMode(mode)
	if !ok || mode == ModeUnset {
		return State{}, errors.New("无效的审批模式")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := diskState{State: clone(m.data.State), Keys: map[string]string{}}
	for k, v := range m.data.Keys {
		next.Keys[k] = v
	}
	if mode == ModeModel {
		if err := validateRoute(next.State, next.State.Approval.Route, true); err != nil {
			return State{}, fmt.Errorf("替我审批需要先选择独立审批模型：%w", err)
		}
	}
	next.State.Approval.Mode = mode
	if err := m.persist(next); err != nil {
		return State{}, err
	}
	m.data = next
	return clone(next.State), nil
}

func validateRoute(s State, r Route, required bool) error {
	if r.ProviderID == "" && r.Model == "" && !required {
		return nil
	}
	if strings.TrimSpace(r.Model) == "" || r.ProviderID == "" {
		return errors.New("请选择渠道并填写模型")
	}
	for _, p := range s.Providers {
		if p.ID == r.ProviderID {
			if !p.Enabled {
				return errors.New("所选渠道已停用")
			}
			return nil
		}
	}
	return errors.New("所选渠道不存在")
}
func (m *Manager) persist(data diskState) error {
	if m.path == "" {
		return nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	enc, err := m.box.Encrypt(string(raw))
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(m.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(m.path), ".settings-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.WriteString(enc); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}
func (m *Manager) Seed(ctx context.Context, db store.Store) error {
	if len(m.Snapshot().Providers) > 0 {
		return nil
	}
	bots, err := db.ListBots(ctx)
	if err != nil {
		return err
	}
	if len(bots) == 0 || bots[0].BaseURL == "" {
		return nil
	}
	b := bots[0]
	key := ""
	if b.APIKeySecretID != "" {
		rec, err := db.GetSecret(ctx, b.APIKeySecretID)
		if err != nil {
			return err
		}
		key, err = m.box.Decrypt(rec.EncryptedValue)
		if err != nil {
			return err
		}
	}
	state := m.Snapshot()
	_, err = m.Save(Input{Providers: []ProviderInput{{Provider: Provider{ID: "primary", Name: "当前接口", BaseURL: b.BaseURL, Enabled: true, AuthRequired: key != "", Models: []string{b.ModelName}}, APIKey: key}}, Default: Route{ProviderID: "primary", Model: b.ModelName}, Approval: state.Approval, Execution: state.Execution})
	return err
}
func (m *Manager) Provider(id string) (Provider, error) {
	for _, p := range m.Snapshot().Providers {
		if p.ID == id {
			if !p.Enabled {
				return Provider{}, errors.New("渠道已停用")
			}
			return p, nil
		}
	}
	return Provider{}, errors.New("渠道不存在")
}
func (m *Manager) RouteFor(bot model.Bot, worker bool) Route {
	s := m.Snapshot()
	if r, ok := s.BotRoutes[bot.ID]; ok && r.ProviderID != "" {
		return r
	}
	if worker && bot.BaseURL != "" {
		return Route{}
	}
	if worker && s.Worker.ProviderID != "" {
		return s.Worker
	}
	return s.Default
}
func (m *Manager) Redact(text string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, enc := range m.data.Keys {
		if enc != "" {
			plain, err := m.box.Decrypt(enc)
			if err == nil && plain != "" {
				text = strings.ReplaceAll(text, plain, "***")
			}
		}
	}
	return text
}

// Frozen pins routing, policy and credentials for the lifetime of one user turn.
func (m *Manager) Frozen() *Manager {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := map[string]string{}
	for k, v := range m.data.Keys {
		keys[k] = v
	}
	return &Manager{box: m.box, data: diskState{State: clone(m.data.State), Keys: keys}}
}

// BindBot changes only the new bot's route, preserving concurrent settings edits.
func (m *Manager) BindBot(id string, route Route) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := validateRoute(m.data.State, route, false); err != nil {
		return err
	}
	if route.ProviderID != "" {
		found := false
		for _, p := range m.data.State.Providers {
			if p.ID == route.ProviderID {
				for _, hidden := range p.HiddenModels {
					if hidden == route.Model {
						return errors.New("所选模型已隐藏，请重新选择")
					}
				}
				for _, name := range p.Models {
					if name == route.Model {
						found = true
					}
				}
			}
		}
		if !found {
			return errors.New("所选模型不在渠道目录，请重新拉取模型")
		}
	}
	next := diskState{State: clone(m.data.State), Keys: m.data.Keys}
	if next.State.BotRoutes == nil {
		next.State.BotRoutes = map[string]Route{}
	}
	if route.ProviderID == "" {
		delete(next.State.BotRoutes, id)
	} else {
		next.State.BotRoutes[id] = route
	}
	if err := m.persist(next); err != nil {
		return err
	}
	m.data = next
	return nil
}

// ValidateSelectable validates a newly selected route without changing saved settings.
func (m *Manager) ValidateSelectable(route Route) error {
	f := m.Frozen()
	return f.BindBot("validation", route)
}
func (m *Manager) BotEnabled(id string) bool {
	for _, v := range m.Snapshot().DisabledBots {
		if v == id {
			return false
		}
	}
	return true
}
func (m *Manager) SetBotEnabled(id string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := diskState{State: clone(m.data.State), Keys: m.data.Keys}
	next.State.DisabledBots = []string{}
	for _, v := range m.data.State.DisabledBots {
		if v != id {
			next.State.DisabledBots = append(next.State.DisabledBots, v)
		}
	}
	if !enabled {
		next.State.DisabledBots = append(next.State.DisabledBots, id)
	}
	if err := m.persist(next); err != nil {
		return err
	}
	m.data = next
	return nil
}
