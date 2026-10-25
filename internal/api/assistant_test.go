package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"strings"
	"testing"
)

func TestBotControlsPreserveFieldsAndValidateModel(t *testing.T) {
	c, s, _ := testClient(t)
	defer s.Close()
	login(t, c, s, "admin", "admin", 200)
	cfg := preferences.Input{Providers: []preferences.ProviderInput{{Provider: preferences.Provider{ID: "p", Name: "channel", BaseURL: "http://model.example/v1", Enabled: true, Models: []string{"a", "b", "hidden"}, HiddenModels: []string{"hidden"}}}}, Default: preferences.Route{ProviderID: "p", Model: "a"}, Approval: preferences.Defaults().Approval, Execution: preferences.Defaults().Execution}
	putJSON(t, c, s.URL+"/api/settings", cfg, 200, nil)
	putJSON(t, c, s.URL+"/api/bots/main", map[string]any{"name": "新主控", "description": "职责", "system_prompt": "keep this", "model_route": preferences.Route{ProviderID: "p", Model: "b"}}, 200, nil)
	putJSON(t, c, s.URL+"/api/bots/main", map[string]any{"name": "重命名"}, 200, nil)
	var rows []botView
	getJSON(t, c, s.URL+"/api/bots", &rows)
	var got botView
	for _, b := range rows {
		if b.ID == "main" {
			got = b
		}
	}
	if got.Name != "重命名" || got.SystemPrompt != "keep this" || got.Description != "职责" || got.EffectiveRoute.Model != "b" || !got.Enabled {
		t.Fatal(got)
	}
	putJSON(t, c, s.URL+"/api/bots/main", map[string]any{"name": "should-not-save", "model_route": preferences.Route{ProviderID: "p", Model: "hidden"}}, 400, nil)
	putJSON(t, c, s.URL+"/api/bots/main/enabled", map[string]bool{"enabled": false}, 200, nil)
	postJSON(t, c, s.URL+"/api/chat", map[string]string{"bot_id": "main", "content": "test"}, 400, nil)
	getJSON(t, c, s.URL+"/api/bots", &rows)
	for _, b := range rows {
		if b.ID == "main" && (b.Name != "重命名" || b.Enabled) {
			t.Fatal("failed edit or disable was not enforced")
		}
	}
}
func TestUploadAndChatAttachmentEndpoints(t *testing.T) {
	c, s, _ := testClient(t)
	defer s.Close()
	login(t, c, s, "admin", "admin", 200)
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	f, _ := writer.CreateFormFile("file", "config.txt")
	_, _ = f.Write([]byte("port=8088"))
	writer.Close()
	req, _ := http.NewRequest("POST", s.URL+"/api/attachments", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	res, e := c.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	var ref model.Attachment
	_ = json.NewDecoder(res.Body).Decode(&ref)
	res.Body.Close()
	if res.StatusCode != 201 || ref.ID == "" || ref.Size != 9 {
		t.Fatal(ref, res.StatusCode)
	}
	if raw := getBody(t, c, s.URL+"/api/attachments/"+ref.ID+"/content"); raw != "port=8088" {
		t.Fatal(raw)
	}
	anon := &http.Client{}
	res, e = anon.Get(s.URL + "/api/attachments/" + ref.ID + "/content")
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("attachment exposed without login")
	}
	var reply struct {
		SessionID string `json:"session_id"`
	}
	postJSON(t, c, s.URL+"/api/chat", map[string]any{"content": "", "attachment_ids": []string{ref.ID}, "excluded_bot_ids": []string{"check"}, "approval_mode": "plan"}, 200, &reply)
	var msgs []model.Message
	getJSON(t, c, s.URL+"/api/sessions/"+reply.SessionID+"/messages", &msgs)
	if len(msgs) != 2 || len(msgs[0].Attachments) != 1 || msgs[0].Attachments[0].ID != ref.ID {
		t.Fatal(msgs)
	}
	postJSON(t, c, s.URL+"/api/chat", map[string]any{"content": "test", "attachment_ids": []string{"missing-id"}}, 400, nil)
	var sessions []model.SessionSummary
	getJSON(t, c, s.URL+"/api/sessions", &sessions)
	var opts map[string]any
	if len(sessions) != 1 || json.Unmarshal(sessions[0].Options, &opts) != nil || opts["approval_mode"] != "plan" {
		t.Fatal("session options not exposed", sessions)
	}
	postJSON(t, c, s.URL+"/api/chat", map[string]any{"content": "switch", "session_id": reply.SessionID, "bot_id": "check"}, 400, nil)

	if strings.Contains(getBody(t, c, s.URL+"/api/usage"), "port=8088") {
		t.Fatal("file content leaked into usage")
	}
}
