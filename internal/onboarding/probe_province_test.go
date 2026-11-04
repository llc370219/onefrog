package onboarding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/monitor"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

type provinceExec struct {
	calls int
	fail  bool
}

func (e *provinceExec) Run(_ context.Context, _ string, _ int, _, _, plain, pin, command string) (string, string, error) {
	e.calls++
	if plain != "test-password" || pin != "SHA256:test" || !strings.Contains(command, "systemctl") {
		return "", "", errors.New("unexpected SSH request")
	}
	if e.fail {
		return "", "", errors.New("SSH failed")
	}
	return "ONEFROG_PROVINCE_APPLIED", "", nil
}

func TestConfigureProvincePreservesTokenAndOnlySavesAfterSSH(t *testing.T) {
	db := store.NewMemory()
	box, _ := secret.New(bytes.Repeat([]byte{3}, 32))
	key, _ := box.Encrypt("test-password")
	rec, err := db.CreateServer(context.Background(), store.ServerRec{Server: model.Server{Name: "test", Host: "192.0.2.1", Port: 22, Username: "root", AuthType: "password"}, HostKey: "SHA256:test", EncryptedKey: key})
	if err != nil {
		t.Fatal(err)
	}
	mon, _ := monitor.New("")
	token, _ := mon.Issue(rec.ID)
	ssh := &provinceExec{}
	m, _ := New("", db, box, mon, ssh)
	if err := m.ConfigureProvince(context.Background(), rec.ID, "广东", 15); err == nil || ssh.calls != 0 {
		t.Fatal("unsafe interval reached SSH")
	}
	if err := m.ConfigureProvince(context.Background(), rec.ID, "火星"); err == nil || ssh.calls != 0 {
		t.Fatal("invalid province reached SSH")
	}
	if err := m.ConfigureProvince(context.Background(), rec.ID, "广东", 120); err != nil {
		t.Fatal(err)
	}
	if got := mon.Views([]string{rec.ID}, time.Now())[0].Province; got == nil || got.Code != "gd" {
		t.Fatal("province not saved")
	}
	if mon.Views([]string{rec.ID}, time.Now())[0].PingIntervalSeconds != 120 {
		t.Fatal("interval not reflected in monitor")
	}
	ssh.fail = true
	if err := m.ConfigureProvince(context.Background(), rec.ID, "北京"); err == nil {
		t.Fatal("SSH failure accepted")
	}
	if got := mon.Views([]string{rec.ID}, time.Now())[0].Province; got == nil || got.Code != "gd" {
		t.Fatal("failed apply changed province")
	}
	ssh.fail = false
	if err := m.ConfigureProvince(context.Background(), rec.ID, ""); err != nil {
		t.Fatal(err)
	}
	if mon.Views([]string{rec.ID}, time.Now())[0].Province != nil {
		t.Fatal("province not cleared")
	}
	if !mon.Authorized(rec.ID, token) {
		t.Fatal("existing probe token invalidated")
	}
}

func TestProvinceScriptPreservesConfigAndRollsBack(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "apply", true: "restart_failure"}[fail], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "probe.json")
			original := []byte(`{"token":"private-token","endpoint":"https://panel.example/api/probe/report","interfaces":["eth0"],"interval_seconds":30,"ping_targets":[{"name":"电信","host":"bj-ct-v4.ip.zstaticcdn.com","port":80},{"name":"custom","host":"1.1.1.1"}]}`)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			agentPath := filepath.Join(filepath.Dir(path), "agent.py")
			if err := os.WriteFile(agentPath, []byte("old-agent"), 0755); err != nil {
				t.Fatal(err)
			}
			script := probeSettingsScript(path, agentPath, "gd", 120)
			py := strings.Split(strings.Split(script, "<<'ONEFROG_PROVINCE'\n")[1], "\nONEFROG_PROVINCE")[0]
			mock := "import subprocess\nsubprocess.run=lambda *a,**kw: None\n"
			if fail {
				mock = "import subprocess\ndef fail(*a,**kw):\n if kw.get('check'): raise RuntimeError('restart failed')\nsubprocess.run=fail\n"
			}
			out, runErr := exec.Command(python, "-c", mock+py).CombinedOutput()
			got, _ := os.ReadFile(path)
			if fail {
				agent, _ := os.ReadFile(agentPath)
				if string(agent) != "old-agent" {
					t.Fatal("agent not restored")
				}
				if runErr == nil || !bytes.Equal(got, original) {
					t.Fatalf("rollback failed: %s %v", out, runErr)
				}
				return
			}
			if runErr != nil {
				t.Fatalf("%s: %v", out, runErr)
			}
			agent, _ := os.ReadFile(agentPath)
			if string(agent) != monitor.AgentScript {
				t.Fatal("agent not upgraded")
			}
			var config struct {
				Token        string
				Endpoint     string
				Interfaces   []string
				Interval     int                  `json:"interval_seconds"`
				PingInterval int                  `json:"ping_interval_seconds"`
				Targets      []monitor.PingTarget `json:"ping_targets"`
			}
			if err := json.Unmarshal(got, &config); err != nil {
				t.Fatal(err)
			}
			if config.PingInterval != 120 {
				t.Fatal("independent interval not saved")
			}
			if config.Token != "private-token" || config.Endpoint != "https://panel.example/api/probe/report" || config.Interval != 30 || len(config.Interfaces) != 1 || len(config.Targets) != 4 || config.Targets[0].Host != "gd-ct-v4.ip.zstaticcdn.com" || config.Targets[3].Name != "custom" {
				t.Fatalf("config changed unexpectedly: %+v", config)
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0600 {
				t.Fatal("config permissions")
			}
		})
	}
}
