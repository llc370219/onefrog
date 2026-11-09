package bot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onefrog/internal/frogbot/onefrog"
)

func TestStopDeniesPendingChatApproval(t *testing.T) {
	dir := t.TempDir()
	b := newBot(t, dir, &fakeAI{agent: func(context.Context, []onefrog.Message) (onefrog.Message, error) {
		return onefrog.Message{Content: "done"}, nil
	}})
	b.Start()
	defer b.Close()

	done := make(chan error, 1)
	go func() {
		_, err := b.chatWrite(fileTools.Get("write_file"), json.RawMessage("{\"path\":\"notes.md\",\"content\":\"hello\"}"))
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	var id string
	for id == "" && time.Now().Before(deadline) {
		for _, a := range b.store.PendingApprovals() {
			id = a.ID
		}
		time.Sleep(10 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("approval card never appeared")
	}
	if n := b.Stop(); n != 0 {
		t.Fatalf("stop cancelled %d tasks, want 0", n)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending approval survived Stop")
	}
	if _, err := os.Stat(filepath.Join(dir, "ws", "notes.md")); !os.IsNotExist(err) {
		t.Fatal("stopped approval still wrote the file")
	}
	if err := b.Decide(id, "once"); err == nil {
		t.Fatal("a denied approval could still be approved")
	}
}

func TestCorruptEventIsSkippedAndLaterHistoryRestored(t *testing.T) {
	dir := t.TempDir()
	ai := &fakeAI{agent: func(context.Context, []onefrog.Message) (onefrog.Message, error) {
		return onefrog.Message{Content: "done"}, nil
	}}
	b := newBot(t, dir, ai)
	if _, err := b.Send("first line remains"); err != nil {
		t.Fatal(err)
	}
	b.Close()

	logPath := filepath.Join(dir, "data", "stream.jsonl")
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"id": "msg_restored", "role": "user", "text": "later line remains", "created_at": time.Now()})
	event, _ := json.Marshal(map[string]any{"seq": 99, "kind": "message", "time": time.Now(), "data": json.RawMessage(body)})
	raw = append(append(raw, []byte("this-line-is-not-an-event\n")...), append(event, '\n')...)
	if err := os.WriteFile(logPath, raw, 0600); err != nil {
		t.Fatal(err)
	}

	restored := newBot(t, dir, ai)
	defer restored.Close()
	if restored.store.Skipped() != 1 {
		t.Fatalf("skipped = %d, want 1", restored.store.Skipped())
	}
	var sawFirst, sawLater bool
	for _, m := range restored.store.Messages() {
		sawFirst = sawFirst || strings.Contains(m.Text, "first line remains")
		sawLater = sawLater || strings.Contains(m.Text, "later line remains")
	}
	if !sawFirst || !sawLater {
		t.Fatalf("history not restored: first=%v later=%v", sawFirst, sawLater)
	}
}
