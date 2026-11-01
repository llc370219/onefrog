package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

func TestStepBudgetWritesHandoffAndContinueUsesIt(t *testing.T) {
	const plain = "budget-secret-plain"
	mem := store.NewMemory()
	box, err := secret.New(bytesRepeat())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = mem.CreateSecret(ctx, store.SecretRec{Secret: model.Secret{Name: "root-password"}, EncryptedValue: mustEncrypt(t, box, plain)}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err = mem.CreateBot(ctx, model.Bot{ID: "main", Name: "主控", BaseURL: "http://model.test/v1", ModelName: "demo", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	settings, err := preferences.New("", box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = settings.Save(preferences.Input{Approval: preferences.Approval{Mode: preferences.ModeOff}, Execution: preferences.Execution{TimeoutSeconds: 30, CommandTimeoutSeconds: 30, MaxSteps: 2, ContextMessages: 24}}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		n := len(bodies)
		mu.Unlock()
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		w.Header().Set("Content-Type", "application/json")
		_, hasTools := req["tools"]
		if n <= 2 {
			if !hasTools {
				t.Errorf("tool round %d had no tools", n)
			}
			_, _ = io.WriteString(w, toolJSON("c"+string(rune('0'+n)), "run_command", `{"server_id":"missing","command":"df -h","risk":"low","reason":"看磁盘"}`))
			return
		}
		if n == 3 {
			if hasTools {
				t.Errorf("summary call still offered tools")
			}
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"做到查看磁盘。磁盘还没看完。下一步执行 df -h。密码是 `+plain+`"}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"接着看 inode"}}]}`)
	}))
	defer srv.Close()
	main, _ := mem.GetBot(ctx, "main")
	main.BaseURL = srv.URL + "/v1"
	if _, err = mem.UpdateBot(ctx, main); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Store: mem, Box: box, HTTP: srv.Client(), Settings: settings}
	first, err := agent.Reply(ctx, "main", "", "", "看看磁盘")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first.Text, plain) {
		t.Fatal("pause summary leaked the vault secret")
	}
	if !strings.Contains(first.Text, "下一步执行 df -h") || !strings.Contains(first.Text, "df -h") || !strings.Contains(first.Text, "继续") {
		t.Fatalf("handoff = %q", first.Text)
	}
	second, err := agent.Reply(ctx, "main", "", first.SessionID, "继续啊")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 4 {
		t.Fatalf("calls %d", len(bodies))
	}
	if !strings.Contains(bodies[3], "下一步执行 df -h") || !strings.Contains(bodies[3], "df -h") {
		t.Fatal("continue turn did not see the handoff")
	}
	if strings.Contains(second.Text, plain) || strings.Contains(bodies[3], plain) {
		t.Fatal("continue turn leaked the vault secret")
	}
}

func TestStepBudgetFallsBackWhenSummaryFails(t *testing.T) {
	mem := store.NewMemory()
	box, err := secret.New(bytesRepeat())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = mem.CreateBot(ctx, model.Bot{ID: "main", Name: "主控", BaseURL: "http://model.test/v1", ModelName: "demo", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	settings, err := preferences.New("", box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = settings.Save(preferences.Input{Approval: preferences.Approval{Mode: preferences.ModeOff}, Execution: preferences.Execution{TimeoutSeconds: 30, CommandTimeoutSeconds: 30, MaxSteps: 1, ContextMessages: 24}}); err != nil {
		t.Fatal(err)
	}
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, toolJSON("c1", "run_command", `{"command":"df -h","risk":"low","reason":"看磁盘"}`))
			return
		}
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	main, _ := mem.GetBot(ctx, "main")
	main.BaseURL = srv.URL + "/v1"
	if _, err = mem.UpdateBot(ctx, main); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Store: mem, Box: box, HTTP: srv.Client(), Settings: settings}
	result, err := agent.Reply(ctx, "main", "", "", "看看磁盘")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "df -h") || !strings.Contains(result.Text, "找不到服务器") || !strings.Contains(result.Text, "继续") {
		t.Fatalf("fallback = %q", result.Text)
	}
}

func mustEncrypt(t *testing.T, box *secret.Box, plain string) string {
	t.Helper()
	enc, err := box.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	return enc
}
