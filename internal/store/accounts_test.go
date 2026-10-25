package store

import (
	"context"
	"onefrog/internal/model"
	"path/filepath"
	"testing"
	"time"
)

func TestAccountPersistenceAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	m, err := NewMemoryAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user, err := m.CreateUser(ctx, "first", "hashed-old")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = m.CreateUser(ctx, "other", "hash")
	user.Username = "other"
	if _, err = m.UpdateUser(ctx, user, "", "keep"); err != ErrConflict {
		t.Fatal(err)
	}
	user.Username = "updated"
	user.DisplayName = "自定义名字"
	user.Avatar = "avatar"
	if _, err = m.UpdateUser(ctx, user, "hashed-new", "keep"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewMemoryAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	u, hash, err := reloaded.UserByName(ctx, "updated")
	if err != nil || hash != "hashed-new" || u.DisplayName != user.DisplayName || u.Avatar != user.Avatar {
		t.Fatal(u, err)
	}
	if _, _, err = reloaded.UserByName(ctx, "first"); err != ErrNotFound {
		t.Fatal("old login remained")
	}
}
func TestDirectoryOrderAndLimit(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	a, _ := m.EnsureSession(ctx, "a", "bot", "first")
	_, _ = m.EnsureSession(ctx, "b", "bot", "second")
	_, _ = m.AddMessage(ctx, model.Message{SessionID: a.ID, Content: "latest", CreatedAt: time.Now().Add(time.Second)})
	rows, err := m.ListChatSessions(ctx)
	if err != nil || len(rows) != 2 || rows[0].ID != "a" {
		t.Fatal(rows, err)
	}
}
