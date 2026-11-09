package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"onefrog/internal/frogbot/onefrog"
	"onefrog/internal/frogbot/vault"
)

func TestSettingsFlow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantAuth := "Bearer test-key"
		if strings.HasPrefix(r.URL.Path, "/new/") {
			wantAuth = ""
		}
		if r.Header.Get("Authorization") != wantAuth {
			t.Error("request used the wrong credentials")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/models", "/new/models":
			fmt.Fprint(w, `{"data":[{"id":"test-model"},{"id":"test-model"}]}`)
		case "/v1/chat/completions", "/new/chat/completions":
			var body struct{ Model string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "test-model" {
				t.Errorf("unexpected model: %+v (%v)", body, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"model replied\"}}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws")}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	post := func(b *Bot, path string, payload APISettings) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		b.Handler("").ServeHTTP(w, req)
		return w
	}

	t.Run("first-run setup and both model paths", func(t *testing.T) {
		if s := b.GetSettings(); s.BaseURL != "" || s.HasAPIKey {
			t.Fatal("expected empty settings on first run")
		}
		key := "test-key"
		draft := APISettings{BaseURL: upstream.URL + "/v1/chat/completions/", APIKey: &key}
		w := post(b, "/api/settings/models", draft)
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"models":["test-model"]}` {
			t.Fatalf("model catalog: %d %s", w.Code, w.Body)
		}
		draft.Model = " test-model "
		w = post(b, "/api/settings", draft)
		if w.Code != 200 || strings.Contains(w.Body.String(), key) || !b.GetSettings().HasAPIKey {
			t.Fatalf("save failed or exposed key: HTTP %d", w.Code)
		}
		file, err := os.ReadFile(b.channelPath())
		if err != nil || bytes.Contains(file, []byte(key)) {
			t.Fatal("settings should be persisted without the plaintext key")
		}
		b.runTurn([]Message{{Role: "user", Text: "hello"}})
		if messages := b.store.Messages(); len(messages) == 0 || messages[len(messages)-1].Text != "model replied" {
			t.Fatal("chat did not use the saved API")
		}
		res, err := b.mgr.Agent.Run(context.Background(), onefrog.Task{Input: "work", Workspace: cfg.Workspace})
		if err != nil || res.Output != "model replied" {
			t.Fatalf("agent did not use the saved API: %+v (%v)", res, err)
		}
	})
	b.Close()

	b, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if s := b.GetSettings(); s.BaseURL != upstream.URL+"/v1" || s.Model != "test-model" || !s.HasAPIKey {
		t.Fatalf("incorrect settings after restart: %+v", s)
	}
	if w := post(b, "/api/settings/models", b.GetSettings()); w.Code != 200 {
		t.Fatalf("saved key wasn't reused for the original address: %d %s", w.Code, w.Body)
	}

	// A changed address never receives the original key, for discovery or chat.
	replacement := APISettings{BaseURL: upstream.URL + "/new", Model: "test-model"}
	if w := post(b, "/api/settings/models", replacement); w.Code != 200 {
		t.Fatalf("model catalog at the new address: %d %s", w.Code, w.Body)
	}
	if w := post(b, "/api/settings", replacement); w.Code != 200 || b.GetSettings().HasAPIKey {
		t.Fatalf("saving the new address should drop the previous key: HTTP %d", w.Code)
	}
	provider, model := b.resolveModel(true)
	if _, err := provider.Chat(context.Background(), onefrog.ChatRequest{Model: model}, nil); err != nil {
		t.Fatalf("replacement connection: %v", err)
	}
}

// 开发分支上短暂存在过的 data/settings.json 迁移进 channels.json，之后只读写 channels.json。
func TestSettingsJSONMigratesToChannels(t *testing.T) {
	dir := t.TempDir()
	b, err := New(Config{DataDir: dir, Workspace: filepath.Join(dir, "ws")})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := b.vault.SealAPIKey("old-key")
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	raw, _ := json.Marshal(map[string]string{"base_url": "https://old.example/v1/", "model": "old-model", "sealed_api_key": sealed})
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // 第二次启动直接读 channels.json
		b, err = New(Config{DataDir: dir, Workspace: filepath.Join(dir, "ws")})
		if err != nil {
			t.Fatal(err)
		}
		s := b.GetSettings()
		if s.BaseURL != "https://old.example/v1" || s.Model != "old-model" || !s.HasAPIKey || len(s.Providers) != 1 || s.Providers[0].ID != "primary" {
			t.Fatalf("run %d: settings not migrated: %+v", i, s)
		}
		if _, m := b.resolveModel(true); m != "old-model" {
			t.Fatalf("run %d: model %q", i, m)
		}
		b.Close()
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("old settings.json should be renamed after migration")
	}
	if file, _ := os.ReadFile(filepath.Join(dir, "channels.json")); bytes.Contains(file, []byte("old-key")) {
		t.Fatal("channels.json contains the plaintext key")
	}
}

// v0.1.1-beta 把模型设置存在 data/config/model.json，Key 在 data/config/secrets.json（同一把主密钥）。
func TestBetaConfigMigratesToChannels(t *testing.T) {
	dir := t.TempDir()
	key, _, err := vault.LoadKey("", dir)
	if err != nil {
		t.Fatal(err)
	}
	cfgDir := filepath.Join(dir, "config")
	v, err := vault.Open(cfgDir, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set("MODEL_API_KEY", "beta-key", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "model.json"), []byte(`{"base_url":"https://beta.example/v1","chat_model":"fast","agent_model":"smart"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{DataDir: dir, Workspace: filepath.Join(dir, "ws")})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	s := b.GetSettings()
	if s.BaseURL != "https://beta.example/v1" || s.Model != "fast" || !s.HasAPIKey || len(s.Providers) != 1 ||
		strings.Join(s.Providers[0].Models, ",") != "fast,smart" {
		t.Fatalf("beta settings not migrated: %+v", s)
	}
	b.modelMu.RLock()
	got := b.model.key
	b.modelMu.RUnlock()
	if got != "beta-key" {
		t.Fatal("beta API key was not carried over")
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "model.json.migrated")); err != nil {
		t.Fatal("beta model.json should be renamed")
	}
}

// 环境变量提供的模型优先，但不写进 channels.json。
func TestEnvOverridesSavedChannel(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DataDir: dir, Workspace: filepath.Join(dir, "ws")}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.SaveSettings(APISettings{BaseURL: "https://saved.example/v1", Model: "saved"}); err != nil {
		t.Fatal(err)
	}
	b.Close()
	cfg.InitialBaseURL, cfg.InitialKey, cfg.ChatModel = "https://env.example/v1", "env-key", "env-model"
	b, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, m := b.resolveModel(false); m != "env-model" {
		t.Fatalf("env model not used: %q", m)
	}
	s := b.GetSettings()
	if !s.EnvOverride || s.Model != "saved" {
		t.Fatalf("settings view: %+v", s)
	}
	if file, _ := os.ReadFile(b.channelPath()); bytes.Contains(file, []byte("env")) {
		t.Fatal("env connection was persisted")
	}
}
