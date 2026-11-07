package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

func mailboxFixture(t *testing.T, baseURL string) (*store.Memory, *secret.Box) {
	t.Helper()
	mem := store.NewMemory()
	box, err := secret.New(bytesRepeat())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now()
	for i, b := range []model.Bot{
		{ID: "main", Name: "主控", Description: "接收指令"},
		{ID: "check", Name: "巡检", Description: "看磁盘和服务"},
	} {
		b.BaseURL, b.ModelName, b.CreatedAt = baseURL, "demo", now.Add(time.Duration(i)*time.Second)
		if _, err := mem.CreateBot(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	return mem, box
}

func messageCall(target, body string) toolCall {
	call := toolCall{ID: "m1", Type: "function"}
	call.Function.Name = "message_agent"
	raw, _ := json.Marshal(map[string]string{"bot_id": target, "message": body})
	call.Function.Arguments = string(raw)
	return call
}

func TestMessageAgentWakesTargetInbox(t *testing.T) {
	var mu sync.Mutex
	var leaderBodies, inboxBodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(body, "【收件箱】") {
			inboxBodies = append(inboxBodies, body)
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"已记下，明早复查磁盘"}}]}`)
			return
		}
		leaderBodies = append(leaderBodies, body)
		if len(leaderBodies) == 1 {
			_, _ = io.WriteString(w, toolJSON("m1", "message_agent", `{"bot_id":"巡检","message":"web-01 磁盘 91%，请之后跟进"}`))
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"已通知巡检"}}]}`)
	}))
	defer srv.Close()
	mem, box := mailboxFixture(t, srv.URL+"/v1")
	agent := &Agent{Store: mem, Box: box, SSH: &scriptSSH{}, HTTP: srv.Client(), Mail: NewMailbox()}
	ctx := context.Background()

	result, err := agent.ReplyWithOptions(ctx, "main", "", "", "告诉巡检 web-01 磁盘快满了", Options{Mode: "direct"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	agent.Mail.Wait()

	var sawMessage bool
	for _, item := range result.Timeline {
		if item.Kind == "message" && item.Bot == "主控" && item.Target == "巡检" && strings.Contains(item.Task, "91%") {
			sawMessage = true
		}
	}
	if !sawMessage {
		t.Fatalf("timeline has no message item: %+v", result.Timeline)
	}
	items, err := mem.ListMessages(ctx, inboxSession("check"))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("inbox messages = %d", len(items))
	}
	if items[0].Role != "user" || items[0].FromBot != "main" || !strings.Contains(items[0].Content, "[来自 主控（id=main）]") {
		t.Fatalf("inbox note = %+v", items[0])
	}
	if items[1].Role != "assistant" || !strings.Contains(items[1].Content, "明早复查") {
		t.Fatalf("inbox reply = %+v", items[1])
	}
	sess, err := mem.GetChatSession(ctx, inboxSession("check"))
	if err != nil || sess.BotID != "check" {
		t.Fatalf("inbox session = %+v %v", sess, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(inboxBodies) != 1 {
		t.Fatalf("inbox model calls = %d", len(inboxBodies))
	}
	if !strings.Contains(leaderBodies[0], `"message_agent"`) {
		t.Fatal("leader was not offered message_agent")
	}
	inbox := inboxBodies[0]
	for _, tool := range []string{`"ask_user"`, `"ask_agent"`, `"request_secret"`, `"panel"`} {
		if strings.Contains(inbox, tool) {
			t.Fatalf("inbox turn was offered %s", tool)
		}
	}
	if !strings.Contains(inbox, `"message_agent"`) {
		t.Fatal("inbox turn cannot reply to the sender")
	}
	if !strings.Contains(inbox, "web-01 磁盘 91%") {
		t.Fatal("inbox turn did not see the note")
	}
}

func TestMessageAgentLimits(t *testing.T) {
	mem, box := mailboxFixture(t, "http://model.test/v1")
	agent := &Agent{Store: mem, Box: box}
	ctx := context.Background()
	speaker, _ := mem.GetBot(ctx, "main")
	tr := &trace{agent: agent, ctx: ctx}

	if got := agent.sendMessage(ctx, speaker, "", messageCall("主控", "hi"), tr); !strings.Contains(got, "自己") {
		t.Fatalf("self message = %q", got)
	}
	if got := agent.sendMessage(ctx, speaker, "", messageCall("巡检", "  "), tr); !strings.Contains(got, "为空") {
		t.Fatalf("empty message = %q", got)
	}
	excluded := context.WithValue(ctx, optionsKey{}, Options{ExcludedBotIDs: []string{"check"}})
	if got := agent.sendMessage(excluded, speaker, "", messageCall("巡检", "hi"), tr); !strings.Contains(got, "排除") {
		t.Fatalf("excluded message = %q", got)
	}
	if agent.canMessage(excluded, speaker) {
		t.Fatal("message_agent offered with no reachable peer")
	}
	deep := context.WithValue(ctx, hopKey{}, maxHops)
	if agent.canMessage(deep, speaker) {
		t.Fatal("message_agent offered past the hop limit")
	}
	if got := agent.sendMessage(deep, speaker, "", messageCall("巡检", "hi"), tr); !strings.Contains(got, "太多次") {
		t.Fatalf("hop limit message = %q", got)
	}
	for i := 0; i < maxSendsPerTurn; i++ {
		if got := agent.sendMessage(ctx, speaker, "", messageCall("巡检", "hi"), tr); !strings.Contains(got, "已送达") {
			t.Fatalf("send %d = %q", i+1, got)
		}
	}
	if got := agent.sendMessage(ctx, speaker, "", messageCall("巡检", "hi"), tr); !strings.Contains(got, "用完") {
		t.Fatalf("send past turn limit = %q", got)
	}
	inbox := usageContext(ctx, "main", "inbox", "")
	if got := agent.sendMessage(inbox, speaker, "", messageCall("巡检", "hi"), &trace{agent: agent, ctx: inbox, sent: maxSendsPerInbox}); !strings.Contains(got, "用完") {
		t.Fatalf("inbox send past limit = %q", got)
	}
	items, _ := mem.ListMessages(ctx, inboxSession("check"))
	if len(items) != maxSendsPerTurn {
		t.Fatalf("delivered %d messages", len(items))
	}
	if !IsInboxSession(inboxSession("check")) || IsInboxSession("chat-1") {
		t.Fatal("IsInboxSession")
	}
}
