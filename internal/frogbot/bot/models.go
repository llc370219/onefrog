package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"onefrog/internal/frogbot/onefrog"
	"onefrog/internal/frogbot/vault"
)

// APISettings describes the connection shared by chat and background tasks.
// APIKey is write-only: omitted keeps the key for the same URL; empty clears it.
type APISettings struct {
	BaseURL           string       `json:"base_url"`
	Model             string       `json:"model"`
	APIKey            *string      `json:"api_key,omitempty"`
	HasAPIKey         bool         `json:"has_api_key"`
	Providers         []APIChannel `json:"providers,omitempty"`
	DefaultProviderID string       `json:"default_provider_id,omitempty"`
	ProviderID        string       `json:"provider_id,omitempty"`
	// EnvOverride 表示模型连接由环境变量（BASE_URL / API_KEY / CHAT_MODEL / AGENT_MODEL）提供，
	// 优先于网页里保存的渠道；网页上的修改要等去掉环境变量后才生效。
	EnvOverride bool `json:"env_override,omitempty"`
}

type modelConnection struct {
	baseURL, model, key string
	provider            onefrog.Provider
}

// 模型配置只有一个来源：data/channels.json（API Key 用主密钥加密）。环境变量可以临时覆盖，但不写盘。
// 旧版本的配置在第一次启动时迁移进 channels.json，原文件改名为 *.migrated：
//   - v0.1.1-beta：data/config/model.json + data/config/secrets.json（MODEL_API_KEY）
//   - 开发分支上短暂存在过的 data/settings.json（base_url / model / sealed_api_key）
func (b *Bot) loadSettings() error {
	if b.cfg.InitialBaseURL != "" {
		model := b.cfg.ChatModel
		if model == "" {
			model = b.cfg.AgentModel
		}
		if model != "" {
			b.envModel = newConnection(b.cfg.InitialBaseURL, model, b.cfg.InitialKey)
		}
	}
	if _, err := os.Stat(b.channelPath()); err == nil {
		return b.loadChannels()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return b.migrateSettings()
}

type oldSettingsFile struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	Sealed  string `json:"sealed_api_key,omitempty"`
}

type betaModelFile struct {
	BaseURL    string `json:"base_url"`
	ChatModel  string `json:"chat_model"`
	AgentModel string `json:"agent_model"`
}

func (b *Bot) migrateSettings() error {
	var baseURL, key string
	var models []string
	var old []string // 迁移成功后改名的文件
	settings := filepath.Join(b.cfg.DataDir, "settings.json")
	betaDir := filepath.Join(b.cfg.DataDir, "config")
	if raw, err := os.ReadFile(settings); err == nil {
		var f oldSettingsFile
		if err := json.Unmarshal(raw, &f); err != nil {
			return fmt.Errorf("%s 格式不对: %w", settings, err)
		}
		if f.Sealed != "" {
			if key, err = b.vault.OpenAPIKey(f.Sealed); err != nil {
				return err
			}
		}
		baseURL, models, old = f.BaseURL, []string{f.Model}, []string{settings}
	} else if raw, err := os.ReadFile(filepath.Join(betaDir, "model.json")); err == nil {
		var f betaModelFile
		if err := json.Unmarshal(raw, &f); err != nil {
			return fmt.Errorf("data/config/model.json 格式不对: %w", err)
		}
		mk, _, err := vault.LoadKey(b.cfg.MasterKey, b.cfg.DataDir)
		if err != nil {
			return err
		}
		v, err := vault.Open(betaDir, mk)
		if err != nil {
			return fmt.Errorf("读取 v0.1.1-beta 的模型密钥失败: %w", err)
		}
		if v.Has("MODEL_API_KEY") {
			vals, err := v.Values([]string{"MODEL_API_KEY"})
			if err != nil {
				return err
			}
			key = vals["MODEL_API_KEY"]
		}
		baseURL, models = f.BaseURL, []string{f.ChatModel, f.AgentModel}
		old = []string{filepath.Join(betaDir, "model.json"), filepath.Join(betaDir, "secrets.json")}
	} else {
		return nil // 全新安装
	}
	models = slices.DeleteFunc(models, func(m string) bool { return strings.TrimSpace(m) == "" })
	models = slices.Compact(models)
	if strings.TrimSpace(baseURL) == "" || len(models) == 0 {
		return nil
	}
	if _, err := b.saveChannelsLocked(APISettings{DefaultProviderID: "primary", Model: models[0], Providers: []APIChannel{{
		ID: "primary", Name: "主力 API", BaseURL: baseURL, Enabled: true, AuthRequired: key != "", APIKey: &key, Models: models,
	}}}); err != nil {
		return fmt.Errorf("迁移旧的模型设置失败: %w", err)
	}
	for _, p := range old {
		if err := os.Rename(p, p+".migrated"); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("旧设置已迁移到 channels.json，但没能改名 %s：%v", p, err)
		}
	}
	log.Printf("已把旧的模型设置迁移到 %s", b.channelPath())
	return nil
}

func newConnection(baseURL, model, key string) modelConnection {
	if normalized, err := normalizeBaseURL(baseURL); err == nil {
		baseURL = normalized
	}
	p := onefrog.NewOpenAI(baseURL, key)
	p.Client.CheckRedirect = noRedirect
	return modelConnection{baseURL: baseURL, model: model, key: key, provider: p}
}

func noRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("请输入有效的 HTTP 或 HTTPS 接口地址，地址中不要包含密钥、查询参数或片段")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	for _, endpoint := range []string{"/chat/completions", "/models"} {
		if strings.HasSuffix(u.Path, endpoint) {
			u.Path = strings.TrimSuffix(u.Path, endpoint)
			break
		}
	}
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func (b *Bot) resolveModel(chat bool) (onefrog.Provider, string) {
	if b.cfg.Host != nil { // 嵌入模式：每个 Bot 的模型由宿主按 Bot 提供
		if chat {
			return b.cfg.ChatProvider, b.cfg.ChatModel
		}
		return b.cfg.AgentProvider, b.cfg.AgentModel
	}
	if b.modelOwner != nil {
		return b.modelOwner.resolveModel(chat)
	}
	b.modelMu.RLock()
	defer b.modelMu.RUnlock()
	if b.envModel.provider != nil {
		return b.envModel.provider, b.envModel.model
	}
	if b.model.provider != nil {
		if b.model.model == "" {
			return nil, ""
		}
		return b.model.provider, b.model.model
	}
	if chat {
		return b.cfg.ChatProvider, b.cfg.ChatModel
	}
	return b.cfg.AgentProvider, b.cfg.AgentModel
}

type settingsReviewer struct {
	bot   *Bot
	model string
}

func (r settingsReviewer) Review(ctx context.Context, req onefrog.ReviewRequest) (onefrog.Verdict, error) {
	provider, _ := r.bot.resolveModel(false)
	if provider == nil {
		return onefrog.Verdict{}, errors.New("尚未配置模型 API")
	}
	reviewer := onefrog.LLMReviewer{Provider: provider, Model: r.model}
	return reviewer.Review(ctx, req)
}

func (b *Bot) GetSettings() APISettings {
	b.modelMu.RLock()
	defer b.modelMu.RUnlock()
	s := b.model.public()
	s.Providers = b.publicChannels()
	s.DefaultProviderID = b.defaultChannel
	if s.DefaultProviderID == "" && len(s.Providers) > 0 {
		s.DefaultProviderID = s.Providers[0].ID
	}
	s.EnvOverride = b.envModel.provider != nil
	return s
}

func (m modelConnection) public() APISettings {
	return APISettings{BaseURL: m.baseURL, Model: m.model, HasAPIKey: m.key != ""}
}

// SaveSettings 保存模型设置。网页总是提交完整的渠道列表；只带 base_url / model / api_key 的旧式请求
// 会更新默认渠道（没有渠道时新建一个「主力 API」）。两种请求最后都写进 channels.json。
func (b *Bot) SaveSettings(draft APISettings) (APISettings, error) {
	if draft.Providers != nil {
		return b.saveChannels(draft)
	}
	baseURL, err := normalizeBaseURL(draft.BaseURL)
	if err != nil {
		return APISettings{}, err
	}
	model := strings.TrimSpace(draft.Model)
	if model == "" {
		return APISettings{}, errors.New("请输入模型 ID")
	}
	b.modelMu.Lock()
	defer b.modelMu.Unlock()
	key := b.catalogKey(draft, baseURL)
	providers := b.publicChannels()
	id := b.defaultChannel
	idx := slices.IndexFunc(providers, func(p APIChannel) bool { return p.ID == id })
	if idx < 0 {
		id = "primary"
		if idx = slices.IndexFunc(providers, func(p APIChannel) bool { return p.ID == id }); idx < 0 {
			providers = append(providers, APIChannel{ID: id, Name: "主力 API"})
			idx = len(providers) - 1
		}
	}
	p := &providers[idx]
	p.BaseURL, p.Enabled, p.AuthRequired, p.APIKey = baseURL, true, key != "", &key
	if !slices.Contains(p.Models, model) {
		p.Models = append(slices.Clone(p.Models), model)
	}
	p.HiddenModels = slices.DeleteFunc(slices.Clone(p.HiddenModels), func(m string) bool { return m == model })
	return b.saveChannelsLocked(APISettings{Providers: providers, DefaultProviderID: id, Model: model})
}

var modelCatalogClient = &http.Client{Timeout: 15 * time.Second, CheckRedirect: noRedirect}

func (b *Bot) FetchRemoteModels(ctx context.Context, draft APISettings) ([]string, error) {
	baseURL, err := normalizeBaseURL(draft.BaseURL)
	if err != nil {
		return nil, err
	}
	b.modelMu.RLock()
	key := b.catalogKey(draft, baseURL)
	b.modelMu.RUnlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, errors.New("无法创建模型列表请求")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := modelCatalogClient.Do(req)
	if err != nil {
		return nil, errors.New("无法连接模型服务，请检查接口地址和网络")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("模型服务拒绝访问（HTTP %d），请检查 API Key", resp.StatusCode)
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return nil, errors.New("此地址未提供模型列表，请检查接口地址或手动填写模型 ID")
	default:
		return nil, fmt.Errorf("读取模型列表失败（HTTP %d）", resp.StatusCode)
	}
	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&data); err != nil || data.Data == nil {
		return nil, errors.New("模型列表格式不兼容，请手动填写模型 ID")
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, m := range data.Data {
		if id := strings.TrimSpace(m.ID); id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	slices.Sort(ids)
	return ids, nil
}
