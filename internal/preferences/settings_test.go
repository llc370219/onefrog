package preferences

import (
	"bytes"
	"onefrog/internal/model"
	"onefrog/internal/secret"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedPersistenceRoutingAndValidation(t *testing.T) {
	box, _ := secret.New(bytes.Repeat([]byte{4}, 32))
	path := filepath.Join(t.TempDir(), "settings.enc")
	m, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	in := Input{Providers: []ProviderInput{{Provider: Provider{ID: "a", Name: "primary", BaseURL: "https://a.example/v1/", Enabled: true, AuthRequired: true}, APIKey: "secret-primary"}, {Provider: Provider{ID: "b", Name: "local", BaseURL: "http://127.0.0.1:11434/v1", Enabled: true}}}, Default: Route{"a", "main"}, Worker: Route{"b", "small"}, BotRoutes: map[string]Route{"expert": {"a", "expert-model"}}, Approval: Approval{Mode: "model", Route: Route{"b", "review"}}, Execution: Defaults().Execution}
	saved, err := m.Save(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("secret-primary")) || bytes.Contains(raw, []byte("a.example")) {
		t.Fatal("settings saved in plaintext")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v", info.Mode())
	}
	restored, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := restored.Key("a")
	if key != "secret-primary" {
		t.Fatal("lost key")
	}
	if restored.RouteFor(model.Bot{ID: "main"}, false).Model != "main" || restored.RouteFor(model.Bot{ID: "worker"}, true).Model != "small" || restored.RouteFor(model.Bot{ID: "expert"}, true).Model != "expert-model" {
		t.Fatal("routing priority incorrect")
	}
	in.Providers[0].APIKey = ""
	in.Providers[0].BaseURL = saved.Providers[0].BaseURL
	if _, err = m.Save(in); err != nil {
		t.Fatal(err)
	}
	key, _ = m.Key("a")
	if key != "secret-primary" {
		t.Fatal("blank overwrote key")
	}
	frozen := m.Frozen()
	in.Execution.MaxSteps = 9
	if _, err = m.Save(in); err != nil {
		t.Fatal(err)
	}
	if frozen.Snapshot().Execution.MaxSteps == 9 {
		t.Fatal("active turn settings changed")
	}
	in.Default.ProviderID = "missing"
	if _, err = m.Save(in); err == nil {
		t.Fatal("invalid reference accepted")
	}
	if m.Snapshot().Default.ProviderID != "a" {
		t.Fatal("invalid save changed state")
	}
	in.Default.ProviderID = "a"
	in.Approval.Route = Route{}
	if _, err = m.Save(in); err == nil {
		t.Fatal("missing reviewer accepted")
	}
	wrong, _ := secret.New(bytes.Repeat([]byte{5}, 32))
	if _, err = New(path, wrong); err == nil {
		t.Fatal("wrong key accepted")
	}
}

func TestModelVisibilityAndBotBinding(t *testing.T) {
	box, _ := secret.New(bytes.Repeat([]byte{7}, 32))
	path := filepath.Join(t.TempDir(), "settings.enc")
	m, _ := New(path, box)
	input := Input{Providers: []ProviderInput{{Provider: Provider{ID: "p", Name: "catalog", BaseURL: "http://model.example/v1", Enabled: true, Models: []string{"a", "b"}, HiddenModels: []string{"b", "b", "missing"}}}}, Default: Route{ProviderID: "p", Model: "a"}, Approval: Defaults().Approval, Execution: Defaults().Execution}
	saved, err := m.Save(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Providers[0].HiddenModels) != 1 || saved.Providers[0].HiddenModels[0] != "b" {
		t.Fatal("hidden models not normalized")
	}
	if err = m.BindBot("bot", Route{ProviderID: "p", Model: "b"}); err == nil {
		t.Fatal("new bot bound hidden model")
	}
	if err = m.BindBot("bot", Route{ProviderID: "p", Model: "missing"}); err == nil {
		t.Fatal("new bot bound unknown model")
	}
	if err = m.BindBot("bot", Route{ProviderID: "p", Model: "a"}); err != nil {
		t.Fatal(err)
	}
	state := m.Snapshot()
	if state.BotRoutes["bot"].Model != "a" {
		t.Fatal("binding not saved")
	}
	input.BotRoutes = state.BotRoutes
	input.Providers[0].HiddenModels = []string{"a", "b"}
	if _, err = m.Save(input); err != nil {
		t.Fatal("hiding current model should preserve route", err)
	}
	restored, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Snapshot().BotRoutes["bot"].Model != "a" || len(restored.Snapshot().Providers[0].HiddenModels) != 2 {
		t.Fatal("visibility/binding lost on restart")
	}
	input.Providers[0].Models = []string{"a", "b", "new"}
	if _, err = m.Save(input); err != nil {
		t.Fatal(err)
	}
	if len(m.Snapshot().Providers[0].HiddenModels) != 2 {
		t.Fatal("catalog refresh changed toggles")
	}
}

func TestApprovalModeChoiceAndLegacy(t *testing.T) {
	box, _ := secret.New(bytes.Repeat([]byte{3}, 32))
	path := filepath.Join(t.TempDir(), "settings.enc")
	m, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	if m.Snapshot().Approval.Mode != ModeUnset {
		t.Fatal("new installs must start without a chosen mode")
	}
	d := Defaults()
	// Unchosen stays valid through a full save (used by Seed).
	if _, err := m.Save(Input{Default: d.Default, Approval: d.Approval, Execution: d.Execution}); err != nil {
		t.Fatal(err)
	}
	// The original no-prompt mode name is accepted and stored as "off".
	if s, err := m.Save(Input{Default: d.Default, Approval: Approval{Mode: "rules"}, Execution: d.Execution}); err != nil || s.Approval.Mode != ModeOff {
		t.Fatalf("legacy rules: %v %q", err, s.Approval.Mode)
	}
	if _, err := m.Save(Input{Default: d.Default, Approval: Approval{Mode: "everything"}, Execution: d.Execution}); err == nil {
		t.Fatal("unknown mode accepted")
	}
	for _, bad := range []string{"", "bogus", "model"} {
		if _, err := m.SetApprovalMode(bad); err == nil {
			t.Fatalf("SetApprovalMode(%q) accepted", bad)
		}
	}
	if _, err := m.SetApprovalMode("smart"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(path, box)
	if err != nil || reloaded.Snapshot().Approval.Mode != ModeSmart {
		t.Fatalf("saved choice not kept: %v", err)
	}
	for in, want := range map[string]string{"rules": ModeOff, " smart ": ModeSmart, "": ModeUnset, "plan": ModePlan} {
		if got, ok := NormalizeMode(in); !ok || got != want {
			t.Fatalf("NormalizeMode(%q) = %q %v", in, got, ok)
		}
	}
}

// The first channel is saved from the API 渠道 tab before 模型路由 is touched.
func TestFirstChannelSavesWithoutRoute(t *testing.T) {
	box, _ := secret.New(bytes.Repeat([]byte{5}, 32))
	m, err := New(filepath.Join(t.TempDir(), "settings.enc"), box)
	if err != nil {
		t.Fatal(err)
	}
	ch := ProviderInput{Provider: Provider{ID: "channel-1", Name: "新 API 渠道", AuthKind: "api_key", BaseURL: "https://relay.example/v1", Enabled: true, AuthRequired: true}, APIKey: "sk-test"}
	// Key entered, models not pulled yet: nothing to route, still saves.
	in := Input{Providers: []ProviderInput{ch}, Execution: Defaults().Execution}
	saved, err := m.Save(in)
	if err != nil || saved.Default != (Route{}) {
		t.Fatalf("save without models: %v %+v", err, saved.Default)
	}
	// Models pulled, first one hidden: main bot gets the first visible model.
	ch.APIKey = ""
	ch.Models = []string{"gpt-hidden", "gpt-5.6-sol", "gpt-6-sol"}
	ch.HiddenModels = []string{"gpt-hidden"}
	in.Providers = []ProviderInput{ch}
	saved, err = m.Save(in)
	if err != nil || saved.Default != (Route{"channel-1", "gpt-5.6-sol"}) {
		t.Fatalf("auto route: %v %+v", err, saved.Default)
	}
	// An explicit choice is kept, and an explicit bad choice is still rejected.
	in.Default = Route{"channel-1", "gpt-6-sol"}
	if saved, err = m.Save(in); err != nil || saved.Default.Model != "gpt-6-sol" {
		t.Fatalf("explicit route: %v %+v", err, saved.Default)
	}
	in.Default = Route{"missing", "gpt-6-sol"}
	if _, err = m.Save(in); err == nil {
		t.Fatal("missing provider accepted")
	}
	in.Default = Route{"channel-1", ""}
	if _, err = m.Save(in); err == nil {
		t.Fatal("half-filled route accepted")
	}
}
