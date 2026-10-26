package store

import (
	"bytes"
	"context"
	"encoding/json"
	"onefrog/internal/model"
	"onefrog/internal/secret"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func persistBox(t *testing.T) *secret.Box {
	t.Helper()
	box, err := secret.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// reopen simulates a restart: a fresh Memory loading the same data directory.
func reopen(t *testing.T, dir string, box *secret.Box) *Memory {
	t.Helper()
	m, err := NewMemoryAccounts(filepath.Join(dir, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = m.EnableServerPersistence(filepath.Join(dir, "servers.enc"), box); err != nil {
		t.Fatal(err)
	}
	if err = m.EnableBotPersistence(filepath.Join(dir, "bots.enc"), box); err != nil {
		t.Fatal(err)
	}
	if err = m.EnablePersistence(dir, box); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestOldDataDirStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	// A v0.1.1 data directory has none of the new files.
	if err := os.WriteFile(filepath.Join(dir, "settings.enc"), []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	m := reopen(t, dir, persistBox(t))
	ctx := context.Background()
	if s, _ := m.ListSecrets(ctx); len(s) != 0 {
		t.Fatal(s)
	}
	if c, _ := m.ListChatSessions(ctx); len(c) != 0 {
		t.Fatal(c)
	}
}

func TestSecretsSurviveRestart(t *testing.T) {
	dir, box, ctx := t.TempDir(), persistBox(t), context.Background()
	m := reopen(t, dir, box)
	a, err := m.CreateSecret(ctx, SecretRec{Secret: model.Secret{Name: "db-pass"}, EncryptedValue: "cipher-a"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := m.CreateSecret(ctx, SecretRec{Secret: model.Secret{Name: "gone"}, EncryptedValue: "cipher-b"})
	if err = m.DeleteSecret(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, secretsFile))
	if strings.Contains(string(raw), "db-pass") || strings.Contains(string(raw), "cipher-a") {
		t.Fatal("secrets file is not encrypted")
	}
	m = reopen(t, dir, box)
	got, err := m.SecretByName(ctx, "db-pass")
	if err != nil || got.ID != a.ID || got.EncryptedValue != "cipher-a" {
		t.Fatal(got, err)
	}
	if _, err = m.SecretByName(ctx, "gone"); err != ErrNotFound {
		t.Fatal("deleted secret came back")
	}
	if _, err = m.CreateSecret(ctx, SecretRec{Secret: model.Secret{Name: "db-pass"}}); err != ErrConflict {
		t.Fatal("name index not restored", err)
	}
}

func TestSessionsSurviveRestart(t *testing.T) {
	dir, box, ctx := t.TempDir(), persistBox(t), context.Background()
	m := reopen(t, dir, box)
	u, _ := m.CreateUser(ctx, "admin", "hash")
	if err := m.CreateSession(ctx, u.ID, "live", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = m.CreateSession(ctx, u.ID, "expired", time.Now().Add(-time.Hour))
	_ = m.CreateSession(ctx, u.ID, "logout", time.Now().Add(time.Hour))
	_ = m.DeleteSession(ctx, "logout")
	m = reopen(t, dir, box)
	if got, err := m.UserBySession(ctx, "live"); err != nil || got.ID != u.ID {
		t.Fatal(got, err)
	}
	for _, k := range []string{"expired", "logout"} {
		if _, err := m.UserBySession(ctx, k); err != ErrNotFound {
			t.Fatal(k, "should not be valid")
		}
	}
	// Changing the password ends other sessions, also after a restart.
	_ = m.CreateSession(ctx, u.ID, "other", time.Now().Add(time.Hour))
	if _, err := m.UpdateUser(ctx, u, "new-hash", "live"); err != nil {
		t.Fatal(err)
	}
	m = reopen(t, dir, box)
	if _, err := m.UserBySession(ctx, "other"); err != ErrNotFound {
		t.Fatal("revoked session came back")
	}
	if _, err := m.UserBySession(ctx, "live"); err != nil {
		t.Fatal(err)
	}
}

func TestChatsSurviveRestart(t *testing.T) {
	dir, box, ctx := t.TempDir(), persistBox(t), context.Background()
	m := reopen(t, dir, box)
	s, err := m.EnsureSession(ctx, "../../etc/passwd", "main", "磁盘")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.AddMessage(ctx, model.Message{SessionID: s.ID, Role: "user", Content: "看看磁盘"}); err != nil {
		t.Fatal(err)
	}
	if _, err = m.AddMessage(ctx, model.Message{SessionID: s.ID, Role: "assistant", Content: "还剩 40%"}); err != nil {
		t.Fatal(err)
	}
	if err = m.SaveChatOptions(ctx, s.ID, json.RawMessage(`{"servers":["a"]}`)); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, chatsDir))
	if len(entries) != 1 || strings.Contains(entries[0].Name(), "passwd") {
		t.Fatal("unexpected chat files", entries)
	}
	m = reopen(t, dir, box)
	msgs, err := m.ListMessages(ctx, s.ID)
	if err != nil || len(msgs) != 2 || msgs[1].Content != "还剩 40%" {
		t.Fatal(msgs, err)
	}
	list, _ := m.ListChatSessions(ctx)
	if len(list) != 1 || list[0].Title != "磁盘" || string(list[0].Options) != `{"servers":["a"]}` {
		t.Fatal(list)
	}
}

func TestChatLimits(t *testing.T) {
	dir, box, ctx := t.TempDir(), persistBox(t), context.Background()
	m := reopen(t, dir, box)
	s, _ := m.EnsureSession(ctx, "big", "main", "big")
	base := time.Now().Add(-time.Hour)
	for i := 0; i < MaxMessagesPerChat+5; i++ {
		m.messages[s.ID] = append(m.messages[s.ID], model.Message{ID: "x", SessionID: s.ID, CreatedAt: base})
	}
	if _, err := m.AddMessage(ctx, model.Message{SessionID: s.ID, Content: "last"}); err != nil {
		t.Fatal(err)
	}
	m = reopen(t, dir, box)
	msgs, _ := m.ListMessages(ctx, s.ID)
	if len(msgs) != MaxMessagesPerChat || msgs[len(msgs)-1].Content != "last" {
		t.Fatal(len(msgs))
	}
	for i := 0; i < MaxChats+3; i++ {
		m.chats[string(rune('a'+i%26))+time.Duration(i).String()] = model.Session{CreatedAt: base.Add(-time.Hour)}
	}
	if _, err := m.EnsureSession(ctx, "newest", "main", "n"); err != nil {
		t.Fatal(err)
	}
	if len(m.chats) != MaxChats {
		t.Fatal(len(m.chats))
	}
	if _, ok := m.chats["newest"]; !ok {
		t.Fatal("newest chat pruned")
	}
}

func TestHistoryAndModulesSurviveRestart(t *testing.T) {
	dir, box, ctx := t.TempDir(), persistBox(t), context.Background()
	m := reopen(t, dir, box)
	h, err := m.AddHistory(ctx, HistoryRec{History: model.History{Action: "systemctl reload nginx", Status: "success", CanRollback: true}, Snapshot: `{"a":1}`})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.FinishRollback(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = m.SaveModule(ctx, model.Module{ID: "custom", Name: "自定义", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = m.SetModuleEnabled(ctx, "custom", false); err != nil {
		t.Fatal(err)
	}
	m = reopen(t, dir, box)
	got, err := m.GetHistory(ctx, h.ID)
	if err != nil || got.Status != "rolled_back" || got.CanRollback || got.Snapshot != `{"a":1}` {
		t.Fatal(got, err)
	}
	mod, err := m.GetModule(ctx, "custom")
	if err != nil || mod.Enabled || mod.Name != "自定义" {
		t.Fatal(mod, err)
	}
}

func TestWrongKeyFailsLoudly(t *testing.T) {
	dir, ctx := t.TempDir(), context.Background()
	m := reopen(t, dir, persistBox(t))
	_, _ = m.CreateSecret(ctx, SecretRec{Secret: model.Secret{Name: "x"}})
	other, _ := secret.New(bytes.Repeat([]byte{1}, 32))
	if err := NewMemory().EnablePersistence(dir, other); err == nil {
		t.Fatal("expected decrypt error with the wrong master key")
	}
}
