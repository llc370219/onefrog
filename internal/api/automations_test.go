package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"onefrog/internal/automation"
)

func TestAutomationsAPI(t *testing.T) {
	client, srv, _ := testClient(t)
	defer srv.Close()
	login(t, client, srv, "admin", "admin", http.StatusOK)

	if got := doStatus(t, client, http.MethodPost, srv.URL+"/api/automations", map[string]any{
		"name": "坏", "prompt": "x", "bot_id": "ghost", "schedule": map[string]any{"kind": "daily", "time": "09:00"},
	}); got != http.StatusBadRequest {
		t.Fatalf("unknown bot status %d", got)
	}
	if got := doStatus(t, client, http.MethodPost, srv.URL+"/api/automations", map[string]any{
		"name": "坏", "prompt": "x", "bot_id": "main", "schedule": map[string]any{"kind": "interval", "minutes": 1},
	}); got != http.StatusBadRequest {
		t.Fatalf("short interval status %d", got)
	}

	var created automation.Automation
	postJSON(t, client, srv.URL+"/api/automations", map[string]any{
		"name": "早间巡检", "prompt": "看看 web-01 的磁盘", "bot_id": "check", "server_id": "web-01",
		"schedule": map[string]any{"kind": "daily", "time": "09:00"}, "enabled": true,
	}, http.StatusCreated, &created)
	if created.ID == "" || created.NextRunAt == nil || created.OwnerID == "" {
		t.Fatalf("created %+v", created)
	}

	var updated automation.Automation
	putJSON(t, client, srv.URL+"/api/automations/"+created.ID, map[string]any{
		"name": "早间巡检", "prompt": "看看 web-01 的磁盘", "bot_id": "check", "server_id": "web-01",
		"schedule": map[string]any{"kind": "daily", "time": "09:00"}, "enabled": false,
	}, http.StatusOK, &updated)
	if updated.Enabled || updated.NextRunAt != nil {
		t.Fatalf("updated %+v", updated)
	}

	// The seeded bot has no model, so the run fails but still finishes.
	if got := doStatus(t, client, http.MethodPost, srv.URL+"/api/automations/"+created.ID+"/run", nil); got != http.StatusAccepted {
		t.Fatalf("run status %d", got)
	}
	var list struct {
		Items []automation.Automation `json:"items"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		getJSON(t, client, srv.URL+"/api/automations", &list)
		if len(list.Items) == 1 && list.Items[0].LastStatus != automation.StatusRunning && list.Items[0].LastStatus != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never finished: %+v", list.Items)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if list.Items[0].LastStatus != automation.StatusFailed || list.Items[0].LastError == "" {
		t.Fatalf("expected failure without a model: %+v", list.Items[0])
	}
	if !strings.HasPrefix(list.Items[0].SessionID, automation.SessionPrefix) {
		t.Fatalf("session %q", list.Items[0].SessionID)
	}

	// Deleting the server detaches the automation from it.
	if got := doStatus(t, client, http.MethodDelete, srv.URL+"/api/servers/web-01", nil); got != http.StatusOK {
		t.Fatalf("delete server %d", got)
	}
	getJSON(t, client, srv.URL+"/api/automations", &list)
	if list.Items[0].ServerID != "" {
		t.Fatalf("server not forgotten: %+v", list.Items[0])
	}

	if got := doStatus(t, client, http.MethodDelete, srv.URL+"/api/automations/"+created.ID, nil); got != http.StatusNoContent {
		t.Fatalf("delete %d", got)
	}
	if got := doStatus(t, client, http.MethodDelete, srv.URL+"/api/automations/"+created.ID, nil); got != http.StatusNotFound {
		t.Fatalf("delete missing %d", got)
	}
}
