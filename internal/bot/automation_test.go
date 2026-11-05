package bot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"onefrog/internal/preferences"
)

func TestRunAutomationUsesBackgroundTurn(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"一切正常"}}]}`)
	}))
	defer srv.Close()
	mem, box := mailboxFixture(t, srv.URL+"/v1")
	agent := &Agent{Store: mem, Box: box, SSH: &scriptSSH{}, HTTP: srv.Client()}
	ctx := context.Background()

	res, err := agent.RunAutomation(ctx, "main", "", "auto-x-main", "每日巡检", "检查一下所有服务器", "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "一切正常" {
		t.Fatalf("text %q", res.Text)
	}
	mu.Lock()
	if len(bodies) == 0 {
		mu.Unlock()
		t.Fatal("no model request")
	}
	first := bodies[0]
	mu.Unlock()
	if !strings.Contains(first, "【自动化任务】") {
		t.Fatal("automation prompt missing")
	}
	if strings.Contains(first, `"ask_user"`) {
		t.Fatal("ask_user offered to a background run")
	}
	msgs, err := mem.ListMessages(ctx, "auto-x-main")
	if err != nil {
		t.Fatal(err)
	}
	var sawUser, sawReply bool
	for _, m := range msgs {
		if m.Role == "user" && m.Content == "检查一下所有服务器" {
			sawUser = true
		}
		if m.Role == "assistant" && m.Content == "一切正常" {
			sawReply = true
		}
	}
	if !sawUser || !sawReply {
		t.Fatalf("session messages %+v", msgs)
	}
}

func TestRunAutomationRejectsMissingOrDisabledBot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected model request")
	}))
	defer srv.Close()
	mem, box := mailboxFixture(t, srv.URL+"/v1")
	settings, err := preferences.New("", box)
	if err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Store: mem, Box: box, SSH: &scriptSSH{}, HTTP: srv.Client(), Settings: settings}
	ctx := context.Background()
	if _, err := agent.RunAutomation(ctx, "ghost", "", "auto-x-ghost", "x", "hi", "u1", ""); err == nil {
		t.Fatal("missing bot accepted")
	}
	_ = settings.SetBotEnabled("check", false)
	if _, err := agent.RunAutomation(ctx, "check", "", "auto-x-check", "x", "hi", "u1", ""); err == nil {
		t.Fatal("disabled bot accepted")
	}
}
