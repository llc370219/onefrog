package migration

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"onefrog/internal/secret"
)

func TestEncryptedArchiveRoundTripAndTampering(t *testing.T) {
	key := bytes.Repeat([]byte{2}, 32)
	box, _ := secret.New(key)
	enc, _ := box.Encrypt(`[{"body":"private bot memory"}]`)
	files := map[string][]byte{"master.key": key, "memories.enc": []byte(enc), "accounts.json": []byte(`[]`)}
	raw, err := Seal(files, Manifest{Version: "dev", Driver: "memory"}, "a long backup password")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("private bot memory")) || bytes.Contains(raw, key) {
		t.Fatal("plaintext exposed")
	}
	got, m, err := Open(raw, "a long backup password")
	if err != nil {
		t.Fatal(err)
	}
	if m.Driver != "memory" || !bytes.Equal(got["memories.enc"], files["memories.enc"]) {
		t.Fatal("round trip changed data")
	}
	if _, _, err = Open(raw, "the wrong password"); err == nil {
		t.Fatal("wrong password accepted")
	}
	raw[len(raw)-1] ^= 1
	if _, _, err = Open(raw, "a long backup password"); err == nil {
		t.Fatal("tamper accepted")
	}
}

func TestArchivePathsAndTypes(t *testing.T) {
	for _, name := range []string{"../master.key", "chats/../../test.enc", "uploads/a/b.enc", "uploads/a\\b.enc", "sessions.enc", ".migration-current", ".restore-123/master.key", "database.json/evil"} {
		if allowed(name) {
			t.Errorf("unsafe name %q", name)
		}
	}
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "accounts.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFiles(dir, bytes.Repeat([]byte{1}, 32)); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestGenerationSwitchPreservesOriginal(t *testing.T) {
	root := t.TempDir()
	old := []byte("old-account")
	if err := os.WriteFile(filepath.Join(root, "accounts.json"), old, 0600); err != nil {
		t.Fatal(err)
	}
	stage, err := Stage(root, map[string][]byte{"accounts.json": []byte("new-account")}, Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := Resolve(root)
	if err != nil || before != root {
		t.Fatal(before, err)
	}
	if err = Activate(root, stage); err != nil {
		t.Fatal(err)
	}
	after, err := Resolve(root)
	if err != nil || after != stage {
		t.Fatal(after, err)
	}
	b, err := os.ReadFile(filepath.Join(root, "accounts.json"))
	if err != nil || !bytes.Equal(b, old) {
		t.Fatal("original lost")
	}
	if _, err = os.Stat(filepath.Join(stage, ".migration-paused")); err != nil {
		t.Fatal("restored instance not paused")
	}
}

func TestTransferProofBoundToPayloadAndPurpose(t *testing.T) {
	code := "test capability"
	data := []byte("ciphertext")
	proof := Proof(code, "upload", data)
	if !Verify(code, "upload", data, proof) {
		t.Fatal("valid proof rejected")
	}
	if Verify(code, "received", data, proof) || Verify("wrong", "upload", data, proof) || Verify(code, "upload", []byte("modified"), proof) {
		t.Fatal("proof not bound")
	}
}

func TestFrogBotBackupIncludesStateAndWorkspace(t *testing.T) {
	dir := t.TempDir()
	const account = "frog-bot/757365722d31/"
	names := []string{account + "stream.jsonl", account + "memory.md", account + "schedules.json",
		account + "secrets.json", account + "checkpoints/task_1.json", account + "workspace/uploads/input.txt"}
	for _, name := range append(append([]string{}, names...), account+"schedules.json.tmp", account+"master.key") {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := ReadFiles(dir, bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if string(files[name]) != name {
			t.Errorf("backup is missing %s", name)
		}
	}
	for _, name := range []string{account + "schedules.json.tmp", account + "master.key",
		account + "workspace/../../outside", account + "workspace/C:/outside", "frog-bot/bogus/stream.jsonl"} {
		if _, ok := files[name]; ok || allowed(name) {
			t.Errorf("unexpected backup path %s", name)
		}
	}
}
