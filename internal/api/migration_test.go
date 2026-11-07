package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"onefrog/internal/boot"
	"onefrog/internal/config"
	"onefrog/internal/maintenance"
	"onefrog/internal/migration"
	"onefrog/internal/model"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

func migrationFixture(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	key := bytes.Repeat([]byte{3}, 32)
	box, _ := secret.New(key)
	if err := os.WriteFile(filepath.Join(dir, "master.key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.NewMemoryAccounts(filepath.Join(dir, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.EnableServerPersistence(filepath.Join(dir, "servers.enc"), box); err != nil {
		t.Fatal(err)
	}
	if err = db.EnableBotPersistence(filepath.Join(dir, "bots.enc"), box); err != nil {
		t.Fatal(err)
	}
	if err = db.EnablePersistence(dir, box); err != nil {
		t.Fatal(err)
	}
	if err = boot.Prepare(context.Background(), db, config.Config{AdminUser: "admin", AdminPassword: "test-password"}); err != nil {
		t.Fatal(err)
	}
	gate, err := maintenance.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db, Box: box, Migration: &Migration{Root: dir, Dir: dir, Driver: "memory", Key: key, Gate: gate}}
	h := s.Handler()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"test-password"}`))
	r.Header.Set(CSRFHeader, "1")
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	return s, h, w.Result().Cookies()[0].Value
}

func migrationJSON(h http.Handler, token, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
	r.Header.Set(CSRFHeader, "1")
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "of_session", Value: token})
	}
	h.ServeHTTP(w, r)
	return w
}

func TestMigrationBackupRestorePreservesMemoryAndKeys(t *testing.T) {
	s, h, token := migrationFixture(t)
	ctx := context.Background()
	b, err := s.Store.CreateBot(ctx, model.Bot{Name: "my bot"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Store.UpsertBotMemory(ctx, model.BotMemory{BotID: b.ID, Title: "memory", Body: "remember me"})
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := s.Box.Encrypt("private api credential")
	_, err = s.Store.CreateSecret(ctx, store.SecretRec{Secret: model.Secret{Name: "api"}, EncryptedValue: enc})
	if err != nil {
		t.Fatal(err)
	}
	w := migrationJSON(h, token, "/api/backup/export", `{"password":"long-test-password"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if s.Migration.Gate.Paused() {
		t.Fatal("export did not resume")
	}
	target, th, tt := migrationFixture(t)
	var upload bytes.Buffer
	mw := multipart.NewWriter(&upload)
	f, _ := mw.CreateFormFile("backup", "test.ofbackup")
	f.Write(w.Body.Bytes())
	mw.WriteField("password", "long-test-password")
	mw.WriteField("confirm", "true")
	mw.Close()
	r := httptest.NewRequest("POST", "/api/backup/restore", &upload)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set(CSRFHeader, "1")
	r.AddCookie(&http.Cookie{Name: "of_session", Value: tt})
	w = httptest.NewRecorder()
	th.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	dir, err := migration.Resolve(target.Migration.Root)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	box, _ := secret.New(key)
	db, err := store.NewMemoryAccounts(filepath.Join(dir, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.EnablePersistence(dir, box); err != nil {
		t.Fatal(err)
	}
	mem, err := db.ListBotMemories(ctx, b.ID)
	if err != nil || len(mem) != 1 || mem[0].Body != "remember me" {
		t.Fatal(mem, err)
	}
	rec, err := db.SecretByName(ctx, "api")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := box.Decrypt(rec.EncryptedValue)
	if err != nil || plain != "private api credential" {
		t.Fatal("key restore failed")
	}
	if _, err = db.UserBySession(ctx, hashToken(token)); err == nil {
		t.Fatal("source login session migrated")
	}
	if !target.Migration.Gate.Paused() {
		t.Fatal("target must remain paused")
	}
}

func TestTransferCodeAuthenticationReplayAndNonemptyTarget(t *testing.T) {
	s, h, token := migrationFixture(t)
	w := migrationJSON(h, "", "/api/migration/code", `{}`)
	if w.Code != 401 {
		t.Fatal("unauthenticated receive enabled")
	}
	w = migrationJSON(h, token, "/api/migration/code", `{}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var reply struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	source, _, _ := migrationFixture(t)
	raw, err := source.exportSnapshot(context.Background(), reply.Code)
	if err != nil {
		t.Fatal(err)
	}
	send := func(proof string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/migration/receive", bytes.NewReader(raw))
		r.Header.Set("X-OneFrog-Migration-Proof", proof)
		h.ServeHTTP(w, r)
		return w
	}
	if w = send("bad"); w.Code != 403 {
		t.Fatal("invalid proof accepted")
	}
	proof := migration.Proof(reply.Code, "upload", raw)
	w = send(proof)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !migration.Verify(reply.Code, "received", raw, w.Body.String()) {
		t.Fatal("ack not authenticated")
	}
	if w = send(proof); w.Code != 409 {
		t.Fatal("replay accepted")
	}
	if !s.Migration.restartRequired {
		t.Fatal("restore not activated")
	}
	nonempty, nh, nt := migrationFixture(t)
	nonempty.Store.CreateBot(context.Background(), model.Bot{Name: "existing"})
	if w = migrationJSON(nh, nt, "/api/migration/code", `{}`); w.Code != 409 {
		t.Fatal("nonempty target accepted")
	}
	if nonempty.Migration.Gate.Paused() {
		t.Fatal("failed receive stranded active target")
	}
}

func TestBackupBusyAndMaintenanceGate(t *testing.T) {
	s, h, token := migrationFixture(t)
	done, _ := s.Migration.Gate.Enter()
	w := migrationJSON(h, token, "/api/backup/export", `{"password":"long-test-password"}`)
	if w.Code != 409 {
		t.Fatal("backed up a live writer")
	}
	done()
	if err := s.Migration.Gate.Pause(); err != nil {
		t.Fatal(err)
	}
	if w = migrationJSON(h, token, "/api/bots", `{"name":"blocked"}`); w.Code != 503 {
		t.Fatal("writer bypassed pause")
	}
	if w = migrationJSON(h, token, "/api/migration/resume", `{"confirm":true}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
