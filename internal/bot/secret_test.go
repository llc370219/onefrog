package bot

import (
	"context"
	"encoding/json"
	"errors"
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

func TestParseSecretRequest(t *testing.T) {
	req, err := ParseSecretRequest(`{"name":"  数据库密码  ","label":"数据库密码\n","description":"生产库"}`)
	if err != nil || req.Name != "数据库密码" || req.Label != "数据库密码" || req.Description != "生产库" {
		t.Fatalf("%+v %v", req, err)
	}
	for _, args := range []string{`{`, `{"label":"x"}`, `{"name":"has space","label":"x"}`, `{"name":"bad}name","label":"x"}`, `{"name":"ok","label":" "}`} {
		if _, err := ParseSecretRequest(args); err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
}

func TestRequestSecretHidesValueAndResumesOnce(t *testing.T) {
	const password = "vault-plain-should-not-leak"
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
			_, _ = io.WriteString(w, toolJSON("c1", "request_secret", `{"name":"数据库密码","label":"数据库密码","description":"生产库"}`))
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"密钥已就绪，可以继续。"}}]}`)
	}))
	defer srv.Close()
	if _, err = mem.CreateBot(ctx, model.Bot{ID: "main", Name: "主控", BaseURL: srv.URL + "/v1", ModelName: "demo", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Store: mem, HTTP: srv.Client(), Box: box}
	first, err := agent.Reply(ctx, "", "", "", "我要连生产库")
	if err != nil {
		t.Fatal(err)
	}
	if first.SecretRequest == nil || first.SecretRequest.Name != "数据库密码" || first.MessageID == "" {
		t.Fatalf("no card: %+v", first.SecretRequest)
	}
	raw, _ := json.Marshal(first)
	if strings.Contains(string(raw), password) {
		t.Fatal("result leaked a value")
	}
	mu.Lock()
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"request_secret"`) {
		t.Fatalf("turn did not stop: %d", len(bodies))
	}
	mu.Unlock()

	answer := Options{SecretAnswer: &SecretReply{MessageID: first.MessageID, Value: password}}
	second, err := agent.ReplyWithOptions(ctx, "", "", first.SessionID, password, answer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Answered || !strings.Contains(second.Text, "可以继续") {
		t.Fatalf("did not resume: answered=%v text=%q", second.Answered, second.Text)
	}
	mu.Lock()
	if len(bodies) != 2 || strings.Contains(bodies[1], password) || !strings.Contains(bodies[1], secretAck("数据库密码")) {
		t.Fatal("model saw the plaintext or missed the acknowledgement")
	}
	mu.Unlock()
	msgs, _ := mem.ListMessages(ctx, first.SessionID)
	joined := ""
	for _, m := range msgs {
		joined += m.Content + "\n"
		raw, _ = json.Marshal(m)
		joined += string(raw) + "\n"
	}
	if strings.Contains(joined, password) {
		t.Fatal("history contains the plaintext")
	}
	if !strings.Contains(joined, secretAck("数据库密码")) {
		t.Fatal("acknowledgement was not stored")
	}
	rec, err := mem.SecretByName(ctx, "数据库密码")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := box.Decrypt(rec.EncryptedValue)
	if err != nil || plain != password {
		t.Fatal("vault did not store the typed value")
	}
	listed, _ := json.Marshal(mustSecrets(t, mem))
	if strings.Contains(string(listed), password) {
		t.Fatal("list leaked the value")
	}
	sess, err := mem.EnsureSession(ctx, first.SessionID, "main", "")
	if err != nil || strings.Contains(string(sess.Options), password) {
		t.Fatal("chat options kept the plaintext")
	}

	again, err := agent.ReplyWithOptions(ctx, "", "", first.SessionID, "different-value", answer, nil)
	if err != nil || !again.Answered {
		t.Fatalf("retry should not start a turn: %+v %v", again, err)
	}
	plain, _ = box.Decrypt(rec.EncryptedValue)
	rec, _ = mem.SecretByName(ctx, "数据库密码")
	plain, _ = box.Decrypt(rec.EncryptedValue)
	if plain != password {
		t.Fatal("retry rotated the vault")
	}
	mu.Lock()
	if len(bodies) != 2 {
		t.Fatalf("retry called the model: %d", len(bodies))
	}
	mu.Unlock()
}

func TestExistingSecretIsNotRequestedAgain(t *testing.T) {
	mem := store.NewMemory()
	ctx := context.Background()
	box, _ := secret.New(bytesRepeat())
	enc, _ := box.Encrypt("already-set")
	if _, err := mem.CreateSecret(ctx, store.SecretRec{Secret: model.Secret{Name: "db-password"}, EncryptedValue: enc}); err != nil {
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
			_, _ = io.WriteString(w, toolJSON("c1", "request_secret", `{"name":"db-password","label":"数据库密码"}`))
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"继续。"}}]}`)
	}))
	defer srv.Close()
	_, _ = mem.CreateBot(ctx, model.Bot{ID: "main", Name: "主控", BaseURL: srv.URL + "/v1", ModelName: "demo", CreatedAt: time.Now()})
	agent := &Agent{Store: mem, HTTP: srv.Client(), Box: box}
	got, err := agent.Reply(ctx, "", "", "", "用数据库密码")
	if err != nil {
		t.Fatal(err)
	}
	if got.SecretRequest != nil || !strings.Contains(got.Text, "继续") {
		t.Fatalf("existing name still prompted: %+v %q", got.SecretRequest, got.Text)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || strings.Contains(bodies[1], "already-set") {
		t.Fatal("existing secret was shown or the turn stopped")
	}
	if !strings.Contains(bodies[1], "不要再向用户索要") {
		t.Fatal("model was not told the name already exists")
	}
}

func TestSecretAnswerRejectsBadValueAndStaleCard(t *testing.T) {
	mem := store.NewMemory()
	ctx := context.Background()
	box, _ := secret.New(bytesRepeat())
	agent := &Agent{Store: mem, Box: box}
	sess, err := mem.EnsureSession(ctx, "", "main", "密钥")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mem.AddMessage(ctx, model.Message{SessionID: sess.ID, Role: "assistant", Content: "需要密钥", SecretRequest: &model.SecretRequest{Name: "db", Label: "数据库"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = agent.answerSecret(ctx, sess.ID, SecretReply{MessageID: msg.ID, Value: strings.Repeat("a", secret.MaxValueBytes+1)})
	if err == nil || strings.Contains(err.Error(), "aaa") {
		t.Fatalf("oversize: %v", err)
	}
	if _, err = mem.SecretByName(ctx, "db"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("oversize value was stored")
	}
	card, _ := mem.ListMessages(ctx, sess.ID)
	if card[0].SecretRequest.ProvidedAt != nil {
		t.Fatal("oversize marked the card provided")
	}
	if _, err = mem.AddMessage(ctx, model.Message{SessionID: sess.ID, Role: "user", Content: "先不用了"}); err != nil {
		t.Fatal(err)
	}
	_, _, err = agent.answerSecret(ctx, sess.ID, SecretReply{MessageID: msg.ID, Value: "later"})
	if !errors.Is(err, ErrSecretStale) {
		t.Fatalf("stale: %v", err)
	}
	if _, err = mem.SecretByName(ctx, "db"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("stale answer stored a secret")
	}
}

func TestWorkerCannotRequestSecret(t *testing.T) {
	tr := &trace{}
	agent := &Agent{Store: store.NewMemory()}
	ctx := context.WithValue(context.Background(), usageKey{}, usageActor{Purpose: "worker"})
	var call toolCall
	call.Function.Name = "request_secret"
	call.Function.Arguments = `{"name":"db","label":"数据库"}`
	out := agent.execCall(ctx, model.Bot{ID: "w"}, credentials{}, "", call, false, tr)
	if tr.secret != nil || !strings.Contains(out, "不能直接向用户索取密钥") {
		t.Fatalf("worker requested a secret: %q", out)
	}
}

func TestProvideRotatesExistingName(t *testing.T) {
	mem := store.NewMemory()
	ctx := context.Background()
	box, _ := secret.New(bytesRepeat())
	oldEnc, _ := box.Encrypt("old-value")
	created, err := mem.CreateSecret(ctx, store.SecretRec{Secret: model.Secret{Name: "db", Description: "keep"}, EncryptedValue: oldEnc})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := mem.EnsureSession(ctx, "", "main", "轮换")
	msg, err := mem.AddMessage(ctx, model.Message{SessionID: sess.ID, Role: "assistant", SecretRequest: &model.SecretRequest{Name: "db", Label: "数据库", Description: "不要换成这个"}})
	if err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Store: mem, Box: box}
	_, done, err := agent.answerSecret(ctx, sess.ID, SecretReply{MessageID: msg.ID, Value: "new-value"})
	if err != nil || done {
		t.Fatal(done, err)
	}
	rec, err := mem.GetSecret(ctx, created.ID)
	if err != nil || rec.Name != "db" || rec.Description != "keep" {
		t.Fatalf("identity changed: %+v %v", rec.Secret, err)
	}
	plain, _ := box.Decrypt(rec.EncryptedValue)
	if plain != "new-value" || rec.EncryptedValue == oldEnc {
		t.Fatal("ciphertext was not rotated")
	}
}

func mustSecrets(t *testing.T, mem *store.Memory) []model.Secret {
	t.Helper()
	items, err := mem.ListSecrets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return items
}
