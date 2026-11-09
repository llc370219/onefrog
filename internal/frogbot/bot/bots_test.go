package bot

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onefrog/internal/frogbot/onefrog"
)

func TestBotsKeepSeparateConversationsAndTasksAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	ai := &fakeAI{agent: func(_ context.Context, _ []onefrog.Message) (onefrog.Message, error) {
		return onefrog.Message{Content: "worker result"}, nil
	}}
	cfg := Config{Name: "Original", DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "workspace"),
		ChatProvider: ai, ChatModel: "chat", AgentProvider: ai, AgentModel: "agent"}
	old, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	old.say("legacy conversation")
	old.Close()

	open := func() *Bots {
		t.Helper()
		bs, err := OpenBots(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(bs.Close)
		return bs
	}
	bs := open()
	h := bs.Handler(nil)
	request := func(method, path, body string, want int) string {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		h.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w.Body.String()
	}

	var profile BotProfile
	raw := request("POST", "/api/bots", `{"name":"Reviewer","instructions":"review-only-marker"}`, http.StatusOK)
	if err := json.Unmarshal([]byte(raw), &profile); err != nil || profile.ID == "" || profile.ID == "main" || profile.AvatarSlot == 0 {
		t.Fatalf("invalid created profile: %s (%v)", raw, err)
	}
	base := "/api/bots/" + profile.ID
	request("POST", base+"/messages", `{"text":"child task marker"}`, http.StatusOK)
	child := bs.runtimes[profile.ID]
	waitFor(t, "child's background task and result", func() bool {
		return countMsgs(child.store, "assistant", "worker result") == 1
	})
	if len(child.store.Tasks()) != 1 || len(bs.main.store.Tasks()) != 0 {
		t.Fatal("task appeared outside its Bot")
	}
	taskID := child.store.Tasks()[0].ID
	request("POST", "/api/bots/main/tasks/"+taskID+"/cancel", "{}", http.StatusBadRequest)
	ai.mu.Lock()
	instructionsUsed := len(ai.reqs) > 1 && strings.Contains(ai.reqs[0], "review-only-marker") && strings.Contains(ai.reqs[1], "review-only-marker")
	ai.mu.Unlock()
	if !instructionsUsed {
		t.Fatal("Bot instructions were not applied to chat and worker")
	}
	att, err := child.Upload("child.txt", strings.NewReader("child file content"))
	if err != nil {
		t.Fatal(err)
	}
	request("GET", "/api/bots/main/files?path="+att.Path, "", http.StatusNotFound)
	if got := request("GET", base+"/files?path="+att.Path, "", http.StatusOK); got != "child file content" {
		t.Fatalf("unexpected child file contents: %s", got)
	}
	request("POST", base+"/secrets", `{"name":"CHILD_ONLY","value":"child-private-value"}`, http.StatusOK)
	if got := request("GET", "/api/bots/main/secrets", "", http.StatusOK); strings.Contains(got, "CHILD_ONLY") {
		t.Fatal("child secret metadata leaked to default Bot")
	}
	if got := request("GET", base+"/secrets", "", http.StatusOK); !strings.Contains(got, "CHILD_ONLY") || strings.Contains(got, "child-private-value") {
		t.Fatal("child secret metadata missing or value exposed")
	}
	request("GET", "/api/bots/unknown/info", "", http.StatusNotFound)
	request("PATCH", base, `{"name":"Reviewer renamed","instructions":"review-only-marker"}`, http.StatusOK)
	bs.Close()

	bs = open()
	h = bs.Handler(nil)
	if got := request("GET", "/api/bots", "", http.StatusOK); !strings.Contains(got, "Reviewer renamed") {
		t.Fatalf("Bot list did not survive restart: %s", got)
	}
	if bs.runtimes[profile.ID].botProfile().AvatarSlot != profile.AvatarSlot {
		t.Fatal("avatar changed after rename and restart")
	}
	legacy := replayText(t, h, "/api/bots/main/stream")
	created := replayText(t, h, base+"/stream")
	if !strings.Contains(legacy, "legacy conversation") || strings.Contains(legacy, "child task marker") ||
		!strings.Contains(created, "child task marker") || strings.Contains(created, "legacy conversation") {
		t.Fatalf("unexpected SSE history: default=%q, created=%q", legacy, created)
	}
	request("POST", "/api/settings", `{"base_url":"http://127.0.0.1:1/v1","model":"updated-model"}`, http.StatusOK)
	if got := request("GET", base+"/info", "", http.StatusOK); !strings.Contains(got, `"chat_model":"updated-model"`) {
		t.Fatalf("child did not inherit the updated model: %s", got)
	}
}

func TestDeleteBotStopsWorkAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	ai := &fakeAI{agent: blockingAgent}
	cfg := Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "workspace"),
		ChatProvider: ai, ChatModel: "chat", AgentProvider: ai, AgentModel: "agent"}
	bs, err := OpenBots(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	h := bs.Handler(nil)
	request := func(method, path string, status int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(`{"name":"Temporary"}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, status, w.Body)
		}
		return w.Body.String()
	}

	var p BotProfile
	if err := json.Unmarshal([]byte(request("POST", "/api/bots", http.StatusOK)), &p); err != nil {
		t.Fatal(err)
	}
	b := bs.runtimes[p.ID]
	att, err := b.Upload("keep.txt", strings.NewReader("keep"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := b.StartTask("wait for deletion", "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task is running", func() bool {
		rec, _ := b.store.Task(task)
		return rec.Status == "running"
	})

	path := "/api/bots/" + p.ID
	request("DELETE", "/api/bots/main", http.StatusBadRequest)
	request("DELETE", path, http.StatusOK)
	request("GET", path+"/info", http.StatusNotFound)
	request("DELETE", path, http.StatusNotFound)
	request("GET", "/api/bots/main/info", http.StatusOK)
	waitFor(t, "deleted Bot's task was stopped", func() bool {
		rec, _ := b.store.Task(task)
		return rec.Status == "cancelled"
	})
	bs.Close()
	if data, err := os.ReadFile(filepath.Join(b.cfg.Workspace, att.Path)); err != nil || string(data) != "keep" {
		t.Fatalf("deleted Bot's files were not retained: %q, %v", data, err)
	}

	reopened, err := OpenBots(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	h = reopened.Handler(nil)
	request("GET", path+"/info", http.StatusNotFound)
	if got := request("GET", "/api/bots", http.StatusOK); strings.Contains(got, p.ID) {
		t.Fatalf("deleted Bot returned after restart: %s", got)
	}
}

func TestBotInboxReplyCorrelationAndMemoryScopes(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "workspace")}
	bs, err := OpenBots(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	peer, err := bs.setProfile("", BotProfile{Name: "Peer"})
	if err != nil {
		t.Fatal(err)
	}
	main, other := bs.runtimes[defaultBotID], bs.runtimes[peer.ID]
	if _, err := bs.Deliver(main.ID(), other.ID(), "question body", "", ""); err != nil {
		t.Fatal(err)
	}
	inbox := other.store.Inbox(false)
	if len(inbox) != 1 || inbox[0].Text != "question body" || inbox[0].RequestID == "" {
		t.Fatalf("request was not durably delivered: %+v", inbox)
	}
	reqID := inbox[0].RequestID
	if _, err := bs.Deliver(main.ID(), other.ID(), "spoof", "", reqID); err == nil {
		t.Fatal("reply_to from the wrong sender was accepted")
	}
	if _, err := bs.Deliver(other.ID(), main.ID(), "reply body", "", reqID); err != nil {
		t.Fatal(err)
	}
	replies := main.store.Inbox(false)
	if len(replies) != 1 || replies[0].Text != "reply body" || replies[0].ReplyTo != reqID {
		t.Fatalf("correlated reply missing: %+v", replies)
	}
	if _, err := bs.Deliver(other.ID(), main.ID(), "second reply", "", reqID); err == nil {
		t.Fatal("duplicate reply was accepted")
	}
	if err := main.writeMemory("private", "private marker", "replace"); err != nil {
		t.Fatal(err)
	}
	if err := main.writeMemory("shared", "shared marker", "replace"); err != nil {
		t.Fatal(err)
	}
	private, err := main.readMemory("private")
	if err != nil || private != "private marker" || strings.Contains(private, "shared marker") {
		t.Fatalf("private memory scope leaked shared data: %q (%v)", private, err)
	}
	shared, err := other.readMemory("shared")
	if err != nil || shared != "shared marker" {
		t.Fatalf("shared memory not visible to peer: %q (%v)", shared, err)
	}
}

type replayRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w replayRecorder) Flush() {
	w.ResponseRecorder.Flush()
	w.cancel() // End the request after the initial replay is flushed.
}

func replayText(t *testing.T, handler http.Handler, path string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := replayRecorder{httptest.NewRecorder(), cancel}
	handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil).WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("stream returned %d: %s", w.Code, w.Body)
	}
	sc := bufio.NewScanner(w.Body)
	var history strings.Builder
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Kind == "replay.done" {
			return history.String()
		}
		history.WriteString(line)
	}
	t.Fatalf("stream ended before replay.done: %v", sc.Err())
	return ""
}
