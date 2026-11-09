package bot

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"onefrog/internal/frogbot/onefrog"
	"onefrog/internal/frogbot/state"
)

// Event 是推给前端 / 写进日志的一条记录。所有状态都由这些事件"重放"得到（事件溯源），
// 所以服务重启后消息、任务、审批都能恢复。
//
//	Seq > 0：持久化事件，写入 data/stream.jsonl，断线重连按 Last-Event-ID 补发。
//	Seq = 0：临时事件（流式文字、输入中状态），只推送不保存。
//	Internal：只给后端用（例如系统通知、已汇报标记），不推给前端。
type Event struct {
	Seq      int64           `json:"seq"`
	Kind     string          `json:"kind"`
	Time     time.Time       `json:"time"`
	Data     json.RawMessage `json:"data"`
	Internal bool            `json:"internal,omitempty"`
}

type Message struct {
	ID           string       `json:"id"`
	Role         string       `json:"role"` // user | assistant | notice（系统通知，只给聊天层看）
	Text         string       `json:"text"`
	FromBotID    string       `json:"from_bot_id,omitempty"`
	ToBotID      string       `json:"to_bot_id,omitempty"`
	RequestID    string       `json:"request_id,omitempty"`
	ReplyTo      string       `json:"reply_to,omitempty"`
	MessageState string       `json:"message_state,omitempty"` // pending | handled | replied
	Hop          int          `json:"hop,omitempty"`
	Card         *Card        `json:"card,omitempty"`
	Tools        []string     `json:"tools,omitempty"`  // 聊天层这一轮用过的工具（界面上显示成小标签）
	Secret       *SecretReq   `json:"secret,omitempty"` // request_secret 发出的密钥卡片（永远不含值）
	Files        []Attachment `json:"files,omitempty"`
	Reactions    []Reaction   `json:"reactions,omitempty"`
	Hidden       bool         `json:"hidden,omitempty"` // 被"重新生成 / 编辑重发"替换掉的消息
	Silent       bool         `json:"silent,omitempty"` // 只记进上下文、不叫醒聊天层的通知
	// 聊天层这一轮的思考过程（只用于展示，不会喂回模型）
	Thinking   string    `json:"thinking,omitempty"`
	ThinkingMs int64     `json:"thinking_ms,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	handled    bool
}

// Card 是 ask_user 的选项卡片。关闭卡片 = 不同意/不选择。
type Card struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Status   string   `json:"status"` // pending | answered | dismissed
	Answer   string   `json:"answer,omitempty"`
}

// Attachment 是用户上传到工作区的文件。
type Attachment struct {
	Path string `json:"path"` // 相对工作区
	Name string `json:"name"`
	Size int64  `json:"size"`
	Mime string `json:"mime,omitempty"`
}

// Reaction 是一条表情回应。By = user | bot。
type Reaction struct {
	By    string `json:"by"`
	Emoji string `json:"emoji"`
}

// SecretReq 是请用户保存密钥的卡片。值直接 POST 到 /api/secrets，不经过这里。
type SecretReq struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`           // pending | saved | cancelled
	Exists      bool   `json:"exists,omitempty"` // 发卡片时已有同名密钥（保存 = 覆盖）
}

type TaskRec struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Input        string    `json:"input"`
	Status       string    `json:"status"`         // queued | running | paused | completed | failed | cancelled
	Prio         float64   `json:"prio,omitempty"` // 排队顺序，小的先跑
	Output       string    `json:"output,omitempty"`
	Error        string    `json:"error,omitempty"`
	Steps        int       `json:"steps,omitempty"`
	StopReason   string    `json:"stop_reason,omitempty"` // 结束：user | stall | restart；暂停：user | chat | restart | held（待办，还没开始过）
	CreatedAt    time.Time `json:"created_at"`
	createdSeq   int64     // event order breaks ties on clocks with coarse resolution
	EndedAt      time.Time `json:"ended_at,omitempty"`
	LastActivity time.Time `json:"-"`
	Awaiting     int       `json:"-"` // 正在等待的审批数
	Reported     bool      `json:"-"`
	Pausing      bool      `json:"-"` // 已请求暂停，等下一个安全点
	Notes        []string  `json:"-"` // 暂停期间收到的补充要求，恢复时交给 agent
	// 以下由事件重放得到，供聊天层的 list_tasks / read_task 使用
	StartedAt time.Time `json:"-"`
	Summary   string    `json:"-"` // agent 的最终总结（task.summary 事件）
	Files     []string  `json:"-"` // 成功写入/修改过的文件
	StepLog   []StepRec `json:"-"`
}

// StepRec 是 agent 的一步工具调用。
type StepRec struct {
	CallID string `json:"call_id"`
	Tool   string `json:"tool"`
	Target string `json:"target"`
	Result string `json:"result"` // running | ok | error | denied
}

// TaskSummary 是任务结束时写进事件日志的结构化结果。
type TaskSummary struct {
	ID          string   `json:"id"`
	Status      string   `json:"status"`
	Summary     string   `json:"summary"`
	Steps       int      `json:"steps"`
	Files       []string `json:"files,omitempty"`
	DurationSec int      `json:"duration_sec"`
	Error       string   `json:"error,omitempty"`
}

func (t TaskRec) clone() TaskRec {
	t.Files = append([]string(nil), t.Files...)
	t.Notes = append([]string(nil), t.Notes...)
	t.StepLog = append([]StepRec(nil), t.StepLog...)
	return t
}

// Duration 是任务实际运行时长。
func (t TaskRec) Duration() time.Duration {
	start, end := t.StartedAt, t.EndedAt
	if start.IsZero() {
		start = t.CreatedAt
	}
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(start)
}

// target 从工具参数里取出"操作对象"，用于展示。
func target(tool string, args json.RawMessage) string {
	var m map[string]any
	_ = json.Unmarshal(args, &m)
	for _, k := range []string{"command", "path", "pattern", "task_id", "schedule_id", "query", "name", "title"} {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	if tool == "list_dir" {
		return "."
	}
	return ""
}

func (t TaskRec) Active() bool { return t.Status == "queued" || t.Status == "running" }

// Open 表示任务还没结束（包括暂停中）。
func (t TaskRec) Open() bool { return t.Active() || t.Status == "paused" }

func terminal(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

type ApprovalRec struct {
	ID, TaskID, Tool string
	Args             json.RawMessage
	Status           string // pending | allow | deny | expired
	Scope, Reason    string
	OnceOnly         bool
	CreatedAt        time.Time
}

type Store struct {
	mu        sync.Mutex
	codec     state.Codec
	f         *os.File
	dirty     bool          // 有写入还没 fsync
	writeErr  time.Time     // 上次记录写盘失败日志的时间（避免刷屏）
	stopSync  chan struct{} // 关闭后台定期 fsync
	syncDone  chan struct{}
	seq       int64
	skipped   int
	log       []Event
	subs      map[chan Event]struct{}
	msgs      []*Message
	byID      map[string]*Message
	cards     map[string]*Message
	tasks     map[string]*TaskRec
	approvals map[string]*ApprovalRec
	secrets   map[string]*Message
	redact    func([]byte) []byte // 写盘和推送前的最后一道脱敏
}

// SetRedactor 设置脱敏函数：每条事件在写进 stream.jsonl、推给前端之前都会经过它。
func (s *Store) SetRedactor(f func([]byte) []byte) {
	s.mu.Lock()
	s.redact = f
	s.mu.Unlock()
}

func (s *Store) scrub(b []byte) []byte {
	if s.redact == nil {
		return b
	}
	return s.redact(b)
}

// Scrub 对外暴露脱敏（例如 SSE 补发历史时再过一遍）。
func (s *Store) Scrub(b []byte) []byte {
	s.mu.Lock()
	f := s.redact
	s.mu.Unlock()
	if f == nil {
		return b
	}
	return f(b)
}

func OpenStore(dir string) (*Store, error) {
	return openStore(dir, nil)
}

func openStore(dir string, codec state.Codec) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{codec: codec, subs: map[chan Event]struct{}{}, byID: map[string]*Message{}, cards: map[string]*Message{},
		tasks: map[string]*TaskRec{}, approvals: map[string]*ApprovalRec{}, secrets: map[string]*Message{}}
	path := filepath.Join(dir, "stream.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // 含聊天记录，只允许本用户读
	if err != nil {
		return nil, err
	}
	good, err := s.replay(f)
	if s.skipped > 0 {
		log.Printf("Frog Bot: 跳过 %d 条无法读取的事件日志，后续事件继续恢复", s.skipped)
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	// 上次崩溃/断电时可能只写了半行：截掉最后一个换行之后的残留，免得下一条事件接在坏数据后面。
	if st, err := f.Stat(); err == nil && st.Size() > good {
		log.Printf("事件日志 %s 末尾有 %d 字节不完整的数据（上次可能异常退出），已截掉", path, st.Size()-good)
		if err := f.Truncate(good); err != nil {
			f.Close()
			return nil, err
		}
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, err
	}
	s.f = f
	s.stopSync, s.syncDone = make(chan struct{}), make(chan struct{})
	go s.syncLoop()
	return s, nil
}

// replay 读入已有的事件，返回最后一个完整行（以换行结尾）之后的位置。
func (s *Store) replay(f *os.File) (int64, error) {
	r := bufio.NewReaderSize(f, 1<<20)
	var off int64
	for {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			return off, nil // 没有换行结尾的残行不算
		}
		if err != nil {
			return off, err
		}
		off += int64(len(line))
		raw, err := state.Decode(s.codec, bytes.TrimRight(line, "\r\n"))
		var e Event
		if err != nil || json.Unmarshal(raw, &e) != nil || e.Seq <= 0 {
			s.skipped++
			continue
		}
		if e.Seq > s.seq {
			s.seq = e.Seq
			s.log = append(s.log, e)
			s.apply(e)
		}
	}
}

func (s *Store) Skipped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skipped
}

// Append 保存一条事件、更新状态并推送。
// syncLoop 每 2 秒把还没落盘的事件 fsync 一次（关键事件在 Append 里立刻 fsync）。
func (s *Store) syncLoop() {
	defer close(s.syncDone)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stopSync:
			return
		case <-t.C:
			s.mu.Lock()
			s.syncLocked()
			s.mu.Unlock()
		}
	}
}

func (s *Store) syncLocked() {
	if !s.dirty || s.f == nil {
		return
	}
	if err := s.f.Sync(); err != nil {
		s.logWriteErr("fsync", err)
		return
	}
	s.dirty = false
}

func (s *Store) logWriteErr(op string, err error) {
	if time.Since(s.writeErr) > time.Minute {
		s.writeErr = time.Now()
		log.Printf("⚠ 写事件日志失败（%s）：%v。磁盘满了或没有权限时，重启后可能丢失最近的记录。", op, err)
	}
}

func (s *Store) Close() error {
	if s.stopSync != nil {
		close(s.stopSync)
		<-s.syncDone
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	return s.f.Close()
}

// 这些事件写完立刻 fsync：用户的输入和决定、任务状态变化。其余（agent 的工具调用过程等）由 syncLoop 定期落盘。
func syncNow(kind string, data []byte) bool {
	switch kind {
	case "agent":
		var ev struct {
			Type onefrog.EventType `json:"type"`
		}
		_ = json.Unmarshal(data, &ev)
		switch ev.Type {
		case onefrog.EvTaskCompleted, onefrog.EvTaskFailed, onefrog.EvTaskCancelled, onefrog.EvTaskPaused,
			onefrog.EvApprovalRequested, onefrog.EvApprovalDecided:
			return true
		}
		return false
	}
	return true
}

// Append 保存一条事件、更新状态并推送。写盘失败时事件仍然生效（内存和界面里都有），并在日志里提示。
func (s *Store) Append(kind string, data any, internal bool) Event {
	raw, _ := json.Marshal(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	raw = s.scrub(raw)
	s.seq++
	e := Event{Seq: s.seq, Kind: kind, Time: time.Now(), Data: raw, Internal: internal}
	line, _ := json.Marshal(e)
	enc, err := state.Encode(s.codec, line)
	if err == nil {
		_, err = s.f.Write(append(enc, '\n'))
	}
	if err != nil {
		s.logWriteErr("write", err)
	} else {
		s.dirty = true
		if syncNow(kind, raw) {
			s.syncLocked()
		}
	}
	s.log = append(s.log, e)
	s.apply(e)
	s.publish(e)
	return e
}

// Touch 记录任务有动静（只推送不保存的流式文字也算），给卡住检测用。
func (s *Store) Touch(taskID string) {
	s.mu.Lock()
	if t := s.tasks[taskID]; t != nil {
		t.LastActivity = time.Now()
	}
	s.mu.Unlock()
}

// Broadcast 推送一条不保存的临时事件。
func (s *Store) Broadcast(kind string, data any) {
	raw, _ := json.Marshal(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	raw = s.scrub(raw)
	s.publish(Event{Kind: kind, Time: time.Now(), Data: raw})
}

func (s *Store) publish(e Event) {
	for ch := range s.subs {
		select {
		case ch <- e:
		default: // 太慢的连接直接断开，让浏览器带 Last-Event-ID 重连补齐
			delete(s.subs, ch)
			close(ch)
		}
	}
}

func (s *Store) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 2048)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
}

// Since 返回 seq 之后、可以给前端看的持久化事件。
func (s *Store) Since(after int64) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := sort.Search(len(s.log), func(i int) bool { return s.log[i].Seq > after })
	var out []Event
	for _, e := range s.log[i:] {
		if !e.Internal {
			out = append(out, e)
		}
	}
	return out
}

// apply 是状态机：根据事件更新内存里的状态（调用方持有锁）。
func (s *Store) apply(e Event) {
	switch e.Kind {
	case "message":
		var m Message
		if json.Unmarshal(e.Data, &m) == nil {
			m.handled = m.Role == "assistant" || m.Silent
			s.msgs = append(s.msgs, &m)
			s.byID[m.ID] = &m
			if m.Card != nil {
				s.cards[m.Card.ID] = &m
			}
			if m.Secret != nil {
				s.secrets[m.Secret.ID] = &m
			}
		}
	case "secret.card":
		var c SecretReq
		if json.Unmarshal(e.Data, &c) == nil {
			if m := s.secrets[c.ID]; m != nil {
				m.Secret = &c
			}
		}
	case "message.hidden":
		var ids []string
		_ = json.Unmarshal(e.Data, &ids)
		for _, id := range ids {
			if m := s.byID[id]; m != nil {
				m.Hidden, m.handled = true, true
			}
		}
	case "message.reaction":
		var r struct {
			ID, By, Emoji string
			On            bool
		}
		if json.Unmarshal(e.Data, &r) == nil {
			if m := s.byID[r.ID]; m != nil {
				var keep []Reaction
				for _, x := range m.Reactions {
					if x.By != r.By { // 每一方在一条消息上只保留一个表情
						keep = append(keep, x)
					}
				}
				if r.On {
					keep = append(keep, Reaction{By: r.By, Emoji: r.Emoji})
				}
				m.Reactions = keep
			}
		}
	case "message.handled":
		var ids []string
		_ = json.Unmarshal(e.Data, &ids)
		for _, id := range ids {
			if m := s.byID[id]; m != nil {
				m.handled = true
			}
		}
	case "message.state":
		var p struct{ ID, State string }
		if json.Unmarshal(e.Data, &p) == nil {
			if m := s.byID[p.ID]; m != nil {
				m.MessageState = p.State
			}
		}
	case "card":
		var c Card
		if json.Unmarshal(e.Data, &c) == nil {
			if m := s.cards[c.ID]; m != nil {
				m.Card = &c
			}
		}
	case "task.created":
		var t TaskRec
		if json.Unmarshal(e.Data, &t) == nil {
			t.LastActivity = e.Time
			t.createdSeq = e.Seq
			s.tasks[t.ID] = &t
		}
	case "task.updated":
		var p TaskRec
		if json.Unmarshal(e.Data, &p) == nil {
			if t := s.tasks[p.ID]; t != nil {
				if p.Status != "" {
					t.Status, t.Pausing = p.Status, false
					if terminal(p.Status) {
						t.EndedAt = e.Time
					}
					if p.Status == "queued" { // 恢复：清掉暂停原因
						t.StopReason = ""
					}
				}
				if p.Prio != 0 {
					t.Prio = p.Prio
				}
				if p.Error != "" {
					t.Error = p.Error
				}
				if p.StopReason != "" {
					t.StopReason = p.StopReason
				}
			}
		}
	case "task.pausing":
		var p TaskRec
		if json.Unmarshal(e.Data, &p) == nil {
			if t := s.tasks[p.ID]; t != nil {
				t.Pausing, t.StopReason = true, p.StopReason
			}
		}
	case "task.prio":
		var p TaskRec
		if json.Unmarshal(e.Data, &p) == nil {
			if t := s.tasks[p.ID]; t != nil {
				t.Prio = p.Prio
			}
		}
	case "task.note":
		var p struct{ ID, Text string }
		if json.Unmarshal(e.Data, &p) == nil {
			if t := s.tasks[p.ID]; t != nil {
				t.Notes = append(t.Notes, p.Text)
			}
		}
	case "task.notes.used":
		var id string
		_ = json.Unmarshal(e.Data, &id)
		if t := s.tasks[id]; t != nil {
			t.Notes = nil
		}
	case "task.summary":
		var sm TaskSummary
		if json.Unmarshal(e.Data, &sm) == nil {
			if t := s.tasks[sm.ID]; t != nil {
				t.Summary = sm.Summary
			}
		}
	case "task.reported":
		var id string
		_ = json.Unmarshal(e.Data, &id)
		if t := s.tasks[id]; t != nil {
			t.Reported = true
		}
	case "agent":
		s.applyAgent(e)
	}
}

func (s *Store) applyAgent(e Event) {
	var ev struct {
		TaskID string            `json:"task_id"`
		Type   onefrog.EventType `json:"type"`
		Data   json.RawMessage   `json:"data"`
	}
	if json.Unmarshal(e.Data, &ev) != nil {
		return
	}
	t := s.tasks[ev.TaskID] // 聊天层直接发起的审批没有对应任务，t 为 nil
	switch ev.Type {
	case onefrog.EvApprovalRequested:
		var r onefrog.ApprovalRequest
		if json.Unmarshal(ev.Data, &r) == nil {
			s.approvals[r.ID] = &ApprovalRec{ID: r.ID, TaskID: ev.TaskID, Tool: r.ToolName, Args: r.Args, Status: "pending", Reason: r.Reason, OnceOnly: r.OnceOnly, CreatedAt: e.Time}
			if t != nil {
				t.Awaiting++
			}
		}
	case onefrog.EvApprovalDecided:
		var d struct {
			ID       string `json:"approval_id"`
			Decision string `json:"decision"`
			Scope    string `json:"scope"`
		}
		if json.Unmarshal(ev.Data, &d) == nil {
			if a := s.approvals[d.ID]; a != nil && a.Status == "pending" {
				a.Status, a.Scope = d.Decision, d.Scope
				if t != nil {
					t.Awaiting--
				}
			}
		}
	}
	if t == nil {
		return
	}
	t.LastActivity = e.Time
	switch ev.Type {
	case onefrog.EvTaskStarted:
		if t.Status == "queued" {
			t.Status = "running"
		}
		if t.StartedAt.IsZero() { // 恢复时保留第一次开始的时间
			t.StartedAt = e.Time
		}
	case onefrog.EvTaskPaused:
		var r onefrog.Result
		if json.Unmarshal(ev.Data, &r) == nil {
			t.Status, t.Steps, t.Pausing = "paused", r.Steps, false
			if t.StopReason == "" {
				t.StopReason = "user"
			}
		}
	case onefrog.EvToolCall:
		var c struct {
			CallID string          `json:"call_id"`
			Tool   string          `json:"tool"`
			Args   json.RawMessage `json:"args"`
		}
		if json.Unmarshal(ev.Data, &c) == nil {
			t.StepLog = append(t.StepLog, StepRec{CallID: c.CallID, Tool: c.Tool, Target: target(c.Tool, c.Args), Result: "running"})
		}
	case onefrog.EvToolResult:
		var r struct {
			CallID string `json:"call_id"`
			Error  bool   `json:"error"`
			Denied bool   `json:"denied"`
		}
		if json.Unmarshal(ev.Data, &r) == nil {
			for i := len(t.StepLog) - 1; i >= 0; i-- {
				st := &t.StepLog[i]
				if st.CallID != r.CallID || st.Result != "running" {
					continue
				}
				st.Result = map[bool]string{true: "error", false: "ok"}[r.Error]
				if r.Denied {
					st.Result = "denied"
				}
				if st.Result == "ok" && (st.Tool == "write_file" || st.Tool == "edit_file") && !slices.Contains(t.Files, st.Target) {
					t.Files = append(t.Files, st.Target)
				}
				break
			}
		}
	case onefrog.EvTaskCompleted, onefrog.EvTaskFailed, onefrog.EvTaskCancelled:
		var r onefrog.Result
		if json.Unmarshal(ev.Data, &r) == nil {
			t.Status, t.Output, t.Steps, t.EndedAt = string(r.Status), r.Output, r.Steps, e.Time
			if r.Error != "" {
				t.Error = r.Error
			}
		}
	}
}

// ---------- 查询 ----------

func (s *Store) Task(id string) (TaskRec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.tasks[id]; t != nil {
		return t.clone(), true
	}
	return TaskRec{}, false
}

func (s *Store) Tasks() []TaskRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TaskRec, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, t.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// MarkReported 原子地把任务标记为"已汇报"；已经汇报过则返回 false（保证只汇报一次）。
func (s *Store) MarkReported(id string) bool {
	s.mu.Lock()
	t := s.tasks[id]
	if t == nil || t.Reported || t.Active() {
		s.mu.Unlock()
		return false
	}
	t.Reported = true
	s.mu.Unlock()
	s.Append("task.reported", id, true)
	return true
}

func (s *Store) PendingApprovals() []ApprovalRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ApprovalRec
	for _, a := range s.approvals {
		if a.Status == "pending" {
			out = append(out, *a)
		}
	}
	return out
}

func (s *Store) Approval(id string) (ApprovalRec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.approvals[id]; a != nil {
		return *a, true
	}
	return ApprovalRec{}, false
}

func (s *Store) Card(id string) (Card, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.cards[id]; m != nil && m.Card != nil {
		return *m.Card, true
	}
	return Card{}, false
}

func (s *Store) PendingCard() (Card, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.cards {
		if m.Card != nil && m.Card.Status == "pending" {
			return *m.Card, true
		}
	}
	return Card{}, false
}

// Unhandled 返回聊天层还没处理的输入（用户消息和系统通知）。
func (s *Store) Unhandled() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, m := range s.msgs {
		if !m.handled {
			out = append(out, *m)
		}
	}
	return out
}

// Inbox returns durable Bot-to-Bot messages, including completed items when requested.
func (s *Store) Inbox(includeCompleted bool) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, m := range s.msgs {
		if m.FromBotID != "" && m.ToBotID != "" && (includeCompleted || m.MessageState == "pending") {
			out = append(out, *m)
		}
	}
	return out
}

// History 返回已处理过的最近 n 条消息，作为聊天层的上下文。
func (s *Store) History(n int) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, m := range s.msgs {
		if m.handled && !m.Hidden {
			out = append(out, *m)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

func (s *Store) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Message, 0, len(s.msgs))
	for _, m := range s.msgs {
		out = append(out, *m)
	}
	return out
}

// TaskApprovals 返回某个任务的全部审批（按时间顺序）。
func (s *Store) TaskApprovals(taskID string) []ApprovalRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ApprovalRec
	for _, a := range s.approvals {
		if a.TaskID == taskID {
			out = append(out, *a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// SecretCard 返回一张密钥卡片。
func (s *Store) SecretCard(id string) (SecretReq, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.secrets[id]; m != nil && m.Secret != nil {
		return *m.Secret, true
	}
	return SecretReq{}, false
}

// PendingSecretCards 返回还没处理的密钥卡片。
func (s *Store) PendingSecretCards() []SecretReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []SecretReq
	for _, m := range s.secrets {
		if m.Secret != nil && m.Secret.Status == "pending" {
			out = append(out, *m.Secret)
		}
	}
	return out
}

// Queue 返回排队中的任务（按顺序）。
func (s *Store) Queue() []TaskRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []TaskRec
	for _, t := range s.tasks {
		if t.Status == "queued" {
			out = append(out, t.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Prio != out[j].Prio {
			return out[i].Prio < out[j].Prio
		}
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].createdSeq < out[j].createdSeq
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// Message 按 ID 取一条消息。
func (s *Store) Message(id string) (Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.byID[id]; m != nil {
		return *m, true
	}
	return Message{}, false
}

// LastUserMessageID 返回最近一条（没被隐藏的）用户消息。
func (s *Store) LastUserMessageID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.msgs) - 1; i >= 0; i-- {
		if s.msgs[i].Role == "user" && !s.msgs[i].Hidden {
			return s.msgs[i].ID
		}
	}
	return ""
}
