package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"onefrog/internal/frogbot/onefrog"
)

func newBotWith(t *testing.T, dir string, ai *fakeAI, f func(*Config)) *Bot {
	t.Helper()
	cfg := Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws"),
		ChatProvider: ai, ChatModel: "chat", AgentProvider: ai, AgentModel: "agent", StallTimeout: time.Minute}
	if f != nil {
		f(&cfg)
	}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func firstUser(ms []onefrog.Message) string {
	for _, m := range ms {
		if m.Role == "user" {
			return m.Content
		}
	}
	return ""
}

func summaries(t *testing.T, dir, id string) int {
	raw, _ := os.ReadFile(filepath.Join(dir, "data", "stream.jsonl"))
	n := 0
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.Contains(l, `"kind":"task.summary"`) && strings.Contains(l, id) {
			n++
		}
	}
	return n
}

// 排队顺序 + 并发上限 + 调整顺序 + 每个任务只汇报一次。
func TestQueueOrderingConcurrencyAndReportOnce(t *testing.T) {
	var cur, peak atomic.Int32
	var mu sync.Mutex
	release := map[string]chan struct{}{}
	var order []string
	gate := func(name string) chan struct{} {
		mu.Lock()
		defer mu.Unlock()
		if release[name] == nil {
			release[name] = make(chan struct{})
		}
		return release[name]
	}
	ai := &fakeAI{agent: func(ctx context.Context, ms []onefrog.Message) (onefrog.Message, error) {
		name := firstUser(ms)
		n := cur.Add(1)
		defer cur.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
		select {
		case <-gate(name):
			return onefrog.Message{Content: "做完了 " + name}, nil
		case <-ctx.Done():
			return onefrog.Message{}, ctx.Err()
		}
	}}
	dir := t.TempDir()
	b := newBotWith(t, dir, ai, func(c *Config) { c.MaxConcurrent = 2 })
	b.Start()
	defer b.Close()
	ids := map[string]string{}
	for _, n := range []string{"A", "B", "C", "D"} {
		id, err := b.StartTask(n, n)
		if err != nil {
			t.Fatal(err)
		}
		ids[n] = id
	}
	waitFor(t, "A,B running", func() bool { return cur.Load() == 2 })
	if p := b.QueuePosition(ids["C"]); p != 1 {
		t.Fatalf("C should be 1st in queue, got %d", p)
	}
	if p := b.QueuePosition(ids["D"]); p != 2 {
		t.Fatalf("D should be 2nd in queue, got %d", p)
	}
	if _, err := b.Reprioritize(ids["D"], 1); err != nil {
		t.Fatal(err)
	}
	if b.QueuePosition(ids["D"]) != 1 || b.QueuePosition(ids["C"]) != 2 {
		t.Fatal("reprioritize did not move D to the front")
	}
	close(gate("A"))
	waitFor(t, "D starts after A", func() bool { mu.Lock(); defer mu.Unlock(); return len(order) == 3 })
	mu.Lock()
	if order[2] != "D" {
		t.Errorf("expected D to start next, order=%v", order)
	}
	mu.Unlock()
	close(gate("B"))
	close(gate("C"))
	close(gate("D"))
	waitFor(t, "all done", func() bool {
		for _, id := range ids {
			if tk, _ := b.store.Task(id); tk.Status != "completed" {
				return false
			}
		}
		return true
	})
	if peak.Load() > 2 {
		t.Errorf("concurrency limit exceeded: peak %d", peak.Load())
	}
	time.Sleep(200 * time.Millisecond)
	for n, id := range ids {
		if c := summaries(t, dir, id); c != 1 {
			t.Errorf("task %s summarized %d times", n, c)
		}
		if c := countMsgs(b.store, "notice", id+") 完成了"); c != 1 { // 叫醒聊天层汇报的通知：每个任务恰好一次
			t.Errorf("task %s reported %d times", n, c)
		}
	}
}

// 暂停：正在写文件的那一步一定做完，然后停在安全点；恢复后从保存的进度继续（不会重做第一步），只汇报一次。
func TestPauseResumeAtSafePoint(t *testing.T) {
	var block atomic.Bool
	block.Store(true)
	var writes atomic.Int32
	entered := make(chan struct{}, 4)
	ai := &fakeAI{agent: func(ctx context.Context, ms []onefrog.Message) (onefrog.Message, error) {
		switch toolSteps(ms) {
		case 0:
			writes.Add(1)
			return toolCall("write_file", map[string]string{"path": "a.txt", "content": "one"}), nil
		default:
			if block.Load() {
				entered <- struct{}{}
				<-ctx.Done() // 模拟模型想了很久
				return onefrog.Message{}, ctx.Err()
			}
			if !strings.Contains(ms[len(ms)-1].Content, "暂停") {
				return onefrog.Message{}, nil
			}
			return onefrog.Message{Content: "全部完成"}, nil
		}
	}}
	dir := t.TempDir()
	b := newBot(t, dir, ai)
	b.Start()
	defer b.Close()
	id, _ := b.StartTask("写 a.txt", "写文件")
	<-entered
	msg, err := b.PauseTask(id, "user")
	if err != nil || !strings.Contains(msg, "暂停") {
		t.Fatalf("pause: %q %v", msg, err)
	}
	waitFor(t, "paused", func() bool { tk, _ := b.store.Task(id); return tk.Status == "paused" })
	if b, _ := os.ReadFile(filepath.Join(dir, "ws", "a.txt")); string(b) != "one" {
		t.Fatal("write before the pause must have completed")
	}
	if _, ok := b.loadCheckpoint(id); !ok {
		t.Fatal("checkpoint should be saved")
	}
	if b.running() != 0 {
		t.Fatal("paused task must free its slot")
	}
	block.Store(false)
	if _, err := b.ResumeTask(id, "顺便检查一下"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "completed", func() bool { tk, _ := b.store.Task(id); return tk.Status == "completed" })
	if writes.Load() != 1 {
		t.Errorf("first step should not be redone, writes=%d", writes.Load())
	}
	time.Sleep(150 * time.Millisecond)
	if c := summaries(t, dir, id); c != 1 {
		t.Errorf("summaries %d", c)
	}
	if _, ok := b.loadCheckpoint(id); ok {
		t.Error("checkpoint should be removed after completion")
	}
}

// 重启后：运行中的任务变成"暂停（重启）"，恢复时从磁盘上的进度快照继续。
func TestResumeAfterRestart(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(filepath.Join(dir, "data"))
	s.Append("task.created", TaskRec{ID: "task_r", Title: "长任务", Input: "整理文档", Status: "queued", CreatedAt: time.Now()}, false)
	s.Append("agent", onefrog.Event{TaskID: "task_r", Type: onefrog.EvTaskStarted}, false)
	s.Close()
	cp := checkpoint{Steps: 1, Messages: []onefrog.Message{
		{Role: "user", Content: "整理文档"},
		toolCall("list_dir", map[string]string{"path": "."}),
		{Role: "tool", ToolCallID: "clist_dir", Content: "PREVIOUS_RESULT"},
	}}
	raw, _ := json.Marshal(cp)
	_ = os.MkdirAll(filepath.Join(dir, "data", "checkpoints"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "data", "checkpoints", "task_r.json"), raw, 0o600)

	var saw atomic.Bool
	ai := &fakeAI{agent: func(ctx context.Context, ms []onefrog.Message) (onefrog.Message, error) {
		for _, m := range ms {
			if m.Content == "PREVIOUS_RESULT" {
				saw.Store(true)
			}
		}
		return onefrog.Message{Content: "整理完了"}, nil
	}}
	b := newBot(t, dir, ai)
	b.Start()
	defer b.Close()
	tk, _ := b.store.Task("task_r")
	if tk.Status != "paused" || tk.StopReason != "restart" {
		t.Fatalf("after restart: %+v", tk)
	}
	if _, err := b.TaskAction("task_r", "resume"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "completed", func() bool { tk, _ := b.store.Task("task_r"); return tk.Status == "completed" })
	if !saw.Load() {
		t.Error("resume should continue from the saved checkpoint")
	}
	waitFor(t, "reported", func() bool { return countMsgs(b.store, "notice", "task_r) 完成了") == 1 })
	time.Sleep(150 * time.Millisecond)
	if c := countMsgs(b.store, "notice", "task_r) 完成了"); c != 1 {
		t.Errorf("reported %d times", c)
	}
}

// 新的聊天层工具：queue_task(hold) / list_tasks / resume_task / pause_task / reprioritize / react。
func TestQueueToolsAndStopAll(t *testing.T) {
	dir := t.TempDir()
	b := newBotWith(t, dir, &fakeAI{agent: blockingAgent}, func(c *Config) { c.MaxConcurrent = 1 })
	b.Start()
	defer b.Close()
	run := func(name string, args any) string {
		t.Helper()
		raw, _ := json.Marshal(args)
		out, err := b.chatTool.Get(name).Execute(context.Background(), onefrog.ToolEnv{}, raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	out := run("queue_task", map[string]any{"task": "以后整理照片", "title": "整理照片", "hold": true})
	id := taskIDRe.FindString(out)
	if tk, _ := b.store.Task(id); tk.Status != "paused" || tk.StopReason != "held" || b.isLaunched(id) {
		t.Fatalf("held task should be a parked to-do: %+v", tk)
	}
	if l := run("list_tasks", map[string]any{"status": "paused"}); !strings.Contains(l, "待办") {
		t.Errorf("list_tasks should show the to-do: %s", l)
	}
	busy, _ := b.StartTask("占住唯一的名额", "占位")
	waitFor(t, "busy running", func() bool { return b.isLaunched(busy) })
	q1 := run("queue_task", map[string]any{"task": "任务一", "title": "一"})
	q2 := run("queue_task", map[string]any{"task": "任务二", "title": "二"})
	if !strings.Contains(q1, "第 1 位") || !strings.Contains(q2, "第 2 位") {
		t.Fatalf("queue positions: %s / %s", q1, q2)
	}
	id2 := taskIDRe.FindString(q2)
	run("reprioritize", map[string]any{"task_id": id2, "position": 1})
	if b.QueuePosition(id2) != 1 {
		t.Error("reprioritize failed")
	}
	if l := run("list_tasks", map[string]any{}); !strings.Contains(l, "排队第 1 位") {
		t.Errorf("list_tasks should show queue positions: %s", l)
	}
	run("pause_task", map[string]any{"task_id": id2})
	if tk, _ := b.store.Task(id2); tk.Status != "paused" {
		t.Error("pausing a queued task should park it")
	}
	run("resume_task", map[string]any{"task_id": id})
	if tk, _ := b.store.Task(id); tk.Status != "queued" || b.QueuePosition(id) != 1 {
		t.Errorf("resumed to-do should be next in queue: %+v pos=%d", tk, b.QueuePosition(id))
	}
	_, _ = b.Send("谢谢")
	b.store.Append("message.handled", []string{b.store.LastUserMessageID()}, true)
	run("react", map[string]any{"emoji": "👍"})
	m, _ := b.store.Message(b.store.LastUserMessageID())
	if len(m.Reactions) != 1 || m.Reactions[0].Emoji != "👍" || m.Reactions[0].By != "bot" {
		t.Errorf("reactions %+v", m.Reactions)
	}
	// "停" 停掉所有：运行中、排队中、暂停中
	b.Stop()
	waitFor(t, "all stopped", func() bool {
		for _, tk := range b.store.Tasks() {
			if tk.Open() {
				return false
			}
		}
		return true
	})
}

// 界面交互：上传附件、带附件发送、重新生成、编辑重发、表情回应、打开文件（不能越界）。
func TestInteractionEndpoints(t *testing.T) {
	dir := t.TempDir()
	b := newBot(t, dir, &fakeAI{agent: blockingAgent})
	b.Start()
	defer b.Close()
	srv := httptest.NewServer(b.Handler(""))
	defer srv.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "../../报告 v1.txt")
	fw.Write([]byte("hello"))
	mw.Close()
	resp, err := http.Post(srv.URL+"/api/uploads", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	var atts []Attachment
	_ = json.NewDecoder(resp.Body).Decode(&atts)
	resp.Body.Close()
	if len(atts) != 1 || !strings.HasPrefix(atts[0].Path, "uploads/") || strings.Contains(atts[0].Path, "..") {
		t.Fatalf("upload %+v", atts)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "ws", atts[0].Path)); string(raw) != "hello" {
		t.Fatal("uploaded content")
	}
	post := func(path string, body any) int {
		raw, _ := json.Marshal(body)
		resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post("/api/messages", map[string]any{"text": "读一下 " + atts[0].Path, "files": atts}); c != 200 {
		t.Fatalf("send: %d", c)
	}
	waitFor(t, "reply", func() bool { return countMsgs(b.store, "assistant", "文件内容") == 1 })
	var user Message
	for _, m := range b.store.Messages() {
		if m.Role == "user" {
			user = m
		}
	}
	if len(user.Files) != 1 {
		t.Fatal("message should carry the attachment")
	}
	// 重新生成：旧回复被隐藏，出现新回复
	if c := post("/api/regenerate", nil); c != 200 {
		t.Fatalf("regenerate %d", c)
	}
	waitFor(t, "regenerated", func() bool {
		hidden, visible := 0, 0
		for _, m := range b.store.Messages() {
			if m.Role == "assistant" && m.Text != "" {
				if m.Hidden {
					hidden++
				} else {
					visible++
				}
			}
		}
		return hidden == 1 && visible == 1
	})
	// 表情回应：点一次加，再点一次取消
	post("/api/messages/"+user.ID+"/react", map[string]string{"emoji": "❤️"})
	if m, _ := b.store.Message(user.ID); len(m.Reactions) != 1 {
		t.Error("reaction not added")
	}
	post("/api/messages/"+user.ID+"/react", map[string]string{"emoji": "❤️"})
	if m, _ := b.store.Message(user.ID); len(m.Reactions) != 0 {
		t.Error("reaction not toggled off")
	}
	// 编辑重发
	if c := post("/api/messages/"+user.ID+"/edit", map[string]string{"text": "做完了吗"}); c != 200 {
		t.Fatalf("edit %d", c)
	}
	if m, _ := b.store.Message(user.ID); !m.Hidden {
		t.Error("edited message should be hidden")
	}
	waitFor(t, "edited reply", func() bool { return countMsgs(b.store, "assistant", "没有任务记录") == 1 })
	// 打开文件：正常文件可以，越界不行
	r1, _ := http.Get(srv.URL + "/api/files?path=" + url.QueryEscape(atts[0].Path))
	r1.Body.Close()
	r2, _ := http.Get(srv.URL + "/api/files?path=../data/master.key")
	r2.Body.Close()
	if r1.StatusCode != 200 || r2.StatusCode != 400 {
		t.Errorf("files: %d %d", r1.StatusCode, r2.StatusCode)
	}
}
