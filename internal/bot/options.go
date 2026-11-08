package bot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"onefrog/internal/attachment"
	"onefrog/internal/audit"
	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/workspace"
	"strings"
)

// normalizeMode treats an empty mode and "team" as the same scope, so a session
// cannot be switched between direct and team by omitting the field.
func normalizeMode(mode string) string {
	if mode == "" {
		return "team"
	}
	return mode
}

// sessionOwner reads the owner marker saved next to a session's chat options.
// A legacy session has none and is unowned until the first task claims it.
func sessionOwner(raw json.RawMessage) string {
	var saved struct {
		OwnerID string `json:"owner_id"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &saved) != nil {
		return ""
	}
	return strings.TrimSpace(saved.OwnerID)
}

type Options struct {
	Workspace      *workspace.Target  `json:"workspace,omitempty"`
	ApprovalRoute  *preferences.Route `json:"approval_route,omitempty"`
	ModelRoute     *preferences.Route `json:"model_route,omitempty"`
	ExcludedBotIDs []string           `json:"excluded_bot_ids,omitempty"`
	ProjectTitle   string             `json:"project_title,omitempty"`
	// Mode is "direct" (one-to-one chat with a single bot and its own memory)
	// or "team" (the default: the lead bot pulls other bots in as needed).
	Mode          string   `json:"mode,omitempty"`
	ApprovalMode  string   `json:"approval_mode,omitempty"`
	AttachmentIDs []string `json:"attachment_ids,omitempty"`
	// ChoiceAnswer answers an ask_user card in this session; it replaces Content.
	ChoiceAnswer *ChoiceReply `json:"choice_answer,omitempty"`
	// SecretAnswer saves a password card. Value is encrypted and then dropped;
	// it is never written into chat options or the transcript.
	SecretAnswer    *SecretReply `json:"secret_answer,omitempty"`
	OwnerID         string       `json:"-"`
	RestrictServers bool         `json:"-"`
	// Approver asks a human to allow one command (smart or manual mode). It is
	// set by interactive transports such as the WebSocket chat.
	Approver Approver `json:"-"`
	// TaskID/ProjectTitle are internal: a durable task runs with its stored
	// session and never writes a new project title.
	TaskID string `json:"-"`
}

// currentCallKey names the tool call being executed, and approvedCallKey names
// the single call whose approval the operator already gave. An approver may
// allow only when the two match, so a delegated or nested call in the same
// window (connect_server's follow-up command, ask_agent's worker, a re-issued
// command) still needs its own decision. It is not a global "approved" flag.
type currentCallKey struct{}

// callIdentity is the immutable identity of the call currently being
// authorized. Handlers that only receive a display label compare against it,
// so a nested call that reuses a call id with another tool or arguments cannot
// inherit the grant.
type callIdentity struct {
	CallID string
	Tool   string
	Hash   string
}

type callIdentityKey struct{}

func withCallIdentity(ctx context.Context, id callIdentity) context.Context {
	return context.WithValue(ctx, callIdentityKey{}, id)
}

func callIdentityOf(ctx context.Context) (callIdentity, bool) {
	id, ok := ctx.Value(callIdentityKey{}).(callIdentity)
	return id, ok
}

type approvedCallKey struct{}

func withCurrentCall(ctx context.Context, callID string) context.Context {
	return context.WithValue(ctx, currentCallKey{}, callID)
}

func withApprovedCall(ctx context.Context, callID string) context.Context {
	return context.WithValue(ctx, approvedCallKey{}, callID)
}

// callAlreadyApproved reports whether the call now being authorized is exactly
// the one the operator approved for this window.
func callAlreadyApproved(ctx context.Context) bool {
	cur, _ := ctx.Value(currentCallKey{}).(string)
	ok, _ := ctx.Value(approvedCallKey{}).(string)
	return cur != "" && cur == ok
}

// hookMark records the one call whose shared authorization already ran. It is
// bound to the speaker, the effective target, the tool, the call id and the
// argument hash, so a delegated or nested call never inherits the permit.
type hookMark struct {
	BotID    string
	ServerID string
	Tool     string
	CallID   string
	Hash     string
}

type hookMarkKey struct{}

func withHookMark(ctx context.Context, mark hookMark) context.Context {
	return context.WithValue(ctx, hookMarkKey{}, mark)
}

// hookMarked reports whether the mark covers this call. Every identity field
// must be present and match: speaker, effective target, tool, call id and
// argument hash. An incomplete mark never matches, so a mark built without the
// target or tool cannot authorize anything.
func (m hookMark) marked(botID, serverID string, call toolCall) bool {
	if m.BotID == "" || m.ServerID == "" || m.Tool == "" || m.CallID == "" || m.Hash == "" {
		return false
	}
	if serverID == "" {
		return false
	}
	return m.BotID == botID &&
		m.ServerID == serverID &&
		m.Tool == call.Function.Name &&
		m.CallID == call.ID &&
		m.Hash == argsHash(call)
}

// hookAlreadyRan reports whether the shared authorization already ran for this
// call, using the effective target recorded in the mark itself.
func hookAlreadyRan(ctx context.Context, botID string, call toolCall) bool {
	mark, ok := ctx.Value(hookMarkKey{}).(hookMark)
	if !ok {
		return false
	}
	return mark.marked(botID, mark.ServerID, call)
}

// hookAlreadyRanFor is the strict form: the caller must state the effective
// target it is about to use.
func hookAlreadyRanFor(ctx context.Context, botID, serverID string, call toolCall) bool {
	mark, ok := ctx.Value(hookMarkKey{}).(hookMark)
	if !ok {
		return false
	}
	return mark.marked(botID, serverID, call)
}

// pendingSentinel is the prefix the shared authorization hook returns when the
// call is parked for the operator: the call has not run.
const pendingSentinel = "onefrog:pending"

// askSentinel is the reason prefix a durable approver returns when the
// operator asked to decide later. The call must then be parked, never run.
const askSentinel = "task:ask"

// ApprovalRequest is shown to the operator before a command runs.
type ApprovalRequest struct {
	ID         string `json:"id"`
	SingleUse  bool   `json:"single_use,omitempty"`
	BotID      string `json:"bot_id"`
	BotName    string `json:"bot_name,omitempty"`
	ServerID   string `json:"server_id"`
	ServerName string `json:"server_name"`
	Command    string `json:"command"`
	// Warning is the first rule finding, kept for simple clients.
	Warning string `json:"warning,omitempty"`
	// Mode is "smart" or "manual".
	Mode string `json:"mode,omitempty"`
	// Risk is the final level (low/medium/high): the higher of the rule
	// classifier and the level the model declared.
	Risk string `json:"risk,omitempty"`
	// Reasons are human-readable lines explaining why confirmation is needed.
	Reasons     []string        `json:"reasons,omitempty"`
	Findings    []audit.Finding `json:"findings,omitempty"`
	ModelRisk   string          `json:"model_risk,omitempty"`
	ModelReason string          `json:"model_reason,omitempty"`
	Untrusted   string          `json:"untrusted,omitempty"`
}

// Decision is the operator's answer to one approval card. Ask means "park the
// task and wait"; the call is not executed until a decision is recorded.
type Decision struct {
	Allow bool
	Ask   bool
	// Always allows the same kind of action for the rest of this task.
	Always bool
	Reason string
}

// Approver returns whether the operator allowed the command. A nil Approver
// means nobody is watching: risky calls are refused, never silently allowed.
type Approver func(ctx context.Context, req ApprovalRequest) (allow bool, reason string)
type optionsKey struct{}
type usageKey struct{}
type usageActor struct{ BotID, Purpose, ProviderID string }

func options(ctx context.Context) Options { v, _ := ctx.Value(optionsKey{}).(Options); return v }

// isDirect reports a user-facing one-to-one chat. Delegated and inbox turns
// are never direct; they keep the worker behaviour.
func isDirect(ctx context.Context) bool { return options(ctx).Mode == "direct" && !isWorker(ctx) }

// commandsPanel reports whether speaker may change the panel or install probes.
// Any bot the user talks to directly may; a delegated worker only does the one
// task it was handed, and asset bots never run commands.
func (a *Agent) commandsPanel(ctx context.Context, speaker model.Bot) bool {
	return !isWorker(ctx) && !a.managesAssets(speaker)
}
func usageContext(ctx context.Context, id, purpose, provider string) context.Context {
	return context.WithValue(ctx, usageKey{}, usageActor{id, purpose, provider})
}
func (a *Agent) allowedBot(ctx context.Context, id string) bool {
	if a.Settings != nil && !a.Settings.BotEnabled(id) {
		return false
	}
	for _, excluded := range options(ctx).ExcludedBotIDs {
		if excluded == id {
			return false
		}
	}
	return true
}
func (a *Agent) ReplyWithOptions(ctx context.Context, botID, serverID, sessionID, content string, opt Options, sink func(Step)) (Result, error) {
	done, gateErr := a.Gate.Enter()
	if gateErr != nil {
		return Result{}, gateErr
	}
	defer done()
	if a.Settings != nil {
		copy := *a
		copy.Settings = a.Settings.Frozen()
		a = &copy
	}
	opt, refs, err := a.Preflight(ctx, opt)
	if err != nil {
		return Result{}, err
	}
	if (opt.ChoiceAnswer != nil || opt.SecretAnswer != nil) && len(refs) > 0 {
		return Result{}, errors.New("回答卡片时不能附带附件")
	}
	if strings.TrimSpace(content) == "" && len(refs) > 0 {
		content = "请分析这些附件。"
	}
	ctx = context.WithValue(ctx, optionsKey{}, opt)
	return a.replyTurn(ctx, botID, serverID, sessionID, content, refs, sink)
}

// Preflight validates and normalizes a turn's per-chat options exactly as the
// interactive chat does: model route, approval mode, excluded bots, mode,
// attachment ownership and total size. Both the ordinary turn and a durable
// task use it, so the background path cannot skip a check.
func (a *Agent) Preflight(ctx context.Context, opt Options) (Options, []model.Attachment, error) {
	if opt.ModelRoute != nil && opt.ModelRoute.ProviderID == "" && opt.ModelRoute.Model == "" {
		opt.ModelRoute = nil
	}
	if opt.ModelRoute != nil {
		if a.Settings == nil {
			return opt, nil, errors.New("请先配置模型渠道")
		}
		if err := a.Settings.ValidateSelectable(*opt.ModelRoute); err != nil {
			return opt, nil, err
		}
	}
	if opt.ApprovalMode != "default" {
		mode, ok := preferences.NormalizeMode(opt.ApprovalMode)
		if !ok {
			return opt, nil, errors.New("审核方式无效")
		}
		opt.ApprovalMode = mode
	}
	if opt.ApprovalMode == "model" {
		if a.Settings == nil {
			return opt, nil, errors.New("请先在设置中配置审批模型")
		}
		r := a.Settings.Snapshot().Approval.Route
		if opt.ApprovalRoute != nil {
			r = *opt.ApprovalRoute
			if err := a.Settings.ValidateSelectable(r); err != nil {
				return opt, nil, err
			}
		}
		if r.ProviderID == "" || r.Model == "" {
			return opt, nil, errors.New("请先在设置中选择独立审批模型")
		}
		if _, err := a.Settings.Provider(r.ProviderID); err != nil {
			return opt, nil, errors.New("审批渠道不可用")
		}
	}
	for _, id := range opt.ExcludedBotIDs {
		if _, err := a.Store.GetBot(ctx, id); err != nil {
			return opt, nil, errors.New("排除的 Bot 不存在，请刷新")
		}
	}
	switch opt.Mode {
	case "", "team":
		opt.Mode = ""
	case "direct":
		opt.ExcludedBotIDs = nil
	default:
		return opt, nil, errors.New("对话模式无效")
	}
	if len(opt.AttachmentIDs) > attachment.MaxFiles {
		return opt, nil, errors.New("每轮最多 4 个附件")
	}
	var refs []model.Attachment
	seen := map[string]bool{}
	size := 0
	for _, id := range opt.AttachmentIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if a.Files == nil {
			return opt, nil, errors.New("附件服务未配置")
		}
		f, err := a.Files.Get(opt.OwnerID, id)
		if err != nil {
			return opt, nil, err
		}
		size += f.Size
		refs = append(refs, f.Attachment)
	}
	if size > attachment.MaxTotal {
		return opt, nil, errors.New("附件合计不能超过 12 MB")
	}
	if opt.Workspace != nil {
		if opt.Workspace.Kind == "local" {
			// Native local workspaces: the root must sit inside the operator's
			// allowlist. Task users and the model can never add roots.
			if a.LocalWorkspace == nil {
				return opt, nil, errors.New("本地工作区未启用")
			}
			if opt.Workspace.ServerID != "local" {
				return opt, nil, errors.New("本地工作区的 server_id 必须是 local")
			}
			if _, err := a.LocalWorkspace.Roots().Pick(opt.Workspace.Root); err != nil {
				return opt, nil, err
			}
		} else if _, err := a.Workspace.Server(ctx, *opt.Workspace); err != nil {
			return opt, nil, err
		}
	}
	return opt, refs, nil
}
func (a *Agent) messageContent(ctx context.Context, item model.Message) (any, error) {
	text := a.redact(ctx, item.Content)
	parts := []map[string]any{}
	for _, ref := range item.Attachments {
		if a.Files == nil {
			return nil, errors.New("附件服务未配置")
		}
		f, err := a.Files.Get(options(ctx).OwnerID, ref.ID)
		if err != nil {
			return nil, fmt.Errorf("历史附件 %s 不可用，请新建对话或重新上传", ref.Name)
		}
		if strings.HasPrefix(f.Mime, "image/") {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + f.Mime + ";base64," + base64.StdEncoding.EncodeToString(f.Data), "detail": "auto"}})
		} else if f.Mime == "application/pdf" {
			parts = append(parts, map[string]any{"type": "file", "file": map[string]string{"filename": f.Name, "file_data": "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(f.Data)}})
		} else {
			body := a.redact(ctx, string(f.Data))
			addUntrusted(ctx, body)
			text += "\n\n<attachment name=" + fmt.Sprintf("%q", f.Name) + ">\n" + body + "\n</attachment>"
		}
	}
	if len(parts) == 0 {
		return text, nil
	}
	return append([]map[string]any{{"type": "text", "text": text}}, parts...), nil
}
