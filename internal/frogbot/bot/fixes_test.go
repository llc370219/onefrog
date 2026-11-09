package bot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onefrog/internal/frogbot/onefrog"
)

// askAI：用户说 "ask:<bot_id>" 时聊天层调用 ask_agent；收到别的 Bot 的提问时直接用 message_agent 回复。
type askAI struct{}

func (askAI) Chat(ctx context.Context, req onefrog.ChatRequest, onText func(string)) (onefrog.Message, error) {
	last := req.Messages[len(req.Messages)-1]
	reply := onefrog.Message{Role: "assistant"}
	switch {
	case last.Role == "tool":
		reply.Content = "已经去问了，有回复再告诉你。"
	case strings.Contains(last.Content, "ask:"):
		to := strings.TrimSpace(last.Content[strings.Index(last.Content, "ask:")+4:])
		reply = toolCall("ask_agent", map[string]string{"to_bot_id": to, "question": "你在忙什么？"})
	default:
		reply.Content = "好的"
	}
	if reply.Content != "" && onText != nil {
		onText(reply.Content)
	}
	reply.Role = "assistant"
	return reply, nil
}

// 两个 Bot 同时互相 ask_agent：都不应在聊天回合里干等（以前会各自等满 75 秒超时）。
func TestMutualAskAgentDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	ai := askAI{}
	cfg := Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws"),
		ChatProvider: ai, ChatModel: "chat", AgentProvider: ai, AgentModel: "agent", TurnTimeout: 60 * time.Second}
	bs, err := OpenBots(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	peer, err := bs.setProfile("", BotProfile{Name: "Peer"})
	if err != nil {
		t.Fatal(err)
	}
	a, b := bs.runtimes[defaultBotID], bs.runtimes[peer.ID]
	start := time.Now()
	if _, err := a.Send("ask:" + b.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send("ask:" + a.ID()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both turns answered without waiting", func() bool {
		return countMsgs(a.store, "assistant", "已经去问了") == 1 && countMsgs(b.store, "assistant", "已经去问了") == 1
	})
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("ask_agent blocked the chat turn for %v", d)
	}
	// 双方都收到了对方的提问（持久收件箱），并且被唤醒处理了
	waitFor(t, "each Bot received the other's question", func() bool {
		ra, rb := 0, 0
		for _, m := range a.store.Inbox(true) {
			if m.FromBotID == b.ID() {
				ra++
			}
		}
		for _, m := range b.store.Inbox(true) {
			if m.FromBotID == a.ID() {
				rb++
			}
		}
		return ra == 1 && rb == 1
	})
}

// 所有 Bot 合计的并发上限：3 个 Bot 各派 2 个任务，上限 2 时同时只有 2 个在跑；取消后其余的自动开始。
func TestGlobalConcurrencyAcrossBots(t *testing.T) {
	dir := t.TempDir()
	ai := &fakeAI{agent: blockingAgent}
	cfg := Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws"),
		ChatProvider: ai, ChatModel: "chat", AgentProvider: ai, AgentModel: "agent", MaxConcurrent: 2}
	bs, err := OpenBots(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	var all []*Bot
	all = append(all, bs.main)
	for _, n := range []string{"B", "C"} {
		p, err := bs.setProfile("", BotProfile{Name: n})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, bs.runtimes[p.ID])
	}
	for _, b := range all {
		for i := 0; i < 2; i++ {
			if _, err := b.StartTask("block", ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	running := func() int {
		n := 0
		for _, b := range all {
			for _, tk := range b.store.Tasks() {
				if tk.Status == "running" {
					n++
				}
			}
		}
		return n
	}
	waitFor(t, "two tasks running", func() bool { return running() == 2 })
	time.Sleep(100 * time.Millisecond)
	if n := running(); n != 2 || bs.main.slots.running() != 2 {
		t.Fatalf("global limit not enforced: %d running", n)
	}
	// 取消正在跑的，其他 Bot 排队的任务应该补上
	for _, b := range all {
		for _, tk := range b.store.Tasks() {
			if tk.Status == "running" {
				_ = b.CancelTask(tk.ID, "user")
			}
		}
	}
	waitFor(t, "queued tasks of other Bots start", func() bool {
		cancelled := 0
		for _, b := range all {
			for _, tk := range b.store.Tasks() {
				if tk.Status == "cancelled" {
					cancelled++
				}
			}
		}
		return cancelled == 2 && running() == 2
	})
}

// 彻底删除：数据目录和工作区都被删掉；普通删除保留（见 TestDeleteBotStopsWorkAndSurvivesRestart）。
func TestPurgeDeleteRemovesData(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws")}
	bs, err := OpenBots(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	p, err := bs.setProfile("", BotProfile{Name: "Temp"})
	if err != nil {
		t.Fatal(err)
	}
	b := bs.runtimes[p.ID]
	if _, err := b.Upload("x.txt", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	dataDir, ws := b.cfg.DataDir, b.cfg.Workspace
	h := bs.Handler(nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/bots/"+p.ID+"/paths", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), p.ID) {
		t.Fatalf("paths: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/bots/"+p.ID+"?purge=1", nil))
	if w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	waitFor(t, "data removed", func() bool {
		_, e1 := os.Stat(dataDir)
		_, e2 := os.Stat(ws)
		return os.IsNotExist(e1) && os.IsNotExist(e2)
	})
	if _, err := os.Stat(bs.main.cfg.DataDir); err != nil {
		t.Fatal("main data dir must survive")
	}
}

// 根路径上的旧 /api/stream 等单 Bot 接口不再提供；只能通过 /api/bots/{id}/…。
func TestRootLegacyAPIRemoved(t *testing.T) {
	dir := t.TempDir()
	bs, err := OpenBots(Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws")})
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	h := bs.Handler(nil)
	for path, want := range map[string]int{"/api/stream": 404, "/api/info": 404, "/api/bots/main/info": 200,
		"/api/settings": 200, "/api/health": 200, "/api/auth/me": 200} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Errorf("GET %s = %d, want %d", path, w.Code, want)
		}
	}
}

func TestLocalOnlyGuard(t *testing.T) {
	h := LocalOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	cases := []struct {
		method, host, origin string
		want                 int
	}{
		{"GET", "127.0.0.1:8080", "", 204},
		{"GET", "localhost:8080", "", 204},
		{"GET", "evil.example:8080", "", 403}, // DNS 重绑定
		{"POST", "127.0.0.1:8080", "http://127.0.0.1:8080", 204},
		{"POST", "127.0.0.1:8080", "https://evil.example", 403},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "/api/x", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s host=%s origin=%s: %d, want %d", c.method, c.host, c.origin, w.Code, c.want)
		}
	}
}

// 事件日志：末尾半行坏数据在打开时被截掉；agent 的流式文字不写盘。
func TestStoreTruncatesPartialLineAndSkipsTextDelta(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Append("message", Message{ID: "m1", Role: "user", Text: "hello"}, false)
	s.Close()
	path := filepath.Join(dir, "stream.jsonl")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"seq":2,"kind":"message","data":{"id":"m2"`) // 断电时写了一半
	f.Close()
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Append("message", Message{ID: "m3", Role: "user", Text: "after crash"}, false)
	s.Close()
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || strings.Contains(string(raw), `"m2"`) || !strings.Contains(lines[1], "after crash") {
		t.Fatalf("partial line not truncated:\n%s", raw)
	}
	s, _ = OpenStore(dir)
	defer s.Close()
	if n := len(s.Messages()); n != 2 {
		t.Fatalf("expected 2 messages after reopen, got %d", n)
	}

	ai := &fakeAI{agent: func(_ context.Context, _ []onefrog.Message) (onefrog.Message, error) {
		return onefrog.Message{Content: "streamed words"}, nil
	}}
	b := newBot(t, t.TempDir(), ai)
	defer b.Close()
	b.Start()
	id, _ := b.StartTask("go", "")
	waitFor(t, "task done", func() bool { rec, _ := b.store.Task(id); return rec.Status == "completed" })
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	for _, e := range b.store.log {
		if e.Kind == "agent" && strings.Contains(string(e.Data), `"text.delta"`) {
			t.Fatal("agent text.delta was persisted")
		}
	}
}

func TestCompactorWiredByDefault(t *testing.T) {
	b := newBot(t, t.TempDir(), &fakeAI{agent: blockingAgent})
	defer b.Close()
	if _, ok := b.mgr.Agent.Compactor.(*onefrog.AutoCompactor); !ok {
		t.Fatal("background agent should compact long conversations by default")
	}
}
