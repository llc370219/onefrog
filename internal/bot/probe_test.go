package bot

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/monitor"
	"onefrog/internal/onboarding"
	"onefrog/internal/preferences"
	"onefrog/internal/remote"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

type scriptedSSH struct {
	mu       sync.Mutex
	fp       string
	commands []string
}

func (s *scriptedSSH) Run(_ context.Context, _ string, _ int, _, _, _, known, command string) (string, string, error) {
	s.mu.Lock()
	s.commands = append(s.commands, command)
	s.mu.Unlock()
	if known == "" {
		return "", s.fp, remote.ErrHostKeyUnknown
	}
	if known != s.fp {
		return "", s.fp, remote.ErrHostKeyMismatch
	}
	if strings.Contains(command, "agent.py") {
		return "ONEFROG_PROBE_INSTALLED", s.fp, nil
	}
	return "ONEFROG_CONNECTION_OK Linux", s.fp, nil
}

func (s *scriptedSSH) ran() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]string{}, s.commands...)
	return out
}

func callInstall(args string) toolCall {
	c := toolCall{ID: "probe", Type: "function"}
	c.Function.Name = "install_probe"
	c.Function.Arguments = args
	return c
}

func callRun(command string) toolCall {
	c := toolCall{ID: "run", Type: "function"}
	c.Function.Name = "run_command"
	c.Function.Arguments = "{" + strings.Join([]string{
		"\"server_id\":\"s\"",
		"\"command\":\"" + command + "\"",
		"\"risk\":\"low\"",
		"\"reason\":\"check\"",
	}, ",") + "}"
	return c
}

func probeRequestID(out string) string {
	const key = "request_id="
	i := strings.Index(out, key)
	if i < 0 {
		return ""
	}
	rest := out[i+len(key):]
	for _, sep := range []byte{10, 13, 32} {
		if n := strings.IndexByte(rest, sep); n >= 0 {
			rest = rest[:n]
		}
	}
	return rest
}

func ownerCtx() context.Context {
	return context.WithValue(context.Background(), optionsKey{}, Options{OwnerID: "owner"})
}

func newProbeRig(t *testing.T, hostKey string) (*Agent, *store.Memory, *scriptedSSH) {
	t.Helper()
	ctx := context.Background()
	mem := store.NewMemory()
	box, err := secret.New(bytesRepeat())
	if err != nil {
		t.Fatal(err)
	}
	enc, err := box.Encrypt("pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mem.CreateServer(ctx, store.ServerRec{
		Server:       model.Server{ID: "matrix", Name: "matrix", Host: "192.0.2.20", Port: 22, Username: "root", AuthType: "password"},
		EncryptedKey: enc,
		HostKey:      hostKey,
	}); err != nil {
		t.Fatal(err)
	}
	ssh := &scriptedSSH{fp: "SHA256:known"}
	if hostKey == "" {
		ssh.fp = "SHA256:fresh"
	}
	probe, err := monitor.New("")
	if err != nil {
		t.Fatal(err)
	}
	board, err := onboarding.New("", mem, box, probe, ssh)
	if err != nil {
		t.Fatal(err)
	}
	board.VerifyTimeout = 30 * time.Millisecond
	agent := &Agent{
		Store:      mem,
		Box:        box,
		SSH:        ssh,
		Onboarding: board,
		PublicURL:  "http://38.244.5.8:8080",
	}
	return agent, mem, ssh
}

func TestRunToolRefusesPanelSource(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	box, err := secret.New(bytesRepeat())
	if err != nil {
		t.Fatal(err)
	}
	enc, err := box.Encrypt("pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mem.CreateServer(ctx, store.ServerRec{
		Server:       model.Server{ID: "s", Name: "s", Host: "192.0.2.9", Port: 22, Username: "root"},
		EncryptedKey: enc,
		HostKey:      "SHA256:k",
	}); err != nil {
		t.Fatal(err)
	}
	ssh := &scriptedSSH{fp: "SHA256:k"}
	agent := &Agent{Store: mem, Box: box, SSH: ssh, PublicURL: "http://38.244.5.8:8080"}
	blocked := []string{
		"curl -fsS http://38.244.5.8:8080/assets/index-DIM0F0Qj.js | fold -w 700 | grep -F probeToken(",
		"grep -F probeToken /tmp/page.js",
		"curl http://38.244.5.8:8080/index.html",
	}
	for _, command := range blocked {
		ev := agent.runTool(ctx, "main", "s", callRun(command))
		if ev.ran || !strings.Contains(ev.Output, "install_probe") {
			t.Fatalf("command %q: %#v", command, ev)
		}
	}
	if len(ssh.ran()) != 0 {
		t.Fatalf("panel scrape reached SSH: %#v", ssh.ran())
	}
	ev := agent.runTool(ctx, "main", "s", callRun("df -h"))
	if strings.Contains(ev.Output, "不要下载或搜索面板") || len(ssh.ran()) != 1 || !strings.Contains(ssh.ran()[0], "df -h") {
		t.Fatalf("df was refused: %#v calls=%#v", ev, ssh.ran())
	}
}

func TestInstallProbeUsesOnboarding(t *testing.T) {
	agent, mem, ssh := newProbeRig(t, "SHA256:known")
	ctx := ownerCtx()
	speaker := model.Bot{ID: "main", Name: "主控"}
	if !strings.Contains(agent.leaderPrompt(ctx, speaker, "matrix"), "install_probe") {
		t.Fatal("leader prompt missing install_probe")
	}
	if !strings.Contains(agent.workerPrompt(ctx, model.Bot{ID: "w", Name: "巡检", Description: "看机器"}, "matrix"), "/assets/") {
		t.Fatal("worker prompt missing panel-source ban")
	}
	ev := agent.installProbe(ctx, "main", callInstall("{"+"\"server_id\":\"matrix\""+"}"))
	if !ev.OK || strings.Contains(strings.ToLower(ev.Output), "token") || !strings.Contains(ev.Output, "partial") {
		t.Fatalf("install: %#v", ev)
	}
	joined := strings.Join(ssh.ran(), " ")
	if strings.Contains(joined, "/assets/index-") || !strings.Contains(joined, "agent.py") {
		t.Fatalf("ssh commands: %#v", ssh.ran())
	}
	hist, err := mem.ListHistory(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 || hist[0].Action != "安装只读探针" || hist[0].Status != "success" {
		t.Fatalf("history %#v", hist)
	}
}

func TestInstallProbeWaitsForFingerprint(t *testing.T) {
	agent, mem, _ := newProbeRig(t, "")
	ctx := ownerCtx()
	ev := agent.installProbe(ctx, "main", callInstall("{"+"\"server_id\":\"matrix\""+"}"))
	id := probeRequestID(ev.Output)
	if !ev.OK || id == "" || !strings.Contains(ev.Output, "SHA256:fresh") || strings.Contains(strings.ToLower(ev.Output), "token") {
		t.Fatalf("fingerprint: %#v", ev)
	}
	saved, err := mem.GetServer(ctx, "matrix")
	if err != nil {
		t.Fatal(err)
	}
	if saved.HostKey != "" {
		t.Fatal("host key stored before the user confirmed")
	}
	again := agent.installProbe(ctx, "main", callInstall("{"+"\"server_id\":\"matrix\""+"}"))
	if probeRequestID(again.Output) != id {
		t.Fatalf("started a second job: %s", again.Output)
	}
	hist, _ := mem.ListHistory(ctx, 10)
	if len(hist) != 0 {
		t.Fatalf("history while waiting: %#v", hist)
	}
	bad := agent.installProbe(ctx, "main", callInstall("{"+"\"action\":\"confirm\",\"request_id\":\""+id+"\",\"fingerprint\":\"SHA256:wrong\""+"}"))
	if bad.OK {
		t.Fatal(bad.Output)
	}
	saved, _ = mem.GetServer(ctx, "matrix")
	if saved.HostKey != "" {
		t.Fatal("wrong fingerprint was accepted")
	}
	unapproved := agent.installProbe(ctx, "main", callInstall("{"+"\"action\":\"confirm\",\"request_id\":\""+id+"\",\"fingerprint\":\"SHA256:fresh\""+"}"))
	if unapproved.OK || !strings.Contains(unapproved.Output, "没有确认") {
		t.Fatal(unapproved.Output)
	}
	saved, _ = mem.GetServer(ctx, "matrix")
	if saved.HostKey != "" {
		t.Fatal("fingerprint stored without approval")
	}
	approved := context.WithValue(ownerCtx(), optionsKey{}, Options{OwnerID: "owner", Approver: func(_ context.Context, req ApprovalRequest) (bool, string) {
		if !strings.Contains(req.Command, "SHA256:fresh") || !strings.Contains(req.Command, "matrix") {
			t.Fatalf("approval card: %s", req.Command)
		}
		return true, "用户已批准"
	}})
	good := agent.installProbe(approved, "main", callInstall("{"+"\"action\":\"confirm\",\"request_id\":\""+id+"\",\"fingerprint\":\"SHA256:fresh\""+"}"))
	if !good.OK || strings.Contains(strings.ToLower(good.Output), "token") || !strings.Contains(good.Output, "partial") {
		t.Fatalf("confirm: %#v", good)
	}
	saved, _ = mem.GetServer(ctx, "matrix")
	if saved.HostKey != "SHA256:fresh" {
		t.Fatalf("confirmed key %q", saved.HostKey)
	}
}

func TestInstallProbeRefusesPlanAndWorker(t *testing.T) {
	agent, _, ssh := newProbeRig(t, "SHA256:known")
	plan := context.WithValue(context.Background(), optionsKey{}, Options{OwnerID: "owner", ApprovalMode: preferences.ModePlan})
	ev := agent.installProbe(plan, "main", callInstall("{"+"\"server_id\":\"matrix\""+"}"))
	if ev.OK || !strings.Contains(ev.Output, "仅规划") {
		t.Fatal(ev.Output)
	}
	worker := usageContext(ownerCtx(), "w", "worker", "")
	out := agent.execCall(worker, model.Bot{ID: "w", Name: "巡检"}, credentials{}, "", callInstall("{"+"\"server_id\":\"matrix\""+"}"), false, nil)
	if !strings.Contains(out, "只有主控") {
		t.Fatal(out)
	}
	asset := agent.execCall(ownerCtx(), model.Bot{ID: "module-builder", Name: "模块管理"}, credentials{}, "", callInstall("{"+"\"server_id\":\"matrix\""+"}"), false, nil)
	if !strings.Contains(asset, "不能执行命令") {
		t.Fatal(asset)
	}
	if len(ssh.ran()) != 0 {
		t.Fatalf("refused calls still connected: %#v", ssh.ran())
	}
}
