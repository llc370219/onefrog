package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"onefrog/internal/model"
)

type Step struct {
	Kind     string `json:"kind"`
	Bot      string `json:"bot,omitempty"`
	Target   string `json:"target,omitempty"`
	Task     string `json:"task,omitempty"`
	Name     string `json:"name,omitempty"`
	Command  string `json:"command,omitempty"`
	ServerID string `json:"server_id,omitempty"`
	OK       bool   `json:"ok,omitempty"`
	Output   string `json:"output,omitempty"`
}

type credentials struct {
	base     string
	model    string
	key      string
	provider string
}

type trace struct {
	agent *Agent
	ctx   context.Context
	sink  func(Step)
	steps []Step
	tools []ToolEvent
}

func (a *Agent) lead(ctx context.Context, leader model.Bot, serverID, sessionID string, sink func(Step)) (Result, error) {
	leader, creds, err := a.credentialsFor(ctx, leader, false)
	if err != nil {
		return Result{}, err
	}
	tr := &trace{agent: a, ctx: ctx, sink: sink}
	messages, err := a.sessionMessages(ctx, sessionID, a.leaderPrompt(ctx, leader, serverID))
	if err != nil {
		return Result{}, err
	}
	text, err := a.converse(ctx, leader, creds, serverID, messages, a.hasPeers(ctx, leader.ID), tr)
	if err != nil {
		return Result{}, err
	}
	text = strings.TrimSpace(a.redact(ctx, text))
	if text == "" {
		text = "没有得到模型回复。"
	}
	_, _ = a.Store.AddMessage(ctx, model.Message{SessionID: sessionID, Role: "assistant", Content: text})
	return Result{SessionID: sessionID, Text: text, Tools: tr.tools, Steps: tr.steps}, nil
}

func (a *Agent) converse(ctx context.Context, speaker model.Bot, creds credentials, serverID string, messages []map[string]any, canDelegate bool, tr *trace) (string, error) {
	limit := a.execution().MaxSteps
	spec := toolDefs(canDelegate, a.managesAssets(speaker))
	for i := 0; i < limit; i++ {
		purpose := "conversation"
		if actor, ok := ctx.Value(usageKey{}).(usageActor); ok {
			purpose = actor.Purpose
		}
		callCtx := usageContext(ctx, speaker.ID, purpose, creds.provider)
		msg, err := a.chat(callCtx, creds.base, creds.model, creds.key, messages, spec)
		if err != nil {
			return "", err
		}
		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": msg.Content, "tool_calls": msg.ToolCalls})
		for _, call := range msg.ToolCalls {
			out := a.execCall(ctx, speaker, creds, serverID, call, canDelegate, tr)
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": out})
		}
	}
	return "这轮步骤太多，先停在这里。", nil
}

func (a *Agent) execCall(ctx context.Context, speaker model.Bot, creds credentials, serverID string, call toolCall, canDelegate bool, tr *trace) string {
	if a.managesAssets(speaker) && call.Function.Name != "manage_module_record" {
		return "模块构建 Bot 只能登记资产与订阅，不能执行命令或派工"
	}
	switch call.Function.Name {
	case "run_command":
		ev := a.runTool(ctx, speaker.ID, serverID, call)
		if tr != nil {
			tr.noteTool(speaker.Name, ev)
		}
		if ev.ran {
			return fmt.Sprintf(untrustedOpen, ev.server) + ev.Output + untrustedClose
		}
		return ev.Output
	case "ask_agent":
		if !canDelegate {
			return "这个 Bot 不能再派给别人。"
		}
		return a.handOff(ctx, speaker, creds, serverID, call, tr)
	case "manage_module_record":
		if !a.managesAssets(speaker) {
			return "只有模块构建 Bot 可以修改资产与订阅"
		}
		return a.manageAsset(call)
	default:
		return "未知工具"
	}
}

func (a *Agent) handOff(ctx context.Context, speaker model.Bot, creds credentials, serverID string, call toolCall, tr *trace) string {
	var args struct {
		BotID string `json:"bot_id"`
		Task  string `json:"task"`
	}
	_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
	args.Task = strings.TrimSpace(args.Task)
	if args.Task == "" {
		return "派工需要写清要做什么"
	}
	target, err := a.findBot(ctx, strings.TrimSpace(args.BotID))
	if err != nil {
		return "没有这个 Bot"
	}
	if !a.allowedBot(ctx, target.ID) {
		return "该 Bot 已停用或被用户排除，禁止派工"
	}
	if target.ID == speaker.ID {
		return "不能把任务派给自己"
	}
	if tr != nil {
		tr.emit(Step{Kind: "delegate", Bot: speaker.Name, Target: target.Name, Task: args.Task})
	}
	child := creds
	if a.Settings != nil || strings.TrimSpace(target.BaseURL) != "" {
		resolved, c, e := a.credentialsFor(ctx, target, true)
		if e != nil {
			return "协作模型配置无效"
		}
		if c.base != "" {
			target = resolved
			child = c
		}
	}
	if child.model == "" {
		child.model = creds.model
	}
	refs := []model.Attachment{}
	for _, id := range options(ctx).AttachmentIDs {
		if a.Files != nil {
			f, e := a.Files.Get(options(ctx).OwnerID, id)
			if e != nil {
				return "协作附件不可用"
			}
			refs = append(refs, f.Attachment)
		}
	}
	taskContent, e := a.messageContent(ctx, model.Message{Content: args.Task, Attachments: refs})
	if e != nil {
		return "协作附件读取失败"
	}
	messages := []map[string]any{
		{"role": "system", "content": a.workerPrompt(ctx, target, serverID)},
		{"role": "user", "content": taskContent},
	}
	workerCtx := usageContext(ctx, target.ID, "worker", child.provider)
	text, err := a.converse(workerCtx, target, child, serverID, messages, false, tr)
	if err != nil {
		return "派工失败：" + a.safe(ctx, err.Error())
	}
	text = strings.TrimSpace(a.redact(ctx, text))
	if text == "" {
		text = "没有结论"
	}
	return target.Name + "：" + text
}

func (t *trace) emit(step Step) {
	step.Task = t.agent.redact(t.ctx, step.Task)
	step.Command = t.agent.redact(t.ctx, step.Command)
	step.Output = t.agent.redact(t.ctx, step.Output)
	t.steps = append(t.steps, step)
	if t.sink != nil {
		t.sink(step)
	}
}

func (t *trace) noteTool(botName string, ev ToolEvent) {
	ev.Command = t.agent.redact(t.ctx, ev.Command)
	ev.Output = t.agent.redact(t.ctx, ev.Output)
	t.tools = append(t.tools, ev)
	t.emit(Step{Kind: "tool", Bot: botName, Name: ev.Name, Command: ev.Command, ServerID: ev.ServerID, OK: ev.OK, Output: ev.Output})
}

func (a *Agent) sessionMessages(ctx context.Context, sessionID, system string) ([]map[string]any, error) {
	items, err := a.Store.ListMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	limit := a.execution().ContextMessages
	if len(items) > limit {
		items = items[len(items)-limit:]
	}
	out := []map[string]any{{"role": "system", "content": system}}
	attachmentBytes := 0
	for _, item := range items {
		for _, ref := range item.Attachments {
			attachmentBytes += ref.Size
		}
		if attachmentBytes > 24*1024*1024 {
			return nil, fmt.Errorf("历史附件上下文超过 24 MB，请新建对话")
		}
		if item.Role != "user" && item.Role != "assistant" {
			continue
		}
		content, err := a.messageContent(ctx, item)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"role": item.Role, "content": content})
	}
	return out, nil
}

func (a *Agent) leaderPrompt(ctx context.Context, bot model.Bot, serverID string) string {
	if a.managesAssets(bot) {
		return a.assetPrompt(ctx, bot)
	}
	var b strings.Builder
	b.WriteString(promptLeader)
	a.writeOperations(&b, ctx)
	writeExtra(&b, bot)
	a.writePeers(&b, ctx, bot.ID)
	a.writeSecrets(&b, ctx)
	a.writeServers(&b, ctx, serverID)
	a.writeAssets(&b, bot)
	return b.String()
}

func (a *Agent) workerPrompt(ctx context.Context, bot model.Bot, serverID string) string {
	if a.managesAssets(bot) {
		return a.assetPrompt(ctx, bot)
	}
	var b strings.Builder
	fmt.Fprintf(&b, promptWorker, bot.Name, strings.TrimSpace(bot.Description))
	a.writeOperations(&b, ctx)
	writeExtra(&b, bot)
	a.writeSecrets(&b, ctx)
	a.writeServers(&b, ctx, serverID)
	a.writeAssets(&b, bot)
	return b.String()
}

func (a *Agent) assetPrompt(ctx context.Context, bot model.Bot) string {
	var b strings.Builder
	b.WriteString(promptAsset)
	a.writeServers(&b, ctx, "")
	a.writeAssets(&b, bot)
	return b.String()
}

// writeExtra appends the bot's own "工作指令" from the web UI.
func writeExtra(b *strings.Builder, bot model.Bot) {
	if extra := strings.TrimSpace(bot.SystemPrompt); extra != "" {
		fmt.Fprintf(b, promptExtra, extra)
	}
}

func (a *Agent) writeOperations(b *strings.Builder, ctx context.Context) {
	mode := a.approvalMode(ctx)
	desc, ok := promptModes[mode]
	if !ok {
		desc = promptModes["smart"]
	}
	fmt.Fprintf(b, promptOperations, desc)
}

func (a *Agent) writePeers(b *strings.Builder, ctx context.Context, self string) {
	bots, err := a.Store.ListBots(ctx)
	if err != nil {
		return
	}
	n := 0
	for _, item := range bots {
		if item.ID == self || !a.allowedBot(ctx, item.ID) {
			continue
		}
		if n == 0 {
			b.WriteString("可以派工的 Bot：\n")
		}
		n++
		fmt.Fprintf(b, "- id=%s name=%s %s\n", item.ID, item.Name, strings.TrimSpace(item.Description))
	}
	if n == 0 {
		b.WriteString("目前没有其他 Bot，需要时自己执行命令。\n")
	}
}

func (a *Agent) writeSecrets(b *strings.Builder, ctx context.Context) {
	items, err := a.Store.ListSecrets(ctx)
	if err != nil || len(items) == 0 {
		return
	}
	b.WriteString("可用密钥名称：")
	for i, item := range items {
		if i > 0 {
			b.WriteString("、")
		}
		b.WriteString(item.Name)
	}
	b.WriteString("。只写名称，明文由服务端在执行前替换，替换结果不会回到对话里。\n")
}

func (a *Agent) writeServers(b *strings.Builder, ctx context.Context, serverID string) {
	servers, err := a.Store.ListServers(ctx)
	if err != nil || len(servers) == 0 {
		return
	}
	if options(ctx).RestrictServers && serverID == "" {
		b.WriteString("用户尚未手动选择服务器；可以分析与规划，但禁止执行远程命令。\n")
		return
	}
	b.WriteString("已接入服务器：\n")
	for _, item := range servers {
		if options(ctx).RestrictServers && item.ID != serverID {
			continue
		}
		fmt.Fprintf(b, "- id=%s name=%s status=%s role=%s\n", item.ID, item.Name, item.Status, item.Role)
	}
	if serverID != "" {
		fmt.Fprintf(b, "用户当前选中的服务器 id=%s。run_command 没写 server_id 时用这个。\n", serverID)
	}
}

func (a *Agent) hasPeers(ctx context.Context, self string) bool {
	bots, err := a.Store.ListBots(ctx)
	if err != nil {
		return false
	}
	for _, item := range bots {
		if item.ID != self && a.allowedBot(ctx, item.ID) {
			return true
		}
	}
	return false
}

func (a *Agent) findBot(ctx context.Context, idOrName string) (model.Bot, error) {
	if idOrName == "" {
		return model.Bot{}, fmt.Errorf("empty")
	}
	if item, err := a.Store.GetBot(ctx, idOrName); err == nil {
		return item, nil
	}
	bots, err := a.Store.ListBots(ctx)
	if err != nil {
		return model.Bot{}, err
	}
	for _, item := range bots {
		if item.ID == idOrName || item.Name == idOrName {
			return item, nil
		}
	}
	return model.Bot{}, fmt.Errorf("missing")
}

func (a *Agent) safe(ctx context.Context, text string) string {
	return clip(a.redact(ctx, text), 300)
}

func toolDefs(delegate, manage bool) []map[string]any {
	command := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "run_command",
			"description": toolRunCommandDesc,
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"server_id": map[string]string{"type": "string"},
					"command":   map[string]string{"type": "string"},
					"risk":      map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}, "description": toolRiskDesc},
					"reason":    map[string]string{"type": "string", "description": toolReasonDesc},
				},
				"required": []string{"command", "risk", "reason"},
			},
		},
	}
	ask := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "ask_agent",
			"description": "把一件具体工作交给另一个 Bot，并拿回它的结论",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"bot_id": map[string]string{"type": "string", "description": "Bot 的 id 或名称"},
					"task":   map[string]string{"type": "string", "description": "要它完成的事"},
				},
				"required": []string{"bot_id", "task"},
			},
		},
	}
	record := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "manage_module_record",
			"description": "读写资产与订阅。action 取 list、create、update、delete。update 只传要改的字段。金额用 amount_yuan，日期用 YYYY-MM-DD。不要填写密码。",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":      map[string]any{"type": "string", "enum": []string{"list", "create", "update", "delete"}},
					"id":          map[string]string{"type": "string"},
					"name":        map[string]string{"type": "string"},
					"server_id":   map[string]string{"type": "string"},
					"provider":    map[string]string{"type": "string", "description": "服务器提供商"},
					"region":      map[string]string{"type": "string"},
					"plan":        map[string]string{"type": "string"},
					"cycle":       map[string]any{"type": "string", "enum": []string{"monthly", "quarterly", "yearly", "once"}},
					"currency":    map[string]string{"type": "string"},
					"amount_yuan": map[string]string{"type": "number"},
					"paid_at":     map[string]string{"type": "string"},
					"starts_at":   map[string]string{"type": "string"},
					"expires_at":  map[string]string{"type": "string"},
					"auto_renew":  map[string]string{"type": "boolean"},
					"status":      map[string]any{"type": "string", "enum": []string{"active", "cancelled"}},
					"ip":          map[string]string{"type": "string"},
					"specs":       map[string]string{"type": "string", "description": "CPU、内存、磁盘、流量等"},
					"order_no":    map[string]string{"type": "string"},
					"account":     map[string]string{"type": "string", "description": "提供商账号备注，不要填密码"},
					"contact":     map[string]string{"type": "string"},
					"tags":        map[string]string{"type": "string"},
					"notes":       map[string]string{"type": "string"},
				},
			},
		},
	}
	out := []map[string]any{}
	if !manage {
		out = append(out, command)
		if delegate {
			out = append(out, ask)
		}
	} else {
		out = append(out, record)
	}
	return out
}

func normalizeCall(raw json.RawMessage) (toolCall, error) {
	var loose struct {
		ThoughtSignature string `json:"thought_signature"`
		ID               string `json:"id"`
		Type             string `json:"type"`
		Function         struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &loose); err != nil {
		return toolCall{}, err
	}
	call := toolCall{ID: loose.ID, Type: loose.Type, ThoughtSignature: loose.ThoughtSignature}
	if call.Type == "" {
		call.Type = "function"
	}
	if call.ID == "" {
		call.ID = "call"
	}
	call.Function.Name = loose.Function.Name
	call.Function.Arguments = argsJSON(loose.Function.Arguments)
	return call, nil
}

func argsJSON(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return "{}"
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return text
		}
	}
	return string(raw)
}
