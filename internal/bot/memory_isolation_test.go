package bot

import (
	"context"
	"strings"
	"testing"

	"onefrog/internal/model"
)

// projectContext builds a group/project turn scope backed by a real group
// session, matching how agent.go and tasks.go tag project turns.
func projectContext(t *testing.T, mem interface {
	EnsureSession(context.Context, string, string, string) (model.Session, error)
}, sessionID, botID string) context.Context {
	t.Helper()
	ctx := context.Background()
	if _, err := mem.EnsureSession(ctx, sessionID, botID, "隔离项目"); err != nil {
		t.Fatal(err)
	}
	return withProject(ctx, sessionID)
}

func TestPrivateMemoryIsNeverReadableAcrossBots(t *testing.T) {
	mem, box := memoryFixture(t, "")
	agent := &Agent{Store: mem, Box: box}
	ctx := context.Background()
	leader, err := mem.GetBot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := mem.GetBot(ctx, "check")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Remember(ctx, leader.ID, "主控私记", "只有主控自己看", "bot"); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Remember(ctx, worker.ID, "巡检私记", "只有巡检自己看", "bot"); err != nil {
		t.Fatal(err)
	}

	// The leader is not a back door into a worker's private notes.
	if out := agent.recallCall(ctx, leader, recallArgs(`{"bot_id":"巡检"}`)); strings.Contains(out, "只有巡检自己看") || !strings.Contains(out, "只能读取自己的记忆") {
		t.Fatalf("leader read worker memory: %s", out)
	}
	if out := agent.recallCall(ctx, leader, recallArgs(`{"bot_id":"check"}`)); strings.Contains(out, "只有巡检自己看") {
		t.Fatalf("leader read worker memory by id: %s", out)
	}

	// Workers and direct chats stay confined to their own notes too.
	workerCtx := usageContext(ctx, worker.ID, "worker", "")
	if out := agent.recallCall(workerCtx, worker, recallArgs(`{"bot_id":"主控"}`)); strings.Contains(out, "只有主控自己看") || !strings.Contains(out, "只能读取自己的记忆") {
		t.Fatalf("worker read leader memory: %s", out)
	}
	directCtx := context.WithValue(ctx, optionsKey{}, Options{Mode: "direct"})
	if out := agent.recallCall(directCtx, worker, recallArgs(`{"bot_id":"main"}`)); strings.Contains(out, "只有主控自己看") || !strings.Contains(out, "只能读取自己的记忆") {
		t.Fatalf("direct chat read leader memory: %s", out)
	}

	// Own memory still works in every role.
	for _, tc := range []struct {
		name string
		ctx  context.Context
		bot  model.Bot
		want string
	}{
		{"leader", ctx, leader, "只有主控自己看"},
		{"worker", workerCtx, worker, "只有巡检自己看"},
		{"direct", directCtx, worker, "只有巡检自己看"},
	} {
		if out := agent.recallCall(tc.ctx, tc.bot, recallArgs("")); !strings.Contains(out, tc.want) {
			t.Fatalf("%s could not read own memory: %s", tc.name, out)
		}
	}

	// Prompts must not inline peer memory titles as a shared index.
	var b strings.Builder
	agent.writePeerMemory(&b, ctx, leader.ID)
	for _, bad := range []string{"主控私记", "巡检私记", "各 Bot 最近记住的标题"} {
		if strings.Contains(b.String(), bad) {
			t.Fatalf("peer memory leaked %q: %s", bad, b.String())
		}
	}
	if !strings.Contains(b.String(), "ask_agent") {
		t.Fatalf("peer hint lost handoff guidance: %s", b.String())
	}
}

func TestProjectScopeDoesNotReadOrWritePrivateMemory(t *testing.T) {
	mem, box := memoryFixture(t, "")
	agent := &Agent{Store: mem, Box: box}
	base := context.Background()
	leader, _ := mem.GetBot(base, "main")
	worker, _ := mem.GetBot(base, "check")
	if _, err := agent.Remember(base, leader.ID, "主控私记", "私有内容", "bot"); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Remember(base, worker.ID, "巡检私记", "私有内容", "bot"); err != nil {
		t.Fatal(err)
	}

	ctx := projectContext(t, mem, "group-isolated", leader.ID)

	// A project turn writes only project notes, even when the tool call names
	// the speaker's own bot id.
	if out := agent.rememberCall(ctx, leader, rememberArgs(`{"title":"项目结论","body":"只属于这个项目"}`), nil); strings.Contains(out, "失败") || !strings.Contains(out, "本项目") {
		t.Fatalf("project remember failed: %s", out)
	}
	notes, err := mem.ListProjectNotes(base, "group-isolated")
	if err != nil || len(notes) != 1 || notes[0].Title != "项目结论" {
		t.Fatalf("project notes: %+v %v", notes, err)
	}
	for _, id := range []string{leader.ID, worker.ID} {
		rows, err := mem.ListBotMemories(base, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Title == "项目结论" || strings.Contains(row.Body, "只属于这个项目") {
				t.Fatalf("project write landed in private memory %s: %+v", id, row)
			}
		}
	}

	// The direct Remember entry point is guarded too: no project turn may
	// write a bot's private notes through it.
	if _, err := agent.Remember(ctx, leader.ID, "越界写入", "不该保存", "user"); err == nil {
		t.Fatal("project context wrote private memory through Remember")
	}
	if rows, _ := mem.ListBotMemories(base, leader.ID); len(rows) != 1 {
		t.Fatalf("private memory changed: %+v", rows)
	}

	// Project recall returns project notes only and ignores bot_id, so it can
	// never surface either bot's private notes.
	for _, raw := range []string{"", `{"bot_id":"主控"}`, `{"bot_id":"check"}`} {
		out := agent.recallCall(ctx, leader, recallArgs(raw))
		if !strings.Contains(out, "只属于这个项目") {
			t.Fatalf("project recall missing project note: %s", out)
		}
		for _, bad := range []string{"主控私记", "巡检私记", "私有内容"} {
			if strings.Contains(out, bad) {
				t.Fatalf("project recall leaked private memory %q: %s", bad, out)
			}
		}
	}

	// Helper functions enforce the same barrier when called directly, and the
	// project prompt branch never falls through to the private-memory helpers.
	if out := agent.formatMemories(ctx, worker); !strings.Contains(out, "只属于这个项目") || strings.Contains(out, "私有内容") {
		t.Fatalf("formatMemories leaked private memory in project: %s", out)
	}
	var own strings.Builder
	agent.writeOwnMemory(&own, ctx, worker)
	if !strings.Contains(own.String(), "只属于这个项目") || strings.Contains(own.String(), "私有内容") {
		t.Fatalf("writeOwnMemory leaked private memory in project: %s", own.String())
	}
	var prompt strings.Builder
	agent.writeMemory(&prompt, ctx, worker)
	for _, bad := range []string{"主控私记", "巡检私记", "私有内容", "你自己的记忆"} {
		if strings.Contains(prompt.String(), bad) {
			t.Fatalf("project prompt leaked private memory %q: %s", bad, prompt.String())
		}
	}
	if !strings.Contains(prompt.String(), "隔离项目记忆") || !strings.Contains(prompt.String(), "只属于这个项目") {
		t.Fatalf("project prompt missing project memory: %s", prompt.String())
	}

	// A delegated worker inside the project inherits the project scope, so its
	// handoff lands in project notes rather than its private memory.
	delegated := usageContext(ctx, worker.ID, "worker", "")
	if out := agent.rememberCall(delegated, worker, rememberArgs(`{"title":"委派结论","body":"来自委派"}`), nil); strings.Contains(out, "失败") {
		t.Fatalf("delegated project remember failed: %s", out)
	}
	notes, err = mem.ListProjectNotes(base, "group-isolated")
	if err != nil || len(notes) != 2 {
		t.Fatalf("delegated note not in project: %+v %v", notes, err)
	}
	if rows, _ := mem.ListBotMemories(base, worker.ID); len(rows) != 1 || rows[0].Title != "巡检私记" {
		t.Fatalf("delegated note contaminated private memory: %+v", rows)
	}
}

func TestOrdinaryChatStillUsesOwnPrivateMemory(t *testing.T) {
	mem, box := memoryFixture(t, "")
	agent := &Agent{Store: mem, Box: box}
	ctx := context.Background()
	worker, _ := mem.GetBot(ctx, "check")

	if out := agent.rememberCall(ctx, worker, rememberArgs(`{"title":"自己的结论","body":"web 磁盘 40%"}`), nil); !strings.Contains(out, "本 Bot") {
		t.Fatalf("plain remember failed: %s", out)
	}
	if out := agent.recallCall(ctx, worker, recallArgs("")); !strings.Contains(out, "web 磁盘 40%") {
		t.Fatalf("plain recall missing own note: %s", out)
	}
	rows, err := mem.ListBotMemories(ctx, worker.ID)
	if err != nil || len(rows) != 1 || rows[0].Title != "自己的结论" {
		t.Fatalf("own memory not stored: %+v %v", rows, err)
	}
}

func rememberArgs(raw string) toolCall {
	var call toolCall
	call.Function.Name = "remember"
	call.Function.Arguments = raw
	return call
}
