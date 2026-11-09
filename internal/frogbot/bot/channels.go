package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"onefrog/internal/frogbot/onefrog"
)

type APIChannel struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	BaseURL      string   `json:"base_url"`
	Enabled      bool     `json:"enabled"`
	AuthRequired bool     `json:"auth_required"`
	APIKey       *string  `json:"api_key,omitempty"`
	HasAPIKey    bool     `json:"has_api_key"`
	Models       []string `json:"models"`
	HiddenModels []string `json:"hidden_models"`
}

type storedChannel struct {
	APIChannel
	Sealed string `json:"sealed_api_key,omitempty"`
	key    string
}

type channelFile struct {
	Providers []storedChannel `json:"providers"`
	DefaultID string          `json:"default_provider_id"`
	Model     string          `json:"model"`
}

func (b *Bot) channelPath() string { return filepath.Join(b.cfg.DataDir, "channels.json") }

func (b *Bot) loadChannels() error {
	raw, err := os.ReadFile(b.channelPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f channelFile
	if err = json.Unmarshal(raw, &f); err != nil {
		return err
	}
	b.model = newConnection("", "", "")
	for i := range f.Providers {
		if f.Providers[i].Sealed != "" {
			f.Providers[i].key, err = b.vault.OpenAPIKey(f.Providers[i].Sealed)
			if err != nil {
				return err
			}
		}
		if f.Providers[i].ID == f.DefaultID {
			b.model = newConnection(f.Providers[i].BaseURL, f.Model, f.Providers[i].key)
		}
	}
	b.channels, b.defaultChannel = f.Providers, f.DefaultID
	return nil
}

func (b *Bot) publicChannels() []APIChannel {
	channels := make([]APIChannel, 0, len(b.channels))
	for _, p := range b.channels {
		c := p.APIChannel
		c.APIKey = nil
		c.HasAPIKey = p.key != ""
		channels = append(channels, c)
	}
	return channels
}

// catalogKey 决定这次请求用哪个 Key：请求里带了就用请求里的；否则只在渠道 ID 和地址都没变时
// 复用已保存的 Key（改了地址就不会把旧 Key 发给新地址）。没指定渠道时按默认渠道算。
func (b *Bot) catalogKey(d APISettings, url string) string {
	if d.APIKey != nil {
		return strings.TrimSpace(*d.APIKey)
	}
	id := d.ProviderID
	if id == "" {
		id = b.defaultChannel
	}
	for _, p := range b.channels {
		if p.ID == id && p.BaseURL == url {
			return p.key
		}
	}
	return ""
}

func (b *Bot) saveChannels(d APISettings) (APISettings, error) {
	b.modelMu.Lock()
	defer b.modelMu.Unlock()
	return b.saveChannelsLocked(d)
}

func (b *Bot) saveChannelsLocked(d APISettings) (APISettings, error) {
	if len(d.Providers) > 32 {
		return APISettings{}, errors.New("最多添加 32 个渠道")
	}
	providers := make([]storedChannel, 0, len(d.Providers))
	seen := map[string]bool{}
	var selected *storedChannel
	for _, c := range d.Providers {
		c.ID, c.Name = strings.TrimSpace(c.ID), strings.TrimSpace(c.Name)
		if c.ID == "" || c.Name == "" || seen[c.ID] {
			return APISettings{}, errors.New("渠道名称和唯一标识不能为空或重复")
		}
		seen[c.ID] = true
		url, err := normalizeBaseURL(c.BaseURL)
		if err != nil {
			return APISettings{}, err
		}
		c.BaseURL = url
		key := b.catalogKey(APISettings{ProviderID: c.ID, APIKey: c.APIKey}, url)
		if !c.AuthRequired {
			key = ""
		}
		c.APIKey = nil
		c.HasAPIKey = key != ""
		if c.Models == nil {
			c.Models = []string{}
		}
		if c.HiddenModels == nil {
			c.HiddenModels = []string{}
		}
		p := storedChannel{APIChannel: c, key: key}
		if key != "" {
			p.Sealed, err = b.vault.SealAPIKey(key)
			if err != nil {
				return APISettings{}, err
			}
		}
		providers = append(providers, p)
	}
	for i := range providers {
		if providers[i].ID == d.DefaultProviderID {
			selected = &providers[i]
		}
	}
	model := strings.TrimSpace(d.Model)
	if len(providers) == 0 {
		d.DefaultProviderID, model = "", ""
	} else if selected == nil || !selected.Enabled || model == "" || slices.Contains(selected.HiddenModels, model) || !slices.Contains(selected.Models, model) {
		return APISettings{}, errors.New("请为默认模型选择已启用渠道中的可用模型")
	}
	f := channelFile{Providers: providers, DefaultID: d.DefaultProviderID, Model: model}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return APISettings{}, err
	}
	tmp, err := os.CreateTemp(b.cfg.DataDir, "channels-*.tmp")
	if err != nil {
		return APISettings{}, err
	}
	defer os.Remove(tmp.Name())
	chmodErr := tmp.Chmod(0o600)
	_, writeErr := tmp.Write(raw)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err = errors.Join(chmodErr, writeErr, syncErr, closeErr); err != nil {
		return APISettings{}, fmt.Errorf("保存渠道配置失败: %w", err)
	}
	if err = os.Rename(tmp.Name(), b.channelPath()); err != nil {
		return APISettings{}, err
	}
	b.channels, b.defaultChannel = providers, d.DefaultProviderID
	b.model = newConnection("", "", "")
	if selected != nil {
		b.model = newConnection(selected.BaseURL, model, selected.key)
	}
	s := b.model.public()
	s.Providers = b.publicChannels()
	s.DefaultProviderID = d.DefaultProviderID
	return s, nil
}

func (b *Bot) TestConnection(ctx context.Context, d APISettings) (int64, error) {
	url, err := normalizeBaseURL(d.BaseURL)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(d.Model) == "" {
		return 0, errors.New("请选择测试模型")
	}
	b.modelMu.RLock()
	key := b.catalogKey(d, url)
	b.modelMu.RUnlock()
	p := onefrog.NewOpenAI(url, key)
	p.Client.CheckRedirect = noRedirect
	p.MaxRetries = 0
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	start := time.Now()
	_, err = p.Chat(ctx, onefrog.ChatRequest{Model: d.Model, Messages: []onefrog.Message{{Role: "user", Content: "Reply OK."}}}, nil)
	if err != nil {
		return 0, errors.New("测试连接失败，请检查接口、密钥和模型是否可用")
	}
	return time.Since(start).Milliseconds(), nil
}
