package api

import (
	"net/http"
	"testing"
	"time"

	"onefrog/internal/asset"
)

func TestAssetRecords(t *testing.T) {
	client, srv, _ := testClient(t)
	defer srv.Close()
	login(t, client, srv, "admin", "admin", http.StatusOK)
	today := time.Now().UTC()
	body := map[string]any{"name": "洛杉矶-01", "provider": "搬瓦工", "cycle": "yearly", "currency": "usd", "amount_cents": 36500, "starts_at": today.Format("2006-01-02"), "expires_at": today.AddDate(0, 0, 9).Format("2006-01-02"), "region": "洛杉矶", "specs": "2C 2G", "auto_renew": true}
	var created asset.View
	postJSON(t, client, srv.URL+"/api/assets", body, http.StatusCreated, &created)
	if created.DaysTotal != 10 || created.DaysLeft != 10 || created.ResidualCents != 36500 || created.DerivedStatus != asset.StatusExpiring || created.Currency != "USD" {
		t.Fatalf("%+v", created)
	}
	var page struct {
		Records []asset.View  `json:"records"`
		Summary asset.Summary `json:"summary"`
	}
	getJSON(t, client, srv.URL+"/api/assets", &page)
	if page.Summary.Count != 1 || page.Summary.Expiring != 1 || page.Summary.ResidualCents["USD"] != 36500 {
		t.Fatalf("%+v", page.Summary)
	}
	body["notes"] = "明年核对"
	body["name"] = "洛杉矶-01"
	var updated asset.View
	putJSON(t, client, srv.URL+"/api/assets/"+created.ID, body, http.StatusOK, &updated)
	if updated.Notes != "明年核对" || updated.Provider != "搬瓦工" {
		t.Fatalf("%+v", updated)
	}
	postJSON(t, client, srv.URL+"/api/assets", map[string]any{"name": ""}, http.StatusBadRequest, nil)
	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/assets/"+created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete %d", res.StatusCode)
	}
}
