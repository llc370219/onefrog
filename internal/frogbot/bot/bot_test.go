package bot

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"onefrog/internal/frogbot/onefrog"
	"onefrog/internal/frogbot/vault"
)

// fakeAI 按模型名分流：chat = 聊天层，agent = 后台 agent。
type fakeAI struct {
	mu    sync.Mutex
	agent func(ctx context.Context, msgs []onefrog.Message) (onefrog.Message, error)
	calls int
	think string   // 聊天层每轮第一步的思考内容
	reqs  []string // 发给模型的全部请求（用来检查有没有明文泄漏）
}

func (f *fakeAI) Chat(ctx context.Context, req onefrog.ChatRequest, onText func(string)) (onefrog.Message, error) {
	f.mu.Lock()
	f.calls++
	raw, _ := json.Marshal(req)
	f.reqs = append(f.reqs, string(raw))
	think := f.think
	f.mu.Unlock()
	if req.Model == "chat" && think != "" && req.OnReasoning != nil && req.Messages[len(req.Messages)-1].Role != "tool" {
		for _, r := range strings.SplitAfter(think, "，") {
			req.OnReasoning(r)
		}
	}
	var m onefrog.Message
	var err error
	if req.Model == "chat" {
		m = fakeChat(req.Messages)
	} else {
		m, err = f.agent(ctx, req.Messages)
	}
	if err == nil && m.Content != "" && onText != nil {
		onText(m.Content)
	}
	m.Role = "assistant"
	return m, err
}

func toolCall(name string, args any) onefrog.Message {
	b, _ := json.Marshal(args)
	return onefrog.Message{ToolCalls: []onefrog.ToolCall{{ID: "c" + name, Name: name, Args: b}}}
}

var (
	taskIDRe = regexp.MustCompile(`task_[0-9a-f]+`)
	readRe   = regexp.MustCompile(`读一下 (\S+)`)
)

// lastCall 返回最近一次 assistant 工具调用的名字。
func lastCall(ms []onefrog.Message) string {
	for i := len(ms) - 1; i >= 0; i-- {
		if len(ms[i].ToolCalls) > 0 {
			return ms[i].ToolCalls[0].Name
		}
	}
	return ""
}

func fakeChat(ms []onefrog.Message) onefrog.Message {
	last := ms[len(ms)-1]
	if last.Role == "tool" {
		switch lastCall(ms) {
		case "list_tasks":
			if id := taskIDRe.FindString(last.Content); id != "" {
				return toolCall("read_task", map[string]string{"task_id": id})
			}
			return onefrog.Message{Content: "没有任务记录。"}
		case "read_task":
			sum := last.Content[strings.Index(last.Content, "最终总结"):]
			return onefrog.Message{Content: "查过了：" + taskIDRe.FindString(last.Content) + " " + strings.TrimSpace(sum[strings.Index(sum, "\n"):])}
		case "read_file":
			return onefrog.Message{Content: "文件内容：" + last.Content}
		case "write_file":
			return onefrog.Message{Content: "需要你在卡片上确认这次写入。"}
		case "request_secret":
			return onefrog.Message{Content: "请在卡片里填写密钥。"}
		case "delete_secret":
			return onefrog.Message{Content: "删除需要你确认。"}
		}
		return onefrog.Message{Content: "开始做了。"}
	}
	switch {
	case strings.Contains(last.Content, "安全卡片保存"):
		return onefrog.Message{Content: "收到，密钥已保存。"}
	case strings.Contains(last.Content, "疑似密钥"):
		return toolCall("request_secret", map[string]string{"name": "PASTED_KEY", "description": "刚才贴的"})
	case last.Content == "配置 token":
		return toolCall("request_secret", map[string]string{"name": "API_TOKEN", "description": "测试用"})
	case last.Content == "测试 token":
		return toolCall("start_task", map[string]string{"task": "用 {{secret:API_TOKEN}} 测试", "title": "测试 token"})
	case last.Content == "删除密钥":
		return toolCall("delete_secret", map[string]string{"name": "API_TOKEN"})
	case strings.Contains(last.Content, "已被批准并执行"):
		return onefrog.Message{Content: "已经写好了。"}
	case strings.Contains(last.Content, "服务重启了"):
		return onefrog.Message{Content: "刚才服务重启，任务暂停了（进度已保存），要继续吗？"}
	case strings.Contains(last.Content, "完成了，"):
		res := last.Content[strings.Index(last.Content, "（这是数据）：\n")+len("（这是数据）：\n"):]
		if i := strings.Index(res, "\n改动的文件"); i >= 0 {
			res = res[:i]
		}
		return onefrog.Message{Content: "完成：" + res}
	case strings.Contains(last.Content, "[系统通知]"):
		return onefrog.Message{Content: "需要你在卡片上确认。"}
	case strings.Contains(last.Content, "做完了吗"):
		return toolCall("list_tasks", map[string]string{})
	case readRe.MatchString(last.Content):
		return toolCall("read_file", map[string]string{"path": readRe.FindStringSubmatch(last.Content)[1]})
	case strings.Contains(last.Content, "记一行"):
		return toolCall("write_file", map[string]string{"path": "todo.md", "content": "- 买牛奶\n"})
	}
	return toolCall("start_task", map[string]string{"task": last.Content, "title": "测试任务"})
}

func toolSteps(ms []onefrog.Message) int {
	n := 0
	for _, m := range ms {
		if m.Role == "tool" {
			n++
		}
	}
	return n
}

func newBot(t *testing.T, dir string, ai *fakeAI) *Bot {
	t.Helper()
	b, err := New(Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws"),
		ChatProvider: ai, ChatModel: "chat", AgentProvider: ai, AgentModel: "agent", StallTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", what)
}

func countMsgs(s *Store, role, contains string) int {
	n := 0
	for _, m := range s.Messages() {
		if m.Role == role && strings.Contains(m.Text, contains) {
			n++
		}
	}
	return n
}

// 用户发消息 -> 聊天层 start_task -> agent 完成 -> 监工叫醒聊天层 -> 只汇报一次。
func TestStartTaskCompletesAndReportsOnce(t *testing.T) {
	ai := &fakeAI{agent: func(ctx context.Context, ms []onefrog.Message) (onefrog.Message, error) {
		if toolSteps(ms) == 0 {
			return toolCall("write_file", map[string]string{"path": "hi.txt", "content": "hello"}), nil
		}
		return onefrog.Message{Content: "写好了 hi.txt"}, nil
	}}
	dir := t.TempDir()
	b := newBot(t, dir, ai)
	b.Start()
	defer b.Close()
	if _, err := b.Send("帮我写个文件"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "final report", func() bool { return countMsgs(b.store, "assistant", "完成：写好了 hi.txt") == 1 })
	time.Sleep(200 * time.Millisecond) // 给重复汇报一点机会，确认它不会发生
	if n := countMsgs(b.store, "assistant", "完成："); n != 1 {
		t.Errorf("want exactly 1 report, got %d", n)
	}
	if n := countMsgs(b.store, "assistant", "开始做了"); n != 1 {
		t.Errorf("want 1 ack, got %d", n)
	}
	if n := countMsgs(b.store, "notice", "完成了"); n != 1 {
		t.Errorf("want 1 notice, got %d", n)
	}
	tasks := b.store.Tasks()
	if len(tasks) != 1 || tasks[0].Status != "completed" || !tasks[0].Reported {
		t.Fatalf("tasks: %+v", tasks)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "ws", "hi.txt")); string(data) != "hello" {
		t.Errorf("file: %q", data)
	}
	if len(b.store.Unhandled()) != 0 {
		t.Error("all input should be handled")
	}
}

func blockingAgent(ctx context.Context, _ []onefrog.Message) (onefrog.Message, error) {
	<-ctx.Done()
	return onefrog.Message{}, ctx.Err()
}

// "停" 由代码直接处理：停止全部任务，不调用模型，不产生汇报。
func TestStopCancelsAll(t *testing.T) {
	ai := &fakeAI{agent: blockingAgent}
	b := newBot(t, t.TempDir(), ai)
	b.Start()
	defer b.Close()
	id1, _ := b.StartTask("任务一", "")
	id2, _ := b.StartTask("任务二", "")
	waitFor(t, "running", func() bool {
		a, _ := b.store.Task(id1)
		c, _ := b.store.Task(id2)
		return a.Status == "running" && c.Status == "running"
	})
	ai.mu.Lock()
	before := ai.calls
	ai.mu.Unlock()
	if _, err := b.Send("停"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cancelled", func() bool {
		a, _ := b.store.Task(id1)
		c, _ := b.store.Task(id2)
		return a.Status == "cancelled" && c.Status == "cancelled" && a.Reported && c.Reported
	})
	if countMsgs(b.store, "assistant", "已停止 2 个") != 1 {
		t.Errorf("stop reply missing: %+v", b.store.Messages())
	}
	time.Sleep(100 * time.Millisecond)
	if countMsgs(b.store, "notice", "") != 0 {
		t.Error("user-cancelled tasks must not wake the chat layer")
	}
	ai.mu.Lock()
	defer ai.mu.Unlock()
	if ai.calls != before {
		t.Errorf("stop must not call the model (calls %d -> %d)", before, ai.calls)
	}
}

// 审批只能通过 HTTP 接口（用户点按钮）完成，并且绑定到具体参数。
func TestApprovalViaAPI(t *testing.T) {
	ai := &fakeAI{agent: func(ctx context.Context, ms []onefrog.Message) (onefrog.Message, error) {
		if toolSteps(ms) == 0 {
			return toolCall("shell", map[string]string{"command": "rm -r junk && echo cleaned"}), nil
		}
		return onefrog.Message{Content: "清理完成"}, nil
	}}
	dir := t.TempDir()
	b := newBot(t, dir, ai)
	_ = os.MkdirAll(filepath.Join(dir, "ws", "junk"), 0o755)
	b.Start()
	defer b.Close()
	srv := httptest.NewServer(b.Handler(""))
	defer srv.Close()

	if b.chatTool.Get("approve") != nil {
		t.Fatal("chat layer must not have an approve tool")
	}
	id, _ := b.StartTask("清理 junk", "清理")
	var appr ApprovalRec
	waitFor(t, "approval requested", func() bool {
		p := b.store.PendingApprovals()
		if len(p) == 1 {
			appr = p[0]
		}
		return len(p) == 1
	})
	if !strings.Contains(string(appr.Args), "rm -r junk") {
		t.Errorf("approval should show exact command: %s", appr.Args)
	}
	// 聊天层被叫醒解释，但不能批准
	waitFor(t, "chat explains", func() bool { return countMsgs(b.store, "assistant", "需要你在卡片上确认") == 1 })
	if _, err := os.Stat(filepath.Join(dir, "ws", "junk")); err != nil {
		t.Fatal("must not run before approval")
	}
	post := func(path, body string) int {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("/api/approvals/appr_bogus", `{"decision":"once"}`); code != 400 {
		t.Errorf("unknown approval should 400, got %d", code)
	}
	if code := post("/api/approvals/"+appr.ID, `{"decision":"once"}`); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	waitFor(t, "completed", func() bool { tk, _ := b.store.Task(id); return tk.Status == "completed" })
	if _, err := os.Stat(filepath.Join(dir, "ws", "junk")); !os.IsNotExist(err) {
		t.Error("junk should be removed after approval")
	}
	if a, _ := b.store.Approval(appr.ID); a.Status != "allow" {
		t.Errorf("approval status %q", a.Status)
	}
	if code := post("/api/approvals/"+appr.ID, `{"decision":"once"}`); code != 400 {
		t.Errorf("second decision on same approval should fail, got %d", code)
	}
}

// 服务重启：上次在跑的任务标记为中断，待审批作废，聊天层如实告诉用户（只一次）。
func TestRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	s.Append("task.created", TaskRec{ID: "task_old", Title: "旧任务", Status: "queued", CreatedAt: time.Now()}, false)
	s.Append("agent", onefrog.Event{TaskID: "task_old", Type: onefrog.EvTaskStarted}, false)
	s.Append("agent", onefrog.Event{TaskID: "task_old", Type: onefrog.EvApprovalRequested,
		Data: onefrog.ApprovalRequest{ID: "appr_old", TaskID: "task_old", ToolName: "shell", Args: json.RawMessage(`{"command":"x"}`)}}, false)
	s.Close() // 模拟进程直接退出

	ai := &fakeAI{agent: blockingAgent}
	b := newBot(t, dir, ai)
	b.Start()
	tk, _ := b.store.Task("task_old")
	if tk.Status != "paused" || tk.StopReason != "restart" || tk.Reported {
		t.Fatalf("task after restart should be paused & resumable: %+v", tk)
	}
	if a, _ := b.store.Approval("appr_old"); a.Status != "expired" {
		t.Errorf("approval should expire, got %q", a.Status)
	}
	waitFor(t, "honest report", func() bool { return countMsgs(b.store, "assistant", "任务暂停了") == 1 })
	b.Close()

	// 再重启一次：已经汇报过，不会重复汇报
	b2 := newBot(t, dir, ai)
	b2.Start()
	time.Sleep(150 * time.Millisecond)
	b2.Close()
	if n := countMsgs(b2.store, "assistant", "任务暂停了"); n != 1 {
		t.Errorf("report should not repeat after second restart, got %d", n)
	}
}

// SSE：带 Last-Event-ID 重连只补发之后的事件，并且不发内部事件。
func TestStreamReplay(t *testing.T) {
	b := newBot(t, t.TempDir(), &fakeAI{agent: blockingAgent})
	defer b.Close()
	b.say("一")
	b.notify("内部通知")
	e := b.store.Append("message", Message{ID: "m2", Role: "assistant", Text: "二"}, false)
	b.say("三")
	srv := httptest.NewServer(b.Handler(""))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/stream", nil)
	req.Header.Set("Last-Event-ID", "2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data: ") {
			var ev Event
			_ = json.Unmarshal([]byte(line[6:]), &ev)
			if ev.Kind == "replay.done" {
				break
			}
			var m Message
			_ = json.Unmarshal(ev.Data, &m)
			got = append(got, m.Text)
		}
	}
	if strings.Join(got, ",") != "二,三" || e.Seq != 3 {
		t.Errorf("replay got %v", got)
	}
}

func TestIsStop(t *testing.T) {
	for _, s := range []string{"停", "停止", "stop", "Stop!", "别做了。"} {
		if !IsStop(s) {
			t.Errorf("%q should stop", s)
		}
	}
	for _, s := range []string{"停车场在哪", "don't stop", "取消订阅邮件"} {
		if IsStop(s) {
			t.Errorf("%q should not stop", s)
		}
	}
}

// helper：跑一个会写 hi.txt 的任务直到汇报完成
func writeHiAgent(ctx context.Context, ms []onefrog.Message) (onefrog.Message, error) {
	if toolSteps(ms) == 0 {
		return toolCall("write_file", map[string]string{"path": "hi.txt", "content": "hello"}), nil
	}
	return onefrog.Message{Content: "写好了 hi.txt，内容是 hello。"}, nil
}

func lastAssistant(s *Store) Message {
	ms := s.Messages()
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].Role == "assistant" {
			return ms[i]
		}
	}
	return Message{}
}

// 问"做完了吗"：聊天层用 list_tasks + read_task 自己查，不派新任务。
func TestChatAnswersTaskDoneFromSummary(t *testing.T) {
	b := newBot(t, t.TempDir(), &fakeAI{agent: writeHiAgent})
	b.Start()
	defer b.Close()
	id, _ := b.StartTask("写 hi.txt", "写文件")
	waitFor(t, "reported", func() bool { return countMsgs(b.store, "assistant", "完成：") == 1 })
	if _, err := b.Send("hi.txt 那个任务做完了吗"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "answer", func() bool { return countMsgs(b.store, "assistant", "查过了：") == 1 })
	m := lastAssistant(b.store)
	if !strings.Contains(m.Text, id) || !strings.Contains(m.Text, "写好了 hi.txt") {
		t.Errorf("answer should cite task id and summary: %q", m.Text)
	}
	if strings.Join(m.Tools, ",") != "查看了任务列表,查看了任务 "+id {
		t.Errorf("tool chips: %v", m.Tools)
	}
	if n := len(b.store.Tasks()); n != 1 {
		t.Errorf("must not start a new task, have %d", n)
	}
}

func TestChatReadsFileAndPathEscapeDenied(t *testing.T) {
	dir := t.TempDir()
	b := newBot(t, dir, &fakeAI{agent: blockingAgent})
	_ = os.WriteFile(filepath.Join(dir, "ws", "notes.md"), []byte("# 笔记\n明天交报告"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("TOPSECRET"), 0o644)
	b.Start()
	defer b.Close()
	b.Send("读一下 notes.md")
	waitFor(t, "file answer", func() bool { return countMsgs(b.store, "assistant", "明天交报告") == 1 })
	b.Send("读一下 ../secret.txt")
	waitFor(t, "escape answer", func() bool { return countMsgs(b.store, "assistant", "超出了工作区") == 1 })
	if countMsgs(b.store, "assistant", "TOPSECRET") != 0 {
		t.Fatal("must not leak files outside workspace")
	}
	if len(b.store.Tasks()) != 0 {
		t.Error("reading should not start tasks")
	}
	// 写也一样：越界直接拒绝，不弹审批
	out, _ := b.chatWrite(fileTools.Get("write_file"), json.RawMessage(`{"path":"../x.txt","content":"x"}`))
	if !strings.HasPrefix(out, "DENIED") || len(b.store.PendingApprovals()) != 0 {
		t.Errorf("escape write: %q", out)
	}
}

// 聊天层写文件：弹审批卡片，只有通过 API 批准后才执行。
func TestChatWriteNeedsApproval(t *testing.T) {
	dir := t.TempDir()
	b := newBot(t, dir, &fakeAI{agent: blockingAgent})
	b.Start()
	defer b.Close()
	srv := httptest.NewServer(b.Handler(""))
	defer srv.Close()
	b.Send("在 todo.md 里记一行：买牛奶")
	var appr ApprovalRec
	waitFor(t, "approval", func() bool {
		p := b.store.PendingApprovals()
		if len(p) == 1 {
			appr = p[0]
		}
		return len(p) == 1
	})
	waitFor(t, "ack", func() bool { return countMsgs(b.store, "assistant", "需要你在卡片上确认这次写入") == 1 })
	if !strings.HasPrefix(appr.TaskID, "chat_") || appr.Tool != "write_file" {
		t.Errorf("approval: %+v", appr)
	}
	todo := filepath.Join(dir, "ws", "todo.md")
	if _, err := os.Stat(todo); err == nil {
		t.Fatal("must not write before approval")
	}
	resp, err := http.Post(srv.URL+"/api/approvals/"+appr.ID, "application/json", strings.NewReader(`{"decision":"once"}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("approve: %v %v", err, resp)
	}
	waitFor(t, "written", func() bool { d, _ := os.ReadFile(todo); return string(d) == "- 买牛奶\n" })
	waitFor(t, "reported", func() bool { return countMsgs(b.store, "assistant", "已经写好了") == 1 })
	if len(b.store.Tasks()) != 0 {
		t.Error("small write should not start a task")
	}
}

// 任务总结和结构化结果写进事件日志，重启后还能读到。
func TestSummariesSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	ai := &fakeAI{agent: writeHiAgent}
	b := newBot(t, dir, ai)
	b.Start()
	id, _ := b.StartTask("写 hi.txt", "写文件")
	waitFor(t, "reported", func() bool { tk, _ := b.store.Task(id); return tk.Reported && tk.Summary != "" })
	b.Close()

	b2 := newBot(t, dir, ai)
	defer b2.Close()
	tk, _ := b2.store.Task(id)
	if tk.Summary != "写好了 hi.txt，内容是 hello。" || strings.Join(tk.Files, ",") != "hi.txt" || len(tk.StepLog) != 1 || tk.StepLog[0].Result != "ok" {
		t.Fatalf("after restart: %+v", tk)
	}
	out, err := b2.readTask(id[len(id)-6:]) // 末尾几位也能找到
	if err != nil || !strings.Contains(out, "写好了 hi.txt") || !strings.Contains(out, "改动的文件：hi.txt") || !strings.Contains(out, "1. write_file hi.txt → ok") {
		t.Errorf("read_task: %v\n%s", err, out)
	}
	if l := b2.listTasks("completed", 0); !strings.Contains(l, id) || !strings.Contains(l, "写好了 hi.txt") {
		t.Errorf("list_tasks: %s", l)
	}
	if h := b2.searchHistory("HI.TXT", 0); !strings.Contains(h, id) {
		t.Errorf("search_history: %s", h)
	}
}

func sseDump(t *testing.T, url string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url+"/api/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		sb.WriteString(sc.Text() + "\n")
		if strings.Contains(sc.Text(), `"replay.done"`) {
			break
		}
	}
	return sb.String()
}

// 密钥全流程：卡片保存 → 后台任务引用 → 必须审批 → 执行时注入 → 输出脱敏；
// 最后检查 stream.jsonl / SSE / 发给模型的请求里都没有明文（包括 base64）。
func TestSecretFullFlowNoPlaintext(t *testing.T) {
	const secret = "tok_Pl41nT3xt-VALUE-9f8e7d6c5b4a"
	var toolOut string
	var mu sync.Mutex
	ai := &fakeAI{agent: func(ctx context.Context, ms []onefrog.Message) (onefrog.Message, error) {
		if toolSteps(ms) == 0 {
			return toolCall("shell", map[string]string{"command": "echo {{secret:API_TOKEN}}; printf %s {{secret:API_TOKEN}} | base64; printf 'u:%s' {{secret:API_TOKEN}} | base64"}), nil
		}
		last := ms[len(ms)-1].Content
		mu.Lock()
		toolOut = last
		mu.Unlock()
		return onefrog.Message{Content: "测试结果：" + last}, nil
	}}
	dir := t.TempDir()
	b := newBot(t, dir, ai)
	b.Start()
	defer b.Close()
	srv := httptest.NewServer(b.Handler(""))
	defer srv.Close()
	post := func(path, body string) int {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// 1) 聊天层请求密钥 → 卡片
	_, _ = b.Send("配置 token")
	var card SecretReq
	waitFor(t, "secret card", func() bool {
		c := b.store.PendingSecretCards()
		if len(c) == 1 {
			card = c[0]
		}
		return len(c) == 1
	})
	if card.Name != "API_TOKEN" {
		t.Fatalf("card %+v", card)
	}
	// 2) 用户在卡片里保存（值只走这个接口）
	if code := post("/api/secrets", `{"request_id":"`+card.ID+`","value":"`+secret+`"}`); code != 200 {
		t.Fatalf("save: %d", code)
	}
	if code := post("/api/secrets", `{"request_id":"`+card.ID+`","value":"again"}`); code != 400 {
		t.Errorf("second save on same card should fail: %d", code)
	}
	waitFor(t, "chat woken after save", func() bool { return countMsgs(b.store, "assistant", "收到，密钥已保存") == 1 })
	if c, _ := b.store.SecretCard(card.ID); c.Status != "saved" {
		t.Errorf("card status %q", c.Status)
	}
	// 3) 派任务使用密钥 → 必须审批（默认 on-request 下 echo 本来是只读、会自动放行）
	_, _ = b.Send("测试 token")
	var appr ApprovalRec
	waitFor(t, "approval for secret use", func() bool {
		p := b.store.PendingApprovals()
		if len(p) == 1 {
			appr = p[0]
		}
		return len(p) == 1
	})
	if !strings.Contains(string(appr.Args), "{{secret:API_TOKEN}}") || strings.Contains(string(appr.Args), secret) {
		t.Errorf("approval must show placeholder only: %s", appr.Args)
	}
	if !strings.Contains(appr.Reason, "API_TOKEN") {
		t.Errorf("reason: %s", appr.Reason)
	}
	if code := post("/api/approvals/"+appr.ID, `{"decision":"session"}`); code != 200 {
		t.Fatal("approve")
	}
	waitFor(t, "task completed", func() bool {
		for _, tk := range b.store.Tasks() {
			if tk.Title == "测试 token" && tk.Status == "completed" {
				return true
			}
		}
		return false
	})
	mu.Lock()
	out := toolOut
	mu.Unlock()
	if !strings.Contains(out, "[secret:API_TOKEN]") || strings.Count(out, "[secret:API_TOKEN]") < 3 {
		t.Errorf("tool output should be redacted 3 times (plain, base64, basic-auth style): %q", out)
	}
	waitFor(t, "chat reported", func() bool { return countMsgs(b.store, "assistant", "完成") >= 1 })

	// 4) 用户在聊天里直接贴密钥 → 自动隐藏 + 发卡片
	_, _ = b.Send("我的 key 是 sk-abcdefghijklmnopqrstuvwxyz123456")
	waitFor(t, "pasted key card", func() bool {
		for _, c := range b.store.PendingSecretCards() {
			if c.Name == "PASTED_KEY" {
				return true
			}
		}
		return false
	})
	// 5) 删除密钥需要审批
	_, _ = b.Send("删除密钥")
	waitFor(t, "delete approval", func() bool {
		for _, a := range b.store.PendingApprovals() {
			if a.Tool == "delete_secret" {
				return true
			}
		}
		return false
	})
	if !b.vault.Has("API_TOKEN") {
		t.Fatal("must not delete before approval")
	}

	// 6) 检查所有出口
	forms := vault.Variants(secret)
	check := func(where, s string) {
		for _, f := range forms {
			if strings.Contains(s, f) {
				t.Errorf("%s contains secret form %q", where, f)
			}
		}
		if strings.Contains(s, "sk-abcdefghijklmnop") {
			t.Errorf("%s contains pasted key", where)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "data", "stream.jsonl"))
	check("stream.jsonl", string(raw))
	sec, _ := os.ReadFile(filepath.Join(dir, "data", "secrets.json"))
	check("secrets.json", string(sec))
	check("SSE", sseDump(t, srv.URL))
	ai.mu.Lock()
	check("model requests", strings.Join(ai.reqs, "\n"))
	ai.mu.Unlock()
	if !strings.Contains(string(raw), "[secret:API_TOKEN]") {
		t.Error("expected redacted marker in stream")
	}
	resp, _ := http.Get(srv.URL + "/api/secrets")
	var list []vault.Info
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 || list[0].Name != "API_TOKEN" || list[0].LastUsed == nil {
		t.Errorf("list %+v", list)
	}
}

func TestSecretCardCancel(t *testing.T) {
	b := newBot(t, t.TempDir(), &fakeAI{agent: blockingAgent})
	b.Start()
	defer b.Close()
	_, _ = b.Send("配置 token")
	waitFor(t, "card", func() bool { return len(b.store.PendingSecretCards()) == 1 })
	c := b.store.PendingSecretCards()[0]
	if err := b.CancelSecret(c.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.store.SecretCard(c.ID); got.Status != "cancelled" {
		t.Errorf("status %q", got.Status)
	}
	if b.vault.Has("API_TOKEN") {
		t.Error("nothing should be saved")
	}
}

// 聊天层思考：流式推送 chat.thinking，回复里保存思考和用时，但下一轮不会喂回模型。
func TestThinkingStreamed(t *testing.T) {
	ai := &fakeAI{agent: blockingAgent, think: "用户在问任务，先查一下，再回答。"}
	b := newBot(t, t.TempDir(), ai)
	ch, unsub := b.store.Subscribe()
	defer unsub()
	b.Start()
	defer b.Close()
	_, _ = b.Send("做完了吗")
	var streamed strings.Builder
	sawDelta := false
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case e := <-ch:
			switch e.Kind {
			case "chat.thinking":
				if sawDelta {
					t.Error("thinking after answer started")
				}
				var d map[string]string
				_ = json.Unmarshal(e.Data, &d)
				streamed.WriteString(d["text"])
			case "chat.delta":
				sawDelta = true
			case "message":
				var m Message
				_ = json.Unmarshal(e.Data, &m)
				if m.Role == "assistant" {
					if m.Thinking != ai.think || m.ThinkingMs < 0 {
						t.Errorf("persisted thinking %q %d", m.Thinking, m.ThinkingMs)
					}
					break loop
				}
			}
		case <-deadline:
			t.Fatal("timeout")
		}
	}
	if streamed.String() != ai.think {
		t.Errorf("streamed %q", streamed.String())
	}
	n := len(ai.reqs)
	_, _ = b.Send("做完了吗")
	waitFor(t, "second reply", func() bool { return countMsgs(b.store, "assistant", "没有任务记录") == 2 })
	ai.mu.Lock()
	defer ai.mu.Unlock()
	for _, r := range ai.reqs[n:] {
		if strings.Contains(r, "先查一下") {
			t.Error("previous thinking was fed back into model history")
		}
	}
}
