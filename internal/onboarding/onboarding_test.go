package onboarding

import (
	"bytes"
	"context"
	"testing"
	"time"

	"onefrog/internal/model"
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
