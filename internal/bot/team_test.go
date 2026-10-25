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
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

type scriptSSH struct {
	command string
}

func (s *scriptSSH) Run(ctx context.Context, host string, port int, user, authType, secret, knownKey, command string) (string, string, error) {
	s.command = command
	return "seen:" + command, "", nil
}

func TestTeamHidesSecretWhileDelegating(t *testing.T) {
	const vault = "p@ss-vault-99"
	mem := store.NewMemory()
	box, err := secret.New(bytesRepeat())
	if err != nil {
		t.Fatal(err)
	}
	enc, err := box.Encrypt(vault)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := mem.CreateSecret(ctx, store.SecretRec{Secret: model.Secret{Name: "db-password"}, EncryptedValue: enc}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.CreateServer(ctx, store.ServerRec{Server: model.Server{ID: "web-01", Name: "web-01", Host: "10.0.0.8", Port: 22, Username: "root", AuthType: "password", Status: "online"}, HostKey: "SHA256:test"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := mem.CreateBot(ctx, model.Bot{ID: "main", Name: "主控", Description: "接收指令，分派任务", BaseURL: "http://model.test/v1", ModelName: "demo", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.CreateBot(ctx, model.Bot{ID: "check", Name: "巡检", Description: "看磁盘、证书和服务", CreatedAt: now.Add(time.Second)}); err != nil {
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
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			_, _ = io.WriteString(w, toolJSON("c1", "ask_agent", `{"bot_id":"巡检","task":"确认 web-01 能否用库密码连上"}`))
		case 2:
			_, _ = io.WriteString(w, toolJSON("c2", "run_command", `{"server_id":"web-01","command":"printf {{secret:db-password}}"}`))
		default:
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"可以连接"}}]}`)
		}
	}))
	defer srv.Close()

	main, _ := mem.GetBot(ctx, "main")
	main.BaseURL = srv.URL + "/v1"
	if _, err := mem.UpdateBot(ctx, main); err != nil {
		t.Fatal(err)
	}
	ssh := &scriptSSH{}
	agent := &Agent{Store: mem, Box: box, SSH: ssh, HTTP: srv.Client()}
	result, err := agent.Reply(ctx, "", "web-01", "", "让巡检确认数据库")
	if err != nil {
		t.Fatal(err)
	}
	if ssh.command != "printf "+vault {
		t.Fatal("secret was not expanded only at execution")
	}
	if strings.Contains(result.Text, vault) {
		t.Fatal("reply contained the vault secret")
	}
	var sawDelegate, sawTool bool
	for _, step := range result.Steps {
		if strings.Contains(step.Task, vault) || strings.Contains(step.Command, vault) || strings.Contains(step.Output, vault) {
			t.Fatal("step leaked the vault secret")
		}
		if step.Kind == "delegate" && step.Bot == "主控" && step.Target == "巡检" {
			sawDelegate = true
		}
		if step.Kind == "tool" && step.Bot == "巡检" {
			sawTool = true
			if step.Command != "printf {{secret:db-password}}" {
				t.Fatal("command shown to the model was expanded")
			}
			if step.Output != "seen:printf ***" || !step.OK {
				t.Fatalf("redacted output = %q ok=%v", step.Output, step.OK)
			}
		}
	}
	if !sawDelegate || !sawTool {
		t.Fatal("missing delegation or tool step")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 3 {
		t.Fatalf("model calls %d", len(bodies))
	}
	for i, body := range bodies {
		if strings.Contains(body, vault) {
			t.Fatalf("model request %d contained the vault secret", i+1)
		}
	}
	if !strings.Contains(bodies[0], "db-password") {
		t.Fatal("leader was not told the secret name")
	}
	if !strings.Contains(bodies[0], "ask_agent") || !strings.Contains(bodies[1], "run_command") {
		t.Fatal("tool sets were not split between leader and worker")
	}
	if strings.Contains(bodies[1], "ask_agent") {
		t.Fatal("worker can still delegate")
	}
	if !strings.Contains(result.Text, "可以连接") {
		t.Fatal("leader did not receive the worker conclusion")
	}
}

func toolJSON(id, name, args string) string {
	payload := map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{
			"content": "",
			"tool_calls": []any{map[string]any{
				"id": id, "type": "function",
				"function": map[string]any{"name": name, "arguments": args},
			}},
		}}},
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

func bytesRepeat() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}
