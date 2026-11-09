package bot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChannelPersistenceAndKeyIsolation(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DataDir: dir, Workspace: filepath.Join(dir, "ws")}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	firstKey, secondKey := "first-private-key", "second-private-key"
	draft := APISettings{DefaultProviderID: "second", Model: "model-b", Providers: []APIChannel{
		{ID: "first", Name: "First", BaseURL: "https://first.example/v1", Enabled: true, AuthRequired: true, APIKey: &firstKey, Models: []string{"model-a"}},
		{ID: "second", Name: "Second", BaseURL: "https://second.example/v1", Enabled: true, AuthRequired: true, APIKey: &secondKey, Models: []string{"model-b", "hidden"}, HiddenModels: []string{"hidden"}},
	}}
	saved, err := b.SaveSettings(draft)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(saved)
	persisted, err := os.ReadFile(b.channelPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{firstKey, secondKey} {
		if strings.Contains(string(raw), key) || strings.Contains(string(persisted), key) {
			t.Fatal("plaintext key exposed")
		}
	}
	if !saved.Providers[0].HasAPIKey || saved.DefaultProviderID != "second" {
		t.Fatal("missing channel metadata")
	}
	if key := b.catalogKey(APISettings{ProviderID: "first"}, "https://second.example/v1"); key != "" {
		t.Fatal("key crossed channel boundary")
	}
	if key := b.catalogKey(APISettings{ProviderID: "second"}, "https://other.example/v1"); key != "" {
		t.Fatal("key crossed address boundary")
	}
	if key := b.catalogKey(APISettings{ProviderID: "first"}, "https://first.example/v1"); key != firstKey {
		t.Fatal("saved key not reusable")
	}
	next, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	restored := next.GetSettings()
	if len(restored.Providers) != 2 || restored.DefaultProviderID != "second" || restored.Model != "model-b" || !restored.HasAPIKey {
		t.Fatal("channels not restored")
	}
	if _, model := next.resolveModel(false); model != "model-b" {
		t.Fatal("task model not restored")
	}
	// A new channel at an existing URL must not inherit its key.
	if key := next.catalogKey(APISettings{ProviderID: "new"}, "https://second.example/v1"); key != "" {
		t.Fatal("new channel inherited another channel key")
	}
	restored.DefaultProviderID = "first"
	restored.Model = "model-a"
	restored.Providers[0].BaseURL = "https://changed.example/v1"
	changed, err := next.SaveSettings(restored)
	if err != nil {
		t.Fatal(err)
	}
	if changed.HasAPIKey {
		t.Fatal("changing address retained old key")
	}
	changed.Model = "hidden"
	changed.DefaultProviderID = "second"
	if _, err := next.SaveSettings(changed); err == nil {
		t.Fatal("hidden model accepted as default")
	}
	if next.GetSettings().DefaultProviderID != "first" {
		t.Fatal("failed save mutated active model")
	}
	changed.DefaultProviderID = "first"
	changed.Model = "model-a"
	changed.Providers[0].Enabled = false
	if _, err := next.SaveSettings(changed); err == nil {
		t.Fatal("disabled default channel accepted")
	}
	// Older clients update the active channel without losing the other channels.
	if _, err := next.SaveSettings(APISettings{BaseURL: "https://changed.example/v1", Model: "legacy-model"}); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if s := restarted.GetSettings(); s.Model != "legacy-model" || len(s.Providers) != 2 {
		t.Fatal("legacy save not restored with channels")
	}
	if _, err := restarted.SaveSettings(APISettings{Providers: []APIChannel{}}); err != nil {
		t.Fatal(err)
	}
	empty, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if s := empty.GetSettings(); len(s.Providers) != 0 || s.Model != "" || s.HasAPIKey {
		t.Fatal("removed channels reappeared")
	}
}
