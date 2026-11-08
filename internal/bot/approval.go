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
	// Pending marks an ask that could not be answered yet: the call did not
	// run and must not be recorded as a completed denial.
	Pending bool `json:"pending,omitempty"`
	// Smart-approval details, kept in the audit history.
	Findings    []audit.Finding `json:"findings,omitempty"`
	ModelRisk   string          `json:"model_risk,omitempty"`
	ModelReason string          `json:"model_reason,omitempty"`
	Untrusted   string          `json:"untrusted,omitempty"`
}

// approvalTimeout bounds the independent approval model call.
const approvalTimeout = 25 * time.Second

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
	SingleUse   bool
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
// interactive transport the call is parked (pending), never silently allowed
// and never recorded as a completed denial.
func (a *Agent) askOperator(ctx context.Context, botID string, rec store.ServerRec, command, mode string, g riskGate) Review {
	review := Review{Mode: mode, Risk: g.Level, Findings: g.Findings, ModelRisk: g.ModelRisk, ModelReason: g.ModelReason, Untrusted: g.Untrusted}
	approver := options(ctx).Approver
	if approver == nil {
		review.Pending = true
		review.Reason = "等待用户审批：当前连接无法弹出确认，未执行"
		a.recordReview(ctx, botID, rec.ID, command, review)
		return review
	}
	if len(g.Reasons) == 0 {
		g.Reasons = []string{"规则和 AI 都判断为低风险；逐条确认模式下每条命令都要确认"}
	}
	req := ApprovalRequest{ID: id.New(), SingleUse: g.SingleUse, BotID: botID, ServerID: rec.ID, ServerName: rec.Name, Command: command, Mode: mode, Risk: g.Level, Reasons: g.Reasons, Findings: g.Findings, ModelRisk: g.ModelRisk, ModelReason: g.ModelReason, Untrusted: g.Untrusted}
	if b, err := a.Store.GetBot(ctx, botID); err == nil {
		req.BotName = b.Name
	}
	if len(g.Findings) > 0 {
		req.Warning = g.Findings[0].Label + "：" + g.Findings[0].Detail
	}
	if consumePermit(ctx, req) {
		// The operator already allowed exactly this call: do not ask again.
		review.Allow = true
		review.Reason = "用户已批准本次操作"
		a.recordReview(ctx, botID, rec.ID, command, review)
		return review
	}
	allow, reason := approver(ctx, req)
	if !allow && (reason == askSentinel || strings.HasPrefix(reason, askSentinel) || reason == pendingSentinel) {
		// The durable approver parked the call: not executed, not denied.
		review.Pending = true
		review.Reason = "等待用户审批：操作已挂起，未执行"
		if tr := traceFrom(ctx); tr != nil {
			tr.emit(Step{Kind: "approval", Bot: req.BotName, Approval: &model.ApprovalCard{
				ID: req.ID, BotName: req.BotName, ServerID: req.ServerID, ServerName: req.ServerName,
				Command: a.redact(ctx, req.Command), Mode: req.Mode, Risk: req.Risk,
				Reasons: a.redactReasons(ctx, req.Reasons), ModelReason: a.redact(ctx, req.ModelReason),
				Untrusted: a.redact(ctx, req.Untrusted), Status: "pending",
			}})
		}
		a.recordReview(ctx, botID, rec.ID, command, review)
		return review
	}
	if g.SingleUse && reason == "always" {
		reason = "用户已批准本次操作"
	}
	review.Allow = allow
	review.Reason = reason
	cardStatus := "denied"
	cardNote := ""
	if allow && reason == "always" {
		cardStatus = "always"
		review.Reason = "用户已批准（本对话始终放行）"
		targetLabel := strings.TrimSpace(req.ServerName)
		if targetLabel == "" {
			targetLabel = strings.TrimSpace(req.ServerID)
		}
		if targetLabel == "" {
			targetLabel = "面板"
		}
		cardNote = "已在本对话放行对「" + targetLabel + "」的同类操作（新开对话后恢复默认审批）"
	} else if allow {
		cardStatus = "approved"
	} else if strings.Contains(reason, "超时") {
		cardStatus = "timeout"
		cardNote = reason
	} else if reason != "" {
		cardNote = reason
	}
	if review.Reason == "" {
		if allow {
			review.Reason = "用户已批准"
		} else {
			review.Reason = "用户拒绝执行"
		}
	}
	if tr := traceFrom(ctx); tr != nil {
		card := &model.ApprovalCard{
			ID:          req.ID,
			BotName:     req.BotName,
			ServerID:    req.ServerID,
			ServerName:  req.ServerName,
			Command:     a.redact(ctx, req.Command),
			Mode:        req.Mode,
			Risk:        req.Risk,
			Reasons:     a.redactReasons(ctx, req.Reasons),
			ModelReason: a.redact(ctx, req.ModelReason),
			Untrusted:   a.redact(ctx, req.Untrusted),
			Status:      cardStatus,
			Note:        a.redact(ctx, cardNote),
		}
		tr.emit(Step{Kind: "approval", Bot: req.BotName, Approval: card})
	}
	a.recordReview(ctx, botID, rec.ID, command, review)
	return review
}

func (a *Agent) redactReasons(ctx context.Context, reasons []string) []string {
	out := make([]string, 0, len(reasons))
	for _, r := range reasons {
		out = append(out, a.redact(ctx, r))
	}
	return out
}

// reviewVerdict is the independent approval model's answer. Risk is evidence
// only: an allow with a high risk level is still an allow.
type reviewVerdict struct {
	Decision string // allow, deny, ask
	Risk     string
	Reason   string
}

// parseReview accepts the three-state decision and the legacy bool form.
// Anything it cannot parse is an error, which the caller turns into an ask.
func parseReview(text string) (reviewVerdict, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) > 2 && strings.TrimSpace(lines[len(lines)-1]) == "```" {
			text = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	var raw struct {
		Decision *string `json:"decision"`
		Allow    *bool   `json:"allow"`
		Risk     string  `json:"risk"`
		Reason   string  `json:"reason"`
	}
	if json.Unmarshal([]byte(text), &raw) != nil || strings.TrimSpace(raw.Reason) == "" {
		return reviewVerdict{}, errors.New("invalid verdict")
	}
	decision := ""
	if raw.Decision != nil {
		decision = strings.ToLower(strings.TrimSpace(*raw.Decision))
	} else if raw.Allow != nil {
		// Legacy bool models: allow=true is an allow even when risk is high.
		if *raw.Allow {
			decision = "allow"
		} else {
			decision = "deny"
		}
	}
	switch decision {
	case "allow", "deny", "ask":
	default:
		return reviewVerdict{}, errors.New("invalid decision")
	}
	risk := strings.ToLower(strings.TrimSpace(raw.Risk))
	if risk == "" {
		risk = "medium"
	}
	if _, ok := riskRank[risk]; !ok {
		return reviewVerdict{}, errors.New("invalid risk")
	}
	return reviewVerdict{Decision: decision, Risk: risk, Reason: raw.Reason}, nil
}
func (a *Agent) recordReview(ctx context.Context, botID, serverID, command string, review Review) {
	command = a.redact(ctx, command)
	raw, _ := json.Marshal(review)
	status := "approval_denied"
	if review.Pending {
		status = "approval_pending"
	} else if review.Allow {
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
