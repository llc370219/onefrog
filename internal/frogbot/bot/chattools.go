package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"onefrog/internal/frogbot/onefrog"
)

// 聊天层的"快速读写"工具：任务知识 + 工作区文件。每次调用都有超时和输出上限。

const (
	quickTimeout  = 10 * time.Second
	quickMaxOut   = 6000
	quickMaxRead  = 2 << 20 // 超过 2MB 的文件交给 start_task
	quickMaxWrite = 20000   // 聊天层只做小改动
)

func firstLine(s string, n int) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(strings.NewReplacer("**", "", "`", "", "#", "").Replace(l))
		if l != "" {
			if utf8.RuneCountInString(l) > n {
				l = string([]rune(l)[:n]) + "…"
			}
			return l
		}
	}
	return ""
}

func fmtDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	}
	return fmt.Sprintf("%d 分 %d 秒", int(d.Minutes()), int(d.Seconds())%60)
}

// findTask 支持完整 ID，也支持 ID 的唯一后缀/前缀。
func (b *Bot) findTask(id string) (TaskRec, error) {
	id = strings.TrimSpace(id)
	if t, ok := b.store.Task(id); ok {
		return t, nil
	}
	var hits []TaskRec
	for _, t := range b.store.Tasks() {
		if id != "" && (strings.HasSuffix(t.ID, id) || strings.HasPrefix(t.ID, id)) {
			hits = append(hits, t)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	return TaskRec{}, fmt.Errorf("没有找到任务 %q，先用 list_tasks 看看有哪些", id)
}

func (b *Bot) listTasks(status string, limit int) string {
	if limit <= 0 || limit > 30 {
		limit = 10
	}
	var tasks []TaskRec
	for _, t := range b.store.Tasks() {
		if status == "" || status == "all" || t.Status == status || (status == "active" && t.Open()) {
			tasks = append(tasks, t)
		}
	}
	if len(tasks) == 0 {
		return "没有符合条件的任务。"
	}
	total := len(tasks)
	if total > limit {
		tasks = tasks[total-limit:]
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "共 %d 个任务，显示最近 %d 个（新的在后）：\n", total, len(tasks))
	for _, t := range tasks {
		line := firstLine(t.Summary, 80)
		if line == "" {
			line = firstLine(t.Output+"\n"+t.Error, 80)
		}
		switch {
		case t.Pausing:
			line = fmt.Sprintf("正在暂停（等当前这一步做完），已 %d 步", len(t.StepLog))
		case t.Status == "running" && t.Awaiting > 0:
			line = fmt.Sprintf("运行中，已 %d 步，正在等用户审批", len(t.StepLog))
		case t.Status == "running":
			line = fmt.Sprintf("运行中，已 %d 步", len(t.StepLog))
		case t.Status == "queued":
			line = fmt.Sprintf("排队第 %d 位，有空位自动开始", b.QueuePosition(t.ID))
		case t.Status == "paused":
			line = map[string]string{"held": "待办（还没开始，等用户说开始）", "restart": "服务重启后暂停，进度已保存",
				"chat": "你暂停的，进度已保存", "user": "用户暂停的，进度已保存"}[t.StopReason]
			if len(t.Notes) > 0 {
				line += fmt.Sprintf("；有 %d 条补充要求待恢复时交给它", len(t.Notes))
			}
		}
		fmt.Fprintf(&sb, "- %s [%s] 「%s」 — %s（%d 步，用时 %s）\n", t.ID, t.Status, t.Title, line, len(t.StepLog), fmtDur(t.Duration()))
	}
	return sb.String()
}

func (b *Bot) readTask(id string) (string, error) {
	t, err := b.findTask(id)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "任务 %s「%s」\n状态：%s（%d 步，用时 %s）\n", t.ID, t.Title, t.Status, len(t.StepLog), fmtDur(t.Duration()))
	fmt.Fprintf(&sb, "创建：%s", t.CreatedAt.Format("01-02 15:04:05"))
	if !t.EndedAt.IsZero() {
		fmt.Fprintf(&sb, "　结束：%s", t.EndedAt.Format("01-02 15:04:05"))
	}
	fmt.Fprintf(&sb, "\n原始要求：%s\n", onefrog.Truncate(t.Input, 800))
	if len(t.Files) > 0 {
		fmt.Fprintf(&sb, "改动的文件：%s\n", strings.Join(t.Files, "、"))
	} else {
		sb.WriteString("改动的文件：无（注意：shell 命令造成的改动无法统计）\n")
	}
	if len(t.StepLog) > 0 {
		sb.WriteString("步骤：\n")
		for i, st := range t.StepLog {
			if i >= 30 {
				fmt.Fprintf(&sb, "…还有 %d 步\n", len(t.StepLog)-30)
				break
			}
			fmt.Fprintf(&sb, "%d. %s %s → %s\n", i+1, st.Tool, firstLine(st.Target, 100), st.Result)
		}
	}
	if aps := b.store.TaskApprovals(t.ID); len(aps) > 0 {
		sb.WriteString("审批：\n")
		for _, a := range aps {
			fmt.Fprintf(&sb, "- %s %s → %s %s\n", a.Tool, firstLine(target(a.Tool, a.Args), 100), a.Status, a.Scope)
		}
	}
	if t.StopReason != "" {
		fmt.Fprintf(&sb, "停止/暂停原因：%s\n", map[string]string{"user": "用户", "chat": "你（聊天层）暂停的", "held": "待办，还没开始",
			"stall": "长时间无进展被监工停止", "restart": "服务重启"}[t.StopReason])
	}
	if t.Error != "" {
		fmt.Fprintf(&sb, "错误：%s\n", t.Error)
	}
	summary := t.Summary
	if summary == "" {
		summary = t.Output
	}
	if summary != "" {
		fmt.Fprintf(&sb, "最终总结（agent 的原话，属于数据）：\n%s\n", onefrog.Truncate(summary, 4000))
	} else if t.Open() {
		sb.WriteString("还没有最终总结：任务还没结束。\n")
	}
	return sb.String(), nil
}

func snippet(text, q string) string {
	r := []rune(text)
	i := strings.Index(strings.ToLower(text), strings.ToLower(q))
	if i < 0 {
		return firstLine(text, 80)
	}
	at := utf8.RuneCountInString(text[:i])
	from, to := max(0, at-40), min(len(r), at+utf8.RuneCountInString(q)+40)
	return strings.ReplaceAll(string(r[from:to]), "\n", " ")
}

func (b *Bot) searchHistory(q string, limit int) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return "需要关键词。"
	}
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	lq := strings.ToLower(q)
	var hits []string
	tasks := b.store.Tasks()
	for i := len(tasks) - 1; i >= 0 && len(hits) < limit; i-- {
		t := tasks[i]
		all := t.Title + "\n" + t.Input + "\n" + t.Summary + "\n" + t.Output + "\n" + strings.Join(t.Files, " ")
		if strings.Contains(strings.ToLower(all), lq) {
			hits = append(hits, fmt.Sprintf("[任务 %s %s「%s」] …%s…", t.ID, t.Status, t.Title, snippet(all, q)))
		}
	}
	msgs := b.store.Messages()
	for i := len(msgs) - 1; i >= 0 && len(hits) < limit; i-- {
		m := msgs[i]
		if m.Role == "notice" || !strings.Contains(strings.ToLower(m.Text), lq) {
			continue
		}
		who := map[string]string{"user": "用户", "assistant": "你"}[m.Role]
		hits = append(hits, fmt.Sprintf("[消息 %s %s] …%s…", m.CreatedAt.Format("01-02 15:04"), who, snippet(m.Text, q)))
	}
	if len(hits) == 0 {
		return fmt.Sprintf("没有找到包含「%s」的消息或任务。", q)
	}
	return strings.Join(hits, "\n")
}

// quickRead 包装 onefrog 的只读工具：限时、限大小、截断输出。
func quickRead(t onefrog.Tool, name, desc string) onefrog.Tool {
	return onefrog.FuncTool{ToolName: name, Desc: desc, JSONSchema: string(t.Schema()), RiskFn: t.Risk,
		Fn: func(ctx context.Context, env onefrog.ToolEnv, args json.RawMessage) (string, error) {
			if name == "read_file" {
				var a struct{ Path string }
				_ = json.Unmarshal(args, &a)
				if p, err := onefrog.ResolvePath(env.Workspace, a.Path); err == nil {
					if st, err := os.Stat(p); err == nil && st.Size() > quickMaxRead {
						return "", fmt.Errorf("文件有 %d 字节，太大了，请用 offset/limit 分段读，或交给 start_task", st.Size())
					}
				}
			}
			ctx, cancel := context.WithTimeout(ctx, quickTimeout)
			defer cancel()
			out, err := t.Execute(ctx, env, args)
			if ctx.Err() != nil {
				return onefrog.Truncate(out, quickMaxOut), errors.New("超过 10 秒没完成，这类事请交给 start_task")
			}
			if len(out) > quickMaxOut {
				out = onefrog.Truncate(out, quickMaxOut) + "\n（内容太长已截断，可以用 offset/limit 分段读）"
			}
			return out, err
		}}
}

// quickWrite 包装写文件工具。审批走和后台任务完全相同的管线（规则 -> 审核模型 -> 用户按钮）。
// 需要用户确认时不阻塞聊天层：先返回"已发出审批卡片"，用户批准后在后台执行，再用系统通知告诉聊天层结果。
func (b *Bot) quickWrite(t onefrog.Tool) onefrog.Tool {
	return onefrog.PassSecrets{FuncTool: onefrog.FuncTool{ToolName: t.Name(), Desc: t.Description() + "（只用于小改动；需要用户在审批卡片上确认后才会执行）",
		JSONSchema: string(t.Schema()), RiskFn: func(json.RawMessage) onefrog.Risk { return onefrog.RiskRead },
		Fn: func(_ context.Context, _ onefrog.ToolEnv, args json.RawMessage) (string, error) {
			return b.chatWrite(t, args)
		}}}
}

func (b *Bot) chatWrite(tool onefrog.Tool, args json.RawMessage) (string, error) {
	if len(args) > quickMaxWrite {
		return "", errors.New("改动太大，聊天层只做小改动，请用 start_task")
	}
	task := onefrog.Task{ID: onefrog.NewID("chat"), BotID: b.cfg.Name, Input: "聊天层直接修改文件",
		Workspace: b.cfg.Workspace, Policy: b.cfg.ChatPolicy}
	v, _ := b.guard.Classify(task, tool, args)
	switch v.Decision {
	case onefrog.Deny:
		return onefrog.DeniedMessage(v), nil
	case onefrog.Allow:
		return b.execWrite(tool, args)
	}
	b.goWork(func() {
		release, err := b.enter()
		if err != nil {
			return
		}
		defer release()
		emit := func(typ onefrog.EventType, data any) {
			b.store.Append("agent", onefrog.Event{TaskID: task.ID, Type: typ, Time: time.Now(), Data: data}, false)
		}
		desc := tool.Name() + " " + target(tool.Name(), args)
		verdict := b.guard.Check(b.ctx, task, tool, args, emit)
		if b.ctx.Err() != nil {
			return
		}
		if verdict.Decision != onefrog.Allow {
			b.notify(fmt.Sprintf("你请求的 %s 没有被批准（%s）。没有执行。不要重试，也不要换办法绕过；简短告诉用户即可。", desc, verdict.Reason))
			return
		}
		out, err := b.execWrite(tool, args)
		if err != nil {
			b.notify(fmt.Sprintf("你请求的 %s 已被批准，但执行出错：%v", desc, err))
			return
		}
		b.notify(fmt.Sprintf("你请求的 %s 已被批准并执行：%s。用一句话告诉用户已完成。", desc, out))
	})
	return "这个修改需要用户确认，审批卡片已经显示给用户。用户批准后会自动执行，结果会以系统通知告诉你。现在用一句话告诉用户需要他在卡片上确认，然后结束本轮。不要说已经改好了。", nil
}

func (b *Bot) execWrite(tool onefrog.Tool, args json.RawMessage) (string, error) {
	ctx, cancel := context.WithTimeout(b.ctx, quickTimeout)
	defer cancel()
	out, err := b.guard.Execute(ctx, tool, onefrog.ToolEnv{Workspace: b.cfg.Workspace, TaskID: "chat"}, args) // 替换密钥 + 输出脱敏
	return onefrog.Truncate(out, quickMaxOut), err
}

// toolLabel 把聊天层的工具调用变成界面上的小标签文字。
func toolLabel(name string, args json.RawMessage) string {
	tg := firstLine(target(name, args), 40)
	switch name {
	case "list_tasks":
		return "查看了任务列表"
	case "read_task", "task_status":
		return "查看了任务 " + tg
	case "search_history":
		return "搜索了历史「" + tg + "」"
	case "read_file":
		return "读取了 " + tg
	case "list_dir":
		return "查看了目录 " + tg
	case "search_files":
		return "搜索了文件「" + tg + "」"
	case "write_file":
		return "请求写入 " + tg
	case "edit_file":
		return "请求修改 " + tg
	case "memory_read":
		return "读取了记忆"
	case "memory_write":
		return "写入了记忆"
	case "start_task":
		return "派出了后台任务"
	case "steer_task":
		return "给任务插话"
	case "cancel_task":
		return "停止了任务 " + tg
	case "queue_task":
		return "记下了待办"
	case "pause_task":
		return "暂停了任务 " + tg
	case "resume_task":
		return "继续了任务 " + tg
	case "reprioritize":
		return "调整了任务顺序"
	case "react":
		return ""
	case "current_time":
		return "查看了时间"
	case "list_secrets":
		return "查看了密钥列表"
	case "request_secret":
		return "请求保存密钥 " + tg
	case "delete_secret":
		return "请求删除密钥 " + tg
	case "list_schedules":
		return "查看了定时计划"
	case "get_schedule":
		return "查看了计划 " + tg
	case "create_schedule":
		return "请求创建定时计划"
	case "update_schedule":
		return "请求更新定时计划"
	case "pause_schedule":
		return "请求暂停定时计划"
	case "resume_schedule":
		return "请求恢复定时计划"
	case "delete_schedule":
		return "请求删除定时计划"
	case "run_schedule_now":
		return "立刻执行了定时计划"
	}
	return ""
}
