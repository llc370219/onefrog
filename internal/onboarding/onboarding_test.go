package onboarding

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/monitor"
	"onefrog/internal/remote"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

type fakeExec struct{ fp string }

func (f fakeExec) Run(_ context.Context, _ string, _ int, _, _, _, known, _ string) (string, string, error) {
	if known == "" {
		return "", f.fp, remote.ErrHostKeyUnknown
	}
	if known != f.fp {
		return "", f.fp, remote.ErrHostKeyMismatch
	}
	return "ONEFROG_CONNECTION_OK\nDarwin\n", f.fp, nil
}

func wait(t *testing.T, m *Manager, key string, want string) View {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v, err := m.Get("owner", key)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status == want {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	v, _ := m.Get("owner", key)
	t.Fatalf("status %q, want %q", v.Status, want)
	return v
}

func TestFingerprintMustBeConfirmed(t *testing.T) {
	db := store.NewMemory()
	box, _ := secret.New(bytes.Repeat([]byte{3}, 32))
	m, err := New("", db, box, nil, fakeExec{fp: "SHA256:abc"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.Start("owner", Request{Name: "n1", Host: "192.0.2.10", Username: "ops", Secret: "pw", Configure: true, ReportBase: "https://panel.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	v = wait(t, m, v.ID, "fingerprint_required")
	if v.Fingerprint != "SHA256:abc" {
		t.Fatalf("fingerprint %q", v.Fingerprint)
	}
	rec, _ := db.GetServer(context.Background(), v.ServerID)
	if rec.HostKey != "" {
		t.Fatal("host key stored before confirmation")
	}
	if _, err := m.Confirm("owner", v.ID, "SHA256:wrong"); err == nil {
		t.Fatal("wrong fingerprint accepted")
	}
	if _, err := m.Confirm("intruder", v.ID, "SHA256:abc"); err == nil {
		t.Fatal("other owner confirmed")
	}
	if _, err := m.Confirm("owner", v.ID, "SHA256:abc"); err != nil {
		t.Fatal(err)
	}
	wait(t, m, v.ID, "partial")
	rec, _ = db.GetServer(context.Background(), v.ServerID)
	if rec.HostKey != "SHA256:abc" {
		t.Fatalf("confirmed key not stored: %q", rec.HostKey)
	}
}

func TestStoredKeyMismatchFails(t *testing.T) {
	db := store.NewMemory()
	box, _ := secret.New(bytes.Repeat([]byte{3}, 32))
	m, _ := New("", db, box, nil, fakeExec{fp: "SHA256:new"})
	enc, _ := box.Encrypt("pw")
	srv, _ := db.CreateServer(context.Background(), store.ServerRec{Server: model.Server{Name: "n1", Host: "192.0.2.11", Port: 22}, EncryptedKey: enc, HostKey: "SHA256:old"})
	rec, _ := db.GetServer(context.Background(), srv.ID)
	v, err := m.Start("owner", Request{ServerID: srv.ID, Name: "n1", Host: rec.Host, Port: rec.Port, Username: "ops", Configure: true, ReportBase: "https://panel.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, m, v.ID, "failed")
	rec, _ = db.GetServer(context.Background(), srv.ID)
	if rec.HostKey != "SHA256:old" {
		t.Fatalf("stored key overwritten: %q", rec.HostKey)
	}
}

type scriptExec struct {
	fp       string
	out      string
	commands []string
}

func (f *scriptExec) Run(_ context.Context, _ string, _ int, _, _, _, known, command string) (string, string, error) {
	f.commands = append(f.commands, command)
	if known == "" {
		return "", f.fp, remote.ErrHostKeyUnknown
	}
	if known != f.fp {
		return "", f.fp, remote.ErrHostKeyMismatch
	}
	return f.out, f.fp, nil
}

func TestProbeLivesOutsidePanelDir(t *testing.T) {
	script := installScript([]byte(`{}`))
	for _, want := range []string{"/opt/onefrog-probe/agent.py", "/etc/onefrog-probe/probe.json", "ConditionPathExists"} {
		if !strings.Contains(script, want) && !strings.Contains(probeUnit(), want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(probeUnit(), "/opt/onefrog/probe") {
		t.Fatal("unit still points into the panel install dir")
	}
	// Re-deploying removes the v0.1.1 copy.
	if !strings.Contains(script, "rmtree('/opt/onefrog/probe'") {
		t.Fatal("legacy probe path is not migrated")
	}
	un := UninstallScript()
	for _, want := range []string{"disable --now onefrog-probe.service", "rm -rf /opt/onefrog-probe /etc/onefrog-probe /opt/onefrog/probe", "ONEFROG_PROBE_REMOVED"} {
		if !strings.Contains(un, want) {
			t.Fatalf("uninstall script missing %q", want)
		}
	}
	if strings.Contains(un, "rm -rf /opt/onefrog ") || strings.Contains(un, "rm -rf /opt/onefrog\n") {
		t.Fatal("uninstall must never delete the panel directory")
	}
}

func TestRemoveProbe(t *testing.T) {
	box, _ := secret.New(bytes.Repeat([]byte{3}, 32))
	enc, _ := box.Encrypt("pw")
	rec := store.ServerRec{Server: model.Server{Host: "192.0.2.5", Port: 22}, EncryptedKey: enc, HostKey: "ssh-ed25519 SHA256:k"}
	for out, want := range map[string]string{"ONEFROG_PROBE_REMOVED\n": "removed", "ONEFROG_PROBE_ABSENT\n": "absent", "sudo: a password is required\n": "failed"} {
		ex := &scriptExec{fp: rec.HostKey, out: out}
		m, _ := New("", store.NewMemory(), box, nil, ex)
		if got := m.RemoveProbe(context.Background(), rec); got.Status != want {
			t.Fatalf("%q: got %v want %s", out, got, want)
		}
	}
	ex := &scriptExec{fp: "ssh-ed25519 SHA256:other"}
	m, _ := New("", store.NewMemory(), box, nil, ex)
	if got := m.RemoveProbe(context.Background(), rec); got.Status != "failed" {
		t.Fatal(got)
	}
	unpinned := rec
	unpinned.HostKey = ""
	ex = &scriptExec{}
	m, _ = New("", store.NewMemory(), box, nil, ex)
	if got := m.RemoveProbe(context.Background(), unpinned); got.Status != "skipped" || len(ex.commands) != 0 {
		t.Fatal("must not connect without a confirmed key", got)
	}
}

func TestModulesWithoutInitializerAreSkipped(t *testing.T) {
	db := store.NewMemory()
	box, _ := secret.New(bytes.Repeat([]byte{3}, 32))
	_, _ = db.SaveModule(context.Background(), model.Module{ID: "assets", Name: "资产与订阅", Enabled: true})
	probe, _ := monitor.New("")
	ex := &scriptExec{fp: "ssh-ed25519 SHA256:k", out: "ONEFROG_CONNECTION_OK\nLinux\nONEFROG_PROBE_INSTALLED\n"}
	m, _ := New("", db, box, probe, ex)
	m.VerifyTimeout = 50 * time.Millisecond
	v, err := m.Start("owner", Request{Name: "n", Host: "192.0.2.9", Secret: "pw", Configure: true, ReportBase: "https://panel.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	v = wait(t, m, v.ID, "fingerprint_required")
	if _, err = m.Confirm("owner", v.ID, "ssh-ed25519 SHA256:k"); err != nil {
		t.Fatal(err)
	}
	v = wait(t, m, v.ID, "partial") // no metrics reported in this test
	for _, s := range v.Steps {
		if s.ID == "modules" && (s.Status != "skipped" || !strings.Contains(s.Message, "资产与订阅")) {
			t.Fatalf("modules step: %+v", s)
		}
	}
	if !strings.Contains(v.Message, "监控尚未确认在线") {
		t.Fatalf("partial must only be caused by the missing report: %q", v.Message)
	}
}
