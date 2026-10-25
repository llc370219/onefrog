package providerlogin

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"onefrog/internal/secret"
)

func TestAcknowledgementPersistsEncrypted(t *testing.T) {
	box, _ := secret.New(bytes.Repeat([]byte{5}, 32))
	path := filepath.Join(t.TempDir(), "oauth.enc")
	legacy, _ := box.Encrypt(`{"owner/oauth-openai":{"owner":"owner","kind":"oauth-openai","email":"a@example.com"}}`)
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.accounts) != 1 {
		t.Fatal("legacy accounts not loaded")
	}
	m.Definitions = []Definition{{Kind: "oauth-openai", ClientID: "x"}}
	if _, err := m.Acknowledge("owner", "oauth-openai"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("acknowledgements")) {
		t.Fatal("oauth.enc must be encrypted")
	}
	m2, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.accounts) != 1 {
		t.Fatal("accounts lost after v2 rewrite")
	}
	a, ok := m2.Acknowledgements("owner")["oauth-openai"]
	if !ok || a.At.IsZero() || a.Version != ConsentVersion {
		t.Fatalf("acknowledgement not persisted: %+v", a)
	}
	if len(m2.Acknowledgements("other")) != 0 {
		t.Fatal("acknowledgement leaked to another owner")
	}
}

func TestUnconfiguredGoogleNeedsSetup(t *testing.T) {
	t.Setenv("OF_GOOGLE_OAUTH_CLIENT_ID", "")
	var g *Definition
	for _, d := range Definitions() {
		if d.Kind == "oauth-antigravity" {
			d := d
			g = &d
		}
	}
	if g == nil || g.Configured || g.SetupHint == "" {
		t.Fatalf("google should be listed as needing configuration: %+v", g)
	}
	box, _ := secret.New(bytes.Repeat([]byte{5}, 32))
	m, _ := New("", box)
	_, _ = m.Acknowledge("owner", "oauth-antigravity")
	if _, err := m.Start("owner", "oauth-antigravity", ConsentVersion); err == nil {
		t.Fatal("unconfigured provider must not start")
	}
}
