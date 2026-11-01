package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"onefrog/internal/model"
)

func TestBotMemoryAPI(t *testing.T) {
	client, srv, _ := testClient(t)
	defer srv.Close()
	login(t, client, srv, "admin", "admin", http.StatusOK)

	var created model.Bot
	postJSON(t, client, srv.URL+"/api/bots", map[string]string{"name": "记录员", "description": "记下结论"}, http.StatusCreated, &created)
	if created.ID == "" || created.Name != "记录员" {
		t.Fatalf("%+v", created)
	}
	if got := doStatus(t, client, http.MethodPost, srv.URL+"/api/bots/missing/memories", map[string]string{"title": "t", "body": "b"}); got != http.StatusNotFound {
		t.Fatalf("missing bot %d", got)
	}
	if got := doStatus(t, client, http.MethodGet, srv.URL+"/api/bots/missing/memories", nil); got != http.StatusNotFound {
		t.Fatalf("missing list %d", got)
	}
	postJSON(t, client, srv.URL+"/api/bots/"+created.ID+"/memories", map[string]string{"title": "  ", "body": "x"}, http.StatusBadRequest, nil)

	const plain = "mem-plain-7788"
	postJSON(t, client, srv.URL+"/api/secrets", map[string]string{"name": "panel-token", "value": plain}, http.StatusCreated, &model.Secret{})
	var item model.BotMemory
	postJSON(t, client, srv.URL+"/api/bots/"+created.ID+"/memories", map[string]string{"title": "面板令牌", "body": "令牌是 " + plain}, http.StatusCreated, &item)
	if item.ID == "" || item.BotID != created.ID || item.Source != "user" || item.Title != "面板令牌" {
		t.Fatalf("%+v", item)
	}
	if strings.Contains(item.Body, plain) || !strings.Contains(item.Body, "***") {
		t.Fatal(item.Body)
	}
	var again model.BotMemory
	postJSON(t, client, srv.URL+"/api/bots/"+created.ID+"/memories", map[string]string{"title": "面板令牌", "body": "已轮换"}, http.StatusCreated, &again)
	if again.ID != item.ID || again.Body != "已轮换" {
		t.Fatalf("same title %+v", again)
	}
	var list []model.BotMemory
	getJSON(t, client, srv.URL+"/api/bots/"+created.ID+"/memories", &list)
	if len(list) != 1 || list[0].Body != "已轮换" || strings.Contains(list[0].Body, plain) {
		t.Fatalf("%+v", list)
	}
	if got := doStatus(t, client, http.MethodDelete, srv.URL+"/api/bots/"+created.ID+"/memories/"+item.ID, nil); got != http.StatusNoContent {
		t.Fatalf("delete %d", got)
	}
	getJSON(t, client, srv.URL+"/api/bots/"+created.ID+"/memories", &list)
	if len(list) != 0 {
		t.Fatal(list)
	}
	if got := doStatus(t, client, http.MethodDelete, srv.URL+"/api/bots/"+created.ID+"/memories/"+item.ID, nil); got != http.StatusNotFound {
		t.Fatalf("delete missing %d", got)
	}
}

func doStatus(t *testing.T, client *http.Client, method, url string, body any) int {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 500 {
		t.Fatalf("%s %s %d %s", method, url, res.StatusCode, raw)
	}
	return res.StatusCode
}
