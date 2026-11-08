package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"onefrog/internal/model"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"onefrog/internal/workspace"
)

func TestCodeTaskKeepsTargetAndAsksBeforeRunning(t *testing.T) {
	ctx := context.Background()
	db := store.NewMemory()
	box, _ := secret.New(bytesRepeat())
	key, _ := box.Encrypt("test-credential")
	_, _ = db.CreateBot(ctx, model.Bot{ID: "main", Name: "主控", BaseURL: "https://model.example/v1", ModelName: "test"})
	_, _ = db.CreateServer(ctx, store.ServerRec{Server: model.Server{ID: "host", Host: "host.example", Username: "developer"}, HostKey: "pinned", EncryptedKey: key})
	target := workspace.Target{ServerID: "host", Root: "/home/developer/repo"}
	var executions []string
	svc := &workspace.Service{Store: db, Box: box, Run: func(_ context.Context, host string, _ int, user, _, _, pin, _ string, payload []byte) (string, string, error) {
		var req struct {
			workspace.Request
			Root string
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			t.Fatal(err)
		}
		if host != "host.example" || user != "developer" || pin != "pinned" || req.Root != target.Root {
			t.Fatalf("wrong target: %+v", req)
		}
		executions = append(executions, req.Action)
		return `{"text":"verification output","exit_code":0}`, "", nil
	}}
	calls := 0
	transport := reviewTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"name":"run_command"`) && calls == 1 || !strings.Contains(string(raw), `"name":"workspace"`) {
			t.Fatalf("unexpected tool scope: %s", raw)
		}
		reply := `{"choices":[{"message":{"content":"完成"}}]}`
		switch calls {
		case 1:
			reply = toolJSON("generic", "run_command", `{"server_id":"other","command":"id"}`)
		case 2, 4, 6:
			if calls == 2 && !strings.Contains(string(raw), "只能使用已绑定的工作区工具") {
				t.Fatal("forged general shell call not refused")
			}
			reply = toolJSON("run", "workspace", `{"action":"run","command":"go test ./..."}`)
		case 3:
			if !strings.Contains(string(raw), "verification output") {
				t.Fatal("test result did not reach the model")
			}
		case 5, 7:
			if !strings.Contains(string(raw), "未执行") && !strings.Contains(string(raw), "等待用户审批") && !strings.Contains(string(raw), "onefrog:pending") {
				t.Fatalf("denial did not reach the model: %s", raw)
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(reply))}, nil
	})
	agent := &Agent{Store: db, Box: box, Workspace: svc, HTTP: &http.Client{Transport: transport}}
	approvals := 0
	res, err := agent.ReplyWithOptions(ctx, "main", "other", "", "跑一下测试", Options{
		Workspace: &target, ApprovalMode: "off", Approver: func(_ context.Context, r ApprovalRequest) (bool, string) {
			approvals++
			if len(executions) != 0 || !r.SingleUse || r.ServerID != "host" || !strings.Contains(r.Command, target.Root+"\ngo test ./...") {
				t.Fatalf("unexpected approval: %+v", r)
			}
			return true, "once"
		},
	}, nil)
	if err != nil || res.SessionID == "" || approvals != 1 || len(executions) != 1 {
		t.Fatalf("execution: %+v %v approvals=%d executions=%v", res, err, approvals, executions)
	}
	other := workspace.Target{ServerID: "host", Root: "/home/developer/other"}
	if _, err := agent.ReplyWithOptions(ctx, "main", "other", res.SessionID, "换目录", Options{Workspace: &other}, nil); err == nil {
		t.Fatal("workspace session could be rebound")
	}
	if _, err := agent.ReplyWithOptions(ctx, "main", "other", res.SessionID, "再跑测试", Options{
		Approver: func(_ context.Context, r ApprovalRequest) (bool, string) { approvals++; return false, "拒绝" },
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Reply(ctx, "main", "other", res.SessionID, "没有审批的请求"); err != nil {
		t.Fatal(err)
	}
	if len(executions) != 1 || approvals != 2 || calls != 7 {
		t.Fatalf("unexpected shell execution: %v approvals=%d calls=%d", executions, approvals, calls)
	}
}
