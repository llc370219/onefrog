package onefrog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	ErrTimeout    = errors.New("任务超过最长运行时间")
	ErrNotRunning = errors.New("任务不在运行中")
)

// Agent 是 agent 内核。一个 Agent 可以同时跑很多任务（每个任务各自一份对话）。
type Agent struct {
	Provider      Provider
	Model         string
	Tools         *Registry
	Guard         *Guard
	Store         Store          // 可选
	Memory        MemoryProvider // 可选
	Compactor     Compactor      // 可选
	Sink          EventSink      // 可选
	SystemPrompt  string         // 为空用默认提示词
	MaxToolOutput int            // 工具输出截断长度，默认 16000 字节
	Heartbeat     time.Duration  // 心跳间隔，默认 15 秒

	mu    sync.Mutex
	steer map[string]chan string
	pause map[string]*pauseCtl
}

// pauseCtl 记录"请在下一个安全点暂停"的请求。
// 安全点 = 两步之间：正在执行的工具（尤其是写文件、跑命令）一定会跑完；
// 正在进行的模型请求没有副作用，会被直接中断，省得白等。
type pauseCtl struct {
	mu        sync.Mutex
	requested bool
	cancelLLM context.CancelFunc
}

func (a *Agent) pauseCtl(id string) *pauseCtl {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pause == nil {
		a.pause = map[string]*pauseCtl{}
	}
	c := a.pause[id]
	if c == nil {
		c = &pauseCtl{}
		a.pause[id] = c
	}
	return c
}

// Pause 请求任务在下一个安全点暂停。任务结束时返回 Status=paused，并已通过 EvCheckpoint 发出对话快照。
// 任务还没开始跑时也可以调用，开始后第一时间暂停。
func (a *Agent) Pause(taskID string) {
	c := a.pauseCtl(taskID)
	c.mu.Lock()
	c.requested = true
	if c.cancelLLM != nil {
		c.cancelLLM()
	}
	c.mu.Unlock()
}

// ClearPause 撤销还没生效的暂停请求（恢复任务前调用，避免残留的请求让它立刻又暂停）。
func (a *Agent) ClearPause(taskID string) {
	a.mu.Lock()
	delete(a.pause, taskID)
	a.mu.Unlock()
}

func (c *pauseCtl) isRequested() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requested
}

func (c *pauseCtl) setLLM(f context.CancelFunc) {
	c.mu.Lock()
	c.cancelLLM = f
	c.mu.Unlock()
}

const defaultSystemPrompt = `你是 onefrog，一个在工作区里帮用户完成任务的 agent。
- 用工具读写文件、执行命令；所有路径都相对于工作区。
- 先看清现状再动手，改动尽量小。
- 如果某个操作被拒绝（DENIED），不要重试，也不要换别的办法绕过去。
- 用户中途插话时，以最新的要求为准。
- 做完后用简短的中文总结你做了什么、结果如何。`

// Steer 在两步之间给正在运行的任务插一句话（用户纠正/补充要求）。
func (a *Agent) Steer(taskID, msg string) error {
	a.mu.Lock()
	ch := a.steer[taskID]
	a.mu.Unlock()
	if ch == nil {
		return ErrNotRunning
	}
	select {
	case ch <- msg:
		return nil
	default:
		return errors.New("插话队列已满")
	}
}

// Run 同步运行一个任务直到结束。取消 ctx 会立刻中断模型请求并杀掉正在执行的命令。
func (a *Agent) Run(ctx context.Context, t Task) (Result, error) {
	if t.ID == "" {
		t.ID = NewID("task")
	}
	if t.MaxSteps <= 0 {
		t.MaxSteps = 50
	}
	if t.Policy == "" {
		t.Policy = PolicyOnRequest
	}
	if t.Workspace == "" {
		t.Workspace = "."
	}
	if ws, err := filepath.Abs(t.Workspace); err == nil {
		t.Workspace = ws
	}
	if t.MaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, t.MaxDuration, ErrTimeout)
		defer cancel()
	}
	steerCh := make(chan string, 16)
	a.mu.Lock()
	if a.steer == nil {
		a.steer = map[string]chan string{}
	}
	a.steer[t.ID] = steerCh
	a.mu.Unlock()
	pc := a.pauseCtl(t.ID)
	defer func() { a.mu.Lock(); delete(a.steer, t.ID); delete(a.pause, t.ID); a.mu.Unlock() }()

	em := &emitter{a: a, t: t}
	if a.Store != nil {
		_ = a.Store.SaveTask(ctx, t)
	}
	em.emit(EvTaskStarted, map[string]any{"input": t.Input, "policy": t.Policy, "workspace": t.Workspace})
	stopHB := em.heartbeat(a.Heartbeat)
	defer stopHB()

	res := Result{TaskID: t.ID, Steps: t.StartStep}
	finish := func(st Status, out string, err error) (Result, error) {
		stopHB() // 先停心跳，保证结束事件是最后一条
		res.Status, res.Output = st, out
		typ := map[Status]EventType{StatusCompleted: EvTaskCompleted, StatusFailed: EvTaskFailed, StatusCancelled: EvTaskCancelled, StatusPaused: EvTaskPaused}[st]
		if err != nil {
			res.Error = err.Error()
		}
		em.emit(typ, res)
		return res, err
	}
	fail := func(err error) (Result, error) {
		if ctx.Err() != nil {
			if cause := context.Cause(ctx); errors.Is(cause, ErrTimeout) {
				return finish(StatusFailed, "", ErrTimeout)
			}
			return finish(StatusCancelled, "", context.Canceled)
		}
		return finish(StatusFailed, "", err)
	}

	msgs, err := a.initialMessages(ctx, t)
	if err != nil {
		return fail(err)
	}
	checkpoint := func() {
		em.emit(EvCheckpoint, map[string]any{"messages": append([]Message(nil), msgs[1:]...), "steps": res.Steps})
	}
	paused := func() (Result, error) {
		msgs = drainSteer(steerCh, msgs, em) // 暂停前收到的插话一起存进快照
		checkpoint()
		return finish(StatusPaused, "", nil)
	}
	tools := a.tools()
	for res.Steps < t.MaxSteps+t.StartStep {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		if pc.isRequested() {
			return paused()
		}
		msgs = drainSteer(steerCh, msgs, em)
		if a.Compactor != nil {
			if msgs, err = a.Compactor.Compact(ctx, msgs); err != nil {
				return fail(err)
			}
		}
		lctx, lcancel := context.WithCancel(ctx)
		pc.setLLM(lcancel)
		if pc.isRequested() { // Pause 恰好发生在上面检查之后
			lcancel()
		}
		reply, err := a.Provider.Chat(lctx, ChatRequest{Model: a.Model, Messages: msgs, Tools: tools.Specs(),
			OnReasoning: func(d string) { em.emit(EvThinkingDelta, map[string]any{"text": d}) }},
			func(d string) { em.emit(EvTextDelta, map[string]any{"text": d}) })
		pc.setLLM(nil)
		lcancel()
		if err != nil && ctx.Err() == nil && pc.isRequested() {
			return paused() // 模型请求被暂停打断：这一步不算
		}
		if err != nil {
			return fail(err)
		}
		res.Steps++
		reply.Role = "assistant"
		msgs = append(msgs, reply)
		if len(reply.ToolCalls) == 0 {
			if len(steerCh) > 0 { // 用户刚插话，接着干
				continue
			}
			return finish(StatusCompleted, reply.Content, nil)
		}
		for _, call := range reply.ToolCalls {
			out := a.runTool(ctx, t, tools, em, call)
			msgs = append(msgs, Message{Role: "tool", ToolCallID: call.ID, Content: out})
		}
		checkpoint() // 一步完整结束（工具结果都齐了）才存快照
	}
	return finish(StatusFailed, "", fmt.Errorf("超过最大步数 %d", t.MaxSteps))
}

func (a *Agent) tools() *Registry {
	if a.Tools == nil {
		return NewRegistry(DefaultTools()...)
	}
	return a.Tools
}

func (a *Agent) initialMessages(ctx context.Context, t Task) ([]Message, error) {
	sys := a.SystemPrompt
	if sys == "" {
		sys = defaultSystemPrompt
	}
	sys += "\n\n工作区: " + t.Workspace
	if t.Instructions != "" {
		sys += "\n\n" + t.Instructions
	}
	if a.Memory != nil {
		mem, err := a.Memory.Recall(ctx, t.BotID, t.Input)
		if err != nil {
			return nil, err
		}
		if mem != "" {
			sys += "\n\n## 记忆\n" + mem
		}
	}
	if len(t.Resume) > 0 {
		msgs := append([]Message{{Role: "system", Content: sys}}, t.Resume...)
		return append(msgs, Message{Role: "user", Content: "[系统] 任务之前暂停了，现在继续。接着完成原来的任务；如果不确定之前做到哪了，先检查工作区现状再动手。"}), nil
	}
	msgs := append([]Message{{Role: "system", Content: sys}}, t.History...)
	return append(msgs, Message{Role: "user", Content: t.Input}), nil
}

func drainSteer(ch chan string, msgs []Message, em *emitter) []Message {
	for {
		select {
		case m := <-ch:
			em.emit(EvSteered, map[string]any{"text": m})
			msgs = append(msgs, Message{Role: "user", Content: "[用户中途补充] " + m})
		default:
			return msgs
		}
	}
}

func (a *Agent) runTool(ctx context.Context, t Task, tools *Registry, em *emitter, call ToolCall) string {
	em.emit(EvToolCall, map[string]any{"call_id": call.ID, "tool": call.Name, "args": call.Args})
	result := func(out string, isErr, denied bool) string {
		out = Truncate(out, a.MaxToolOutput)
		em.emit(EvToolResult, map[string]any{"call_id": call.ID, "tool": call.Name,
			"output": Truncate(out, 4000), "error": isErr, "denied": denied})
		return out
	}
	tool := tools.Get(call.Name)
	if tool == nil {
		return result("error: 没有这个工具: "+call.Name, true, false)
	}
	g := a.Guard
	if g == nil {
		g, _ = NewGuard(Rules{}, nil, nil)
	}
	if v := g.Check(ctx, t, tool, call.Args, em.emit); v.Decision != Allow {
		return result(DeniedMessage(v), false, true)
	}
	out, err := g.Execute(ctx, tool, ToolEnv{Workspace: t.Workspace, TaskID: t.ID}, call.Args)
	if err != nil {
		return result(strings.TrimSpace("error: "+err.Error()+"\n"+out), true, false)
	}
	return result(out, false, false)
}

// Truncate 把过长的文本截成「开头 + 提示 + 结尾」，避免撑爆上下文。
func Truncate(s string, n int) string {
	if n <= 0 {
		n = 16000
	}
	if len(s) <= n {
		return s
	}
	head, tail := n*2/3, n/3
	return strings.ToValidUTF8(s[:head]+fmt.Sprintf("\n…[省略 %d 字节]…\n", len(s)-head-tail)+s[len(s)-tail:], "")
}

// emitter 给事件编号，并同时写入 Store 和 Sink（加锁保证顺序）。
type emitter struct {
	a   *Agent
	t   Task
	mu  sync.Mutex
	seq int64
}

func (e *emitter) emit(typ EventType, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	ev := Event{Seq: e.seq, TaskID: e.t.ID, BotID: e.t.BotID, Type: typ, Time: time.Now(), Data: data}
	if e.a.Store != nil {
		_ = e.a.Store.SaveEvent(context.Background(), ev)
	}
	if e.a.Sink != nil {
		e.a.Sink.Emit(ev)
	}
}

func (e *emitter) heartbeat(every time.Duration) (stop func()) {
	if every <= 0 {
		every = 15 * time.Second
	}
	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		tk := time.NewTicker(every)
		defer tk.Stop()
		for {
			select {
			case <-tk.C:
				e.emit(EvHeartbeat, nil)
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done); <-exited }) }
}
