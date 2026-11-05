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

func TestGenerateSecretNeverReachesModel(t *testing.T) {
	mem := store.NewMemory()
	ctx := context.Background()
	box, err := secret.New(bytesRepeat())
	if err != nil {
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
		if n == 1 {
			_, _ = io.WriteString(w, toolJSON("c1", "generate_secret", `{"name":"mysql-root","description":"MySQL root","length":40,"charset":"symbols"}`))
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"已生成。"}}]}`)
	}))
	defer srv.Close()
	if _, err = mem.CreateBot(ctx, model.Bot{ID: "main", Name: "主控", BaseURL: srv.URL + "/v1", ModelName: "demo", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Store: mem, HTTP: srv.Client(), Box: box}
	res, err := agent.Reply(ctx, "", "", "", "给 MySQL 生成一个 root 密码")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := mem.SecretByName(ctx, "mysql-root")
	if err != nil || rec.Description != "MySQL root" {
		t.Fatalf("not stored: %+v %v", rec.Secret, err)
	}
	plain, err := box.Decrypt(rec.EncryptedValue)
	if err != nil || len(plain) != 40 {
		t.Fatalf("bad value length %d %v", len(plain), err)
	}
	mu.Lock()
	if len(bodies) != 2 || !strings.Contains(bodies[0], `"generate_secret"`) {
		t.Fatalf("tool not offered or turn stopped: %d", len(bodies))
	}
	if strings.Contains(bodies[1], plain) || !strings.Contains(bodies[1], "{{secret:mysql-root}}") {
		t.Fatal("model saw the plaintext or missed the reference")
	}
	mu.Unlock()
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), plain) {
		t.Fatal("result leaked the plaintext")
	}
	msgs, _ := mem.ListMessages(ctx, res.SessionID)
	for _, m := range msgs {
		raw, _ = json.Marshal(m)
		if strings.Contains(string(raw), plain) {
			t.Fatal("history leaked the plaintext")
		}
	}
}

func TestGenerateSecretKeepsExistingAndWorksForWorkers(t *testing.T) {
	mem := store.NewMemory()
	box, _ := secret.New(bytesRepeat())
	oldEnc, _ := box.Encrypt("old-value")
	if _, err := mem.CreateSecret(context.Background(), store.SecretRec{Secret: model.Secret{Name: "db"}, EncryptedValue: oldEnc}); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Store: mem, Box: box}
	ctx := context.WithValue(context.Background(), usageKey{}, usageActor{Purpose: "worker"})
	run := func(args string) string {
		var call toolCall
		call.Function.Name = "generate_secret"
		call.Function.Arguments = args
		return agent.execCall(ctx, model.Bot{ID: "w", Name: "运维"}, credentials{}, "", call, false, &trace{agent: agent, ctx: ctx})
	}
	if out := run(`{"name":"db"}`); !strings.Contains(out, "不会覆盖") {
		t.Fatalf("existing name: %q", out)
	}
	rec, _ := mem.SecretByName(context.Background(), "db")
	if rec.EncryptedValue != oldEnc {
		t.Fatal("existing secret was overwritten")
	}
	for _, bad := range []string{`{"name":"model-key"}`, `{"name":"x y"}`, `{"name":"t","length":4}`, `{"name":"t","charset":"emoji"}`, `{`} {
		if out := run(bad); strings.Contains(out, "已随机生成") {
			t.Fatalf("accepted %s", bad)
		}
	}
	out := run(`{"name":"token","charset":"hex","length":16}`)
	if !strings.Contains(out, "{{secret:token}}") {
		t.Fatalf("worker could not generate: %q", out)
	}
	rec, err := mem.SecretByName(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := box.Decrypt(rec.EncryptedValue)
	if len(plain) != 16 || strings.Contains(out, plain) || strings.Trim(plain, "0123456789abcdef") != "" {
		t.Fatalf("bad hex value or leaked: %q", out)
	}
}
