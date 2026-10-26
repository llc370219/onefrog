package bot

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/store"
)

func TestParseChoice(t *testing.T) {
	c, err := ParseChoice(`{"prompt":"清理哪个目录？\n","options":[{"label":"<b>/tmp</b>","value":"tmp"},{"label":"/var/log"}],"multi_select":true,"allow_custom":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Prompt != "清理哪个目录？" || !c.MultiSelect || !c.AllowCustom || len(c.Options) != 2 {
		t.Fatalf("parsed %+v", c)
	}
	if c.Options[0].Label != "<b>/tmp</b>" || c.Options[1].Value != "/var/log" {
		t.Fatalf("labels must stay plain text and value defaults to label: %+v", c.Options)
	}
	long := strings.Repeat("长", 80)
	c, err = ParseChoice(`{"prompt":"x","options":[{"label":"` + long + `"},{"label":"b"}]}`)
	if err != nil || len([]rune(c.Options[0].Label)) != maxChoiceLabel {
		t.Fatalf("long label not clipped: %v %d", err, len([]rune(c.Options[0].Label)))
	}
	for name, args := range map[string]string{
		"not json":   `{`,
		"no prompt":  `{"prompt":" ","options":[{"label":"a"},{"label":"b"}]}`,
		"one option": `{"prompt":"x","options":[{"label":"a"}]}`,
		"seven":      `{"prompt":"x","options":[{"label":"1"},{"label":"2"},{"label":"3"},{"label":"4"},{"label":"5"},{"label":"6"},{"label":"7"}]}`,
		"empty":      `{"prompt":"x","options":[{"label":""},{"label":"b"}]}`,
		"duplicate":  `{"prompt":"x","options":[{"label":"a"},{"label":"a"}]}`,
	} {
		if _, err := ParseChoice(args); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestNormalizeAnswer(t *testing.T) {
	c := model.Choice{Prompt: "p", Options: []model.ChoiceOption{{Label: "A", Value: "a"}, {Label: "B", Value: "b"}}}
	if _, err := normalizeAnswer(c, ChoiceReply{Values: []string{"a", "b"}}); err == nil {
		t.Fatal("single-select accepted two values")
	}
	if _, err := normalizeAnswer(c, ChoiceReply{Values: []string{"zzz"}}); err == nil {
		t.Fatal("unknown value accepted")
	}
	if _, err := normalizeAnswer(c, ChoiceReply{Custom: "别的"}); err == nil {
		t.Fatal("custom text accepted without allow_custom")
	}
	if _, err := normalizeAnswer(c, ChoiceReply{}); err == nil {
		t.Fatal("empty answer accepted")
	}
	c.MultiSelect, c.AllowCustom = true, true
	ans, err := normalizeAnswer(c, ChoiceReply{Values: []string{"b", "a"}, Custom: " 先备份 "})
	if err != nil || ans.Values[0] != "a" || ans.Custom != "先备份" {
		t.Fatalf("multi answer %+v %v", ans, err)
	}
	if got := answerText(c, ans); got != "我的选择：A、B\n补充：先备份" {
		t.Fatalf("answer text %q", got)
	}
}

func TestAskUserEndsTurnAndAnswerResumesOnce(t *testing.T) {
	mem := store.NewMemory()
	ctx := context.Background()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		n := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			_, _ = io.WriteString(w, toolJSON("c1", "ask_user", `{"prompt":"磁盘快满了，先清理哪个目录？","options":[{"label":"/tmp","value":"tmp"},{"label":"/var/log","value":"log"}],"allow_custom":true}`))
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"好的，先看 /var/log。"}}]}`)
	}))
	defer srv.Close()
	if _, err := mem.CreateBot(ctx, model.Bot{ID: "main", Name: "主控", BaseURL: srv.URL + "/v1", ModelName: "demo", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Store: mem, HTTP: srv.Client()}
	first, err := agent.Reply(ctx, "", "", "", "磁盘快满了")
	if err != nil {
		t.Fatal(err)
	}
	if first.Choice == nil || first.MessageID == "" || len(first.Choice.Options) != 2 {
		t.Fatalf("no card returned: %+v", first)
	}
	mu.Lock()
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"ask_user"`) {
		t.Fatalf("turn did not stop after ask_user or tool missing: %d", len(bodies))
	}
	mu.Unlock()
	msgs, _ := mem.ListMessages(ctx, first.SessionID)
	if last := msgs[len(msgs)-1]; last.Choice == nil || last.ID != first.MessageID {
		t.Fatalf("card not stored: %+v", msgs)
	}

	answer := Options{ChoiceAnswer: &ChoiceReply{MessageID: first.MessageID, Values: []string{"log"}}}
	second, err := agent.ReplyWithOptions(ctx, "", "", first.SessionID, "", answer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Answered || !strings.Contains(second.Text, "/var/log") {
		t.Fatalf("answer did not resume the turn: %+v", second)
	}
	mu.Lock()
	if len(bodies) != 2 || !strings.Contains(bodies[1], "我的选择：/var/log") || !strings.Contains(bodies[1], "已用选项卡片询问用户") {
		t.Fatalf("model did not see the card and answer: %d", len(bodies))
	}
	mu.Unlock()

	again, err := agent.ReplyWithOptions(ctx, "", "", first.SessionID, "", answer, nil)
	if err != nil || !again.Answered {
		t.Fatalf("same answer should be idempotent: %+v %v", again, err)
	}
	other := Options{ChoiceAnswer: &ChoiceReply{MessageID: first.MessageID, Values: []string{"tmp"}}}
	if _, err = agent.ReplyWithOptions(ctx, "", "", first.SessionID, "", other, nil); !errors.Is(err, ErrChoiceAnswered) {
		t.Fatalf("different second answer: %v", err)
	}
	mu.Lock()
	if len(bodies) != 2 {
		t.Fatalf("repeat answers started new turns: %d", len(bodies))
	}
	mu.Unlock()
	msgs, _ = mem.ListMessages(ctx, first.SessionID)
	users := 0
	for _, m := range msgs {
		if m.Role == "user" {
			users++
		}
	}
	if users != 2 {
		t.Fatalf("user messages = %d", users)
	}

	// A card the operator typed past can no longer be answered.
	mu.Lock()
	bodies = nil
	mu.Unlock()
	card, err := agent.Reply(ctx, "", "", "", "再问一次")
	if err != nil || card.Choice == nil {
		t.Fatalf("second card: %+v %v", card, err)
	}
	if _, err = agent.Reply(ctx, "", "", card.SessionID, "算了，先别清理"); err != nil {
		t.Fatal(err)
	}
	stale := Options{ChoiceAnswer: &ChoiceReply{MessageID: card.MessageID, Values: []string{"tmp"}}}
	if _, err = agent.ReplyWithOptions(ctx, "", "", card.SessionID, "", stale, nil); !errors.Is(err, ErrChoiceStale) {
		t.Fatalf("stale card answered: %v", err)
	}
}

func TestWorkerCannotAskUser(t *testing.T) {
	tr := &trace{}
	agent := &Agent{Store: store.NewMemory()}
	ctx := context.WithValue(context.Background(), usageKey{}, usageActor{Purpose: "worker"})
	var call toolCall
	call.Function.Name = "ask_user"
	call.Function.Arguments = `{"prompt":"x","options":[{"label":"a"},{"label":"b"}]}`
	out := agent.execCall(ctx, model.Bot{ID: "w"}, credentials{}, "", call, false, tr)
	if tr.choice != nil || !strings.Contains(out, "不能直接向用户提问") {
		t.Fatalf("worker asked the user: %q", out)
	}
}
