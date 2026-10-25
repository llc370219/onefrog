package bot

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"onefrog/internal/audit"
	"onefrog/internal/id"
	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/store"
)

type taskKey struct{}
type Review struct {
	Allow      bool   `json:"allow"`
	Reason     string `json:"reason"`
	Risk       string `json:"risk"`
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
	Mode       string `json:"mode"`
	// Smart-approval details, kept in the audit history.
	Findings    []audit.Finding `json:"findings,omitempty"`
	ModelRisk   string          `json:"model_risk,omitempty"`
	ModelReason string          `json:"model_reason,omitempty"`
	Untrusted   string          `json:"untrusted,omitempty"`
}

func (a *Agent) credentialsFor(ctx context.Context, b model.Bot, worker bool) (model.Bot, credentials, error) {
	if a.Settings != nil {
		route := a.Settings.RouteFor(b, worker)
		if !worker && options(ctx).ModelRoute != nil {
			route = *options(ctx).ModelRoute
		}
		if route.ProviderID != "" {
			p, err := a.Settings.Provider(route.ProviderID)
			if err != nil {
				return b, credentials{}, err
			}
			key := ""
			if !strings.HasPrefix(p.AuthKind, "oauth-") {
				key, err = a.Settings.Key(route.ProviderID)
			}
			if err != nil {
				return b, credentials{}, err
			}
			b.BaseURL = p.BaseURL
			b.ModelName = route.Model
			return b, credentials{base: p.BaseURL, model: route.Model, key: key, provider: route.ProviderID}, nil
		}
	}
	key, err := a.apiKey(ctx, b.APIKeySecretID)
	return b, credentials{base: b.BaseURL, model: b.ModelName, key: key}, err
}

// approvalMode is the effective mode for this turn: the per-chat override, else
// the saved setting. An unchosen setting behaves like smart approval. Without
// settings (tests/embedded use) it is "off".
func (a *Agent) approvalMode(ctx context.Context) string {
	if mode, _ := preferences.NormalizeMode(options(ctx).ApprovalMode); mode != "" && mode != "default" {
		return mode
	}
	if a.Settings == nil {
		return preferences.ModeOff
	}
	if mode, _ := preferences.NormalizeMode(a.Settings.Snapshot().Approval.Mode); mode != preferences.ModeUnset {
		return mode
	}
	return preferences.ModeSmart
}

var riskRank = map[string]int{"low": 1, "medium": 2, "high": 3}

// riskGate is the smart-approval verdict for one command.
type riskGate struct {
	Ask         bool
	Level       string // final risk: the higher of the rules and the model
	Findings    []audit.Finding
	ModelRisk   string // as declared by the model; "" when missing or invalid
	ModelReason string
	Untrusted   string // why the command looks driven by untrusted content
	Reasons     []string
}

var secretPlaceholder = regexp.MustCompile(`\{\{secret:[^}]*\}\}`)

// assess combines the server-side rule classifier, the risk the model declared
// and the prompt-injection guard. Any one of them can require confirmation;
// the model can only raise the level, never lower a rule hit.
func (a *Agent) assess(ctx context.Context, command, modelRisk, modelReason string) riskGate {
	rules := audit.Classify(secretPlaceholder.ReplaceAllString(command, "SECRET"))
	g := riskGate{Findings: rules.Findings, Level: rules.Level, ModelReason: a.redact(ctx, clip(strings.TrimSpace(modelReason), 300))}
	modelRisk = strings.ToLower(strings.TrimSpace(modelRisk))
	if _, ok := riskRank[modelRisk]; ok {
		g.ModelRisk = modelRisk
	}
	declared := g.ModelRisk
	if declared == "" {
		declared = "medium"
	}
	if riskRank[declared] > riskRank[g.Level] {
		g.Level = declared
	}
	for _, f := range rules.Findings {
		g.Reasons = append(g.Reasons, "规则："+f.Label+"："+f.Detail)
	}
	switch g.ModelRisk {
	case "":
		g.Reasons = append(g.Reasons, "AI 没有声明风险等级，按需要确认处理")
	case "medium":
		g.Reasons = append(g.Reasons, "AI 自评风险：中")
	case "high":
		g.Reasons = append(g.Reasons, "AI 自评风险：高")
	}
	if why := untrustedSource(ctx, command); why != "" {
		g.Untrusted = why
		g.Reasons = append(g.Reasons, why)
		if riskRank[g.Level] < riskRank["medium"] {
			g.Level = "medium"
		}
	}
	g.Ask = rules.Risky || g.ModelRisk != "low" || g.Untrusted != ""
	return g
}

// askOperator shows the approval card and waits for the operator. Without an
// interactive transport the command is refused, never silently allowed.
func (a *Agent) askOperator(ctx context.Context, botID string, rec store.ServerRec, command, mode string, g riskGate) Review {
	review := Review{Mode: mode, Risk: g.Level, Findings: g.Findings, ModelRisk: g.ModelRisk, ModelReason: g.ModelReason, Untrusted: g.Untrusted}
	approver := options(ctx).Approver
	if approver == nil {
		if mode == preferences.ModeManual {
			review.Reason = "需要人工确认：请在网页的实时对话中操作（当前连接无法弹出确认）"
		} else {
			review.Reason = "这条命令有风险，需要用户确认，但当前连接无法弹出确认；请在网页的实时对话中操作"
		}
		a.recordReview(ctx, botID, rec.ID, command, review)
		return review
	}
	if len(g.Reasons) == 0 {
		g.Reasons = []string{"规则和 AI 都判断为低风险；逐条确认模式下每条命令都要确认"}
	}
	req := ApprovalRequest{ID: id.New(), BotID: botID, ServerID: rec.ID, ServerName: rec.Name, Command: command, Mode: mode, Risk: g.Level, Reasons: g.Reasons, Findings: g.Findings, ModelRisk: g.ModelRisk, ModelReason: g.ModelReason, Untrusted: g.Untrusted}
	if len(g.Findings) > 0 {
		req.Warning = g.Findings[0].Label + "：" + g.Findings[0].Detail
	}
	allow, reason := approver(ctx, req)
	review.Allow = allow
	review.Reason = reason
	if review.Reason == "" {
		if allow {
			review.Reason = "用户已批准"
		} else {
			review.Reason = "用户拒绝执行"
		}
	}
	a.recordReview(ctx, botID, rec.ID, command, review)
	return review
}

// approve handles the advanced "model" and "plan" modes.
func (a *Agent) approve(ctx context.Context, botID, serverID, command string, g riskGate) Review {
	review := Review{Mode: a.approvalMode(ctx), Risk: g.Level, Findings: g.Findings, ModelRisk: g.ModelRisk, ModelReason: g.ModelReason}
	if a.Settings == nil {
		review.Reason = "未配置审批"
		a.recordReview(ctx, botID, serverID, command, review)
		return review
	}
	state := a.Settings.Snapshot()
	if route := options(ctx).ApprovalRoute; route != nil && review.Mode == preferences.ModeModel {
		state.Approval.Route = *route
	}
	switch review.Mode {
	case preferences.ModePlan:
		review.Reason = "仅规划模式，不执行远程命令"
	case preferences.ModeModel:
		review.ProviderID = state.Approval.Route.ProviderID
		review.Model = state.Approval.Route.Model
		p, err := a.Settings.Provider(review.ProviderID)
		if err != nil {
			review.Reason = "审批渠道不可用"
			break
		}
		key := ""
		if !strings.HasPrefix(p.AuthKind, "oauth-") {
			key, err = a.Settings.Key(p.ID)
		}
		if err != nil {
			review.Reason = "审批密钥无法解密"
			break
		}
		request, _ := ctx.Value(taskKey{}).(string)
		payload, _ := json.Marshal(map[string]any{"user_request": a.redact(ctx, request), "server_id": serverID, "command": a.redact(ctx, command), "rule_check": g.Reasons})
		system := promptReviewer
		if state.Approval.Instructions != "" {
			system += "\n用户额外限制：" + a.redact(ctx, state.Approval.Instructions)
		}
		checkCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		checkCtx = usageContext(checkCtx, botID, "approval", p.ID)
		msg, err := a.chat(checkCtx, p.BaseURL, review.Model, key, []map[string]any{{"role": "system", "content": system}, {"role": "user", "content": string(payload)}}, nil)
		cancel()
		if err != nil {
			review.Reason = "审批模型调用失败，已停止执行"
			break
		}
		parsed, err := parseReview(msg.Content)
		if err != nil {
			review.Reason = "审批结果无效，已停止执行"
			break
		}
		review.Allow = parsed.Allow
		review.Risk = parsed.Risk
		review.Reason = a.redact(ctx, clip(parsed.Reason, 300))
	default:
		review.Reason = "未知审批模式，已停止执行"
	}
	a.recordReview(ctx, botID, serverID, command, review)
	return review
}
func parseReview(text string) (Review, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) > 2 && strings.TrimSpace(lines[len(lines)-1]) == "```" {
			text = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	var raw struct {
		Allow  *bool  `json:"allow"`
		Risk   string `json:"risk"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal([]byte(text), &raw) != nil || raw.Allow == nil || strings.TrimSpace(raw.Reason) == "" || (raw.Risk != "low" && raw.Risk != "medium" && raw.Risk != "high") {
		return Review{}, errors.New("invalid verdict")
	}
	return Review{Allow: *raw.Allow && raw.Risk != "high", Risk: raw.Risk, Reason: raw.Reason}, nil
}
func (a *Agent) recordReview(ctx context.Context, botID, serverID, command string, review Review) {
	command = a.redact(ctx, command)
	raw, _ := json.Marshal(review)
	status := "approval_denied"
	if review.Allow {
		status = "approval_allowed"
	}
	_, _ = a.Store.AddHistory(ctx, store.HistoryRec{History: model.History{BotID: botID, ServerID: serverID, Action: "审批：" + command, Command: command, Status: status}, Snapshot: string(raw)})
}
func (a *Agent) execution() preferences.Execution {
	if a.Settings != nil {
		return a.Settings.Snapshot().Execution
	}
	return preferences.Defaults().Execution
}
