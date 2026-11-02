package api

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"onefrog/internal/model"
)

func TestDevModulesSandboxAndDataAPI(t *testing.T) {
	client, srv, _ := testClient(t)
	defer srv.Close()
	login(t, client, srv, "admin", "admin", http.StatusOK)

	var created struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	postJSON(t, client, srv.URL+"/api/dev/modules", map[string]any{
		"name":        "打卡",
		"description": "每日打卡卡片",
		"use_starter": true,
	}, http.StatusCreated, &created)
	if created.ID == "" || !created.Enabled {
		t.Fatalf("created: %+v", created)
	}

	// Verify it appears in /api/modules as Type="custom".
	var mods []model.Module
	getJSON(t, client, srv.URL+"/api/modules", &mods)
	foundCustom, foundDevelop := false, false
	for _, m := range mods {
		if m.ID == created.ID && m.Type == "custom" {
			foundCustom = true
		}
		if m.ID == "develop" && m.Name == "自定义模块" {
			foundDevelop = true
		}
	}
	if !foundCustom || !foundDevelop {
		t.Fatalf("modules missing custom=%v develop=%v", foundCustom, foundDevelop)
	}

	// Add a record via /api/dev/modules/:id/data.
	var dataRes struct {
		Records []struct {
			ID   string         `json:"id"`
			Data map[string]any `json:"data"`
		} `json:"records"`
	}
	postJSON(t, client, srv.URL+"/api/dev/modules/"+created.ID+"/data", map[string]any{
		"action": "add_record",
		"data":   map[string]any{"note": "早安打卡"},
	}, http.StatusOK, &dataRes)
	if len(dataRes.Records) != 1 || dataRes.Records[0].Data["note"] != "早安打卡" {
		t.Fatalf("dataRes: %+v", dataRes)
	}

	// Verify system_overview returns read-only linked server/module/asset state without secrets.
	postJSON(t, client, srv.URL+"/api/secrets", map[string]string{"name": "priv", "value": "top-secret-999"}, http.StatusCreated, &map[string]any{})
	overviewRaw := getOrPostBody(t, client, srv.URL+"/api/dev/modules/"+created.ID+"/data", map[string]any{"action": "system_overview"})
	if strings.Contains(overviewRaw, "top-secret-999") || !strings.Contains(overviewRaw, "\"servers\"") {
		t.Fatalf("unexpected system_overview: %s", overviewRaw)
	}

	// Direct top-level navigation (Sec-Fetch-Dest: document) must be rejected.
	docReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/dev/modules/"+created.ID+"/frame", nil)
	docReq.Header.Set("Sec-Fetch-Dest", "document")
	docRes, err := client.Do(docReq)
	if err != nil {
		t.Fatal(err)
	}
	docRes.Body.Close()
	if docRes.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for direct document load, got %d", docRes.StatusCode)
	}

	// Iframe load must return strict sandbox CSP and window.onefrog bridge.
	frameReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/dev/modules/"+created.ID+"/frame", nil)
	frameReq.Header.Set("Sec-Fetch-Dest", "iframe")
	frameRes, err := client.Do(frameReq)
	if err != nil {
		t.Fatal(err)
	}
	defer frameRes.Body.Close()
	raw, _ := io.ReadAll(frameRes.Body)
	csp := frameRes.Header.Get("Content-Security-Policy")
	if frameRes.StatusCode != http.StatusOK || !strings.Contains(csp, "sandbox allow-scripts") || !strings.Contains(csp, "connect-src 'none'") || !strings.Contains(string(raw), "window.onefrog") {
		t.Fatalf("bad frame response status=%d csp=%q", frameRes.StatusCode, csp)
	}
}

func getOrPostBody(t *testing.T, client *http.Client, url string, body any) string {
	t.Helper()
	res := postRaw(t, client, url, body)
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", res.StatusCode, raw)
	}
	return string(raw)
}
