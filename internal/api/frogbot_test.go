package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"onefrog/internal/boot"
	"onefrog/internal/bot"
	"onefrog/internal/config"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

type frogTestSSH struct{ calls atomic.Int32 }

func (f *frogTestSSH) Run(_ context.Context, host string, _ int, _, _, _, pin, command string) (string, string, error) {
	f.calls.Add(1)
	if host != "example.test" || pin != "SHA256:frog-test" || command != "df -h" {
		return "", "", fmt.Errorf("unexpected execution: %s %s %s", host, pin, command)
	}
	return "disk sample", pin, nil
}

func TestFrogBotChatDispatchesAndGatesRemoteCommand(t *testing.T) {
	ctx := t.Context()
	mem := store.NewMemory()
	key := bytes.Repeat([]byte{8}, 32)
	box, _ := secret.New(key)
	if err := boot.Prepare(ctx, mem, config.Config{AdminUser: "admin", AdminPassword: "admin", Seed: true}); err != nil {
		t.Fatal(err)
	}
	pass, _ := bcrypt.GenerateFromPassword([]byte("other"), bcrypt.MinCost)
	if _, err := mem.CreateUser(ctx, "other", string(pass)); err != nil {
		t.Fatal(err)
	}
	host, err := mem.GetServer(ctx, "web-01")
	if err != nil {
		t.Fatal(err)
	}
	host.Host, host.HostKey = "example.test", "SHA256:frog-test"
	if _, err := mem.UpdateServer(ctx, host); err != nil {
		t.Fatal(err)
	}

	ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Role, Content string }
			Tools    []struct{ Function struct{ Name string } }
		}
		if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Messages) == 0 {
			http.Error(w, "invalid model request", 400)
			return
		}
		tools := map[string]bool{}
		for _, tool := range req.Tools {
			tools[tool.Function.Name] = true
		}
		if tools["shell"] {
			t.Error("host shell advertised to the embedded agent")
		}
		msg := map[string]any{"content": "已收到任务结果。"}
		if last := req.Messages[len(req.Messages)-1]; last.Role == "user" {
			switch {
			case tools["start_task"] && strings.Contains(last.Content, "integration-marker") && !strings.Contains(last.Content, "[系统通知]"):
				msg["tool_calls"] = []map[string]any{fakeFrogCall("start_task", `{"title":"检查磁盘","task":"检查服务器 web-01 的磁盘，运行 df -h","input":"检查服务器 web-01 的磁盘，运行 df -h"}`)}
			case tools["run_command"]:
				msg["tool_calls"] = []map[string]any{fakeFrogCall("run_command", `{"server_id":"web-01","command":"df -h","risk":"low","reason":"查看磁盘"}`)}
			}
		}
		if tools["run_onefrog_command"] {
			t.Error("legacy run_onefrog_command still offered")
		}
		if tools["run_command"] && req.Messages[len(req.Messages)-1].Role == "tool" {
			msg["content"] = "后台检查完成，获得磁盘样本。"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": msg}}})
	}))
	defer ai.Close()
	main, _ := mem.GetBot(ctx, "main")
	main.BaseURL, main.ModelName = ai.URL+"/v1", "fake"
	if _, err := mem.UpdateBot(ctx, main); err != nil {
		t.Fatal(err)
	}
	ssh := &frogTestSSH{}
	agent := &bot.Agent{Store: mem, Box: box, SSH: ssh}
	dir := t.TempDir()
	manager := NewFrogBots(dir, key, agent, nil)
	defer manager.Close()
	app := &Server{Store: mem, Box: box, Agent: agent, FrogBots: manager}
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	newClient := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: jar, Timeout: 8 * time.Second}
	}
	client, other := newClient(), newClient()
	// The leader runs as "main"; pre-0.2 pages used "frog-bot" for it.
	base := server.URL + "/api/frog-bot/bots/main"
	res, err := other.Get(base + "/info")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("anonymous info: %d", res.StatusCode)
	}
	login(t, client, server, "admin", "admin", 200)
	login(t, other, server, "other", "other", 200)
	withoutCSRF := &http.Client{Jar: client.Jar, Transport: http.DefaultTransport.(csrfTransport).base}
	res, err = withoutCSRF.Post(base+"/messages", "application/json", strings.NewReader(`{"text":"missing CSRF"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("missing CSRF: %d", res.StatusCode)
	}
	postJSON(t, client, server.URL+"/api/frog-bot/bots/frog-bot/messages", map[string]string{"text": "integration-marker: 检查磁盘"}, 200, nil)

	stream, err := client.Get(base + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	sc := bufio.NewScanner(stream.Body)
	var id string
	for sc.Scan() {
		line := strings.TrimPrefix(sc.Text(), "data: ")
		var ev struct {
			Kind string
			Data struct {
				Type string
				Data struct{ ID string }
			}
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Kind == "agent" && ev.Data.Type == "approval.requested" {
			id = ev.Data.Data.ID
			break
		}
	}
	if id == "" || ssh.calls.Load() != 0 {
		t.Fatalf("expected pending approval and no SSH; id %q calls %d stream error %v", id, ssh.calls.Load(), sc.Err())
	}
	otherStream, err := other.Get(base + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	scOther := bufio.NewScanner(otherStream.Body)
	replay := false
	for scOther.Scan() {
		if strings.Contains(scOther.Text(), "integration-marker") || strings.Contains(scOther.Text(), id) {
			otherStream.Body.Close()
			t.Fatal("another account saw chat or approval")
		}
		if strings.Contains(scOther.Text(), "replay.done") {
			replay = true
			break
		}
	}
	otherStream.Body.Close()
	if !replay {
		t.Fatal("second account's stream did not replay")
	}
	postJSON(t, other, base+"/approvals/"+id, map[string]string{"decision": "once"}, 400, nil)
	postJSON(t, client, base+"/approvals/"+id, map[string]string{"decision": "session"}, 400, nil)
	postJSON(t, client, base+"/approvals/"+id, map[string]string{"decision": "once"}, 200, nil)
	completed := false
	for sc.Scan() {
		if strings.Contains(sc.Text(), "\"type\":\"task.completed\"") {
			if ssh.calls.Load() != 1 {
				t.Fatalf("SSH calls = %d", ssh.calls.Load())
			}
			completed = true
			break
		}
	}
	if !completed {
		t.Fatalf("no task completion; %v", sc.Err())
	}
	stream.Body.Close()
	manager.Close()
	account, _, err := mem.UserByName(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, hex.EncodeToString([]byte(account.ID)), "stream.jsonl"))
	if err != nil || bytes.Contains(raw, []byte("integration-marker")) || len(raw) == 0 {
		t.Fatalf("event journal missing or saved plaintext: %v", err)
	}

	restored := NewFrogBots(dir, key, agent, nil)
	defer restored.Close()
	if err := restored.Restore(); err != nil {
		t.Fatal(err)
	}
	acct, err := restored.account(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	handler := acct.handler
	// Take only the replay, without waiting on a live network stream.
	replayCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/bots/main/stream", nil).WithContext(replayCtx))
	if !strings.Contains(rec.Body.String(), "integration-marker") || !strings.Contains(rec.Body.String(), "task.completed") {
		t.Fatal("encrypted journal did not replay chat and task completion after restart")
	}
}

func fakeFrogCall(name, args string) map[string]any {
	return map[string]any{"id": "call_" + name, "type": "function", "function": map[string]string{"name": name, "arguments": args}}
}

// A command on a server the user selected for the bot follows the approval
// mode (here: no card for a plain read), and every enabled OneFrog bot except
// the module builder appears as its own assistant runtime.
func TestFrogBotSelectedServerAndBotList(t *testing.T) {
	ctx := t.Context()
	mem := store.NewMemory()
	key := bytes.Repeat([]byte{9}, 32)
	box, _ := secret.New(key)
	if err := boot.Prepare(ctx, mem, config.Config{AdminUser: "admin", AdminPassword: "admin", Seed: true}); err != nil {
		t.Fatal(err)
	}
	host, err := mem.GetServer(ctx, "web-01")
	if err != nil {
		t.Fatal(err)
	}
	host.Host, host.HostKey = "example.test", "SHA256:frog-test"
	if _, err := mem.UpdateServer(ctx, host); err != nil {
		t.Fatal(err)
	}
	ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Role, Content string }
			Tools    []struct{ Function struct{ Name string } }
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		tools := map[string]bool{}
		for _, tool := range req.Tools {
			tools[tool.Function.Name] = true
		}
		msg := map[string]any{"content": "完成。"}
		if last := req.Messages[len(req.Messages)-1]; last.Role == "user" {
			switch {
			case tools["start_task"] && strings.Contains(last.Content, "scope-marker") && !strings.Contains(last.Content, "[系统通知]"):
				msg["tool_calls"] = []map[string]any{fakeFrogCall("start_task", `{"title":"看磁盘","task":"在 web-01 上运行 df -h","input":"在 web-01 上运行 df -h"}`)}
			case tools["run_command"]:
				msg["tool_calls"] = []map[string]any{fakeFrogCall("run_command", `{"server_id":"web-01","command":"df -h","risk":"low","reason":"查看磁盘"}`)}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": msg}}})
	}))
	defer ai.Close()
	main, _ := mem.GetBot(ctx, "main")
	main.BaseURL, main.ModelName = ai.URL+"/v1", "fake"
	if _, err := mem.UpdateBot(ctx, main); err != nil {
		t.Fatal(err)
	}
	ssh := &frogTestSSH{}
	agent := &bot.Agent{Store: mem, Box: box, SSH: ssh}
	manager := NewFrogBots(t.TempDir(), key, agent, nil)
	defer manager.Close()
	app := &Server{Store: mem, Box: box, Agent: agent, FrogBots: manager}
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 8 * time.Second}
	login(t, client, server, "admin", "admin", 200)
	api := server.URL + "/api/frog-bot"

	res, err := client.Get(api + "/bots")
	if err != nil {
		t.Fatal(err)
	}
	var profiles []struct {
		ID, Name, HostID string
		Leader           bool
	}
	_ = json.NewDecoder(res.Body).Decode(&profiles)
	res.Body.Close()
	if len(profiles) == 0 || profiles[0].ID != "main" || !profiles[0].Leader || profiles[0].HostID != "main" {
		t.Fatalf("bots = %+v", profiles)
	}
	t.Logf("assistant bots: %+v", profiles)
	for _, p := range profiles {
		if _, err := mem.GetBot(ctx, p.HostID); err != nil || (p.ID != "main") != strings.HasPrefix(p.ID, "bot_") {
			t.Fatalf("profile %+v does not map to a OneFrog bot", p)
		}
	}
	res = postRaw(t, client, api+"/bots", map[string]string{"name": "x"})
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode == 200 {
		t.Fatalf("creating a bot inside the assistant must be refused: %s", raw)
	}
	var scope struct{ Servers []bot.FrogServer }
	postJSON(t, client, api+"/bots/main/scope", map[string]any{"servers": []string{"web-01"}}, 200, &scope)
	selected := false
	for _, s := range scope.Servers {
		selected = selected || (s.ID == "web-01" && s.Selected && s.Pinned)
	}
	if !selected {
		t.Fatalf("scope = %+v", scope)
	}
	postJSON(t, client, api+"/bots/main/scope", map[string]any{"servers": []string{"nope"}}, 400, nil)

	postJSON(t, client, api+"/bots/main/messages", map[string]string{"text": "scope-marker 看磁盘"}, 200, nil)
	stream, err := client.Get(api + "/bots/main/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	sc := bufio.NewScanner(stream.Body)
	done := false
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, `"type":"approval.requested"`) {
			t.Fatal("a plain read on a selected server must not ask")
		}
		if strings.Contains(line, `"type":"task.completed"`) {
			done = true
			break
		}
	}
	if !done || ssh.calls.Load() != 1 {
		t.Fatalf("done %v ssh %d err %v", done, ssh.calls.Load(), sc.Err())
	}
}
