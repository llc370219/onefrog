package bot

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"onefrog/internal/task"
)

// sayReply builds a tool-call reply whose content is say.
func sayReply(id, name, args, say string) string {
	payload := map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{
			"content": say,
			"tool_calls": []any{map[string]any{
				"id": id, "type": "function",
				"function": map[string]string{"name": name, "arguments": args},
			}},
		}}},
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

// transcriptMessages lists a session's messages.
func transcriptMessages(t *testing.T, f *runtimeFixture, session string) []model.Message {
	t.Helper()
	msgs, err := f.agent.Store.ListMessages(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

// TestTaskCommentaryPersistsRedacted proves the assistant text that
// accompanies a tool-call turn reaches the durable transcript redacted,
// exactly once, under a deterministic id.
func TestTaskCommentaryPersistsRedacted(t *testing.T) {
	const secretText = "commentary-secret-42"
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 8,
		func(int) string {
			return sayReply("c1", "inspect_services", `{"action":"list"}`, "先看磁盘，密码是 "+secretText)
		},
		// An explicit finish ends the task without relying on the plain-reply
		// completion path.
		func(int) string {
			return toolReply("c2", "finish", `{"summary":"磁盘已检查","verification":["inspect_services 列表正常"]}`)
		},
	)
	// A stored secret the commentary must not carry.
	enc, _ := f.agent.Box.Encrypt(secretText)
	if _, err := f.agent.Store.CreateSecret(context.Background(), store.SecretRec{Secret: model.Secret{Name: "db", Description: "test"}, EncryptedValue: enc}); err != nil {
		t.Fatal(err)
	}
	stop := runBackground(t, f.runner)
	defer stop()

	it := f.submit(t, context.Background(), "sess-commentary", "检查磁盘")
	it = f.waitTerminal(t, it.ID)

	found := 0
	for _, m := range transcriptMessages(t, f, it.SessionID) {
		if m.Role != "assistant" || !strings.Contains(m.Content, "先看磁盘") {
			continue
		}
		found++
		if strings.Contains(m.Content, secretText) {
			t.Fatalf("commentary leaked the secret: %q", m.Content)
		}
		if !strings.Contains(m.Content, "***") {
			t.Fatalf("commentary is not redacted: %q", m.Content)
		}
		if !strings.HasPrefix(m.ID, "task-commentary-") {
			t.Fatalf("commentary id is not deterministic: %q", m.ID)
		}
	}
	if found != 1 {
		t.Fatalf("expected exactly one commentary message, got %d", found)
	}
}

// TestTaskCommentaryVisibleBeforeNextModelRequest proves ordering: when the
// second model request is made, the turn-1 commentary is already in the
// transcript, so a reader sees what the task said while the tool runs.
func TestTaskCommentaryVisibleBeforeNextModelRequest(t *testing.T) {
	var f *runtimeFixture
	replies := []func(call int) string{
		func(int) string {
			return sayReply("c1", "inspect_services", `{"action":"list"}`, "正在看磁盘")
		},
		// Runs inside the second model request, before its response exists:
		// everything the task said in turn 1 must already be durable.
		func(int) string {
			msgs, err := f.agent.Store.ListMessages(context.Background(), "sess-order")
			if err != nil {
				// Not t.Fatal: this runs on the HTTP handler goroutine, and
				// Goexit here would deadlock the fixture.
				t.Errorf("list transcript: %v", err)
				return toolReply("c2", "finish", `{"summary":"磁盘已检查","verification":["inspect_services 列表正常"]}`)
			}
			seen := false
			for _, m := range msgs {
				if m.Role == "assistant" && strings.HasPrefix(m.ID, "task-commentary-") && strings.Contains(m.Content, "正在看磁盘") {
					seen = true
				}
			}
			if !seen {
				t.Errorf("turn-1 commentary missing when the next model request was made")
			}
			return toolReply("c2", "finish", `{"summary":"磁盘已检查","verification":["inspect_services 列表正常"]}`)
		},
	}
	f = newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 8, replies...)
	stop := runBackground(t, f.runner)
	defer stop()

	it := f.submit(t, context.Background(), "sess-order", "检查磁盘")
	f.waitTerminal(t, it.ID)
}

// TestTaskCommentaryFinishNotDuplicated proves the text of a plain closing
// reply is never stored as commentary: saveFinal owns the final message.
func TestTaskCommentaryFinishNotDuplicated(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 8,
		func(int) string { return plainReply("检查完毕，一切正常") },
	)
	stop := runBackground(t, f.runner)
	defer stop()

	it := f.submit(t, context.Background(), "sess-finish", "检查磁盘")
	it = f.waitTerminal(t, it.ID)

	closing := 0
	for _, m := range transcriptMessages(t, f, it.SessionID) {
		if m.Role != "assistant" || !strings.Contains(m.Content, "检查完毕") {
			continue
		}
		closing++
		if strings.HasPrefix(m.ID, "task-commentary-") {
			t.Fatalf("final text was persisted as commentary: %q", m.ID)
		}
	}
	if closing != 1 {
		t.Fatalf("expected exactly one final message, got %d", closing)
	}
}

// guardStore observes the context and pauses the task from inside
// ListMessages, the last read before AddMessage in writeCommentary.
type guardStore struct {
	store.Store
	mu      sync.Mutex
	armed   bool
	taskID  string
	book    *task.Book
	entered chan struct{}
	release chan struct{}
}

func (g *guardStore) ListMessages(ctx context.Context, session string) ([]model.Message, error) {
	msgs, err := g.Store.ListMessages(ctx, session)
	g.mu.Lock()
	armed := g.armed
	g.mu.Unlock()
	if armed && strings.HasPrefix(session, "sess-guard") {
		g.mu.Lock()
		g.armed = false
		g.mu.Unlock()
		// The operator pause lands while the transcript read is in flight.
		_, _ = g.book.Update(g.taskID, func(tk *task.Task) error {
			tk.Status = task.StatusPaused
			return nil
		})
		close(g.entered)
		<-g.release
	}
	return msgs, err
}

// TestTaskCommentarySuppressedAfterPauseDuringWrite proves the guard after
// ListMessages: a pause that lands while the transcript read is in flight
// stops the write, even though every earlier check had passed. The checks are
// best-effort race gates, not an atomic guarantee across Book and Store.
func TestTaskCommentarySuppressedAfterPauseDuringWrite(t *testing.T) {
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 8)
	guard := &guardStore{Store: f.agent.Store, entered: make(chan struct{}), release: make(chan struct{})}
	f.agent.Store = guard
	now := time.Now().UTC()
	const id = "t-commentary-guard"
	if _, err := f.book.Create(task.Task{
		ID: id, OwnerID: f.owner, BotID: "main", SessionID: "sess-guard-pause", Goal: "检查磁盘",
		Status: task.StatusRunning, CreatedAt: now, UpdatedAt: now,
		StepBudget: 8, MaxSteps: 99, Deadline: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.agent.Store.EnsureSession(context.Background(), "sess-guard-pause", "main", "守卫"); err != nil {
		t.Fatal(err)
	}
	guard.mu.Lock()
	guard.taskID = id
	guard.book = f.book
	guard.armed = true
	guard.mu.Unlock()

	st := &taskState{book: f.book, id: id, turn: 1, finalMu: &f.runner.finalMu}
	base := context.WithValue(context.Background(), taskSessionKey{}, "sess-guard-pause")
	ctx, cancel := context.WithCancel(base)
	done := make(chan error, 1)
	go func() { done <- st.writeCommentary(ctx, f.agent, "进行中的说明") }()
	<-guard.entered
	cancel()
	close(guard.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("writeCommentary returned an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writeCommentary did not return after the pause")
	}
	msgs, err := guard.Store.ListMessages(context.Background(), "sess-guard-pause")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if strings.HasPrefix(m.ID, "task-commentary-") {
			t.Fatalf("commentary was written after the task was paused: %q", m.ID)
		}
	}
}

// TestTaskCommentaryIdempotent proves a replayed write for the same task and
// turn appends nothing: the deterministic id makes the second call a no-op.
func TestTaskCommentaryIdempotent(t *testing.T) {
	f := newRuntimeFixture(t, "", preferences.ModeOff, 8)
	now := time.Now().UTC()
	const id = "t-commentary-replay"
	if _, err := f.book.Create(task.Task{
		ID: id, OwnerID: f.owner, BotID: "main", SessionID: "sess-replay", Goal: "检查磁盘",
		Status: task.StatusRunning, CreatedAt: now, UpdatedAt: now,
		StepBudget: 8, MaxSteps: 99, Deadline: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.agent.Store.EnsureSession(context.Background(), "sess-replay", "main", "重放"); err != nil {
		t.Fatal(err)
	}
	st := &taskState{book: f.book, id: id, turn: 1, finalMu: &f.runner.finalMu}
	ctx := context.WithValue(context.Background(), taskSessionKey{}, "sess-replay")
	if err := st.writeCommentary(ctx, f.agent, "进行中的说明"); err != nil {
		t.Fatal(err)
	}
	msgs, err := f.agent.Store.ListMessages(context.Background(), "sess-replay")
	if err != nil {
		t.Fatal(err)
	}
	before := 0
	for _, m := range msgs {
		if strings.HasPrefix(m.ID, "task-commentary-") {
			before++
		}
	}
	if before != 1 {
		t.Fatalf("expected exactly one commentary message, got %d", before)
	}
	// Same task, same turn: the deterministic id must make this a no-op.
	if err := st.writeCommentary(ctx, f.agent, "进行中的说明"); err != nil {
		t.Fatal(err)
	}
	msgs, err = f.agent.Store.ListMessages(context.Background(), "sess-replay")
	if err != nil {
		t.Fatal(err)
	}
	after := 0
	for _, m := range msgs {
		if strings.HasPrefix(m.ID, "task-commentary-") {
			after++
		}
	}
	if after != 1 {
		t.Fatalf("replay appended a duplicate commentary: %d -> %d", before, after)
	}
}

var _ = secret.New
