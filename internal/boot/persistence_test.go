package boot_test

import (
	"bytes"
	"context"
	"onefrog/internal/boot"
	"onefrog/internal/config"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"path/filepath"
	"testing"
)

func TestDemoSeedPreservesEditedBots(t *testing.T) {
	ctx := context.Background()
	box, _ := secret.New(bytes.Repeat([]byte{5}, 32))
	dir := t.TempDir()
	cfg := config.Config{AdminUser: "admin", AdminPassword: "test-password", Seed: true}
	first, _ := store.NewMemoryAccounts(filepath.Join(dir, "accounts.json"))
	_ = first.EnableBotPersistence(filepath.Join(dir, "bots.enc"), box)
	if e := boot.Prepare(ctx, first, cfg); e != nil {
		t.Fatal(e)
	}
	b, _ := first.GetBot(ctx, "main")
	b.Name = "my assistant"
	_, _ = first.UpdateBot(ctx, b)
	again, _ := store.NewMemoryAccounts(filepath.Join(dir, "accounts.json"))
	_ = again.EnableBotPersistence(filepath.Join(dir, "bots.enc"), box)
	if e := boot.Prepare(ctx, again, cfg); e != nil {
		t.Fatal(e)
	}
	b, e := again.GetBot(ctx, "main")
	if e != nil || b.Name != "my assistant" {
		t.Fatal("seed overwrote custom bot", b, e)
	}
}
