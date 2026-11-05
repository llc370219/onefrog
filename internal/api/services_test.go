package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"onefrog/internal/boot"
	"onefrog/internal/bot"
	"onefrog/internal/config"
	"onefrog/internal/model"
	"onefrog/internal/secret"
	"onefrog/internal/services"
	"onefrog/internal/store"
)

type serviceSSH struct{ calls atomic.Int32 }

func (s *serviceSSH) Run(context.Context, string, int, string, string, string, string, string) (string, string, error) {
	s.calls.Add(1)
	return `{"items":[{"kind":"docker","name":"web","state":"running","detail":"Up","image":"nginx"}],"sources":{"systemd":{"status":"ok"},"docker":{"status":"ok"},"ports":{"status":"ok"}}}`, "", nil
}
func TestServiceInventoryAuthScanAndSharedSnapshot(t *testing.T) {
	st := store.NewMemory()
	box, _ := secret.New(bytes.Repeat([]byte{7}, 32))
	ctx := context.Background()
	if err := boot.Prepare(ctx, st, config.Config{AdminUser: "admin", AdminPassword: "admin"}); err != nil {
		t.Fatal(err)
	}
	cipher, _ := box.Encrypt("ssh-secret")
	st.CreateServer(ctx, store.ServerRec{Server: model.Server{ID: "node", Name: "node", Host: "invalid"}, EncryptedKey: cipher, HostKey: "SHA256:test"})
	ssh := &serviceSSH{}
	agent := &bot.Agent{Store: st, Box: box, SSH: ssh}
	app := &Server{Store: st, Box: box, Agent: agent}
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	for _, path := range []string{"/api/services", "/api/servers/node/services/scan"} {
		method := "GET"
		if strings.HasSuffix(path, "scan") {
			method = "POST"
		}
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader("{}"))
		req.Header.Set("X-OneFrog-CSRF", "1")
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 401 {
			t.Fatalf("anonymous inventory accessible: %d", res.StatusCode)
		}
	}
	login(t, client, srv, "admin", "admin", 200)
	var listed struct {
		Nodes []services.View `json:"nodes"`
	}
	getJSON(t, client, srv.URL+"/api/services", &listed)
	if len(listed.Nodes) != 1 || listed.Nodes[0].Status != "pending" || ssh.calls.Load() != 0 {
		t.Fatal("list unexpectedly ran SSH or invented services")
	}
	var got services.View
	postJSON(t, client, srv.URL+"/api/servers/node/services/scan", map[string]string{}, 200, &got)
	if got.Status != "ok" || len(got.Items) != 1 || agent.Services != app.Services {
		t.Fatalf("scan not shared: %#v", got)
	}
	getJSON(t, client, srv.URL+"/api/services", &listed)
	if listed.Nodes[0].Items[0].Name != "web" {
		t.Fatal("module snapshot not updated")
	}
	postJSON(t, client, srv.URL+"/api/servers/missing/services/scan", map[string]string{}, 404, nil)
	if ssh.calls.Load() != 1 {
		t.Fatal("unknown server reached SSH")
	}
	st.DeleteServer(ctx, "node")
	getJSON(t, client, srv.URL+"/api/services", &listed)
	if len(listed.Nodes) != 0 {
		t.Fatal("deleted server snapshot leaked")
	}
}

type watchBody struct {
	IntervalSeconds int                          `json:"interval_seconds"`
	Servers         map[string][]services.Target `json:"servers"`
	Status          []services.WatchStatus       `json:"status"`
}

func TestServiceWatchSelectionAndCleanup(t *testing.T) {
	st := store.NewMemory()
	box, _ := secret.New(bytes.Repeat([]byte{7}, 32))
	ctx := context.Background()
	if err := boot.Prepare(ctx, st, config.Config{AdminUser: "admin", AdminPassword: "admin"}); err != nil {
		t.Fatal(err)
	}
	cipher, _ := box.Encrypt("ssh-secret")
	st.CreateServer(ctx, store.ServerRec{Server: model.Server{ID: "node", Name: "node", Host: "invalid"}, EncryptedKey: cipher, HostKey: "SHA256:test"})
	ssh := &serviceSSH{}
	app := &Server{Store: st, Box: box, Agent: &bot.Agent{Store: st, Box: box, SSH: ssh}}
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	req, _ := http.NewRequest("GET", srv.URL+"/api/services/watch", nil)
	req.Header.Set("X-OneFrog-CSRF", "1")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("anonymous watch accessible: %d", res.StatusCode)
	}
	login(t, client, srv, "admin", "admin", 200)
	var got watchBody
	getJSON(t, client, srv.URL+"/api/services/watch", &got)
	if got.IntervalSeconds != services.DefaultInterval || len(got.Status) != 0 {
		t.Fatalf("default watch: %#v", got)
	}
	body := map[string]any{"interval_seconds": 90, "servers": map[string][]services.Target{"node": {{Kind: "docker", Name: "web"}}}}
	putJSON(t, client, srv.URL+"/api/services/watch", body, 200, &got)
	if got.IntervalSeconds != 90 || len(got.Status) != 1 || got.Status[0].State != services.StatePending {
		t.Fatalf("saved watch: %#v", got)
	}
	app.ServiceWatch.Poll(ctx)
	getJSON(t, client, srv.URL+"/api/services/watch", &got)
	if len(got.Status) != 1 || got.Status[0].State != services.StateOK {
		t.Fatalf("poll result: %#v", got.Status)
	}
	putJSON(t, client, srv.URL+"/api/services/watch", map[string]any{"interval_seconds": 5}, 400, nil)
	putJSON(t, client, srv.URL+"/api/services/watch", map[string]any{"servers": map[string][]services.Target{"node": {{Kind: "shell", Name: "x"}}}}, 400, nil)
	putJSON(t, client, srv.URL+"/api/services/watch", map[string]any{"servers": map[string][]services.Target{"ghost": {{Kind: "docker", Name: "web"}}}}, 404, nil)
	del, _ := http.NewRequest("DELETE", srv.URL+"/api/servers/node", nil)
	res, err = client.Do(del)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("delete server: %d", res.StatusCode)
	}
	var after watchBody
	getJSON(t, client, srv.URL+"/api/services/watch", &after)
	if len(after.Servers) != 0 || len(after.Status) != 0 || after.IntervalSeconds != 90 {
		t.Fatalf("deleted server still watched: %#v", after)
	}
}
