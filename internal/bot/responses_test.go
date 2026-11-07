package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

func TestResponsesAgentContinuesAfterApprovedHostCommand(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	if _, err := mem.CreateBot(ctx, model.Bot{ID: "main", Name: "主控"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.CreateServer(ctx, store.ServerRec{Server: model.Server{ID: "host", Name: "部署宿主机", Host: "host.docker.internal", Username: "onefrog-ops"}, HostKey: "SHA256:pinned"}); err != nil {
		t.Fatal(err)
	}
	box, _ := secret.New(bytesRepeat())
	settings, _ := preferences.New("", box)
	route := preferences.Route{ProviderID: "p", Model: "tools"}
	_, err := settings.Save(preferences.Input{
		Providers: []preferences.ProviderInput{{
			Provider: preferences.Provider{ID: "p", Name: "Responses", BaseURL: "https://models.example/v1", Protocol: preferences.ProtocolResponses, Enabled: true, AuthRequired: true, Models: []string{"tools"}},
			APIKey:   "test-api-key",
		}},
		Default: route, Execution: preferences.Defaults().Execution,
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	const cmd = "touch /home/onefrog-ops/onefrog-test"
	transport := reviewTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Path != "/v1/responses" || req.Header.Get("Authorization") != "Bearer test-api-key" {
			t.Fatalf("wrong upstream route: %s", req.URL)
		}
		var body struct {
			Store *bool            `json:"store"`
			Input []map[string]any `json:"input"`
			Tools []map[string]any `json:"tools"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Store == nil || *body.Store || len(body.Tools) == 0 || body.Tools[0]["name"] == nil || body.Tools[0]["strict"] != false {
			t.Fatalf("wrong tool definitions or storage policy: %+v", body)
		}
		payload := `{"status":"completed","output":[{"type":"reasoning","id":"rs_1","encrypted_content":"opaque","summary":[]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"run_command","arguments":"{\"server_id\":\"host\",\"command\":\"touch /home/onefrog-ops/onefrog-test\",\"risk\":\"medium\"}"}]}`
		if calls == 2 {
			if len(body.Input) != 4 || body.Input[1]["encrypted_content"] != "opaque" || body.Input[2]["call_id"] != "call_1" ||
				body.Input[3]["type"] != "function_call_output" || body.Input[3]["call_id"] != "call_1" ||
				!strings.Contains(body.Input[3]["output"].(string), "seen:"+cmd) {
				t.Fatalf("missing continuation state: %+v", body.Input)
			}
			payload = `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"宿主机文件已创建"}]}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})
	ssh := &scriptSSH{}
	agent := &Agent{Store: mem, Box: box, Settings: settings, SSH: ssh, HTTP: &http.Client{Transport: transport}}
	approvals := 0
	res, err := agent.ReplyWithOptions(ctx, "main", "host", "", "在宿主机创建测试文件", Options{
		RestrictServers: true,
		ApprovalMode:    preferences.ModeManual,
		Approver: func(_ context.Context, req ApprovalRequest) (bool, string) {
			approvals++
			if req.ServerID != "host" || req.Command != cmd || ssh.command != "" {
				t.Fatalf("incorrect approval or already executed: %+v", req)
			}
			return true, "批准一次"
		},
	}, nil)
	if err != nil || res.Text != "宿主机文件已创建" || ssh.command != cmd || calls != 2 || approvals != 1 {
		t.Fatalf("result: %+v; error: %v; calls: %d; approvals: %d", res, err, calls, approvals)
	}
}
