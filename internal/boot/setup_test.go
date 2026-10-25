package boot

import (
	"context"
	"path/filepath"
	"testing"

	"onefrog/internal/config"
	"onefrog/internal/store"
)

func TestFreshInstallRequiresNewCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	m, err := store.NewMemoryAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{AdminUser: "admin", AdminPassword: "temp-pass-123", ForceCredentialSetup: true}
	if err = Prepare(context.Background(), m, cfg); err != nil {
		t.Fatal(err)
	}
	user, _, err := m.UserByName(context.Background(), "admin")
	if err != nil || !user.MustChangeCredentials {
		t.Fatalf("%+v %v", user, err)
	}
	again, err := store.NewMemoryAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	user, _, err = again.UserByName(context.Background(), "admin")
	if err != nil || !user.MustChangeCredentials {
		t.Fatalf("reload %+v %v", user, err)
	}
	if err = Prepare(context.Background(), again, cfg); err != nil {
		t.Fatal(err)
	}
	user, _, err = again.UserByName(context.Background(), "admin")
	if err != nil || !user.MustChangeCredentials {
		t.Fatal("second boot cleared the flag")
	}
}

func TestExistingDemoDoesNotForceCredentialChange(t *testing.T) {
	m := store.NewMemory()
	cfg := config.Config{AdminUser: "admin", AdminPassword: "admin"}
	if err := Prepare(context.Background(), m, cfg); err != nil {
		t.Fatal(err)
	}
	user, _, err := m.UserByName(context.Background(), "admin")
	if err != nil || user.MustChangeCredentials {
		t.Fatalf("%+v %v", user, err)
	}
}
