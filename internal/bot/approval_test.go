package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"strings"
	"testing"
)

type reviewTransport func(*http.Request) (*http.Response, error)

func (f reviewTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestApprovalGateAndModelIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, mode, command, reply string
		allow                      bool
		wantCalls                  int
	}{
		{"approve", "model", "uptime", `{"allow":true,"risk":"low","reason":"只读状态检查"}`, true, 1},
		{"deny", "model", "uptime", `{"allow":false,"risk":"medium","reason":"不在授权范围"}`, false, 1},
		{"malformed", "model", "uptime", `maybe`, false, 1},
		{"high risk true", "model", "uptime", `{"allow":true,"risk":"high","reason":"危险"}`, false, 1},
		{"hard rule first", "model", "rm -rf /tmp/example", `{"allow":true,"risk":"low","reason":"ok"}`, false, 0},
		{"plan", "plan", "uptime", "", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			box, _ := secret.New(bytesRepeat())
			settings, _ := preferences.New("", box)
			_, err := settings.Save(preferences.Input{Providers: []preferences.ProviderInput{{Provider: preferences.Provider{ID: "review", Name: "review", BaseURL: "http://review.example/v1", Enabled: true, AuthRequired: true}, APIKey: "review-key"}}, Default: preferences.Route{ProviderID: "review", Model: "worker"}, Approval: preferences.Approval{Mode: tc.mode, Route: preferences.Route{ProviderID: "review", Model: "review-only"}}, Execution: preferences.Defaults().Execution})
			if err != nil {
				t.Fatal(err)
			}
			mem := store.NewMemory()
			ssh := &scriptSSH{}
			calls := 0
			transport := reviewTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				raw, _ := io.ReadAll(r.Body)
				if r.Header.Get("Authorization") != "Bearer review-key" || !strings.Contains(string(raw), `"model":"review-only"`) || strings.Contains(string(raw), `"tools"`) {
					t.Fatal("review used wrong model/key or tools")
				}

				reply := `{"choices":[{"message":{"content":` + jsonString(tc.reply) + `}}]}`
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(reply)), Header: make(http.Header)}, nil
			})
			a := &Agent{Store: mem, Box: box, SSH: ssh, Settings: settings, HTTP: &http.Client{Transport: transport}}
			_, _ = mem.CreateServer(context.Background(), store.ServerRec{Server: model.Server{ID: "srv", Name: "srv"}, HostKey: "SHA256:test"})
			ctx := context.WithValue(context.Background(), taskKey{}, "检查主机运行时间")
			call := toolCall{ID: "c", Type: "function"}
			call.Function.Name = "run_command"
			call.Function.Arguments = `{"server_id":"srv","command":` + jsonString(tc.command) + `}`
			ev := a.runTool(ctx, "bot", "srv", call)
			if ev.OK != tc.allow || calls != tc.wantCalls {
				t.Fatalf("ok=%v calls=%d output=%s", ev.OK, calls, ev.Output)
			}
			if !tc.allow && ssh.command != "" {
				t.Fatal("denied command executed")
			}
			h, _ := mem.ListHistory(ctx, 20)
			found := false
			for _, row := range h {
				if row.Status == "approval_allowed" || row.Status == "approval_denied" {
					found = true
				}
			}
			if !found {
				t.Fatal("approval missing audit record")
			}
		})
	}
}

func jsonString(s string) string { raw, _ := json.Marshal(s); return string(raw) }

func TestManualApprovalIsDefault(t *testing.T) {
	if preferences.Defaults().Approval.Mode != "manual" {
		t.Fatal("new installs must default to manual approval")
	}
	for _, tc := range []struct {
		name, command string
		approver      Approver
		allow         bool
		warn          bool
	}{
		{"no approver denies", "uptime", nil, false, false},
		{"operator allows", "uptime", func(context.Context, ApprovalRequest) (bool, string) { return true, "" }, true, false},
		{"operator denies", "uptime", func(context.Context, ApprovalRequest) (bool, string) { return false, "" }, false, false},
		{"keyword only warns", "rm -rf /tmp/example", func(context.Context, ApprovalRequest) (bool, string) { return true, "" }, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			box, _ := secret.New(bytesRepeat())
			settings, _ := preferences.New("", box)
			mem := store.NewMemory()
			ssh := &scriptSSH{}
			a := &Agent{Store: mem, Box: box, SSH: ssh, Settings: settings}
			_, _ = mem.CreateServer(context.Background(), store.ServerRec{Server: model.Server{ID: "srv", Name: "srv"}, HostKey: "SHA256:test"})
			var seen ApprovalRequest
			opt := Options{}
			if tc.approver != nil {
				opt.Approver = func(ctx context.Context, r ApprovalRequest) (bool, string) { seen = r; return tc.approver(ctx, r) }
			}
			ctx := context.WithValue(context.Background(), optionsKey{}, opt)
			call := toolCall{ID: "c", Type: "function"}
			call.Function.Name = "run_command"
			call.Function.Arguments = `{"server_id":"srv","command":` + jsonString(tc.command) + `}`
			ev := a.runTool(ctx, "bot", "srv", call)
			if ev.OK != tc.allow || (ssh.command != "") != tc.allow {
				t.Fatalf("ok=%v executed=%q output=%s", ev.OK, ssh.command, ev.Output)
			}
			if tc.approver != nil && (seen.Command != tc.command || (seen.Warning != "") != tc.warn) {
				t.Fatalf("approval request %+v", seen)
			}
		})
	}
}

func TestUnconfirmedHostKeyRefused(t *testing.T) {
	mem := store.NewMemory()
	ssh := &scriptSSH{}
	box, _ := secret.New(bytesRepeat())
	a := &Agent{Store: mem, Box: box, SSH: ssh}
	_, _ = mem.CreateServer(context.Background(), store.ServerRec{Server: model.Server{ID: "srv", Name: "srv"}})
	call := toolCall{ID: "c", Type: "function"}
	call.Function.Name = "run_command"
	call.Function.Arguments = `{"server_id":"srv","command":"uptime"}`
	if ev := a.runTool(context.Background(), "bot", "srv", call); ev.OK || ssh.command != "" {
		t.Fatal("command ran without a confirmed host key")
	}
}
