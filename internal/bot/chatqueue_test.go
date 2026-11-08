package bot

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"onefrog/internal/preferences"
	"onefrog/internal/task"
)

func TestOrderedChatSurvivesRestartAndKeepsReplyOrder(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasks.enc")
	f := newRuntimeFixture(t, path, preferences.ModeOff, 4,
		func(int) string { return plainReply("第一轮答复") },
		func(int) string { return plainReply("第二轮答复") },
	)
	f.agent.TasksRunner = f.runner
	send := func(request, text string) task.Task {
		t.Helper()
		it, err := f.agent.SubmitTask(ctx, SubmitRequest{
			OwnerID: f.owner, BotID: "main", SessionID: "chat-queue-test",
			ClientRequestID: request, Content: text, OrderedChat: true,
			Options: Options{Mode: "direct"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return it
	}
	first := send("queue-first", "第一条问题")
	second := send("queue-second", "第二条追问")
	if replay := send("queue-second", "第二条追问"); replay.ID != second.ID {
		t.Fatal("retry created a second queued message")
	}
	cancelled := send("queue-cancel", "撤回这条未执行的消息")
	if _, err := f.runner.Pause(ctx, f.owner, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.WithdrawChat(f.owner, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	before, err := f.agent.Store.ListMessages(ctx, first.SessionID)
	if err != nil || len(before) != 0 {
		t.Fatalf("queued messages polluted the transcript: %v, %v", before, err)
	}

	book, err := task.New(path, f.agent.Box)
	if err != nil {
		t.Fatal(err)
	}
	f.book, f.agent.Tasks = book, book
	f.runner = NewRunner(f.agent, book)
	f.agent.TasksRunner = f.runner
	f.runner.dispatch()
	after, err := book.Get(second.ID)
	if err != nil || after.Status != task.StatusQueued || !after.StartedAt.IsZero() {
		t.Fatalf("queued follow-up jumped a parked turn after restart: %+v, %v", after, err)
	}
	if _, err := f.runner.Resume(ctx, f.owner, first.ID); err != nil {
		t.Fatal(err)
	}
	stop := runBackground(t, f.runner)
	defer stop()
	if final := f.waitTerminal(t, second.ID); final.Status != task.StatusCompleted {
		t.Fatalf("follow-up did not complete: %s (%s)", final.Status, final.LastError)
	}
	stop()
	if firstCall := f.model.body(0); !strings.Contains(firstCall, "第一条问题") || strings.Contains(firstCall, "第二条追问") {
		t.Fatalf("first turn did not receive exactly its own input: %s", firstCall)
	}
	if secondCall := f.model.body(1); !strings.Contains(secondCall, "第二条追问") || !strings.Contains(secondCall, "第一轮答复") || strings.Contains(secondCall, "撤回这条") {
		t.Fatalf("follow-up lost prior context or included a cancelled message: %s", secondCall)
	}
	rows, err := f.agent.Store.ListMessages(ctx, first.SessionID)
	if err != nil || len(rows) != 4 {
		t.Fatalf("expected two complete user/bot turns, got %v, %v", rows, err)
	}
	id1, _ := ChatQueueMessage(first)
	id2, _ := ChatQueueMessage(second)
	if rows[0].ID != id1 || rows[1].Content != "第一轮答复" || rows[2].ID != id2 || rows[3].Content != "第二轮答复" {
		t.Fatalf("out-of-order transcript: %v", rows)
	}
}

func TestOrderedChatDoesNotDuplicateGoalWhenFirstWindowRestarts(t *testing.T) {
	ctx := context.Background()
	f := newSubmissionFixture(t)
	it, err := f.agent.SubmitTask(ctx, SubmitRequest{
		OwnerID: f.owner, BotID: "main", ClientRequestID: "queue-open",
		Content: "检查运行状态", OrderedChat: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	it, err = f.book.Update(it.ID, func(t *task.Task) error {
		t.Status = task.StatusRunning
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.WithdrawChat(f.owner, it.ID); err == nil {
		t.Fatal("stale withdraw stopped an active turn")
	}
	opened, err := f.runner.openChatTurn(ctx, it)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.openChatTurn(ctx, opened); err != nil {
		t.Fatal(err)
	}
	rows, err := f.agent.Store.ListMessages(ctx, it.SessionID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("goal written more than once: %v, %v", rows, err)
	}
}
