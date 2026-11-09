package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"onefrog/internal/frogbot/onefrog"
	"onefrog/internal/frogbot/state"
)

// 监工：普通 Go 代码，不调模型。负责任务队列（排队、暂停、恢复、调整顺序）、
// 盯着 agent 的事件，在需要时叫醒聊天层。

// TaskOpts 是创建任务时的选项。
type TaskOpts struct {
	Hold       bool   // true = 只记成待办（暂停状态），等用户或聊天层说"开始"再跑
	ID         string // 可选：确定性任务 ID（定时唤醒器用，避免重启后重复派发）
	ScheduleID string // 可选：来源定时计划
}

type sched struct {
	mu       sync.Mutex
	launched map[string]bool // 已交给 Manager、还没结束/暂停的任务
}

// taskSlots 是所有 Bot 共用的后台任务并发上限（MAX_CONCURRENT，默认 4）。
// 一个 Bot 没抢到空位时登记在 waiters 里，任何任务结束腾出空位都会让它们重新排一次。
type taskSlots struct {
	mu      sync.Mutex
	used    int
	max     int
	waiters map[*Bot]struct{}
}

func newTaskSlots(max int) *taskSlots {
	if max <= 0 {
		max = 4
	}
	return &taskSlots{max: max, waiters: map[*Bot]struct{}{}}
}

func (s *taskSlots) acquire(b *Bot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used >= s.max {
		s.waiters[b] = struct{}{}
		return false
	}
	s.used++
	return true
}

func (s *taskSlots) release() {
	s.mu.Lock()
	if s.used > 0 {
		s.used--
	}
	waiters := s.waiters
	s.waiters = map[*Bot]struct{}{}
	s.mu.Unlock()
	for w := range waiters {
		go w.schedule() // 不在调用方持有的锁里排队；Bot 已关闭时 schedule 直接返回
	}
}

func (s *taskSlots) running() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used
}

// StartTask 创建任务：有空位就立刻开始，没有就排队。立刻返回 ID。
func (b *Bot) StartTask(input, title string) (string, error) {
	return b.CreateTask(input, title, TaskOpts{})
}

func (b *Bot) CreateTask(input, title string, o TaskOpts) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", errors.New("任务内容为空")
	}
	if title == "" {
		title = input
		if r := []rune(title); len(r) > 20 {
			title = string(r[:20]) + "…"
		}
	}
	id := o.ID
	if id == "" {
		id = onefrog.NewID("task")
	} else if _, exists := b.store.Task(id); exists {
		// 同 ID 已存在：幂等，直接返回（定时器重启后的二次派发）
		b.schedule()
		return id, nil
	}
	rec := TaskRec{ID: id, Title: title, Input: input, Status: "queued", Prio: float64(time.Now().UnixNano()), CreatedAt: time.Now()}
	if o.Hold {
		rec.Status, rec.StopReason = "paused", "held"
	}
	if o.ScheduleID != "" {
		// 标题前缀让界面能看出是定时触发的
		if !strings.HasPrefix(rec.Title, "定时·") {
			rec.Title = "定时·" + rec.Title
		}
	}
	b.store.Append("task.created", rec, false)
	b.schedule()
	return id, nil
}

// QueuePosition 返回排队位置（1 = 下一个），不在队列里返回 0。
func (b *Bot) QueuePosition(id string) int {
	for i, t := range b.waiting() {
		if t.ID == id {
			return i + 1
		}
	}
	return 0
}

// waiting 返回真正在等空位的任务（排除已经交给 agent、只是还没发出"开始"事件的）。
func (b *Bot) waiting() []TaskRec {
	var out []TaskRec
	for _, t := range b.store.Queue() {
		if !b.isLaunched(t.ID) {
			out = append(out, t)
		}
	}
	return out
}

func (b *Bot) running() int {
	b.sch.mu.Lock()
	defer b.sch.mu.Unlock()
	return len(b.sch.launched)
}

// schedule 在有空位时按顺序启动排队的任务。任何可能腾出空位或改变队列的地方都会调用它。
func (b *Bot) schedule() {
	b.sch.mu.Lock()
	defer b.sch.mu.Unlock()
	if b.ctx.Err() != nil {
		return
	}
	for _, t := range b.store.Queue() {
		if b.sch.launched[t.ID] {
			continue
		}
		if !b.slots.acquire(b) {
			return // 全局空位满了；有任务结束时会再叫我
		}
		release, err := b.enter()
		if err != nil {
			b.slots.release()
			return
		}
		profile := b.botProfile()
		task := onefrog.Task{ID: t.ID, BotID: profile.ID, Input: t.Input, Workspace: b.cfg.Workspace,
			Policy: b.cfg.Policy, MaxDuration: b.cfg.TaskMaxTime, Instructions: b.secretsBrief()}
		if profile.Instructions != "" {
			task.Instructions += "\n\n职责与工作偏好：\n" + profile.Instructions
		}
		if cp, ok := b.loadCheckpoint(t.ID); ok {
			task.Resume, task.StartStep = cp.Messages, cp.Steps
		}
		if len(t.Notes) > 0 { // 暂停期间用户补充的要求
			note := onefrog.Message{Role: "user", Content: "[用户在暂停期间补充] " + strings.Join(t.Notes, "\n")}
			if len(task.Resume) > 0 {
				task.Resume = append(task.Resume, note)
			} else {
				task.Input += "\n\n" + note.Content
			}
			b.store.Append("task.notes.used", t.ID, true)
		}
		b.mgr.Agent.ClearPause(t.ID)
		if _, err := b.mgr.StartWithRelease(task, release); err != nil {
			b.slots.release()
			b.store.Append("task.updated", TaskRec{ID: t.ID, Status: "failed", Error: err.Error()}, false)
			continue
		}
		b.sch.launched[t.ID] = true
	}
}

func (b *Bot) landed(id string) {
	b.sch.mu.Lock()
	was := b.sch.launched[id]
	delete(b.sch.launched, id)
	b.sch.mu.Unlock()
	if was {
		b.slots.release()
	}
}

// PauseTask 暂停任务：运行中的在下一个安全点暂停（正在执行的命令/写文件一定会跑完），排队中的直接挂起。
// by = user | chat。等待中的审批保持不变（超时照常生效）。
func (b *Bot) PauseTask(id, by string) (string, error) {
	t, ok := b.findTaskRec(id)
	if !ok {
		return "", fmt.Errorf("没有这个任务: %s", id)
	}
	switch {
	case t.Status == "running" || (t.Status == "queued" && b.isLaunched(t.ID)):
		if t.Pausing {
			return "这个任务已经在暂停中了。", nil
		}
		b.store.Append("task.pausing", TaskRec{ID: t.ID, StopReason: by}, false)
		_ = b.mgr.Pause(t.ID)
		if t.Awaiting > 0 {
			return "已请求暂停。它正在等用户审批：审批卡片保持有效，处理完这一步后就会暂停。", nil
		}
		return "已请求暂停：当前这一步做完后（不会打断正在执行的命令或写入）就停下，进度会保存。", nil
	case t.Status == "queued":
		b.store.Append("task.updated", TaskRec{ID: t.ID, Status: "paused", StopReason: by}, false)
		return "已把排队中的任务挂起，不会自动开始。", nil
	case t.Status == "paused":
		return "这个任务本来就是暂停状态。", nil
	}
	return "", fmt.Errorf("任务已经%s，不能暂停", statusCN(t.Status))
}

func (b *Bot) isLaunched(id string) bool {
	b.sch.mu.Lock()
	defer b.sch.mu.Unlock()
	return b.sch.launched[id]
}

// ResumeTask 让暂停的任务继续：放回队列最前面，有空位马上接着跑（从保存的进度继续）。
func (b *Bot) ResumeTask(id, note string) (string, error) {
	t, ok := b.findTaskRec(id)
	if !ok {
		return "", fmt.Errorf("没有这个任务: %s", id)
	}
	if t.Status != "paused" {
		return "", fmt.Errorf("任务现在是%s，不是暂停状态", statusCN(t.Status))
	}
	if note = strings.TrimSpace(note); note != "" {
		b.store.Append("task.note", map[string]string{"id": t.ID, "text": note}, false)
	}
	b.store.Append("task.updated", TaskRec{ID: t.ID, Status: "queued", Prio: b.frontPrio()}, false)
	b.schedule()
	if b.isLaunched(t.ID) {
		return "已继续，从上次保存的进度接着做。", nil
	}
	return fmt.Sprintf("已放回队列第 %d 位，有空位就接着做。", b.QueuePosition(t.ID)), nil
}

func (b *Bot) frontPrio() float64 {
	q := b.waiting()
	if len(q) == 0 {
		return float64(time.Now().UnixNano())
	}
	return q[0].Prio - 1e6
}

// Reprioritize 调整排队顺序：position 1 = 下一个运行。
func (b *Bot) Reprioritize(id string, position int) (string, error) {
	q := b.waiting()
	idx := -1
	for i, t := range q {
		if t.ID == id || strings.HasSuffix(t.ID, id) {
			idx = i
		}
	}
	if idx < 0 {
		return "", errors.New("这个任务不在排队中")
	}
	me := q[idx]
	rest := append(append([]TaskRec(nil), q[:idx]...), q[idx+1:]...)
	position = max(1, min(position, len(q)))
	var prio float64
	switch {
	case len(rest) == 0:
		prio = me.Prio
	case position == 1:
		prio = rest[0].Prio - 1e6
	case position > len(rest):
		prio = rest[len(rest)-1].Prio + 1e6
	default:
		prio = (rest[position-2].Prio + rest[position-1].Prio) / 2
	}
	b.store.Append("task.prio", TaskRec{ID: me.ID, Prio: prio}, false)
	b.schedule()
	return fmt.Sprintf("已调整：「%s」现在排第 %d 位。", me.Title, b.QueuePosition(me.ID)), nil
}

// CancelTask 停止一个任务（运行中、排队中、暂停中都可以）。
func (b *Bot) CancelTask(id, reason string) error {
	t, ok := b.findTaskRec(id)
	if !ok {
		return fmt.Errorf("没有这个任务: %s", id)
	}
	if !t.Open() {
		return fmt.Errorf("任务已经%s", statusCN(t.Status))
	}
	b.store.Append("task.updated", TaskRec{ID: t.ID, StopReason: reason}, false)
	// 和 schedule 用同一把锁：检查"还没开始"和标记取消之间，不能被别的 goroutine 抢先派发出去。
	b.sch.mu.Lock()
	launched := b.sch.launched[t.ID]
	if !launched {
		b.store.Append("task.updated", TaskRec{ID: t.ID, Status: "cancelled", Error: "在开始前被取消"}, false)
	}
	b.sch.mu.Unlock()
	if launched {
		return b.mgr.Cancel(t.ID) // 结束事件由 agent 发出，之后统一汇报
	}
	b.removeCheckpoint(t.ID)
	go b.report(t.ID)
	return nil
}

// RetryTask 用同样的要求重新开一个任务（原任务如果还在跑会先停掉）。
func (b *Bot) RetryTask(id string) (string, error) {
	t, ok := b.findTaskRec(id)
	if !ok {
		return "", fmt.Errorf("没有这个任务: %s", id)
	}
	if t.Open() {
		_ = b.CancelTask(t.ID, "user")
	}
	return b.StartTask(t.Input, t.Title)
}

func (b *Bot) findTaskRec(id string) (TaskRec, bool) {
	if t, ok := b.store.Task(id); ok {
		return t, true
	}
	if id = strings.TrimSpace(id); len(id) >= 4 {
		for _, t := range b.store.Tasks() {
			if strings.HasSuffix(t.ID, id) {
				return t, true
			}
		}
	}
	return TaskRec{}, false
}

func statusCN(s string) string {
	return map[string]string{"queued": "在排队", "running": "在运行", "paused": "暂停中", "completed": "完成了",
		"failed": "失败了", "cancelled": "被停止了"}[s]
}

// ---------- 进度快照 ----------

type checkpoint struct {
	Messages []onefrog.Message `json:"messages"`
	Steps    int               `json:"steps"`
}

func (b *Bot) checkpointPath(id string) string {
	return filepath.Join(b.cfg.DataDir, "checkpoints", filepath.Base(id)+".json")
}

func (b *Bot) saveCheckpoint(id string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	raw = b.vault.RedactBytes(raw)
	p := b.checkpointPath(id)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = state.WriteFile(b.cfg.Codec, p, raw)
}

func (b *Bot) loadCheckpoint(id string) (checkpoint, bool) {
	var cp checkpoint
	raw, err := state.ReadFile(b.cfg.Codec, b.checkpointPath(id))
	if err != nil || json.Unmarshal(raw, &cp) != nil || len(cp.Messages) == 0 {
		return cp, false
	}
	return cp, true
}

func (b *Bot) removeCheckpoint(id string) { _ = os.Remove(b.checkpointPath(id)) }

// ---------- agent 事件 ----------

func (b *Bot) onAgentEvent(e onefrog.Event) {
	switch e.Type {
	case onefrog.EvHeartbeat, onefrog.EvThinkingDelta:
		b.store.Broadcast("agent", e) // 心跳和思考片段只推送，不保存
		return
	case onefrog.EvCheckpoint:
		b.saveCheckpoint(e.TaskID, e.Data) // 快照单独存文件，不进事件日志
		return
	}
	if e.Type == onefrog.EvTextDelta {
		// agent 的流式文字只推给界面，不写进事件日志（结果在 task.completed 里有完整的一份）。
		b.store.Touch(e.TaskID)
		b.store.Broadcast("agent", e)
		return
	}
	b.store.Append("agent", e, false)
	switch {
	case e.Terminal():
		b.landed(e.TaskID)
		b.removeCheckpoint(e.TaskID)
		if b.sched != nil {
			st := "completed"
			if t, ok := b.store.Task(e.TaskID); ok {
				st = t.Status
			}
			b.sched.MarkTaskFinished(e.TaskID, st)
		}
		b.runBackground(func() { b.report(e.TaskID); b.schedule() }) // 不在 agent 的 goroutine 里做重活
	case e.Type == onefrog.EvTaskPaused:
		b.landed(e.TaskID)
		b.runBackground(func() {
			t, _ := b.store.Task(e.TaskID)
			if t.StopReason == "chat" {
				b.notify(fmt.Sprintf("任务「%s」(%s) 已在安全点暂停，进度已保存（第 %d 步）。之后可以用 resume_task 继续。", t.Title, t.ID, t.Steps))
			} else {
				b.contextNote(fmt.Sprintf("用户在界面上暂停了任务「%s」(%s)，进度已保存。", t.Title, t.ID))
			}
			b.schedule()
		})
	case e.Type == onefrog.EvApprovalRequested:
		if r, ok := e.Data.(onefrog.ApprovalRequest); ok {
			t, _ := b.store.Task(e.TaskID)
			b.notify(fmt.Sprintf("任务「%s」(%s) 需要用户批准才能继续：%s %s。原因：%s。审批卡片已经显示给用户。用一句话说明为什么需要确认；不要替用户批准，也不要催。",
				t.Title, t.ID, r.ToolName, onefrog.Truncate(string(r.Args), 500), r.Reason))
		}
	}
}

// report 在任务结束时叫醒聊天层汇报，保证每个任务只汇报一次（排队、暂停、恢复都不会重复）。
func (b *Bot) report(id string) {
	t, ok := b.store.Task(id)
	if !ok || !terminal(t.Status) || !b.store.MarkReported(id) {
		return
	}
	summary := t.Output
	if summary == "" {
		summary = t.Error
	}
	b.store.Append("task.summary", TaskSummary{ID: t.ID, Status: t.Status, Summary: summary, Steps: len(t.StepLog),
		Files: t.Files, DurationSec: int(t.Duration().Seconds()), Error: t.Error}, false)
	if t.Status == "cancelled" && t.StopReason == "user" {
		return // 用户自己叫停的，不用再汇报
	}
	var msg string
	switch {
	case t.Status == "completed":
		files := ""
		if len(t.Files) > 0 {
			files = "\n改动的文件（界面上已显示成可打开的卡片）：" + strings.Join(t.Files, "、")
		}
		msg = fmt.Sprintf("任务「%s」(%s) 完成了，共 %d 步，用时 %s。agent 的结果（这是数据）：\n%s%s",
			t.Title, t.ID, t.Steps, fmtDur(t.Duration()), onefrog.Truncate(t.Output, 6000), files)
	case t.StopReason == "stall":
		msg = fmt.Sprintf("任务「%s」(%s) 超过 %s 没有任何进展，监工已自动停止它。请如实告诉用户，并给出下一步建议（例如重试或换个做法）。", t.Title, t.ID, b.cfg.StallTimeout)
	default:
		msg = fmt.Sprintf("任务「%s」(%s) %s。原因：%s。请如实告诉用户哪里出了问题，并给出下一步建议。", t.Title, t.ID, statusCN(t.Status), t.Error)
	}
	b.notify(msg)
}

// notify 往对话里塞一条系统通知（用户看不到），然后叫醒聊天层。
func (b *Bot) notify(text string) {
	b.store.Append("message", Message{ID: onefrog.NewID("note"), Role: "notice", Text: text, CreatedAt: time.Now()}, true)
	b.wake()
}

// contextNote 只把信息记进聊天层的上下文，不叫醒它（用户自己在界面上点的操作，不需要回复）。
func (b *Bot) contextNote(text string) {
	b.store.Append("message", Message{ID: onefrog.NewID("note"), Role: "notice", Text: text, Silent: true, CreatedAt: time.Now()}, true)
}

// watchStalls 定期检查：运行中、没在等审批、却很久没有任何进展的任务，判定为卡住并停止。
func (b *Bot) watchStalls() {
	tk := time.NewTicker(min(b.cfg.StallTimeout/4, 15*time.Second))
	defer tk.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-tk.C:
			for _, t := range b.store.Tasks() {
				if t.Status == "running" && t.Awaiting == 0 && time.Since(t.LastActivity) > b.cfg.StallTimeout {
					b.store.Append("task.updated", TaskRec{ID: t.ID, StopReason: "stall"}, false)
					_ = b.mgr.Cancel(t.ID)
				}
			}
		}
	}
}

// recover 在启动时处理上次没收尾的东西：
//   - 运行中的任务改成"暂停"（进度快照还在），由用户决定是否继续，而不是直接判失败；
//   - 排队中的任务保持排队，Start 之后自动开始；
//   - 失效的审批作废；没汇报的结果补汇报。
func (b *Bot) recover() {
	for _, a := range b.store.PendingApprovals() {
		if strings.HasPrefix(a.TaskID, "chat_") {
			b.notify(fmt.Sprintf("你之前请求的 %s %s 还在等用户审批时服务重启了，这个审批已失效，操作没有执行。", a.Tool, target(a.Tool, a.Args)))
		}
		b.store.Append("agent", onefrog.Event{TaskID: a.TaskID, Type: onefrog.EvApprovalDecided, Time: time.Now(),
			Data: map[string]any{"approval_id": a.ID, "decision": "expired", "reason": "服务重启，审批已失效"}}, false)
	}
	var interrupted []string
	for _, t := range b.store.Tasks() {
		if t.Status == "running" || (t.Status == "queued" && !t.StartedAt.IsZero()) || t.Pausing {
			b.store.Append("task.updated", TaskRec{ID: t.ID, Status: "paused", StopReason: "restart"}, false)
			interrupted = append(interrupted, fmt.Sprintf("「%s」(%s)", t.Title, t.ID))
		}
	}
	if len(interrupted) > 0 {
		b.notify(fmt.Sprintf("服务重启了，这些任务被暂停（进度已保存，可以继续）：%s。简短告诉用户，并问是否继续（用 ask_user 卡片，选项如「继续」「先不用」）。", strings.Join(interrupted, "、")))
	}
	for _, t := range b.store.Tasks() {
		if !t.Reported && terminal(t.Status) {
			b.report(t.ID)
		}
	}
}
