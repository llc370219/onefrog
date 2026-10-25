package bot

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"onefrog/internal/attachment"
	"onefrog/internal/audit"
	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"strings"
)

type Options struct {
	ApprovalRoute   *preferences.Route `json:"approval_route,omitempty"`
	ModelRoute      *preferences.Route `json:"model_route,omitempty"`
	ExcludedBotIDs  []string           `json:"excluded_bot_ids,omitempty"`
	ApprovalMode    string             `json:"approval_mode,omitempty"`
	AttachmentIDs   []string           `json:"attachment_ids,omitempty"`
	OwnerID         string             `json:"-"`
	RestrictServers bool               `json:"-"`
	// Approver asks a human to allow one command (smart or manual mode). It is
	// set by interactive transports such as the WebSocket chat.
	Approver Approver `json:"-"`
}

// ApprovalRequest is shown to the operator before a command runs.
type ApprovalRequest struct {
	ID         string `json:"id"`
	BotID      string `json:"bot_id"`
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

// Approver returns whether the operator allowed the command.
type Approver func(ctx context.Context, req ApprovalRequest) (allow bool, reason string)
type optionsKey struct{}
type usageKey struct{}
type usageActor struct{ BotID, Purpose, ProviderID string }

func options(ctx context.Context) Options { v, _ := ctx.Value(optionsKey{}).(Options); return v }
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
	if a.Settings != nil {
		copy := *a
		copy.Settings = a.Settings.Frozen()
		a = &copy
	}
	if opt.ModelRoute != nil && opt.ModelRoute.ProviderID == "" && opt.ModelRoute.Model == "" {
		opt.ModelRoute = nil
	}
	if opt.ModelRoute != nil {
		if a.Settings == nil {
			return Result{}, errors.New("请先配置模型渠道")
		}
		if err := a.Settings.ValidateSelectable(*opt.ModelRoute); err != nil {
			return Result{}, err
		}
	}
	if opt.ApprovalMode != "default" {
		mode, ok := preferences.NormalizeMode(opt.ApprovalMode)
		if !ok {
			return Result{}, errors.New("审核方式无效")
		}
		opt.ApprovalMode = mode
	}
	if opt.ApprovalMode == "model" {
		if a.Settings == nil {
			return Result{}, errors.New("请先在设置中配置审批模型")
		}
		r := a.Settings.Snapshot().Approval.Route
		if opt.ApprovalRoute != nil {
			r = *opt.ApprovalRoute
			if err := a.Settings.ValidateSelectable(r); err != nil {
				return Result{}, err
			}
		}
		if r.ProviderID == "" || r.Model == "" {
			return Result{}, errors.New("请先在设置中选择独立审批模型")
		}
		if _, err := a.Settings.Provider(r.ProviderID); err != nil {
			return Result{}, errors.New("审批渠道不可用")
		}
	}
	for _, id := range opt.ExcludedBotIDs {
		if _, err := a.Store.GetBot(ctx, id); err != nil {
			return Result{}, errors.New("排除的 Bot 不存在，请刷新")
		}
	}
	if len(opt.AttachmentIDs) > attachment.MaxFiles {
		return Result{}, errors.New("每轮最多 4 个附件")
	}
	ctx = context.WithValue(ctx, optionsKey{}, opt)
	var refs []model.Attachment
	seen := map[string]bool{}
	size := 0
	for _, id := range opt.AttachmentIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if a.Files == nil {
			return Result{}, errors.New("附件服务未配置")
		}
		f, err := a.Files.Get(opt.OwnerID, id)
		if err != nil {
			return Result{}, err
		}
		size += f.Size
		refs = append(refs, f.Attachment)
	}
	if size > attachment.MaxTotal {
		return Result{}, errors.New("附件合计不能超过 12 MB")
	}
	if strings.TrimSpace(content) == "" && len(refs) > 0 {
		content = "请分析这些附件。"
	}
	return a.replyTurn(ctx, botID, serverID, sessionID, content, refs, sink)
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
