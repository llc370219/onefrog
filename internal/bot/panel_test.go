package bot

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/remote"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"onefrog/internal/update"
)

type gateSSH struct{ fp string }

func (s gateSSH) Run(_ context.Context, _ string, _ int, _, _, _, knownKey, _ string) (string, string, error) {
	if knownKey == "" {
		return "", s.fp, remote.ErrHostKeyUnknown
	}
	if knownKey != s.fp {
		return "", s.fp, remote.ErrHostKeyMismatch
	}
	return "ok", s.fp, nil
}

type memUpdates struct {
	st      update.Status
	applied string
	checks  int
}

func (m *memUpdates) Status() update.Status { return m.st }
func (m *memUpdates) CheckNow(context.Context) (update.Status, error) {
	m.checks++
	return m.st, nil
}
func (m *memUpdates) Apply() (string, error) {
	if !m.st.Available {
		return "", update.ErrNoUpdate
	}
	m.applied = m.st.Latest
	return m.st.Latest, nil
}

func callPanel(args string) toolCall {
	c := toolCall{ID: "p", Type: "function"}
	c.Function.Name = "panel"
	c.Function.Arguments = args
	return c
}

func TestPanelOperatesTheConsole(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	box, err := secret.New(bytesRepeat())
	if err != nil {
		t.Fatal(err)
	}
	settings, err := preferences.New(filepath.Join(t.TempDir(), "settings.enc"), box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mem.CreateBot(ctx, model.Bot{ID: "main", Name: "主控"}); err != nil {
		t.Fatal(err)
	}
	if _, err = mem.CreateBot(ctx, model.Bot{ID: "check", Name: "巡检"}); err != nil {
		t.Fatal(err)
	}
	if _, err = mem.CreateServer(ctx, store.ServerRec{Server: model.Server{ID: "web", Name: "web-1", Host: "203.0.113.10", Port: 22, Username: "ops", Status: "unknown"}}); err != nil {
		t.Fatal(err)
	}
	updates := &memUpdates{st: update.Status{Current: "v0.1.8", Latest: "v0.1.9", Available: true, CanApply: true}}
	ssh := gateSSH{fp: "SHA256:abc"}
	agent := &Agent{Store: mem, Box: box, Settings: settings, SettingsLive: settings, SSH: ssh, Updates: updates}
	speaker := model.Bot{ID: "main", Name: "主控"}

	ev := agent.panelAct(ctx, speaker, callPanel(`{"action":"overview"}`))
	if !ev.OK || !strings.Contains(ev.Output, "web-1") || !strings.Contains(ev.Output, "模型还没接上") {
		t.Fatalf("overview: %#v", ev)
	}
	if !strings.Contains(agent.leaderPrompt(ctx, speaker, ""), "panel") {
		t.Fatal("leader prompt missing panel")
	}

	ev = agent.panelAct(ctx, speaker, callPanel(`{"action":"set_module","id":"compare","enabled":true}`))
	if !ev.OK {
		t.Fatal(ev.Output)
	}
	mod, err := mem.GetModule(ctx, "compare")
	if err != nil || !mod.Enabled || mod.Name != "磁盘对比" {
		t.Fatalf("module %#v %v", mod, err)
	}

	ev = agent.panelAct(ctx, speaker, callPanel(`{"action":"set_bot","id":"main","enabled":false}`))
	if ev.OK || !strings.Contains(ev.Output, "不能停用") {
		t.Fatal(ev.Output)
	}
	ev = agent.panelAct(ctx, speaker, callPanel(`{"action":"set_bot","id":"巡检","enabled":false}`))
	if !ev.OK || settings.BotEnabled("check") {
		t.Fatalf("bot switch: %s enabled=%v", ev.Output, settings.BotEnabled("check"))
	}

	ev = agent.panelAct(ctx, speaker, callPanel(`{"action":"test_server","id":"web-1"}`))
	if ev.OK || ev.ServerID != "web" || !strings.Contains(ev.Output, "SHA256:abc") {
		t.Fatalf("unknown key: %#v", ev)
	}
	saved, _ := mem.GetServer(ctx, "web")
	if saved.HostKey != "" || saved.Status == "online" {
		t.Fatalf("fingerprint was trusted: %#v", saved.Server)
	}
	saved.HostKey = "SHA256:abc"
	if _, err = mem.UpdateServer(ctx, saved); err != nil {
		t.Fatal(err)
	}
	ev = agent.panelAct(ctx, speaker, callPanel(`{"action":"test_server","id":"web"}`))
	if !ev.OK {
		t.Fatal(ev.Output)
	}
	saved, _ = mem.GetServer(ctx, "web")
	if saved.Status != "online" {
		t.Fatalf("status %s", saved.Status)
	}

	plan := context.WithValue(ctx, optionsKey{}, Options{ApprovalMode: preferences.ModePlan})
	ev = agent.panelAct(plan, speaker, callPanel(`{"action":"set_module","id":"compare","enabled":false}`))
	if ev.OK {
		t.Fatal("plan mode changed a module")
	}

	ev = agent.panelAct(ctx, speaker, callPanel(`{"action":"apply_update"}`))
	if ev.OK || updates.applied != "" {
		t.Fatalf("upgrade without approval: %#v applied=%s", ev, updates.applied)
	}
	approved := context.WithValue(ctx, optionsKey{}, Options{Approver: func(context.Context, ApprovalRequest) (bool, string) {
		return true, "用户已批准"
	}})
	ev = agent.panelAct(approved, speaker, callPanel(`{"action":"apply_update"}`))
	if !ev.OK || updates.applied != "v0.1.9" || !strings.Contains(ev.Output, "v0.1.9") {
		t.Fatalf("upgrade: %#v applied=%s", ev, updates.applied)
	}
	ev = agent.panelAct(ctx, speaker, callPanel(`{"action":"check_update"}`))
	if !ev.OK || updates.checks != 1 || !strings.Contains(ev.Output, "v0.1.9") {
		t.Fatalf("check: %#v checks=%d", ev, updates.checks)
	}

	worker := usageContext(ctx, "check", "worker", "")
	if out := agent.execCall(worker, model.Bot{ID: "check", Name: "巡检"}, credentials{}, "", callPanel(`{"action":"overview"}`), false, nil); !strings.Contains(out, "只有主控") {
		t.Fatal(out)
	}
}

func TestPanelTestDoesNotAcceptAModelFingerprint(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	if _, err := mem.CreateServer(ctx, store.ServerRec{Server: model.Server{ID: "web", Name: "web", Host: "203.0.113.8"}}); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Store: mem, SSH: gateSSH{fp: "SHA256:abc"}}
	ev := agent.panelAct(ctx, model.Bot{ID: "main"}, callPanel(`{"action":"test_server","id":"web","fingerprint":"SHA256:abc"}`))
	if ev.OK || !strings.Contains(ev.Output, "尚未确认") {
		t.Fatal(ev.Output)
	}
	saved, err := mem.GetServer(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if saved.HostKey != "" {
		t.Fatal(saved.HostKey)
	}
}

type removedProbes struct{ ids []string }

func (r *removedProbes) Remove(id string) error {
	r.ids = append(r.ids, id)
	return nil
}

func panelArgs(v map[string]any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestPanelDeletesAServerAfterApproval(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	if _, err := mem.CreateServer(ctx, store.ServerRec{Server: model.Server{ID: "old", Name: "panstar-usp-dc2", Host: "154.21.205.100", Port: 22}}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.CreateServer(ctx, store.ServerRec{Server: model.Server{ID: "dc2", Name: "DC2", Host: "203.0.113.20"}}); err != nil {
		t.Fatal(err)
	}
	probes := &removedProbes{}
	agent := &Agent{Store: mem, Probes: probes}
	speaker := model.Bot{ID: "main", Name: "主控"}

	ev := agent.panelAct(ctx, speaker, callPanel(panelArgs(map[string]any{"action": "delete_server", "id": "panstar-usp-dc2"})))
	if ev.OK || !strings.Contains(ev.Output, "没有删除") {
		t.Fatal(ev.Output)
	}
	if _, err := mem.GetServer(ctx, "old"); err != nil {
		t.Fatal("refused delete removed the server")
	}

	plan := context.WithValue(ctx, optionsKey{}, Options{ApprovalMode: preferences.ModePlan})
	ev = agent.panelAct(plan, speaker, callPanel(panelArgs(map[string]any{"action": "delete_server", "id": "old"})))
	if ev.OK || !strings.Contains(ev.Output, "仅规划") {
		t.Fatal(ev.Output)
	}

	denied := context.WithValue(ctx, optionsKey{}, Options{Approver: func(context.Context, ApprovalRequest) (bool, string) {
		return false, "先留着"
	}})
	ev = agent.panelAct(denied, speaker, callPanel(panelArgs(map[string]any{"action": "delete_server", "id": "old"})))
	if ev.OK || !strings.Contains(ev.Output, "先留着") {
		t.Fatal(ev.Output)
	}
	if _, err := mem.GetServer(ctx, "old"); err != nil {
		t.Fatal(err)
	}

	approved := context.WithValue(ctx, optionsKey{}, Options{Approver: func(_ context.Context, req ApprovalRequest) (bool, string) {
		if !strings.Contains(req.Command, "panstar-usp-dc2") || !strings.Contains(req.Command, "154.21.205.100") || !strings.Contains(req.Command, "探针") {
			t.Fatalf("approval card: %s", req.Command)
		}
		return true, "用户已批准"
	}})
	ev = agent.panelAct(approved, speaker, callPanel(panelArgs(map[string]any{"action": "delete_server", "id": "panstar-usp-dc2", "uninstall_probe": false})))
	if !ev.OK || ev.ServerID != "old" || !strings.Contains(ev.Output, "已删除") || !strings.Contains(ev.Output, "未卸载探针") {
		t.Fatalf("%#v", ev)
	}
	if _, err := mem.GetServer(ctx, "old"); err == nil {
		t.Fatal("server still present")
	}
	if _, err := mem.GetServer(ctx, "dc2"); err != nil {
		t.Fatal("kept server was removed")
	}
	if len(probes.ids) != 1 || probes.ids[0] != "old" {
		t.Fatalf("probes %#v", probes.ids)
	}
	if !strings.Contains(agent.leaderPrompt(ctx, speaker, ""), "delete_server") {
		t.Fatal("prompt does not mention delete_server")
	}

	keep := context.WithValue(ctx, optionsKey{}, Options{Approver: func(_ context.Context, req ApprovalRequest) (bool, string) {
		if !strings.Contains(req.Command, "尝试卸载") {
			t.Fatalf("default should uninstall the probe: %s", req.Command)
		}
		return true, "用户已批准"
	}})
	ev = agent.panelAct(keep, speaker, callPanel(panelArgs(map[string]any{"action": "delete_server", "id": "DC2"})))
	if !ev.OK || !strings.Contains(ev.Output, "未卸载探针") || !strings.Contains(ev.Output, "SSH") {
		t.Fatal(ev.Output)
	}

	if _, err := mem.CreateServer(ctx, store.ServerRec{Server: model.Server{ID: "a", Name: "same", Host: "203.0.113.1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.CreateServer(ctx, store.ServerRec{Server: model.Server{ID: "b", Name: "same", Host: "203.0.113.2"}}); err != nil {
		t.Fatal(err)
	}
	ev = agent.panelAct(keep, speaker, callPanel(panelArgs(map[string]any{"action": "delete_server", "id": "same"})))
	if ev.OK || !strings.Contains(ev.Output, "id=a") || !strings.Contains(ev.Output, "id=b") {
		t.Fatal(ev.Output)
	}
	if _, err := mem.GetServer(ctx, "a"); err != nil {
		t.Fatal(err)
	}
}
