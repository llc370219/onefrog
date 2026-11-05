package bot

import (
	"context"
	"errors"
	"strings"

	"onefrog/internal/preferences"
)

// isAutomation reports whether this turn is a scheduled automation run.
func isAutomation(ctx context.Context) bool {
	actor, ok := ctx.Value(usageKey{}).(usageActor)
	return ok && actor.Purpose == "automation"
}

// RunAutomation runs one scheduled task in sessionID. Nobody is watching the
// turn, so it works like an inbox turn: no ask_user, no delegation, and
// commands that need confirmation are refused. "Off" approval is raised to
// smart so a background run never executes risky commands unchecked.
func (a *Agent) RunAutomation(ctx context.Context, botID, serverID, sessionID, title, prompt, ownerID, mode string) (Result, error) {
	copy := *a
	agent := &copy
	if a.SettingsLive != nil {
		agent.Settings = a.SettingsLive.Frozen()
	} else if a.Settings != nil {
		agent.Settings = a.Settings.Frozen()
	}
	if strings.TrimSpace(mode) == "" {
		mode = "default"
	}
	opt := Options{Mode: "direct", OwnerID: ownerID, RestrictServers: true, ApprovalMode: mode}
	ctx = context.WithValue(ctx, optionsKey{}, opt)
	ctx = usageContext(ctx, botID, "automation", "")
	if agent.approvalMode(ctx) == preferences.ModeOff {
		opt.ApprovalMode = preferences.ModeSmart
		ctx = context.WithValue(ctx, optionsKey{}, opt)
	}
	b, err := agent.Store.GetBot(ctx, botID)
	if err != nil {
		return Result{}, errors.New("Bot 不存在")
	}
	if !agent.allowedBot(ctx, b.ID) {
		return Result{}, errors.New("该 Bot 已停用")
	}
	if agent.Settings != nil {
		resolved, _, err := agent.credentialsFor(ctx, b, false)
		if err != nil {
			return Result{}, err
		}
		b = resolved
	}
	if strings.TrimSpace(b.BaseURL) == "" {
		return Result{}, errors.New("Bot 还没有接上模型")
	}
	if title = strings.TrimSpace(title); title == "" {
		title = "自动化"
	}
	sess, err := agent.Store.EnsureSession(ctx, sessionID, b.ID, "自动化 · "+title)
	if err != nil {
		return Result{}, err
	}
	if sess.BotID != b.ID {
		return Result{}, errors.New("自动化对话的 Bot 不一致")
	}
	return agent.ReplyWithOptions(ctx, b.ID, serverID, sess.ID, prompt, opt, nil)
}
