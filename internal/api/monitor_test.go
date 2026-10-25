package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"onefrog/internal/monitor"
	"testing"
)

func TestProbeReportAuthIsolationAndActualMetrics(t *testing.T) {
	client, srv, _ := testClient(t)
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/monitor")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("monitor visible anonymously")
	}
	login(t, client, srv, "admin", "admin", 200)
	var credentials struct {
		Token string `json:"token"`
	}
	postJSON(t, client, srv.URL+"/api/servers/web-01/probe/token", map[string]string{}, 200, &credentials)
	type view struct {
		Nodes []monitor.View `json:"nodes"`
	}
	var initial view
	getJSON(t, client, srv.URL+"/api/monitor", &initial)
	for _, n := range initial.Nodes {
		if n.Latest != nil || n.Status != "pending" {
			t.Fatal("seed data exposed as probe samples")
		}
	}
	report := map[string]any{"server_id": "web-01", "sample": monitor.Sample{CPUCores: 2, CPUPercent: 42, MemoryTotal: 2048, MemoryUsed: 1024, DiskTotal: 10000, DiskUsed: 1000, RXRate: 1234, TXRate: 2345}}
	send := func(token string, want int) {
		raw, _ := json.Marshal(report)
		req, _ := http.NewRequest("POST", srv.URL+"/api/probe/report", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		r, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer r.Body.Close()
		if r.StatusCode != want {
			b, _ := io.ReadAll(r.Body)
			t.Fatalf("status %d want %d %s", r.StatusCode, want, b)
		}
	}
	send("bad", 401)
	send(credentials.Token, 204)
	var got view
	getJSON(t, client, srv.URL+"/api/monitor", &got)
	found := false
	for _, n := range got.Nodes {
		if n.ServerID == "web-01" {
			found = true
			if n.Latest == nil || n.Latest.CPUPercent != 42 || n.Status != "online" {
				t.Fatal("report not reflected in monitor")
			}
		}
	}
	if !found {
		t.Fatal("missing node")
	}
	report["server_id"] = "db-01"
	send(credentials.Token, 401)
}
