package bot

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
)

// Bot-to-bot messages. ask_agent is synchronous: the caller waits for the
// answer inside its own turn. message_agent is asynchronous: the note lands in
// the target bot's inbox session ("inbox-<botID>") and the target handles it
// later in a background turn, with its own history and memory.

const (
	maxHops          = 3    // a chain of messages started by one user turn
	maxSendsPerTurn  = 3    // per user-facing turn
	maxSendsPerInbox = 1    // per background inbox turn
	maxMessageRunes  = 2000 // one message body
	inboxPrefix      = "inbox-"
	inboxTimeout     = 10 * time.Minute
)

type hopKey struct{}

func hop(ctx context.Context) int { n, _ := ctx.Value(hopKey{}).(int); return n }

func inboxSession(botID string) string { return inboxPrefix + botID }

// IsInboxSession reports whether a chat session is a bot's inbox.
func IsInboxSession(id string) bool { return strings.HasPrefix(id, inboxPrefix) }

// isInbox reports whether this turn is a background inbox turn.
func isInbox(ctx context.Context) bool {
	actor, ok := ctx.Value(usageKey{}).(usageActor)
	return ok && actor.Purpose == "inbox"
}

func sendLimit(ctx context.Context) int {
	if isInbox(ctx) || isAutomation(ctx) {
		return maxSendsPerInbox
	}
	return maxSendsPerTurn
}

func messageAgentTool() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "message_agent",
			"description": "给另一个 Bot 发一条消息。消息进入对方的收件箱，对方稍后在后台带着自己的记忆处理；本工具立即返回，不会等对方回复。本轮就需要对方结果时改用派工。",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"bot_id":  map[string]string{"type": "string", "description": "Bot 的 id 或名称"},
					"message": map[string]string{"type": "string", "description": "要告诉对方的事，写清背景和希望对方做什么"},
				},
				"required": []string{"bot_id", "message"},
			},
		},
	}
}

func (a *Agent) canMessage(ctx context.Context, speaker model.Bot) bool {
	return hop(ctx) < maxHops && a.hasPeers(ctx, speaker.ID)
}

func (a *Agent) sendMessage(ctx context.Context, speaker model.Bot, serverID string, call toolCall, tr *trace) string {
	var args struct {
		BotID   string `json:"bot_id"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
	msg := strings.TrimSpace(args.Message)
	if r := []rune(msg); len(r) > maxMessageRunes {
		msg = string(r[:maxMessageRunes])
	}
	if msg == "" {
		return "消息不能为空"
	}
	target, err := a.findBot(ctx, strings.TrimSpace(args.BotID))
	if err != nil {
		return "没有这个 Bot"
	}
	if !a.allowedBot(ctx, target.ID) {
		return "该 Bot 已停用或被用户排除，不能发消息"
	}
	if target.ID == speaker.ID {
		return "不能给自己发消息"
	}
	if hop(ctx) >= maxHops {
		return "这条消息链已经转了太多次，不能再发。把结论写进回复。"
	}
	if tr == nil {
		return "当前不能发消息"
	}
	if tr.sent >= sendLimit(ctx) {
		return "这一轮发消息的次数已用完，把剩下的内容写进回复。"
	}
	tr.sent++
	sess, err := a.Store.EnsureSession(ctx, inboxSession(target.ID), target.ID, target.Name+" 收件箱")
	if err != nil || sess.BotID != target.ID {
		return "对方收件箱不可用"
	}
	body := "[来自 " + speaker.Name + "（id=" + speaker.ID + "）] " + a.redact(ctx, msg)
	if _, err := a.Store.AddMessage(ctx, model.Message{SessionID: sess.ID, Role: "user", FromBot: speaker.ID, Content: body}); err != nil {
		return "对方收件箱不可用"
	}
	tr.emit(Step{Kind: "message", Bot: speaker.Name, Target: target.Name, Task: msg})
	if a.Mail != nil {
		opt := options(ctx)
		a.Mail.enqueue(mailJob{
			agent:    a,
			botID:    target.ID,
			serverID: serverID,
			hop:      hop(ctx) + 1,
			ownerID:  opt.OwnerID,
			mode:     opt.ApprovalMode,
			route:    opt.ApprovalRoute,
			excluded: append([]string(nil), opt.ExcludedBotIDs...),
		})
	}
	return "已送达 " + target.Name + " 的收件箱，对方会在后台处理。不要等待，继续完成你自己的事。"
}

type mailJob struct {
	agent    *Agent
	botID    string
	serverID string
	hop      int
	ownerID  string
	mode     string
	route    *preferences.Route
	excluded []string
}

// Mailbox runs background inbox turns, at most one at a time per bot.
type Mailbox struct {
	mu      sync.Mutex
	queue   map[string][]mailJob
	running map[string]bool
	wg      sync.WaitGroup
}

func NewMailbox() *Mailbox {
	return &Mailbox{queue: map[string][]mailJob{}, running: map[string]bool{}}
}

// Wait blocks until every queued inbox turn has finished.
func (m *Mailbox) Wait() { m.wg.Wait() }

func (m *Mailbox) enqueue(job mailJob) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue[job.botID] = append(m.queue[job.botID], job)
	if m.running[job.botID] {
		return
	}
	m.running[job.botID] = true
	m.wg.Add(1)
	go m.drain(job.botID)
}

func (m *Mailbox) drain(botID string) {
	defer m.wg.Done()
	for {
		m.mu.Lock()
		jobs := m.queue[botID]
		if len(jobs) == 0 {
			delete(m.queue, botID)
			delete(m.running, botID)
			m.mu.Unlock()
			return
		}
		job := jobs[0]
		m.queue[botID] = jobs[1:]
		m.mu.Unlock()
		job.agent.wake(job)
	}
}

// wake runs one background turn in the target bot's inbox. Nobody is watching
// it, so there is no Approver: commands that need confirmation are refused.
func (a *Agent) wake(job mailJob) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("inbox %s: panic: %v", job.botID, r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), inboxTimeout)
	defer cancel()
	copy := *a
	agent := &copy
	if a.SettingsLive != nil {
		agent.Settings = a.SettingsLive.Frozen()
	} else if a.Settings != nil {
		agent.Settings = a.Settings.Frozen()
	}
	opt := Options{OwnerID: job.ownerID, ExcludedBotIDs: job.excluded, ApprovalMode: job.mode, ApprovalRoute: job.route, RestrictServers: true}
	ctx = context.WithValue(ctx, optionsKey{}, opt)
	if agent.approvalMode(ctx) == preferences.ModeOff {
		opt.ApprovalMode = preferences.ModeSmart
		ctx = context.WithValue(ctx, optionsKey{}, opt)
	}
	ctx = context.WithValue(ctx, hopKey{}, job.hop)
	ctx = context.WithValue(ctx, taskKey{}, "")
	ctx = withTaint(ctx)
	ctx = withGrants(ctx)
	ctx = usageContext(ctx, job.botID, "inbox", "")
	b, err := agent.Store.GetBot(ctx, job.botID)
	if err != nil || !agent.allowedBot(ctx, b.ID) {
		return
	}
	b, _, err = agent.credentialsFor(ctx, b, false)
	if err != nil || strings.TrimSpace(b.BaseURL) == "" {
		return
	}
	session := inboxSession(b.ID)
	items, err := agent.Store.ListMessages(ctx, session)
	if err != nil || len(items) == 0 || items[len(items)-1].Role != "user" {
		return
	}
	for i := len(items) - 1; i >= 0 && items[i].Role == "user"; i-- {
		addUntrusted(ctx, items[i].Content)
	}
	if _, err := agent.lead(ctx, b, job.serverID, session, nil); err != nil {
		log.Printf("inbox %s: %s", b.ID, agent.safe(ctx, err.Error()))
	}
}

// writeMailbox tells the bot it can message other bots. listPeers is for
// prompts that do not already list them.
func (a *Agent) writeMailbox(b *strings.Builder, ctx context.Context, self model.Bot, listPeers bool) {
	if !a.canMessage(ctx, self) {
		return
	}
	b.WriteString(promptMailbox)
	if isInbox(ctx) {
		b.WriteString(promptInbox)
	}
	if !listPeers {
		return
	}
	bots, err := a.Store.ListBots(ctx)
	if err != nil {
		return
	}
	b.WriteString("可以发消息的 Bot：\n")
	for _, item := range bots {
		if item.ID == self.ID || !a.allowedBot(ctx, item.ID) {
			continue
		}
		b.WriteString("- id=" + item.ID + " name=" + item.Name + "\n")
	}
}
