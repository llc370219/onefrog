package bot

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onefrog/internal/preferences"
	"onefrog/internal/task"
)

func TestChatCorrectionReplacesStaleModelProposal(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	f := newRuntimeFixture(t, filepath.Join(t.TempDir(), "tasks.enc"), preferences.ModeOff, 5,
		func(int) string {
			close(started)
			select {
			case <-release:
			case <-time.After(5 * time.Second):
			}
			return toolReply("old", "run_command", `{"server_id":"web","command":"df -h","risk":"low","reason":"原先的命令"}`)
		},
		func(int) string { return plainReply("已经改按新要求处理") },
	)
	f.agent.TasksRunner = f.runner
	ctx := context.Background()
	it, err := f.agent.SubmitTask(ctx, SubmitRequest{OwnerID: f.owner, BotID: "main", ServerID: "web",
		ClientRequestID: "steer-test", Content: "检查磁盘", OrderedChat: true})
	if err != nil {
		t.Fatal(err)
	}
	stop := runBackground(t, f.runner)
	defer stop()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
	_, err = f.runner.GuideChat(f.owner, it.ID, "别跑原来的命令，只说明接下来会检查什么")
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	result := f.waitTerminal(t, it.ID)
	stop()
	if result.Status != task.StatusCompleted || f.ssh.count() != 0 || f.model.count() != 2 {
		t.Fatalf("old model proposal was not redirected: status=%s calls=%d ssh=%d", result.Status, f.model.count(), f.ssh.count())
	}
	if got := f.model.body(1); !strings.Contains(got, "别跑原来的命令") || !strings.Contains(got, "检查磁盘") {
		t.Fatal("corrected model request did not retain the original context and correction")
	}
	rows, err := f.agent.Store.ListMessages(ctx, it.SessionID)
	if err != nil || len(rows) != 3 || rows[1].Role != "user" || !strings.HasPrefix(rows[1].ID, "chat-steer-") {
		t.Fatalf("correction was not persisted exactly once as a user message: %v, %v", rows, err)
	}
}

func TestUnanswerableWaitingStateIsReleasedWithoutTouchingRealQuestion(t *testing.T) {
	f := newSubmissionFixture(t)
	ctx := context.Background()
	park := func(id, card string) task.Task {
		t.Helper()
		it, err := f.agent.SubmitTask(ctx, SubmitRequest{OwnerID: f.owner, BotID: "main", Content: id, ClientRequestID: id})
		if err != nil {
			t.Fatal(err)
		}
		it, err = f.book.Update(it.ID, func(t *task.Task) error {
			t.Status = task.StatusWaitingUser
			t.Pending = &task.Pending{Kind: task.PendingUser, CardMessageID: card}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return it
	}
	orphan := park("unanswerable", "")
	question := park("real-question", "card-1")
	if _, err := f.runner.GuideChat(f.owner, question.ID, "不是卡片答案"); err == nil {
		t.Fatal("correction must not answer a pending question")
	}
	f.runner.repairUnanswerableWaits()
	first, _ := f.book.Get(orphan.ID)
	second, _ := f.book.Get(question.ID)
	if first.Status != task.StatusFailed || first.Pending != nil || first.LastError != incompleteVerification {
		t.Fatalf("unanswerable wait was not released: %s", first.Status)
	}
	if second.Status != task.StatusWaitingUser || second.Pending.CardMessageID != "card-1" {
		t.Fatal("migration touched a real question")
	}
}

func TestOpenEndedQuestionAcceptsTextWithoutFakeOptions(t *testing.T) {
	c, err := ParseChoice(`{"prompt":"模块叫什么？","options":[],"allow_custom":true}`)
	if err != nil {
		t.Fatal(err)
	}
	a, err := normalizeAnswer(c, ChoiceReply{Custom: "资产统计"})
	if err != nil || a.Custom != "资产统计" {
		t.Fatalf("answer was not accepted: %+v %v", a, err)
	}
	if _, err := ParseChoice(`{"prompt":"模块叫什么？","options":[]}`); err == nil {
		t.Fatal("accepted a question with neither options nor free text")
	}
}
