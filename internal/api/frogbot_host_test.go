package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"onefrog/internal/automation"
	"onefrog/internal/bot"
	frog "onefrog/internal/frogbot/bot"
	"onefrog/internal/model"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

func TestFrogBotUsesPanelVault(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	box, _ := secret.New(bytes.Repeat([]byte{3}, 32))
	v := &panelVault{st: mem, box: box}
	if err := v.Set("GITHUB_TOKEN", "ghp_example_value_123456", "GitHub", nil); err != nil {
		t.Fatal(err)
	}
	rec, err := mem.SecretByName(ctx, "GITHUB_TOKEN")
	if err != nil {
		t.Fatalf("secret not stored in the OneFrog vault: %v", err)
	}
	if plain, _ := box.Decrypt(rec.EncryptedValue); plain != "ghp_example_value_123456" {
		t.Fatal("stored value is not the encrypted secret")
	}
	// A panel secret whose name Frog Bot cannot reference stays hidden from it.
	enc, _ := box.Encrypt("another-panel-secret")
	if _, err := mem.CreateSecret(ctx, store.SecretRec{Secret: model.Secret{Name: "数据库密码"}, EncryptedValue: enc}); err != nil {
		t.Fatal(err)
	}
	if list := v.List(); len(list) != 1 || list[0].Name != "GITHUB_TOKEN" {
		t.Fatalf("list = %+v", list)
	}
	if !v.Has("GITHUB_TOKEN") || v.Allowed("shell", nil, []string{"MISSING"}) == nil {
		t.Fatal("Has/Allowed mismatch")
	}
	vals, err := v.Values([]string{"GITHUB_TOKEN"})
	if err != nil || vals["GITHUB_TOKEN"] != "ghp_example_value_123456" {
		t.Fatalf("values: %v %v", vals, err)
	}
	out := v.Redact("token ghp_example_value_123456 b64 " + base64.StdEncoding.EncodeToString([]byte("ghp_example_value_123456")))
	if strings.Contains(out, "ghp_example") || strings.Count(out, "[secret:GITHUB_TOKEN]") != 2 {
		t.Fatalf("not redacted: %s", out)
	}
	if _, err := v.SealAPIKey("x"); err == nil {
		t.Fatal("embedded Frog Bot must not store model keys")
	}
	if err := v.Set("GITHUB_TOKEN", "ghp_rotated_value_654321", "", nil); err != nil {
		t.Fatal(err)
	}
	if vals, _ := v.Values([]string{"GITHUB_TOKEN"}); vals["GITHUB_TOKEN"] != "ghp_rotated_value_654321" {
		t.Fatal("update did not replace the value")
	}
	if err := v.Delete("GITHUB_TOKEN"); err != nil || v.Has("GITHUB_TOKEN") {
		t.Fatalf("delete: %v", err)
	}
}

func TestFrogBotSchedulesAreOneFrogAutomations(t *testing.T) {
	mem := store.NewMemory()
	key := bytes.Repeat([]byte{5}, 32)
	box, _ := secret.New(key)
	book, err := automation.New("", nil, mem)
	if err != nil {
		t.Fatal(err)
	}
	agent := &bot.Agent{Store: mem, Box: box}
	f := NewFrogBots(t.TempDir(), key, agent, nil)
	defer f.Close()
	f.SetAutomations(book)
	main := func() string { return "" }
	autos := frogAutomations{book: book, owner: "u1", hostID: main, main: true}
	it, err := autos.Create(frogDraft("daily"))
	if err != nil {
		t.Fatal(err)
	}
	all := book.List()
	if len(all) != 1 || all[0].BotID != FrogBotID || all[0].OwnerID != "u1" || !all[0].Enabled {
		t.Fatalf("automation = %+v", all)
	}
	if list, _ := (frogAutomations{book: book, owner: "u2", hostID: main, main: true}).List(); len(list) != 0 {
		t.Fatal("another account sees this plan")
	}
	if _, err := (frogAutomations{book: book, owner: "u2", hostID: main, main: true}).SetEnabled(it.ID, false); err == nil {
		t.Fatal("another account paused this plan")
	}
	if got, err := autos.SetEnabled(it.ID, false); err != nil || got.Enabled {
		t.Fatalf("pause: %+v %v", got, err)
	}

	s := &Server{FrogBots: f}
	runner := s.wrapAutomationRunner(nil)
	run := all[0]
	run.ApprovalMode = "plan"
	id1, sid, err := runner.Submit(context.Background(), run, "run-1")
	if err != nil || id1 == "" || sid != FrogBotID+":main" {
		t.Fatalf("submit: %q %q %v", id1, sid, err)
	}
	id2, _, err := runner.Submit(context.Background(), run, "run-1")
	if err != nil || id2 != id1 {
		t.Fatalf("retry of the same firing must adopt the same task: %q vs %q (%v)", id2, id1, err)
	}
	acct, _ := f.account("u1")
	if st := acct.bots.Runtime("main").TaskState(id1); !st.Found {
		t.Fatal("task not created in the owner's leader runtime")
	}
	if acct.scope.mode(id1) != "plan" {
		t.Fatal("the automation's approval mode must follow its run")
	}
	if other, _ := f.account("u2"); other.bots.Runtime("main").TaskState(id1).Found {
		t.Fatal("task leaked into another account")
	}
	obs, err := runner.Observe(context.Background(), run, id1)
	if err != nil || obs.SessionID != FrogBotID+":main" {
		t.Fatalf("observe: %+v %v", obs, err)
	}
	if err := autos.Delete(it.ID); err != nil || len(book.List()) != 0 {
		t.Fatalf("delete: %v", err)
	}
}

type frogDraftT = frog.AutomationDraft

func frogDraft(kind string) frogDraftT {
	return frogDraftT{Title: "每日巡检", Prompt: "检查磁盘", Kind: kind, Hour: 9}
}

func TestFrogBotListEndpoint(t *testing.T) {
	mem := store.NewMemory()
	key := bytes.Repeat([]byte{6}, 32)
	box, _ := secret.New(key)
	agent := &bot.Agent{Store: mem, Box: box}
	f := NewFrogBots(t.TempDir(), key, agent, nil)
	defer f.Close()
	acct, err := f.account("u1")
	if err != nil {
		t.Fatal(err)
	}
	h := acct.handler
	for _, path := range []string{"/api/settings", "/api/bots/main/schedules", "/api/update", "/api/bots/main/paths"} {
		rec := httptest.NewRecorder()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil).WithContext(ctx))
		cancel()
		if rec.Code == 200 {
			t.Fatalf("%s must be managed by OneFrog, got 200: %s", path, rec.Body.String())
		}
	}
}
