package bot

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/secret"
	"onefrog/internal/services"
	"onefrog/internal/store"
)

type inventorySSH struct{ calls atomic.Int32 }

func (s *inventorySSH) Run(context.Context, string, int, string, string, string, string, string) (string, string, error) {
	s.calls.Add(1)
	return `{"items":[{"kind":"systemd","name":"nginx.service","state":"active","detail":"running"}],"sources":{"systemd":{"status":"ok"},"docker":{"status":"unavailable"},"ports":{"status":"ok"}}}`, "", nil
}
func TestInspectServicesRespectsScopeApprovalAndSharedCache(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	box, _ := secret.New(bytes.Repeat([]byte{7}, 32))
	cipher, _ := box.Encrypt("test-password")
	st.CreateServer(ctx, store.ServerRec{Server: model.Server{ID: "node", Name: "test-node"}, EncryptedKey: cipher, HostKey: "SHA256:test"})
	ssh := &inventorySSH{}
	a := &Agent{Store: st, Box: box, SSH: ssh}
	a.Services = services.New(st, box, ssh, a.RedactServiceOutput)
	speaker := model.Bot{ID: "main", Name: "主控"}
	call := toolCall{}
	call.Function.Name = "inspect_services"
	call.Function.Arguments = `{"action":"scan","server_id":"test-node"}`
	restricted := context.WithValue(ctx, optionsKey{}, Options{RestrictServers: true})
	out := a.inspectServices(restricted, speaker, "", call, nil)
	if ssh.calls.Load() != 0 || !strings.Contains(out, "没有执行") {
		t.Fatalf("unselected host scanned without approval: %s", out)
	}
	plan := context.WithValue(ctx, optionsKey{}, Options{ApprovalMode: preferences.ModePlan})
	a.inspectServices(plan, speaker, "node", call, nil)
	if ssh.calls.Load() != 0 {
		t.Fatal("plan mode ran SSH")
	}
	manual := context.WithValue(ctx, optionsKey{}, Options{ApprovalMode: preferences.ModeManual})
	a.inspectServices(manual, speaker, "node", call, nil)
	if ssh.calls.Load() != 0 {
		t.Fatal("manual mode bypassed approval")
	}
	out = a.inspectServices(ctx, speaker, "node", call, nil)
	if ssh.calls.Load() != 1 || !strings.Contains(out, "untrusted_output") || !strings.Contains(out, "nginx.service") {
		t.Fatalf("scan result missing: %s", out)
	}
	if a.Services.Views([]string{"node"})[0].Status != "partial" {
		t.Fatal("AI scan did not update module")
	}
	call.Function.Arguments = `{"action":"list"}`
	out = a.inspectServices(restricted, speaker, "", call, nil)
	if strings.Contains(out, "nginx.service") {
		t.Fatal("list bypassed server scope")
	}
	out = a.inspectServices(ctx, speaker, "", call, nil)
	if !strings.Contains(out, "untrusted_output") || !strings.Contains(out, "nginx.service") {
		t.Fatal("cached server output trusted or missing")
	}
}
