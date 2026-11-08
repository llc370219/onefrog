package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"onefrog/internal/model"
)

type Step struct {
	Kind     string              `json:"kind"`
	Bot      string              `json:"bot,omitempty"`
	Target   string              `json:"target,omitempty"`
	Task     string              `json:"task,omitempty"`
	Name     string              `json:"name,omitempty"`
	Command  string              `json:"command,omitempty"`
	ServerID string              `json:"server_id,omitempty"`
	OK       bool                `json:"ok,omitempty"`
	Output   string              `json:"output,omitempty"`
	Approval *model.ApprovalCard `json:"approval,omitempty"`
}

type credentials struct {
	base     string
	model    string
	key      string
	provider string
}

type trace struct {
	agent    *Agent
	ctx      context.Context
	sink     func(Step)
	steps    []Step
	tools    []ToolEvent
	timeline []model.TurnItem
	// choice is set when the leader asked the user with ask_user; the turn
	// then ends and the card is stored with the reply.
	choice *model.Choice
	// secret is set when the leader asked for a vault secret. The plaintext is
	// not part of this trace.
	secret *model.SecretRequest
	// sent counts message_agent calls in this turn.
	sent int
}

type traceKey struct{}

func traceFrom(ctx context.Context) *trace {
	tr, _ := ctx.Value(traceKey{}).(*trace)
	return tr
}

func hasInteractiveCardCall(calls []toolCall) bool {
	for _, c := range calls {
		if c.Function.Name == "ask_user" || c.Function.Name == "request_secret" {
			return true
		}
	}
	return false
}

func (a *Agent) lead(ctx context.Context, leader model.Bot, serverID, sessionID string, sink func(Step)) (Result, error) {
	tr := &trace{agent: a, ctx: ctx, sink: sink}
	return a.leadWithTrace(ctx, leader, serverID, sessionID, tr)
}

// leadWithTrace runs one leader turn on an existing trace so a durable task
// keeps its own conversation window and journal.
func (a *Agent) leadWithTrace(ctx context.Context, leader model.Bot, serverID, sessionID string, tr *trace) (Result, error) {
	leader, creds, err := a.credentialsFor(ctx, leader, false)
	if err != nil {
		return Result{}, err
	}
	if tr.agent == nil {
		tr.agent = a
	}
	if tr.ctx == nil {
		tr.ctx = ctx
	}
	ctx = context.WithValue(ctx, traceKey{}, tr)
	tr.ctx = ctx
	var system string
	canDelegate := false
	if options(ctx).Workspace != nil {
		system = a.workspacePrompt(ctx, leader)
	} else if isInbox(ctx) || isAutomation(ctx) {
		// A background inbox or automation turn works like a worker: no user
		// to ask, no delegation, only an optional note via message_agent.
		system, canDelegate = a.workerPrompt(ctx, leader, serverID), false
		if isAutomation(ctx) {
			system += promptAutomation
		}
	} else {
		canDelegate = a.hasPeers(ctx, leader.ID)
		if isDirect(ctx) {
			system = a.directPrompt(ctx, leader, serverID)
		} else {
			system = a.leaderPrompt(ctx, leader, serverID)
		}
	}
	var messages []map[string]any
	if st := taskFrom(ctx); st != nil {
		// A durable task resumes from its stored window; only a brand-new
		// task builds one from the session.
		if it, e := st.book.Get(st.id); e == nil && len(it.Checkpoint.Messages) > 0 {
			messages = copyMessages(it.Checkpoint.Messages)
		}
	}
	if len(messages) == 0 {
		var err error
		messages, err = a.sessionMessages(ctx, sessionID, system, leader.Name)
		if err != nil {
			return Result{}, err
		}
	}
	text, err := a.converse(ctx, leader, creds, serverID, messages, canDelegate, tr)
	if err != nil && !(errors.Is(err, errSuspended) && (tr.choice != nil || tr.secret != nil)) {
		return Result{}, err
	}
	text = strings.TrimSpace(a.redact(ctx, text))
	if tr.choice != nil {
		tr.choice.Prompt = a.redact(ctx, tr.choice.Prompt)
		for i := range tr.choice.Options {
			tr.choice.Options[i].Label = a.redact(ctx, tr.choice.Options[i].Label)
		}
		msg, err := a.Store.AddMessage(ctx, model.Message{SessionID: sessionID, Role: "assistant", Content: text, Choice: tr.choice, Timeline: tr.timeline})
		if err != nil {
			return Result{}, err
		}
		return Result{SessionID: sessionID, Text: text, Tools: tr.tools, Steps: tr.steps, Timeline: tr.timeline, Choice: tr.choice, MessageID: msg.ID}, nil
	}
	if tr.secret != nil {
		tr.secret.Label = a.redact(ctx, tr.secret.Label)
		tr.secret.Description = a.redact(ctx, tr.secret.Description)
		if text == "" {
			text = "需要一项密钥才能继续。请在卡片里输入，明文不会进入对话。"
		}
		msg, err := a.Store.AddMessage(ctx, model.Message{SessionID: sessionID, Role: "assistant", Content: text, SecretRequest: tr.secret, Timeline: tr.timeline})
		if err != nil {
			return Result{}, err
		}
		return Result{SessionID: sessionID, Text: text, Tools: tr.tools, Steps: tr.steps, Timeline: tr.timeline, SecretRequest: tr.secret, MessageID: msg.ID}, nil
	}
	if text == "" && len(tr.timeline) == 0 {
		text = "没有得到模型回复。"
	}
	// A durable task owns its own transcript writes: the task loop stores the
	// closing message together with its checkpoint, so writing it here as well
	// would duplicate it. Cards are the exception, because their message id is
	// what the task binds the pending state to.
	if taskFrom(ctx) != nil {
		return Result{SessionID: sessionID, Text: text, Tools: tr.tools, Steps: tr.steps, Timeline: tr.timeline}, nil
	}
	msg, _ := a.Store.AddMessage(ctx, model.Message{SessionID: sessionID, Role: "assistant", Content: text, Timeline: tr.timeline})
	return Result{SessionID: sessionID, Text: text, Tools: tr.tools, Steps: tr.steps, Timeline: tr.timeline, MessageID: msg.ID}, nil
}

func (a *Agent) converse(ctx context.Context, speaker model.Bot, creds credentials, serverID string, messages []map[string]any, canDelegate bool, tr *trace) (string, error) {
	limit := a.execution().MaxSteps
	spec := a.toolSpec(ctx, speaker, canDelegate)
	if state := taskFrom(ctx); state != nil {
		out, err := a.taskLoop(ctx, state, speaker, creds, serverID, messages, spec, canDelegate, tr)
		return out.Text, err
	}
	mediaNote := ""
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
		if note, rest := peelMediaNote(msg.Content); note != "" {
			mediaNote = note
			msg.Content = rest
		}
		if len(msg.ToolCalls) == 0 {
			return joinMediaNote(mediaNote, msg.Content), nil
		}
		if tr != nil && !isWorker(ctx) && !hasInteractiveCardCall(msg.ToolCalls) {
			if say := strings.TrimSpace(a.redact(ctx, msg.Content)); say != "" {
				tr.emit(Step{Kind: "say", Bot: speaker.Name, Output: say})
			}
		}
		entry := map[string]any{"role": "assistant", "content": msg.Content, "tool_calls": msg.ToolCalls}
		if len(msg.ResponseItems) > 0 {
			entry["response_items"] = msg.ResponseItems
		}
		messages = append(messages, entry)
		for _, call := range msg.ToolCalls {
			out := a.execCall(ctx, speaker, creds, serverID, call, canDelegate, tr)
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": out})
		}
		if tr != nil && (tr.choice != nil || tr.secret != nil) && !isWorker(ctx) {
			return joinMediaNote(mediaNote, msg.Content), nil
		}
	}
	return joinMediaNote(mediaNote, a.pauseForBudget(ctx, speaker, creds, messages)), nil
}

// toolSpec builds the tool list for one speaker. It is shared by the ordinary
// turn loop and the durable task loop so both offer exactly the same tools.
func (a *Agent) toolSpec(ctx context.Context, speaker model.Bot, canDelegate bool) []map[string]any {
	var spec []map[string]any
	if options(ctx).Workspace != nil {
		spec = []map[string]any{workspaceTool, askUserTool, rememberTool, recallTool}
	} else if a.developsModules(speaker) {
		spec = []map[string]any{developModuleTool, rememberTool, recallTool}
		if !isWorker(ctx) {
			spec = append(spec, askUserTool)
			if canDelegate {
				spec = append(spec, askAgentTool())
			}
		}
	} else {
		spec = toolDefs(canDelegate, a.managesAssets(speaker))
		if !a.managesAssets(speaker) {
			spec = append(spec, inspectServicesTool)
		}
		if a.managesAssets(speaker) && !isWorker(ctx) {
			spec = append(spec, askUserTool)
			if canDelegate {
				spec = append(spec, askAgentTool())
			}
		}
		if !a.managesAssets(speaker) && !isWorker(ctx) {
			spec = append(spec, askUserTool, requestSecretTool)
			if a.commandsPanel(ctx, speaker) {
				spec = append(spec, panelTool, installProbeTool)
			}
			spec = append(spec, developModuleTool)
			spec = append(spec, toolDefs(false, true)...)
		}
	}
	if projectID(ctx) != "" && a.managesAssets(speaker) {
		spec = append(spec, rememberTool, recallTool)
	}
	if options(ctx).Workspace == nil && a.canMessage(ctx, speaker) {
		spec = append(spec, messageAgentTool())
	}
	if taskFrom(ctx) != nil {
		spec = append(spec, taskToolDefs()...)
	}
	// Native background jobs exist only for a bound local workspace.
	if jobToolsActive(ctx) && taskFrom(ctx) != nil {
		spec = append(spec, jobToolDefs()...)
	}
	return spec
}

// pauseForBudget runs when the tool loop hits its limit. The tool results are
// still only in this turn, so a tool-free call writes them down. That note is
// what the next "继续" sees; otherwise the model starts the task over and hits
// the same limit.
func (a *Agent) pauseForBudget(ctx context.Context, speaker model.Bot, creds credentials, messages []map[string]any) string {
	facts := budgetHandoff(messages)
	prompt := "步骤预算已经用完，不要再调用工具。根据上面已经拿到的结果，用中文写三段：做到哪了、关键发现、用户说「继续」时要做的下一步。不要复述密钥明文，不要把已经成功的命令再做一遍。"
	callMessages := append(append([]map[string]any{}, messages...), map[string]any{"role": "user", "content": prompt})
	purpose := "conversation"
	if actor, ok := ctx.Value(usageKey{}).(usageActor); ok {
		purpose = actor.Purpose
	}
	msg, err := a.chat(usageContext(ctx, speaker.ID, purpose, creds.provider), creds.base, creds.model, creds.key, callMessages, nil)
	summary := ""
	if err == nil {
		summary = strings.TrimSpace(a.redact(ctx, msg.Content))
	}
	var b strings.Builder
	b.WriteString("这轮步骤用完了，先停在这里。说「继续」会从下面接着做，已经完成的不要重做。")
	if summary != "" {
		b.WriteString("\n\n")
		b.WriteString(summary)
	}
	if facts != "" {
		b.WriteString("\n\n已完成：\n")
		b.WriteString(facts)
	}
	return clip(strings.TrimSpace(a.redact(ctx, b.String())), 4000)
}

// budgetHandoff lists the tool calls already made in this turn, so a failed
// summary still leaves the next turn something to continue from.
func budgetHandoff(messages []map[string]any) string {
	pending := map[string]toolCall{}
	var lines []string
	for _, m := range messages {
		role, _ := m["role"].(string)
		switch role {
		case "assistant":
			calls, _ := m["tool_calls"].([]toolCall)
			for _, call := range calls {
				pending[call.ID] = call
			}
		case "tool":
			id, _ := m["tool_call_id"].(string)
			out, _ := m["content"].(string)
			lines = append(lines, "- "+toolLabel(pending[id])+" → "+clip(strings.Join(strings.Fields(out), " "), 180))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	omitted := ""
	if len(lines) > 6 {
		omitted = "（更早的步骤已省略）\n"
		lines = lines[len(lines)-6:]
	}
	return omitted + strings.Join(lines, "\n")
}

func toolLabel(call toolCall) string {
	name := call.Function.Name
	if name == "" {
		name = "工具"
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
	for _, key := range []string{"command", "task", "action"} {
		s, ok := args[key].(string)
		s = strings.Join(strings.Fields(s), " ")
		if ok && s != "" {
			return name + " " + clip(s, 160)
		}
	}
	return name
}

func (a *Agent) execCall(ctx context.Context, speaker model.Bot, creds credentials, serverID string, call toolCall, canDelegate bool, tr *trace) string {
	if options(ctx).Workspace != nil {
		switch call.Function.Name {
		case "workspace":
			return a.workspaceCall(ctx, speaker, call, tr)
		case "job_start":
			return a.jobStartCall(ctx, speaker, call, tr)
		case "job_status":
			return a.jobStatusCall(ctx, speaker, call, tr)
		case "job_cancel":
			return a.jobCancelCall(ctx, speaker, call, tr)
		case "job_wait":
			// Parked by the task layer; without a task state this is unusable.
			if st := taskFrom(ctx); st != nil {
				out, parked := a.jobWaitCall(ctx, st, speaker, call, tr)
				if parked {
					return out
				}
				return out
			}
			return "job_wait 只能在持久任务里使用"
		case "ask_user", "remember", "recall":
		default:
			return "代码任务只能使用已绑定的工作区工具"
		}
	}
	if a.managesAssets(speaker) && call.Function.Name != "manage_module_record" && call.Function.Name != "manage_modules" && call.Function.Name != "ask_agent" && call.Function.Name != "ask_user" && call.Function.Name != "message_agent" {
		return "模块管理 Bot 只负责开关面板模块与维护模块数据，不能执行命令；需要服务器操作或新建自定义模块时请用 ask_agent 协同对应 Bot。"
	}
	if a.developsModules(speaker) && call.Function.Name != "develop_module" && call.Function.Name != "remember" && call.Function.Name != "recall" && call.Function.Name != "ask_user" && call.Function.Name != "ask_agent" && call.Function.Name != "message_agent" {
		return "自定义模块 Bot 只负责开发和维护自定义模块，不能执行服务器命令；需要服务器或资产操作时请用 ask_agent 协同对应 Bot。"
	}
	// The shared authorization hook runs once per call, after the capability
	// checks above and before any side effect. A refusal is returned as the
	// tool result; a parked call returns the pending notice and does not run.
	// A nil AuthorizeCall still authorizes: the central hook is the default,
	// never an optional bypass. The returned context already carries the hook
	// mark, so it must be used as-is; re-marking here would drop the effective
	// server and tool identity the handler checks.
	if !isTaskStateTool(call.Function.Name) && !CallAlreadyAuthorized(ctx, speaker.ID, serverID, call) {
		authorize := a.AuthorizeCall
		if authorize == nil {
			authorize = a.authorizeCall
		}
		next, blocked := authorize(ctx, speaker, serverID, call)
		if blocked != "" {
			return blocked
		}
		ctx = next
	}
	switch call.Function.Name {
	case "connect_server":
		ev := a.connectServer(ctx, speaker.ID, call)
		if tr != nil {
			tr.noteTool(speaker.Name, ev)
		}
		extra := ""
		if ev.OK {
			var follow struct {
				Command string `json:"command"`
				Risk    string `json:"risk"`
				Reason  string `json:"reason"`
			}
			_ = json.Unmarshal([]byte(call.Function.Arguments), &follow)
			if cmd := strings.TrimSpace(follow.Command); cmd != "" {
				next := toolCall{ID: call.ID + "-cmd", Type: "function"}
				next.Function.Name = "run_command"
				raw, _ := json.Marshal(map[string]string{"server_id": ev.ServerID, "command": cmd, "risk": follow.Risk, "reason": follow.Reason})
				next.Function.Arguments = string(raw)
				cmdEv := a.runTool(ctx, speaker.ID, ev.ServerID, next)
				if tr != nil {
					tr.noteTool(speaker.Name, cmdEv)
				}
				if cmdEv.ran {
					extra = "\n" + fmt.Sprintf(untrustedOpen, cmdEv.server) + cmdEv.Output + untrustedClose
				} else if cmdEv.Output != "" {
					extra = "\n" + cmdEv.Output
				}
			}
		}
		return ev.Output + extra
	case "inspect_services":
		return a.inspectServices(ctx, speaker, serverID, call, tr)
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
	case "message_agent":
		return a.sendMessage(ctx, speaker, serverID, call, tr)
	case "ask_user":
		if isWorker(ctx) || tr == nil {
			return "协作 Bot 不能直接向用户提问。把需要用户决定的问题写进结论，由主控来问。"
		}
		if tr.choice != nil || tr.secret != nil {
			return "这一轮已经在等用户了，等用户回应后再继续。"
		}
		c, err := ParseChoice(call.Function.Arguments)
		if err != nil {
			return err.Error()
		}
		tr.choice = &c
		return "已把选项卡片展示给用户。本轮到此结束，等用户选择后再继续。"
	case "request_secret":
		return a.requestSecret(ctx, call, tr)
	case "generate_secret":
		if a.managesAssets(speaker) || a.developsModules(speaker) {
			return "这个 Bot 不能生成密钥"
		}
		return a.generateSecret(ctx, speaker, call, tr)
	case "remember":
		return a.rememberCall(ctx, speaker, call, tr)
	case "recall":
		return a.recallCall(ctx, speaker, call)
	case "panel":
		if !a.commandsPanel(ctx, speaker) {
			return "协作任务里不能直接改管理面板，把需要改的内容写进汇报，由对话里的 Bot 来改"
		}
		ev := a.panelAct(ctx, speaker, call)
		if tr != nil {
			tr.noteTool(speaker.Name, ev)
		}
		return ev.Output
	case "install_probe":
		if !a.commandsPanel(ctx, speaker) {
			return "协作任务里不能安装探针，把需要安装的服务器写进汇报，由对话里的 Bot 来装"
		}
		ev := a.installProbe(ctx, speaker.ID, call)
		if tr != nil {
			tr.noteTool(speaker.Name, ev)
		}
		return ev.Output
	case "manage_module_record":
		if !a.managesAssets(speaker) && isWorker(ctx) {
			return "普通协作 Bot 不能直接修改资产与订阅"
		}
		return a.manageAssetWithContext(ctx, speaker.ID, call)
	case "manage_modules":
		if !a.managesAssets(speaker) && isWorker(ctx) {
			return "普通协作 Bot 不能直接使用 manage_modules"
		}
		return a.manageModulesCall(ctx, speaker, call, tr)
	case "develop_module":
		if isWorker(ctx) && !a.developsModules(speaker) {
			return "普通协作 Bot 不能直接修改自定义模块"
		}
		if a.managesAssets(speaker) {
			return "模块管理 Bot 不能新建自定义模块，请使用自定义模块 Bot"
		}
		return a.developModuleCall(ctx, speaker, call, tr)
	default:
		return "未知工具"
	}
}

func (a *Agent) handOff(ctx context.Context, speaker model.Bot, creds credentials, serverID string, call toolCall, tr *trace) string {
	// Inside a durable task, delegation must be a durable child task with its
	// own session and journal, never an inline worker turn. The runtime
	// intercepts ask_agent before dispatch; if this is reached anyway the
	// window was built without the delegation hook, so refuse loudly instead
	// of doing work the parent cannot account for.
	if taskFrom(ctx) != nil {
		return "任务内的派工必须作为持久子任务执行；当前窗口没有安装委托，未执行。"
	}
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
	// A delegated worker gets its own branch of the context: no task state, no
	// task approver, no approved-call permit. Otherwise its turn would write
	// into the leader's checkpoint and could finish the parent task. Its tool
	// calls are still gated by the shared authorization hook, which is the same
	// for leader and worker, and a park on a worker call is reported back as a
	// refusal here (the parent stays in control).
	workerOpt := options(ctx)
	workerOpt.Approver = nil
	workerCtx := context.WithValue(ctx, optionsKey{}, workerOpt)
	workerCtx = context.WithValue(workerCtx, taskStateKey{}, (*taskState)(nil))
	workerCtx = usageContext(workerCtx, target.ID, "worker", child.provider)
	text, err := a.converse(workerCtx, target, child, serverID, messages, false, tr)
	if err != nil {
		return "派工失败：" + a.safe(ctx, err.Error())
	}
	text = strings.TrimSpace(a.redact(ctx, text))
	if text == "" {
		text = "没有结论"
	}
	if tr != nil && text != "没有结论" {
		// The worker speaks in the chat under its own name, like a group member.
		tr.emit(Step{Kind: "say", Bot: target.Name, Output: text})
	}
	if text != "没有结论" && !strings.HasPrefix(text, "这轮步骤用完了") {
		title := clip(strings.Join(strings.Fields(args.Task), " "), 40)
		if title == "" {
			title = "最近一次协作"
		}
		_, _ = a.rememberWork(ctx, target.ID, title, text, "handoff")
	}
	return target.Name + "：" + text
}

func (t *trace) emit(step Step) {
	step.Task = t.agent.redact(t.ctx, step.Task)
	step.Command = t.agent.redact(t.ctx, step.Command)
	step.Output = t.agent.redact(t.ctx, step.Output)
	switch step.Kind {
	case "say":
		t.timeline = append(t.timeline, model.TurnItem{Kind: "say", Bot: step.Bot, Text: step.Output})
	case "approval":
		if step.Approval != nil {
			t.timeline = append(t.timeline, model.TurnItem{Kind: "approval", Bot: step.Bot, Approval: step.Approval})
		}
	case "delegate":
		t.steps = append(t.steps, step)
		t.timeline = append(t.timeline, model.TurnItem{Kind: "delegate", Bot: step.Bot, Target: step.Target, Task: step.Task})
	case "message":
		t.steps = append(t.steps, step)
		t.timeline = append(t.timeline, model.TurnItem{Kind: "message", Bot: step.Bot, Target: step.Target, Task: step.Task})
	default:
		t.steps = append(t.steps, step)
		t.timeline = append(t.timeline, model.TurnItem{
			Kind:     "step",
			Bot:      step.Bot,
			Name:     step.Name,
			Command:  step.Command,
			ServerID: step.ServerID,
			OK:       step.OK,
			Output:   clip(step.Output, 600),
		})
	}
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

// sessionMessages rebuilds the chat history for speaker. Lines other bots said
// in the same turn are tagged with their name so speaker never mistakes a
// worker's reply for its own words.
func (a *Agent) sessionMessages(ctx context.Context, sessionID, system, speaker string) ([]map[string]any, error) {
	items, err := a.Store.ListMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return a.chatHistory(ctx, items, system, speaker)
}

func (a *Agent) chatHistory(ctx context.Context, items []model.Message, system, speaker string) ([]map[string]any, error) {
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
		if item.Role == "assistant" && len(item.Timeline) > 0 {
			if s, ok := content.(string); ok {
				var says []string
				for _, ti := range item.Timeline {
					if ti.Kind == "say" && strings.TrimSpace(ti.Text) != "" {
						line := strings.TrimSpace(ti.Text)
						if ti.Bot != "" && ti.Bot != speaker {
							line = "[" + ti.Bot + "] " + line
						}
						says = append(says, line)
					}
				}
				if len(says) > 0 {
					joined := strings.Join(says, "\n")
					if strings.TrimSpace(s) == "" {
						content = joined
					} else {
						content = joined + "\n" + s
					}
				}
			}
		}
		if item.Choice != nil {
			if s, ok := content.(string); ok {
				content = s + choiceContext(item.Choice)
			}
		}
		if item.SecretRequest != nil {
			if s, ok := content.(string); ok {
				content = s + secretContext(item.SecretRequest)
			}
		}
		out = append(out, map[string]any{"role": item.Role, "content": content})
	}
	return out, nil
}

func (a *Agent) leaderPrompt(ctx context.Context, bot model.Bot, serverID string) string {
	if a.managesAssets(bot) {
		return a.assetPrompt(ctx, bot)
	}
	if a.developsModules(bot) {
		return a.developPrompt(ctx, bot)
	}
	var b strings.Builder
	b.WriteString(promptLeader)
	b.WriteString(promptPriorityReply)
	a.writeOperations(&b, ctx)
	b.WriteString(promptAskUser)
	b.WriteString(promptRequestSecret)
	b.WriteString(promptConnect)
	b.WriteString(promptPanel)
	b.WriteString(promptDevelopLeader)
	b.WriteString(promptProbe)
	writeExtra(&b, bot)
	a.writePeers(&b, ctx, bot.ID)
	a.writeMailbox(&b, ctx, bot, false)
	a.writeMemory(&b, ctx, bot)
	a.writePeerMemory(&b, ctx, bot.ID)
	a.writeSecrets(&b, ctx)
	a.writeServers(&b, ctx, serverID)
	a.writeAssets(&b, bot)
	a.writeDevModules(&b)
	return b.String()
}

func (a *Agent) workerPrompt(ctx context.Context, bot model.Bot, serverID string) string {
	if a.managesAssets(bot) {
		return a.assetPrompt(ctx, bot)
	}
	if a.developsModules(bot) {
		return a.developPrompt(ctx, bot)
	}
	var b strings.Builder
	fmt.Fprintf(&b, promptWorker, bot.Name, strings.TrimSpace(bot.Description))
	// promptPriorityReply is for bots that may delegate; a worker must not.
	a.writeOperations(&b, ctx)
	b.WriteString(promptConnect)
	b.WriteString(promptNoPanelSource)
	a.writeMailbox(&b, ctx, bot, true)
	a.writeMemory(&b, ctx, bot)
	writeExtra(&b, bot)
	a.writeSecrets(&b, ctx)
	a.writeServers(&b, ctx, serverID)
	a.writeAssets(&b, bot)
	return b.String()
}

// directPrompt is used when the user opened one bot and talks only to it.
// The bot works from its own memory and may ask peers for help on its own.
func (a *Agent) directPrompt(ctx context.Context, bot model.Bot, serverID string) string {
	if a.managesAssets(bot) {
		return a.assetPrompt(ctx, bot)
	}
	if a.developsModules(bot) {
		return a.developPrompt(ctx, bot)
	}
	var b strings.Builder
	fmt.Fprintf(&b, promptDirect, bot.Name, strings.TrimSpace(bot.Description))
	a.writeOperations(&b, ctx)
	b.WriteString(promptAskUser)
	b.WriteString(promptRequestSecret)
	b.WriteString(promptConnect)
	b.WriteString(promptPanel)
	b.WriteString(promptDevelopLeader)
	b.WriteString(promptProbe)
	writeExtra(&b, bot)
	a.writeHelpers(&b, ctx, bot.ID)
	a.writeMailbox(&b, ctx, bot, false)
	b.WriteString(promptDirectMemory)
	a.writeOwnMemory(&b, ctx, bot)
	a.writeSecrets(&b, ctx)
	a.writeServers(&b, ctx, serverID)
	a.writeAssets(&b, bot)
	a.writeDevModules(&b)
	return b.String()
}

func (a *Agent) assetPrompt(ctx context.Context, bot model.Bot) string {
	var b strings.Builder
	b.WriteString(promptAsset)
	b.WriteString(promptPriorityReply)
	if !isWorker(ctx) {
		b.WriteString(promptAskUser)
		if isDirect(ctx) {
			a.writeHelpers(&b, ctx, bot.ID)
		} else {
			a.writePeers(&b, ctx, bot.ID)
			a.writePeerMemory(&b, ctx, bot.ID)
		}
	}
	writeExtra(&b, bot)
	a.writeMailbox(&b, ctx, bot, isWorker(ctx))
	if projectID(ctx) != "" {
		a.writeMemory(&b, ctx, bot)
	}
	a.writeAllModulesSummary(&b, ctx)
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

// writeHelpers lists bots a direct-chat bot may ask for help. Unlike
// writePeers it does not expose their memory titles.
func (a *Agent) writeHelpers(b *strings.Builder, ctx context.Context, self string) {
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
			b.WriteString("需要时可以请这些 Bot 协助（ask_agent 当场协助，message_agent 异步通知）：\n")
		}
		n++
		fmt.Fprintf(b, "- id=%s name=%s %s\n", item.ID, item.Name, strings.TrimSpace(item.Description))
	}
	if n == 0 {
		b.WriteString("目前没有其他可协助的 Bot，需要时自己执行命令。\n")
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
	if err != nil {
		return
	}
	if len(servers) == 0 {
		b.WriteString("目前没有已接入的服务器。用户这条消息里给出地址和登录信息时调用 connect_server，不要只回复操作步骤。\n")
		return
	}
	if options(ctx).RestrictServers && serverID == "" {
		b.WriteString("用户尚未手动选择已有服务器。用户点名某台时直接 run_command，审批卡片会请用户确认这台机器。用户这条消息里如果给出了新的地址和登录信息，调用 connect_server；接入成功后本轮可以操作这台新机器。\n")
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
	ask := askAgentTool()
	record := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "manage_module_record",
			"description": "读写资产与订阅。action 取 list、create、update、delete。update 只传要改的字段。金额用 amount_yuan，日期用 YYYY-MM-DD。不要填写密码。",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":       map[string]any{"type": "string", "enum": []string{"list", "create", "update", "delete"}},
					"id":           map[string]string{"type": "string"},
					"name":         map[string]string{"type": "string"},
					"server_id":    map[string]string{"type": "string"},
					"provider":     map[string]string{"type": "string", "description": "订阅服务商或服务器提供商；支持软件会员、域名等，不要求关联服务器"},
					"region":       map[string]string{"type": "string"},
					"plan":         map[string]string{"type": "string"},
					"cycle":        map[string]any{"type": "string", "enum": []string{"monthly", "quarterly", "yearly", "once"}},
					"currency":     map[string]string{"type": "string", "description": "原币种，如 USD、CNY、CAD。新订阅未指定时默认 USD；修改时保留原币种"},
					"amount_yuan":  map[string]string{"type": "number", "description": "按 currency 填写本期金额，不限人民币"},
					"paid_at":      map[string]string{"type": "string"},
					"starts_at":    map[string]string{"type": "string"},
					"expires_at":   map[string]string{"type": "string"},
					"auto_renew":   map[string]string{"type": "boolean"},
					"status":       map[string]any{"type": "string", "enum": []string{"active", "cancelled"}},
					"ip":           map[string]string{"type": "string"},
					"specs":        map[string]string{"type": "string", "description": "CPU、内存、磁盘等"},
					"traffic_gb":   map[string]string{"type": "number", "description": "每月流量额度，单位 GB；不限量填 0"},
					"traffic_mode": map[string]any{"type": "string", "enum": []string{"both", "out", "in", "max"}, "description": "流量计费方式：both 双向合计，out 只算出站，in 只算入站，max 取入站和出站中较大者"},
					"order_no":     map[string]string{"type": "string"},
					"account":      map[string]string{"type": "string", "description": "提供商账号备注，不要填密码"},
					"contact":      map[string]string{"type": "string"},
					"tags":         map[string]string{"type": "string"},
					"notes":        map[string]string{"type": "string"},
				},
			},
		},
	}
	connect := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "connect_server",
			"description": toolConnectDesc,
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":      map[string]string{"type": "string", "description": "页面上的名称，可省略"},
					"host":      map[string]string{"type": "string"},
					"port":      map[string]string{"type": "integer"},
					"username":  map[string]string{"type": "string"},
					"auth_type": map[string]any{"type": "string", "enum": []string{"password", "private_key"}},
					"secret":    map[string]string{"type": "string", "description": "密码、私钥，或 {{secret:名称}}"},
					"role":      map[string]string{"type": "string"},
					"command":   map[string]string{"type": "string", "description": "接入成功后可选执行的一条命令"},
					"risk":      map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}, "description": toolRiskDesc},
					"reason":    map[string]string{"type": "string", "description": toolReasonDesc},
				},
				"required": []string{"host", "secret"},
			},
		},
	}
	out := []map[string]any{}
	if !manage {
		out = append(out, connect, command, generateSecretTool, rememberTool, recallTool)
		if delegate {
			out = append(out, ask)
		}
	} else {
		out = append(out, record, manageModulesTool)
	}
	return out
}

func askAgentTool() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "ask_agent",
			"description": "把一件具体工作交给另一个 Bot。它会带着自己的记忆完成，并把这次结论记进自己的记忆。",
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
