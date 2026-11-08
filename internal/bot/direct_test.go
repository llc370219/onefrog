package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"onefrog/internal/model"
)

func TestDirectChatTalksToOneBotWithOwnMemory(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	n := 0
	a, s, _ := featureAgent(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		n++
		step := n
		mu.Unlock()
		switch step {
		case 1:
			_, _ = io.WriteString(w, toolJSON("ask", "ask_agent", `{"bot_id":"main","task":"看一下面板版本"}`))
		case 2:
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"v0.1.27"}}]}`)
		default:
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"主控说是 v0.1.27"}}]}`)
		}
	})
	defer s.Close()
	ctx := context.Background()
	if _, err := a.Remember(ctx, "worker", "巡检笔记", "web 磁盘 40%", "bot"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Remember(ctx, "main", "主控私记", "只给主控", "bot"); err != nil {
		t.Fatal(err)
	}
	res, err := a.ReplyWithOptions(ctx, "worker", "", "", "面板什么版本", Options{Mode: "direct", ExcludedBotIDs: []string{"main"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := json.Marshal(bodies[0])
	raw := string(first)
	for _, bad := range []string{"运维主控", "各 Bot 最近记住的标题", "只给主控"} {
		if strings.Contains(raw, bad) {
			t.Fatalf("direct prompt leaked %q: %s", bad, raw)
		}
	}
	// A sub-bot works on its own in a direct chat: it gets the same panel,
	// probe and module tools as the lead, not a "please ask main" stub.
	for _, want := range []string{"单独对话", "巡检笔记", "需要时可以请这些 Bot 协助", "ask_agent", "message_agent", "\"name\":\"panel\"", "\"name\":\"install_probe\"", "\"name\":\"develop_module\"", "【管理面板：panel】"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("direct prompt missing %q", want)
		}
	}
	if len(res.Steps) == 0 || a.Usage.ForBot("main").Requests == 0 {
		t.Fatal("direct bot could not ask main for help", res.Steps)
	}
	sessions, _ := a.Store.ListChatSessions(ctx)
	var saved Options
	if len(sessions) != 1 || json.Unmarshal(sessions[0].Options, &saved) != nil || saved.Mode != "direct" || len(saved.ExcludedBotIDs) != 0 {
		t.Fatalf("mode not saved: %s", sessions[0].Options)
	}
	// A later turn that omits the mode keeps the session direct.
	mu.Lock()
	bodies, n = nil, 2
	mu.Unlock()
	if _, err = a.ReplyWithOptions(ctx, "worker", "", res.SessionID, "谢谢", Options{}, nil); err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(bodies[0])
	if strings.Contains(string(again), "运维主控") || !strings.Contains(string(again), "单独对话") {
		t.Fatal("session lost direct mode")
	}
}

func TestDirectChatModeValidationAndLimits(t *testing.T) {
	a, s, _ := featureAgent(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	})
	defer s.Close()
	ctx := context.Background()
	if _, err := a.ReplyWithOptions(ctx, "worker", "", "", "hi", Options{Mode: "group"}, nil); err == nil || !strings.Contains(err.Error(), "对话模式无效") {
		t.Fatal(err)
	}
	if _, err := a.Remember(ctx, "main", "主控私记", "只给主控", "bot"); err != nil {
		t.Fatal(err)
	}
	direct := context.WithValue(ctx, optionsKey{}, Options{Mode: "direct"})
	worker := model.Bot{ID: "worker", Name: "巡检"}
	if out := a.recallCall(direct, worker, recallArgs(`{"bot_id":"main"}`)); !strings.Contains(out, "独立对话只能读取自己的记忆") {
		t.Fatal(out)
	}
	if out := a.execCall(direct, worker, credentials{}, "", callPanel(`{"action":"overview"}`), false, nil); strings.Contains(out, "不能直接改管理面板") {
		t.Fatal(out)
	}
	if !a.commandsPanel(direct, worker) {
		t.Fatal("sub-bot lost panel access in direct chat")
	}
	if !a.commandsPanel(direct, model.Bot{ID: "main", Name: "主控"}) {
		t.Fatal("main lost panel access in direct chat")
	}
	// A delegated worker still only does the task it was handed.
	delegated := usageContext(direct, "worker", "worker", "")
	if a.commandsPanel(delegated, worker) {
		t.Fatal("delegated worker got panel access")
	}
	// A sub-bot may manage peers but must not switch off or delete the lead.
	if out, ok := a.panelSetBot(direct, worker, "main", new(bool)); ok || !strings.Contains(out, "主控") {
		t.Fatal(out)
	}
	if out, ok := a.panelDeleteBot(direct, worker, "main"); ok || !strings.Contains(out, "主控") {
		t.Fatal(out)
	}
	// Team mode is not a shared memory pool: naming another bot is refused and
	// its private note never comes back.
	if out := a.recallCall(ctx, worker, recallArgs(`{"bot_id":"main"}`)); strings.Contains(out, "只给主控") || !strings.Contains(out, "只能读取自己的记忆") {
		t.Fatal(out)
	}
}
