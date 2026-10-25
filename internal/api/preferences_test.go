package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"onefrog/internal/preferences"
	"strings"
	"testing"
)

func TestMultiProviderSettingsAndProbes(t *testing.T) {
	client, srv, _ := testClient(t)
	defer srv.Close()
	login(t, client, srv, "admin", "admin", 200)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-provider-key" {
			t.Error("missing provider auth")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"data":[{"id":"model-b"},{"id":"model-a"}]}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer upstream.Close()
	in := preferences.Input{Providers: []preferences.ProviderInput{{Provider: preferences.Provider{ID: "api1", Name: "API 1", BaseURL: upstream.URL + "/v1", Enabled: true, AuthRequired: true, Models: []string{"model-a"}}, APIKey: "private-provider-key"}}, Default: preferences.Route{ProviderID: "api1", Model: "model-a"}, Approval: preferences.Approval{Mode: "model", Route: preferences.Route{ProviderID: "api1", Model: "model-b"}}, Execution: preferences.Defaults().Execution}
	putJSON(t, client, srv.URL+"/api/settings", in, 200, nil)
	raw := getBody(t, client, srv.URL+"/api/settings")
	if strings.Contains(raw, "private-provider-key") || strings.Contains(raw, "encrypted") {
		t.Fatal("credential leak")
	}
	var saved preferences.State
	if json.Unmarshal([]byte(raw), &saved) != nil || saved.Approval.Route.Model != "model-b" {
		t.Fatal("settings lost")
	}
	probe := map[string]any{"id": "api1", "base_url": upstream.URL + "/v1", "auth_required": true, "model": "model-a"}
	var list struct {
		Models []string `json:"models"`
	}
	postJSON(t, client, srv.URL+"/api/settings/providers/models", probe, 200, &list)
	if len(list.Models) != 2 || list.Models[0] != "model-a" {
		t.Fatal("models not discovered")
	}
	postJSON(t, client, srv.URL+"/api/settings/providers/test", probe, 200, nil)
	// A saved credential must never be reused for a different destination.
	in.Providers[0].APIKey = ""
	in.Providers[0].BaseURL = "https://different.example/v1"
	putJSON(t, client, srv.URL+"/api/settings", in, 400, nil)
	in.Providers[0].BaseURL = upstream.URL + "/v1"
	in.Default.ProviderID = "missing"
	putJSON(t, client, srv.URL+"/api/settings", in, 400, nil)
}

func TestCreateBotWithCatalogModel(t *testing.T) {
	client, srv, _ := testClient(t)
	defer srv.Close()
	login(t, client, srv, "admin", "admin", 200)
	input := preferences.Input{Providers: []preferences.ProviderInput{{Provider: preferences.Provider{ID: "p", Name: "catalog", BaseURL: "http://model.example/v1", Enabled: true, Models: []string{"visible", "hidden"}, HiddenModels: []string{"hidden"}}}}, Default: preferences.Route{ProviderID: "p", Model: "visible"}, Approval: preferences.Defaults().Approval, Execution: preferences.Defaults().Execution}
	putJSON(t, client, srv.URL+"/api/settings", input, 200, nil)
	var before []struct {
		ID string `json:"id"`
	}
	getJSON(t, client, srv.URL+"/api/bots", &before)
	postJSON(t, client, srv.URL+"/api/bots", map[string]any{"name": "bad", "model_route": preferences.Route{ProviderID: "p", Model: "hidden"}}, 400, nil)
	var bots []struct {
		ID string `json:"id"`
	}
	getJSON(t, client, srv.URL+"/api/bots", &bots)
	if len(bots) != len(before) {
		t.Fatal("failed creation left a bot behind")
	}
	var created struct {
		ID string `json:"id"`
	}
	postJSON(t, client, srv.URL+"/api/bots", map[string]any{"name": "expert", "model_route": preferences.Route{ProviderID: "p", Model: "visible"}}, 201, &created)
	var saved preferences.State
	getJSON(t, client, srv.URL+"/api/settings", &saved)
	if saved.BotRoutes[created.ID].Model != "visible" {
		t.Fatal("bot model not bound")
	}
	// Later changes use the same routing settings as the creation dialog.
	input.BotRoutes = map[string]preferences.Route{created.ID: {ProviderID: "p", Model: "visible"}}
	input.Providers[0].HiddenModels = []string{"hidden", "visible"}
	putJSON(t, client, srv.URL+"/api/settings", input, 200, nil)
	getJSON(t, client, srv.URL+"/api/settings", &saved)
	if saved.BotRoutes[created.ID].Model != "visible" {
		t.Fatal("existing bot was unbound when model hidden")
	}
}

func TestApprovalModeChoice(t *testing.T) {
	client, srv, _ := testClient(t)
	defer srv.Close()
	login(t, client, srv, "admin", "admin", 200)
	var me map[string]any
	getJSON(t, client, srv.URL+"/api/auth/me", &me)
	if me["approval_choice_required"] != true {
		t.Fatalf("fresh install must ask for an approval mode: %v", me)
	}
	var got struct {
		Mode           string `json:"mode"`
		EffectiveMode  string `json:"effective_mode"`
		ChoiceRequired bool   `json:"choice_required"`
	}
	getJSON(t, client, srv.URL+"/api/settings/approval", &got)
	if got.Mode != "" || got.EffectiveMode != "smart" || !got.ChoiceRequired {
		t.Fatalf("unchosen mode: %+v", got)
	}
	putJSON(t, client, srv.URL+"/api/settings/approval", map[string]string{"mode": "bogus"}, 400, nil)
	putJSON(t, client, srv.URL+"/api/settings/approval", map[string]string{"mode": ""}, 400, nil)
	putJSON(t, client, srv.URL+"/api/settings/approval", map[string]string{"mode": "model"}, 400, nil)
	putJSON(t, client, srv.URL+"/api/settings/approval", map[string]string{"mode": "off"}, 200, &got)
	if got.Mode != "off" {
		t.Fatalf("mode not saved: %+v", got)
	}
	me = nil
	getJSON(t, client, srv.URL+"/api/auth/me", &me)
	if me["approval_choice_required"] != false || me["username"] == nil {
		t.Fatalf("choice still required or user fields lost: %v", me)
	}
	putJSON(t, client, srv.URL+"/api/settings/approval", map[string]string{"mode": "smart"}, 200, &got)
	var state preferences.State
	getJSON(t, client, srv.URL+"/api/settings", &state)
	if state.Approval.Mode != "smart" {
		t.Fatalf("settings disagree: %q", state.Approval.Mode)
	}
}
