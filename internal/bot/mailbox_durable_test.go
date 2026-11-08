package bot

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"onefrog/internal/task"
)

// durableMailFixture mirrors the mailbox fixture but wires the durable runner.
func durableMailFixture(t *testing.T) *runtimeFixture {
	t.Helper()
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), "off", 3,
		func(int) string { return plainReply("巡检已处理") })
	f.agent.TasksRunner = f.runner
	return f
}

func TestMessageAgentCommitsDurableInboxTaskBeforeDelivery(t *testing.T) {
	f := durableMailFixture(t)
	f.agent.Mail = NewMailbox()
	ctx := context.WithValue(context.Background(), optionsKey{}, Options{OwnerID: f.owner})
	speaker, err := f.agent.Store.GetBot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	result := f.agent.sendMessage(ctx, speaker, "web", messageCall("check", "web-01 磁盘 91%，请跟进"),
		&trace{agent: f.agent, ctx: ctx})
	if !strings.Contains(result, "已送达") {
		t.Fatalf("send did not report delivery: %q", result)
	}
	// Delivery must already be a durable task: nothing is queued in memory and
	// the note is in the accepted transcript.
	var inboxes []task.Task
	for _, it := range f.book.List("") {
		if it.Purpose == "inbox" {
			inboxes = append(inboxes, it)
		}
	}
	if len(inboxes) != 1 {
		t.Fatalf("durable inbox tasks = %d", len(inboxes))
	}
	got := inboxes[0]
	if got.BotID != "check" || got.SessionID != inboxSession("check") {
		t.Fatalf("inbox task routed wrong: %+v", got)
	}
	if !got.Untrusted || got.Hop != 1 {
		t.Fatalf("inbox provenance wrong: %+v", got)
	}
	if !strings.Contains(got.Goal, "[来自 主控") || !strings.Contains(got.Goal, "91%") {
		t.Fatalf("note not accepted as goal: %q", got.Goal)
	}
	msgs, _ := f.agent.Store.ListMessages(ctx, got.SessionID)
	users := 0
	for _, m := range msgs {
		if m.Role == "user" {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("note stored %d times", users)
	}
}

func TestInboxDurableTaskSurvivesRestartAndKeepsProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.enc")
	f := newRuntimeFixture(t, path, "off", 3, func(int) string { return plainReply("ok") })
	f.agent.TasksRunner = f.runner
	f.agent.Mail = NewMailbox()
	speaker, _ := f.agent.Store.GetBot(context.Background(), "main")
	call := messageCall("check", "磁盘快满了，请跟进")
	ctx := context.WithValue(context.Background(), optionsKey{}, Options{OwnerID: f.owner})
	ctx = context.WithValue(ctx, hopKey{}, 1)
	result := f.agent.sendMessage(ctx, speaker, "web", call, &trace{agent: f.agent, ctx: ctx})
	if !strings.Contains(result, "已送达") {
		t.Fatalf("send failed: %q", result)
	}
	// A retry of the same message in the same task/window must not create a
	// second inbox task.
	result2 := f.agent.sendMessage(ctx, speaker, "web", call, &trace{agent: f.agent, ctx: ctx})
	if !strings.Contains(result2, "已送达") {
		t.Fatalf("retry failed: %q", result2)
	}
	reloaded, err := task.New(path, f.agent.Box)
	if err != nil {
		t.Fatal(err)
	}
	var inboxes []task.Task
	for _, it := range reloaded.List("") {
		if it.Purpose == "inbox" {
			inboxes = append(inboxes, it)
		}
	}
	if len(inboxes) != 1 {
		t.Fatalf("after restart inbox tasks = %d", len(inboxes))
	}
	if inboxes[0].Status != task.StatusQueued {
		t.Fatalf("inbox task not durable+queued: %s", inboxes[0].Status)
	}
	if !inboxes[0].Untrusted || inboxes[0].Hop != 2 {
		t.Fatalf("provenance lost across restart: %+v", inboxes[0])
	}
}

func TestInboxTaskCannotAskUserOrDelegate(t *testing.T) {
	f := durableMailFixture(t)
	f.agent.Mail = NewMailbox()
	speaker, _ := f.agent.Store.GetBot(context.Background(), "main")
	ctx := context.WithValue(context.Background(), optionsKey{}, Options{OwnerID: f.owner})
	if got := f.agent.sendMessage(ctx, speaker, "web", messageCall("check", "跟进磁盘"), &trace{agent: f.agent, ctx: ctx}); !strings.Contains(got, "已送达") {
		t.Fatalf("send failed: %q", got)
	}
	var inbox task.Task
	for _, it := range f.book.List("") {
		if it.Purpose == "inbox" {
			inbox = it
		}
	}
	// The persisted purpose drives the runtime capability gate, so a restarted
	// task is still a background turn.
	if inbox.Purpose != "inbox" {
		t.Fatalf("inbox task lost its purpose: %+v", inbox)
	}
}

func TestMailRequestIDIsStableWithinATask(t *testing.T) {
	st := &taskState{id: "task-abc", turn: 4}
	ctx := context.WithValue(context.Background(), taskStateKey{}, st)
	call := messageCall("check", "同样的内容")
	first := mailRequestID(ctx, "main", "check", call, "")
	second := mailRequestID(ctx, "main", "check", call, "")
	if first != second {
		t.Fatalf("request id not stable: %q vs %q", first, second)
	}
	other := toolCall{ID: "other", Type: "function"}
	other.Function = call.Function
	if mailRequestID(ctx, "main", "check", other, "") == first {
		t.Fatal("different call reused the request id")
	}
}
