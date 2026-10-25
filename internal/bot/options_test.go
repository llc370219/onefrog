package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"onefrog/internal/attachment"
	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"onefrog/internal/usage"
	"strings"
	"testing"
)

func featureAgent(t *testing.T, handler http.HandlerFunc) (*Agent, *httptest.Server, *scriptSSH) {
	t.Helper()
	srv := httptest.NewServer(handler)
	m := store.NewMemory()
	box, _ := secret.New(bytes.Repeat([]byte{7}, 32))
	cfg, _ := preferences.New("", box)
	d := preferences.Defaults()
	_, e := cfg.Save(preferences.Input{Providers: []preferences.ProviderInput{{Provider: preferences.Provider{ID: "p", Name: "test", BaseURL: srv.URL + "/v1", Enabled: true, Models: []string{"base", "alternate", "review"}}}}, Default: preferences.Route{ProviderID: "p", Model: "base"}, Approval: preferences.Approval{Mode: "rules", Route: preferences.Route{ProviderID: "p", Model: "review"}}, Execution: d.Execution})
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	_, _ = m.CreateBot(ctx, model.Bot{ID: "main", Name: "主控"})
	_, _ = m.CreateBot(ctx, model.Bot{ID: "worker", Name: "巡检"})
	_, _ = m.CreateServer(ctx, store.ServerRec{Server: model.Server{ID: "web", Name: "web", Host: "127.0.0.1", Port: 22}, HostKey: "SHA256:test"})
	ssh := &scriptSSH{}
	u, _ := usage.New("", box)
	return &Agent{Store: m, Box: box, Settings: cfg, Files: attachment.New("", box), Usage: u, SSH: ssh, HTTP: srv.Client()}, srv, ssh
}
func TestTurnRouteAttachmentsAndUsage(t *testing.T) {
	var sent map[string]any
	a, s, _ := featureAgent(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`)
	})
	defer s.Close()
	ref, _ := a.Files.Put("user", "config.txt", []byte("port=8088"))
	img, _ := a.Files.Put("user", "test.png", []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0})
	pdf, _ := a.Files.Put("user", "runbook.pdf", []byte("%PDF-1.4\nfixture"))
	res, e := a.ReplyWithOptions(context.Background(), "main", "", "", "请分析", Options{OwnerID: "user", ModelRoute: &preferences.Route{ProviderID: "p", Model: "alternate"}, AttachmentIDs: []string{ref.ID, img.ID, pdf.ID}, RestrictServers: true}, nil)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(sent)
	if sent["model"] != "alternate" || !bytes.Contains(raw, []byte("port=8088")) || !bytes.Contains(raw, []byte("image_url")) || !bytes.Contains(raw, []byte("file_data")) {
		t.Fatalf("missing model or attachments: %s", raw)
	}
	if a.Settings.Snapshot().Default.Model != "base" {
		t.Fatal("turn route changed global")
	}
	if a.Usage.ForBot("main").TotalTokens != 15 {
		t.Fatal("usage not recorded")
	}
	msgs, _ := a.Store.ListMessages(context.Background(), res.SessionID)
	sessions, _ := a.Store.ListChatSessions(context.Background())
	var saved Options
	if len(sessions) != 1 || json.Unmarshal(sessions[0].Options, &saved) != nil || saved.ModelRoute == nil || saved.ModelRoute.Model != "alternate" || len(saved.AttachmentIDs) != 0 {
		t.Fatal("chat options not retained safely", sessions)
	}
	if len(msgs[0].Attachments) != 3 {
		t.Fatal("attachments not retained")
	}
}
func TestExcludedBotCannotBeDelegated(t *testing.T) {
	n := 0
	a, s, _ := featureAgent(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			_, _ = io.WriteString(w, toolJSON("ask", "ask_agent", `{"bot_id":"worker","task":"read disk"}`))
		} else {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"finished"}}]}`)
		}
	})
	defer s.Close()
	res, e := a.ReplyWithOptions(context.Background(), "main", "", "", "检查", Options{ExcludedBotIDs: []string{"worker"}}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(res.Steps) != 0 || n != 2 || a.Usage.ForBot("worker").Requests != 0 {
		t.Fatal("excluded bot was invoked")
	}
}
func TestManualServerAndReviewOverride(t *testing.T) {
	n := 0
	var models []string
	a, s, ssh := featureAgent(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		models = append(models, body.Model)
		n++
		if body.Model == "review" {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"allow\":false,\"risk\":\"medium\",\"reason\":\"declined\"}"}}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
		} else if n == 1 {
			_, _ = io.WriteString(w, toolJSON("run", "run_command", `{"server_id":"web","command":"df -h"}`))
		} else {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"done"}}]}`)
		}
	})
	defer s.Close()
	res, e := a.ReplyWithOptions(context.Background(), "main", "", "", "查看磁盘", Options{RestrictServers: true}, nil)
	if e != nil || ssh.command != "" || len(res.Tools) != 1 || !strings.Contains(res.Tools[0].Output, "手动选择") {
		t.Fatal(res, e)
	}
	n = 0
	models = nil
	state := a.Settings.Snapshot()
	_, e = a.Settings.Save(preferences.Input{Providers: []preferences.ProviderInput{{Provider: state.Providers[0]}}, Default: state.Default, Approval: preferences.Approval{Mode: "rules"}, Execution: state.Execution})
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.ReplyWithOptions(context.Background(), "main", "web", "", "查看磁盘", Options{RestrictServers: true, ApprovalMode: "model", ApprovalRoute: &preferences.Route{ProviderID: "p", Model: "review"}}, nil)
	if e != nil || ssh.command != "" || !strings.Contains(strings.Join(models, ","), "review") {
		t.Fatal("review override failed", e, models)
	}
	if a.Settings.Snapshot().Approval.Mode != "rules" || a.Usage.ForBot("main").TotalTokens != 5 {
		t.Fatal("review config or usage wrong")
	}
}
func TestDisabledBotAndHiddenModelRejected(t *testing.T) {
	a, s, _ := featureAgent(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected model request") })
	defer s.Close()
	_ = a.Settings.SetBotEnabled("worker", false)
	if _, e := a.ReplyWithOptions(context.Background(), "worker", "", "", "hello", Options{}, nil); e == nil {
		t.Fatal("disabled bot allowed")
	}
	if _, e := a.ReplyWithOptions(context.Background(), "main", "", "", "hello", Options{ModelRoute: &preferences.Route{ProviderID: "p", Model: "invented"}}, nil); e == nil {
		t.Fatal("unknown model accepted")
	}
}
