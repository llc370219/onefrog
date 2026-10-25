package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"onefrog/internal/model"
	"onefrog/internal/preferences"
)

func (s *Server) getSettings(c *gin.Context) {
	if err := s.Settings.Seed(c.Request.Context(), s.Store); mapStore(c, err) {
		return
	}
	state := s.Settings.Snapshot()
	// Compatibility for clients that still read the original three fields.
	out := map[string]any{}
	raw, _ := json.Marshal(state)
	_ = json.Unmarshal(raw, &out)
	out["base_url"] = ""
	out["model_name"] = ""
	out["has_api_key"] = false
	if p, err := s.Settings.Provider(state.Default.ProviderID); err == nil {
		out["base_url"] = p.BaseURL
		out["model_name"] = state.Default.Model
		out["has_api_key"] = p.HasAPIKey
	}
	c.JSON(200, out)
}
func (s *Server) putSettings(c *gin.Context) {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 512<<10))
	if err != nil {
		fail(c, 400, "请求无效")
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		fail(c, 400, "请求无效")
		return
	}
	if _, ok := fields["providers"]; !ok {
		var legacy settingsReq
		if json.Unmarshal(raw, &legacy) != nil {
			fail(c, 400, "请求无效")
			return
		}
		state := s.Settings.Snapshot()
		pid := state.Default.ProviderID
		if pid == "" {
			pid = "primary"
		}
		inputs := []preferences.ProviderInput{}
		for _, p := range state.Providers {
			inputs = append(inputs, preferences.ProviderInput{Provider: p})
		}
		found := false
		for i := range inputs {
			if inputs[i].ID == pid {
				inputs[i].BaseURL = legacy.BaseURL
				inputs[i].Models = []string{legacy.ModelName}
				inputs[i].APIKey = legacy.APIKey
				found = true
			}
		}
		if !found {
			inputs = append(inputs, preferences.ProviderInput{Provider: preferences.Provider{ID: pid, Name: "当前接口", BaseURL: legacy.BaseURL, Enabled: true, AuthRequired: true, Models: []string{legacy.ModelName}}, APIKey: legacy.APIKey})
		}
		_, e := s.Settings.Save(preferences.Input{Providers: inputs, Default: preferences.Route{ProviderID: pid, Model: legacy.ModelName}, Worker: state.Worker, BotRoutes: state.BotRoutes, Approval: state.Approval, Execution: state.Execution})
		if e != nil {
			fail(c, 400, e.Error())
			return
		}
		s.getSettings(c)
		return
	}
	var in preferences.Input
	if json.Unmarshal(raw, &in) != nil {
		fail(c, 400, "请求无效")
		return
	}
	// Keep keys bound to their destination: an address change needs a new key.
	old := s.Settings.Snapshot()
	for _, p := range in.Providers {
		for _, saved := range old.Providers {
			if p.ID == saved.ID && strings.TrimRight(strings.TrimSpace(p.BaseURL), "/") != saved.BaseURL && saved.HasAPIKey && strings.TrimSpace(p.APIKey) == "" && !p.ClearAPIKey {
				fail(c, 400, "修改接口地址时请重新输入密钥，或清除原密钥")
				return
			}
		}
	}
	state, err := s.Settings.Save(in)
	if err != nil {
		fail(c, 400, err.Error())
		return
	}
	c.JSON(200, state)
}

type providerProbe struct {
	preferences.ProviderInput
	Model string `json:"model"`
}

func (s *Server) probe(c *gin.Context) (providerProbe, string, bool) {
	var req providerProbe
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求无效")
		return req, "", false
	}
	base, err := preferences.ValidateURL(req.BaseURL)
	if err != nil {
		fail(c, 400, err.Error())
		return req, "", false
	}
	req.BaseURL = base
	key := strings.TrimSpace(req.APIKey)
	if key == "" && !req.ClearAPIKey {
		for _, p := range s.Settings.Snapshot().Providers {
			if p.ID == req.ID && p.BaseURL == req.BaseURL {
				key, err = s.Settings.Key(p.ID)
				if err != nil {
					fail(c, 500, "密钥无法解密")
					return req, "", false
				}
			}
		}
	}
	if req.AuthRequired && key == "" {
		fail(c, 400, "请输入 API Key")
		return req, "", false
	}
	return req, key, true
}
func providerHTTP(ctx context.Context, method, url, key string, body any) ([]byte, int, error) {
	var data io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		data = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, data)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	return raw, res.StatusCode, err
}
func (s *Server) providerModels(c *gin.Context) {
	req, key, ok := s.probe(c)
	if !ok {
		return
	}
	if strings.HasPrefix(req.AuthKind, "oauth-") {
		if !s.validOAuthProbe(c, req) {
			return
		}
		models, e := s.OAuth.Models(c.Request.Context(), c.MustGet("user").(model.User).ID, req.AuthKind)
		if e != nil {
			fail(c, 400, e.Error())
			return
		}
		c.JSON(200, gin.H{"models": models})
		return
	}
	raw, status, err := providerHTTP(c.Request.Context(), "GET", req.BaseURL+"/models", key, nil)
	if err != nil {
		fail(c, 502, "无法连接模型目录，请检查地址和网络")
		return
	}
	if status != 200 {
		c.JSON(502, gin.H{"error": "模型目录返回非成功状态，请检查渠道兼容性", "upstream_status": status})
		return
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &list) != nil {
		fail(c, 502, "模型列表格式不兼容，请检查渠道协议")
		return
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, m := range list.Data {
		if m.ID != "" && !seen[m.ID] {
			ids = append(ids, m.ID)
			seen[m.ID] = true
		}
	}
	sort.Strings(ids)
	c.JSON(200, gin.H{"models": ids})
}
func (s *Server) testProvider(c *gin.Context) {
	req, key, ok := s.probe(c)
	if !ok {
		return
	}
	if strings.HasPrefix(req.AuthKind, "oauth-") {
		if !s.validOAuthProbe(c, req) {
			return
		}
		started := time.Now()
		_, e := s.OAuth.CompleteChat(c.Request.Context(), c.MustGet("user").(model.User).ID, req.AuthKind, req.Model, []map[string]any{{"role": "user", "content": "Reply OK"}}, nil)
		if e != nil {
			fail(c, 400, e.Error())
			return
		}
		c.JSON(200, gin.H{"ok": true, "model": req.Model, "latency_ms": time.Since(started).Milliseconds()})
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		fail(c, 400, "请先选择或输入测试模型")
		return
	}
	started := time.Now()
	raw, status, err := providerHTTP(c.Request.Context(), "POST", req.BaseURL+"/chat/completions", key, map[string]any{"model": req.Model, "messages": []map[string]string{{"role": "user", "content": "Reply only OK. Do not call tools."}}})
	if err != nil {
		fail(c, 502, "模型连接失败或超时")
		return
	}
	if status != 200 {
		c.JSON(502, gin.H{"error": "模型调用失败，请检查模型名称、权限和余额", "upstream_status": status})
		return
	}
	var reply struct {
		Choices []json.RawMessage `json:"choices"`
	}
	if json.Unmarshal(raw, &reply) != nil || len(reply.Choices) == 0 {
		fail(c, 502, "接口未返回有效的 Chat Completions 响应")
		return
	}
	c.JSON(200, gin.H{"ok": true, "latency_ms": time.Since(started).Milliseconds(), "model": req.Model})
}
