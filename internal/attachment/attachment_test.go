package attachment

import (
	"bytes"
	"onefrog/internal/secret"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedOwnerBoundFiles(t *testing.T) {
	box, _ := secret.New(bytes.Repeat([]byte{9}, 32))
	dir := t.TempDir()
	m := New(dir, box)
	ref, err := m.Put("owner", "../../config.txt", []byte("secret-config-test"))
	if err != nil {
		t.Fatal(err)
	}
	if ref.Name != "config.txt" || ref.Mime != "text/plain" {
		t.Fatal(ref)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ref.ID+".enc"))
	if bytes.Contains(raw, []byte("secret-config-test")) {
		t.Fatal("plaintext file persisted")
	}
	again := New(dir, box)
	f, err := again.Get("owner", ref.ID)
	if err != nil || string(f.Data) != "secret-config-test" {
		t.Fatal(err)
	}
	if _, err = again.Get("another", ref.ID); err == nil {
		t.Fatal("cross-user file access")
	}
	if _, err = again.Get("owner", "../../foo"); err == nil {
		t.Fatal("path traversal")
	}
}
func TestFileLimits(t *testing.T) {
	m := New("", nil)
	for _, data := range [][]byte{nil, bytes.Repeat([]byte{'x'}, MaxSize+1), []byte{0, 0, 0, 1}, bytes.Repeat([]byte{'a'}, 256*1024+1)} {
		if _, err := m.Put("user", "bad", data); err == nil {
			t.Fatal("invalid file accepted")
		}
	}
	ref, err := m.Put("user", "runbook.pdf", []byte("%PDF-1.4\nexample"))
	if err != nil || ref.Mime != "application/pdf" {
		t.Fatal(ref, err)
	}
}
