package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onefrog/internal/bot"
	frog "onefrog/internal/frogbot/bot"
	kernel "onefrog/internal/frogbot/onefrog"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

type replyAI struct{ text string }

func (r replyAI) Chat(_ context.Context, _ kernel.ChatRequest, onText func(string)) (kernel.Message, error) {
	if onText != nil {
		onText(r.text)
	}
	return kernel.Message{Role: "assistant", Content: r.text}, nil
}

// The single Frog Bot of v0.1.32/33 becomes the leader's runtime: its
// encrypted conversation is still there after the upgrade.
func TestFrogBotUpgradeKeepsLegacyConversation(t *testing.T) {
	mem := store.NewMemory()
	key := bytes.Repeat([]byte{4}, 32)
	box, _ := secret.New(key)
	dir := t.TempDir()
	owner := "u-legacy"
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("onefrog/frog-bot/state/" + owner))
	codec, err := secret.New(mac.Sum(nil))
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, hex.EncodeToString([]byte(owner)))
	vault := &panelVault{st: mem, box: box}
	old, err := frog.New(frog.Config{Host: &frog.Host{Vault: vault}, Codec: codec, Name: "Frog Bot",
		DataDir: data, Workspace: filepath.Join(data, "workspace"), ChatProvider: replyAI{"legacy-reply-marker"},
		AgentProvider: replyAI{"x"}, DisableShell: true, Policy: kernel.PolicyUntrusted})
	if err != nil {
		t.Fatal(err)
	}
	old.Start()
	h := old.Handler("")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/messages", strings.NewReader(`{"text":"legacy-question-marker"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("legacy send: %d %s", rec.Code, rec.Body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(replay(h, "/api/stream"), "legacy-reply-marker") {
		time.Sleep(50 * time.Millisecond)
	}
	old.Close()

	f := NewFrogBots(dir, key, &bot.Agent{Store: mem, Box: box}, nil)
	defer f.Close()
	acct, err := f.account(owner)
	if err != nil {
		t.Fatal(err)
	}
	got := replay(acct.handler, "/api/bots/main/stream")
	if !strings.Contains(got, "legacy-question-marker") || !strings.Contains(got, "legacy-reply-marker") {
		t.Fatalf("legacy conversation lost after upgrade: %.400s", got)
	}
}

func replay(h http.Handler, path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil).WithContext(ctx))
	return rec.Body.String()
}
