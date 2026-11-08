package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"onefrog/internal/audit"
	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/store"
	"onefrog/internal/workspace"
)

// PendingNotice is the tool result for a call parked for the operator: it has
// not run and is not a denial. Runtime compares the returned text with this
// constant to park the task instead of recording a completed refusal.
const PendingNotice = pendingSentinel

// callPermit is the one-call approval a durable task carries back in after the
// operator allowed a parked call. Every field is required: speaker, effective
// target, tool name, call id and argument hash. It is consumed on first use, so
// a delegated worker, connect_server's follow-up command or any re-issued call
// can never inherit it. Empty fields never match (no wildcards).
type callPermit struct {
	BotID    string
	ServerID string
	Tool     string
	CallID   string
	Hash     string
	Command  string
	used     bool
}

type callPermitKey struct{}

func withCallPermit(ctx context.Context, p *callPermit) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, callPermitKey{}, p)
}

// permitMatches reports whether the permit covers this concrete call. Every
// identity field must be present and equal: there are no wildcards.
func permitMatches(p *callPermit, botID, serverID string, call toolCall) bool {
	if p == nil || p.used {
		return false
	}
	if p.BotID == "" || p.ServerID == "" || p.Tool == "" || p.CallID == "" || p.Hash == "" {
		return false
	}
	return p.BotID == botID &&
		p.ServerID == serverID &&
		p.Tool == call.Function.Name &&
		p.CallID == call.ID &&
		p.Hash == argsHash(call)
}

// consumePermit answers the approval card for exactly the call the operator
// already allowed. The approval request carries no call identity, so it must
// match the current call marker too; otherwise it stays unconsumed and the
// handler asks again.
func consumePermit(ctx context.Context, req ApprovalRequest) bool {
	p, _ := ctx.Value(callPermitKey{}).(*callPermit)
	cur, _ := ctx.Value(currentCallKey{}).(string)
	if p == nil || p.used || cur == "" || p.CallID != cur {
		return false
	}
	// The approval request has no call identity, so cross-check the immutable
	// identity of the call being authorized: tool and argument hash must match
	// the permit. Otherwise a nested call reusing the id could ride along.
	id, ok := callIdentityOf(ctx)
	if !ok || id.CallID != p.CallID || id.Tool != p.Tool || id.Hash != p.Hash {
		return false
	}
	if p.BotID != req.BotID || p.ServerID != req.ServerID {
		return false
	}
	if strings.TrimSpace(p.Command) != strings.TrimSpace(req.Command) {
		return false
	}
	p.used = true
	return true
}

// consumePermitForCall matches a permit against the concrete tool call. It is
// the single entry the shared hook uses; a mismatch leaves the permit intact
// but unusable for this call.
func consumePermitForCall(ctx context.Context, botID, serverID string, call toolCall) bool {
	p, _ := ctx.Value(callPermitKey{}).(*callPermit)
	if !permitMatches(p, botID, serverID, call) {
		return false
	}
	p.used = true
	return true
}

// callGrant records that the shared authorization already decided one exact
// call. Handlers use it to skip their own approval card only for that call:
// speaker, effective target, tool name, call id and argument hash must all
// match. A nested or re-issued call in the same window never matches.
type callGrant struct {
	BotID    string
	ServerID string
	Tool     string
	CallID   string
	Hash     string
	Command  string
}

type callGrantKey struct{}

func withCallGrant(ctx context.Context, g callGrant) context.Context {
	return context.WithValue(ctx, callGrantKey{}, g)
}

// grantForCall reports whether the shared authorization already decided this
// exact call. Handlers call it with the concrete tool call they are serving.
func grantForCall(ctx context.Context, botID, serverID string, call toolCall) bool {
	g, ok := ctx.Value(callGrantKey{}).(callGrant)
	if !ok {
		return false
	}
	if g.BotID == "" || g.ServerID == "" || g.Tool == "" || g.CallID == "" || g.Hash == "" {
		return false
	}
	return g.BotID == botID &&
		g.ServerID == serverID &&
		g.Tool == call.Function.Name &&
		g.CallID == call.ID &&
		g.Hash == argsHash(call)
}

// grantForHandler reports whether the shared authorization already decided the
// call whose handler is now running. Handlers that only receive a display label
// (panel mutations, probe confirm) compare against the immutable identity of
// the current call: the tool name and argument hash must match too, so a nested
// call reusing the same call id with another tool or arguments cannot inherit
// the grant.
func grantForHandler(ctx context.Context, botID, serverID string) bool {
	g, ok := ctx.Value(callGrantKey{}).(callGrant)
	if !ok {
		return false
	}
	id, ok := callIdentityOf(ctx)
	if !ok || id.CallID == "" || id.Tool == "" || id.Hash == "" {
		return false
	}
	if g.BotID == "" || g.ServerID == "" || g.Tool == "" || g.CallID == "" || g.Hash == "" {
		return false
	}
	return g.BotID == botID && g.ServerID == serverID &&
		g.CallID == id.CallID && g.Tool == id.Tool && g.Hash == id.Hash
}

// CentralAuthorize is the default shared hook. Runtime installs it on
// Agent.AuthorizeCall (or calls authorizeCall directly); a nil hook must never
// be treated as an approval.
func (a *Agent) CentralAuthorize(ctx context.Context, speaker model.Bot, serverID string, call toolCall) (context.Context, string) {
	return a.authorizeCall(ctx, speaker, serverID, call)
}

// PermitForApprovedCall builds the one-shot permit for a call the operator just
// approved. Runtime attaches it to the context it hands to the shared hook, so
// the approved call is not reviewed or asked a second time. Every identity
// field is filled from the concrete call: there are no wildcards.
func PermitForApprovedCall(botID, serverID string, call toolCall) *callPermit {
	return &callPermit{
		BotID:    botID,
		ServerID: serverID,
		Tool:     call.Function.Name,
		CallID:   call.ID,
		Hash:     argsHash(call),
		Command:  call.Function.Arguments,
	}
}

// MarkAuthorizedCall records that the shared hook already decided this exact
// call. Runtime calls it with the effective server so execCall does not run the
// hook a second time.
func MarkAuthorizedCall(ctx context.Context, botID, serverID string, call toolCall) context.Context {
	return withHookMark(ctx, hookMark{
		BotID:    botID,
		ServerID: serverID,
		Tool:     call.Function.Name,
		CallID:   call.ID,
		Hash:     argsHash(call),
	})
}

// CallAlreadyAuthorized is the strict exported form for runtime: the caller
// states the effective target it is about to use.
func CallAlreadyAuthorized(ctx context.Context, botID, serverID string, call toolCall) bool {
	return hookAlreadyRanFor(ctx, botID, serverID, call)
}

// newCallGrant builds the exact-call grant bound to the speaker, target, tool,
// call id and argument hash.
func newCallGrant(botID, serverID string, call toolCall, label string) callGrant {
	return callGrant{BotID: botID, ServerID: serverID, Tool: call.Function.Name, CallID: call.ID, Hash: argsHash(call), Command: label}
}

// effectiveServer resolves the target this call will really touch. The hook,
// the permit and the handler grant all use this identity, so a call naming
// another server than the selected one can never match an earlier approval.
func (a *Agent) effectiveServer(ctx context.Context, fallback string, call toolCall) (string, bool) {
	var args struct {
		Action    string `json:"action"`
		ServerID  string `json:"server_id"`
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
	resolve := func(id string) (string, bool) {
		if strings.TrimSpace(id) == "" {
			return "", false
		}
		rec, err := a.Store.GetServer(ctx, id)
		if err != nil {
			rec, err = a.serverByName(ctx, id)
		}
		if err != nil {
			return "", false
		}
		return rec.ID, true
	}
	switch call.Function.Name {
	case "run_command", "inspect_services":
		id := strings.TrimSpace(args.ServerID)
		if id == "" {
			id = fallback
		}
		return resolve(id)
	case "workspace":
		if target := options(ctx).Workspace; target != nil {
			if target.Kind == "local" {
				return "local", true
			}
			return target.ServerID, true
		}
		return "", true
	case "job_start", "job_status", "job_cancel", "job_wait":
		if target := options(ctx).Workspace; target != nil && target.Kind == "local" {
			return "local", true
		}
		return "", true
	case "install_probe":
		action := strings.ToLower(strings.TrimSpace(args.Action))
		if action == "confirm" {
			owner := strings.TrimSpace(options(ctx).OwnerID)
			if owner == "" {
				owner = "panel"
			}
			if a.Onboarding != nil {
				if v, err := a.Onboarding.Get(owner, strings.TrimSpace(args.RequestID)); err == nil && strings.TrimSpace(v.ServerID) != "" {
					return v.ServerID, true
				}
			}
			return "panel", true
		}
		return resolve(strings.TrimSpace(args.ServerID))
	}
	return "panel", true
}

// callVerdict is the three-state authorization result. Risk is evidence for the
// operator, never a reason to deny by itself.
type callVerdict struct {
	Decision string // allow, deny, ask
	Reason   string
	Level    string
	Reasons  []string
	// Args is the candidate's full tool arguments, redacted. It is what the
	// independent reviewer sees as the concrete request (workspace diff, panel
	// options, command text), not just a short display label.
	Args        string
	Findings    []audit.Finding
	Untrusted   string
	ModelRisk   string
	ModelReason string
}

// evidenceFor collects the trusted, server-side evidence for one call: the rule
// classifier and whether the command looks driven by untrusted content. The
// executing model's own risk self-report is deliberately not an input.
func (a *Agent) evidenceFor(ctx context.Context, command string) ([]audit.Finding, string, string) {
	rules := audit.Classify(secretPlaceholder.ReplaceAllString(command, "SECRET"))
	level := rules.Level
	untrusted := untrustedSource(ctx, command)
	if untrusted != "" && riskRank[level] < riskRank["medium"] {
		level = "medium"
	}
	return rules.Findings, level, untrusted
}

// selectedServerKey carries the turn's original selected server, which is the
// scope the user actually chose. The candidate call may name another server;
// the reviewer must see both.
type selectedServerKey struct{}

func withSelectedServer(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, selectedServerKey{}, id)
}

func selectedServer(ctx context.Context) string {
	id, _ := ctx.Value(selectedServerKey{}).(string)
	return id
}

// authorizeCall is the single shared authorization hook for side-effecting
// tools. It returns the context to continue with and, when non-empty, the tool
// result: a refusal or PendingNotice. A granted call carries a call grant (and
// the consumed permit) so the handler does not ask a second time.
func (a *Agent) authorizeCall(ctx context.Context, speaker model.Bot, serverID string, call toolCall) (context.Context, string) {
	// The current-call marker is what lets the runtime's approval decision and
	// the handlers agree on "this exact call". It is set once per call and
	// never inherited by a nested call.
	ctx = withCurrentCall(ctx, call.ID)
	ctx = withCallIdentity(ctx, callIdentity{CallID: call.ID, Tool: call.Function.Name, Hash: argsHash(call)})
	ctx = withSelectedServer(ctx, serverID)
	target, ok := a.effectiveServer(ctx, serverID, call)
	if !ok {
		return ctx, "找不到服务器，未执行"
	}
	if hookAlreadyRanFor(ctx, speaker.ID, target, call) {
		return ctx, ""
	}
	mark := func(next context.Context) context.Context {
		return withHookMark(next, hookMark{BotID: speaker.ID, ServerID: target, Tool: call.Function.Name, CallID: call.ID, Hash: argsHash(call)})
	}
	tool := call.Function.Name
	if readOnlyCall(tool, call.Function.Arguments) {
		// Read-only helpers are approval-exempt; the handler still enforces
		// memory scope, project isolation and other hard limits.
		return mark(ctx), ""
	}
	if consumed := consumePermitForCall(ctx, speaker.ID, target, call); consumed {
		// The operator already allowed this exact call: skip the second card and
		// hand the handlers a grant for this same call.
		return mark(withCallGrant(ctx, newCallGrant(speaker.ID, target, call, call.Function.Arguments))), ""
	}
	switch tool {
	case "run_command":
		return a.authorizeRunCommand(ctx, speaker, target, call, mark)
	case "workspace":
		return a.authorizeWorkspace(ctx, speaker, target, call, mark)
	case "job_start":
		return a.authorizeJobStart(ctx, speaker, target, call, mark)
	case "job_cancel":
		return a.authorizeJobCancel(ctx, speaker, target, call, mark)
	case "job_status":
		return mark(ctx), ""
	case "job_wait":
		return mark(ctx), ""
	case "panel":
		return a.authorizePanel(ctx, speaker, target, call, mark)
	case "install_probe":
		return a.authorizeInstallProbe(ctx, speaker, target, call, mark)
	case "inspect_services":
		return a.authorizeInspectServices(ctx, speaker, target, call, mark)
	case "manage_module_record":
		return a.authorizeAssetRecord(ctx, speaker, target, call, mark)
	case "manage_modules":
		return a.authorizeManageModules(ctx, speaker, target, call, mark)
	case "develop_module":
		return a.authorizeDevelopModule(ctx, speaker, target, call, mark)
	case "generate_secret":
		return a.authorizeGenerateSecret(ctx, speaker, target, call, mark)
	case "message_agent":
		return a.authorizeMessageAgent(ctx, speaker, target, call, mark)
	case "connect_server":
		return a.authorizeConnectServer(ctx, speaker, target, call, mark)
	default:
		// ask_user, request_secret, ask_agent and the durable task tools carry
		// no remote side effect of their own; delegation and connect follow-ups
		// run as their own calls and ask again.
		return mark(ctx), ""
	}
}

// readOnlyCall lists the approval-exempt calls. It only skips the approval
// card; memory scope, project isolation and capability checks still apply.
func readOnlyCall(tool, raw string) bool {
	var args struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal([]byte(raw), &args)
	action := strings.ToLower(strings.TrimSpace(args.Action))
	switch tool {
	case "remember", "recall", "ask_user", "request_secret", "ask_agent", "plan", "checkpoint", "wait", "finish", "job_status", "job_wait":
		return true
	case "workspace":
		switch action {
		case "list", "status", "diff", "read":
			return true
		}
	case "panel":
		switch action {
		case "overview", "check_update":
			return true
		}
	case "inspect_services":
		return action == "" || action == "list"
	case "manage_modules":
		return action == "" || action == "list" || action == "list_records"
	case "develop_module":
		return action == "" || action == "list" || action == "inspect"
	case "manage_module_record":
		return action == "" || action == "list"
	}
	return false
}

// decideCall applies the approval mode to one candidate call. Risk findings and
// untrusted-source notes are evidence: they are shown to whoever decides, but
// they never deny or escalate to a human by themselves.
func (a *Agent) decideCall(ctx context.Context, botID string, rec store.ServerRec, label string, v callVerdict, mode string, askHuman bool, command string) (allow, pending bool, why string) {
	if mode == preferences.ModePlan {
		a.recordReview(ctx, botID, rec.ID, label, Review{Reason: "仅规划模式，不执行远程操作", Risk: v.Level, Mode: mode, Findings: v.Findings})
		return false, false, "仅规划模式，不执行远程操作"
	}
	if mode == preferences.ModeOff {
		// Off keeps the built-in catastrophic guard: that is a capability hard
		// limit, not an approval decision about keywords.
		if command != "" {
			if hard := audit.Decide(command); !hard.Allow {
				a.recordReview(ctx, botID, rec.ID, label, Review{Reason: hard.Reason, Risk: "high", Mode: mode, Findings: v.Findings})
				return false, false, hard.Reason
			}
		}
		if askHuman {
			// Scope the user did not choose (another server) still needs a
			// decision: turning approvals off does not widen the selected scope.
			gate := riskGate{Level: v.Level, Findings: v.Findings, Reasons: v.Reasons, Untrusted: v.Untrusted}
			review := a.askOperator(ctx, botID, rec, label, mode, gate)
			if review.Pending {
				return false, true, ""
			}
			if !review.Allow {
				return false, false, review.Reason
			}
			return true, false, ""
		}
		return true, false, ""
	}
	if mode == preferences.ModeManual {
		askHuman = true
	}
	if mode == preferences.ModeModel {
		// The independent reviewer decides every candidate, whatever the risk
		// level or rule findings say. Only its own deny refuses; allow runs;
		// ask (or any reviewer failure) falls through to the operator.
		reviewed := a.reviewWithModel(ctx, botID, rec.ID, label, v, mode)
		switch reviewed.Decision {
		case "allow":
			a.recordReview(ctx, botID, rec.ID, label, Review{Allow: true, Reason: reviewed.Reason, Risk: reviewed.Level, Mode: mode, Findings: v.Findings, ModelRisk: reviewed.ModelRisk, ModelReason: reviewed.ModelReason})
			return true, false, ""
		case "deny":
			a.recordReview(ctx, botID, rec.ID, label, Review{Reason: reviewed.Reason, Risk: reviewed.Level, Mode: mode, Findings: v.Findings, ModelRisk: reviewed.ModelRisk, ModelReason: reviewed.ModelReason})
			return false, false, reviewed.Reason
		default:
			// Reviewer unavailable, ambiguous or asked for a human: fall through
			// to the operator card. Never allow silently.
			askHuman = true
			if reviewed.Reason != "" {
				v.Reasons = append([]string{"审批模型没有给出放行结论：" + reviewed.Reason}, v.Reasons...)
			}
		}
	}
	if !askHuman {
		return true, false, ""
	}
	gate := riskGate{Level: v.Level, Findings: v.Findings, Reasons: v.Reasons, Untrusted: v.Untrusted, ModelRisk: v.ModelRisk, ModelReason: v.ModelReason}
	review := a.askOperator(ctx, botID, rec, label, mode, gate)
	if review.Pending {
		return false, true, ""
	}
	if !review.Allow {
		return false, false, review.Reason
	}
	return true, false, ""
}

// reviewWithModel asks the independent approval model. It only ever sees the
// user's original goal, the scope, the candidate tool/target/arguments, the
// trusted rule findings and the untrusted-source note. Transport errors,
// missing routes and unparseable answers become "ask".
func (a *Agent) reviewWithModel(ctx context.Context, botID, serverID, label string, v callVerdict, mode string) callVerdict {
	if a.Settings == nil {
		return callVerdict{Decision: "ask", Reason: "审批模型未配置", Level: v.Level}
	}
	state := a.Settings.Snapshot()
	if route := options(ctx).ApprovalRoute; route != nil {
		state.Approval.Route = *route
	}
	providerID, modelName := state.Approval.Route.ProviderID, state.Approval.Route.Model
	if providerID == "" || strings.TrimSpace(modelName) == "" {
		return callVerdict{Decision: "ask", Reason: "没有选择独立审批模型", Level: v.Level}
	}
	p, err := a.Settings.Provider(providerID)
	if err != nil {
		return callVerdict{Decision: "ask", Reason: "审批渠道不可用", Level: v.Level}
	}
	key := ""
	if !strings.HasPrefix(p.AuthKind, "oauth-") {
		key, err = a.Settings.Key(p.ID)
	}
	if err != nil {
		return callVerdict{Decision: "ask", Reason: "审批密钥无法解密", Level: v.Level}
	}
	request, _ := ctx.Value(taskKey{}).(string)
	payload, _ := json.Marshal(map[string]any{
		"user_request":   a.redact(ctx, request),
		"scope":          a.scopeNote(ctx, serverID),
		"candidate":      a.redact(ctx, label),
		"candidate_args": a.redact(ctx, v.Args),
		"rule_check":     v.Reasons,
		"untrusted":      v.Untrusted,
	})
	system := promptReviewer
	if state.Approval.Instructions != "" {
		system += "\n用户额外限制：" + a.redact(ctx, state.Approval.Instructions)
	}
	checkCtx, cancel := context.WithTimeout(ctx, approvalTimeout)
	checkCtx = usageContext(checkCtx, botID, "approval", p.ID)
	msg, err := a.chat(checkCtx, p.BaseURL, modelName, key, []map[string]any{{"role": "system", "content": system}, {"role": "user", "content": string(payload)}}, nil)
	cancel()
	if err != nil {
		return callVerdict{Decision: "ask", Reason: "审批模型调用失败", Level: v.Level}
	}
	parsed, err := parseReview(msg.Content)
	if err != nil {
		return callVerdict{Decision: "ask", Reason: "审批结果无效", Level: v.Level}
	}
	return callVerdict{
		Decision:    parsed.Decision,
		Reason:      a.redact(ctx, clip(parsed.Reason, 300)),
		Level:       parsed.Risk,
		ModelRisk:   parsed.Risk,
		ModelReason: a.redact(ctx, clip(parsed.Reason, 300)),
	}
}

// scopeNote tells the reviewer what the user actually chose: the turn's
// originally selected server and the project scope. It is not the candidate
// target, so the reviewer can notice a call that leaves the chosen scope.
func (a *Agent) scopeNote(ctx context.Context, serverID string) map[string]any {
	scope := map[string]any{"selected_server": selectedServer(ctx)}
	if id := projectID(ctx); id != "" {
		scope["project"] = id
	}
	if options(ctx).RestrictServers {
		scope["restricted_to"] = selectedServer(ctx)
	}
	scope["candidate_target"] = serverID
	return scope
}

// authorizeRunCommand is the run_command branch of the shared hook.
func (a *Agent) authorizeRunCommand(ctx context.Context, speaker model.Bot, fallbackServer string, call toolCall, mark func(context.Context) context.Context) (context.Context, string) {
	var args struct {
		ServerID string `json:"server_id"`
		Command  string `json:"command"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return ctx, "run_command 参数不是有效的 JSON，未执行"
	}
	if args.ServerID == "" {
		args.ServerID = fallbackServer
	}
	rec, err := a.Store.GetServer(ctx, args.ServerID)
	if err != nil {
		rec, err = a.serverByName(ctx, args.ServerID)
		if err != nil {
			return ctx, "找不到服务器，未执行"
		}
	}
	label := a.redact(ctx, args.Command)
	findings, level, untrusted := a.evidenceFor(ctx, args.Command)
	v := callVerdict{Level: level, Findings: findings, Untrusted: untrusted, Args: call.Function.Arguments}
	for _, f := range findings {
		v.Reasons = append(v.Reasons, "规则："+f.Label+"："+f.Detail)
	}
	if untrusted != "" {
		v.Reasons = append(v.Reasons, untrusted)
	}
	// Compare against the turn's original selection, not the resolved target:
	// the target is always this call's own server, so using it would make every
	// call look selected.
	selected := selectedServer(ctx)
	unselected := options(ctx).RestrictServers && (selected == "" || rec.ID != selected) && !serverGranted(ctx, rec.ID)
	askHuman := untrusted != "" || unselected
	if unselected {
		v.Level = "high"
		v.Reasons = append([]string{"这台服务器不是本轮手动选中的"}, v.Reasons...)
	}
	mode := a.approvalMode(ctx)
	if mode == preferences.ModeManual {
		askHuman = true
	}
	if mode == preferences.ModeSmart {
		// Smart asks only when a rule hit or untrusted source says the call is
		// not obviously safe; risk level alone is not a reason to interrupt.
		askHuman = askHuman || len(findings) > 0
	}
	allow, pending, why := a.decideCall(ctx, speaker.ID, rec, label, v, mode, askHuman, args.Command)
	if pending {
		if unselected {
			return mark(ctx), "已拦截：未手动选择的服务器 " + rec.Name + " 需要审批后才能执行。等待用户审批：操作已挂起，未执行。"
		}
		return mark(ctx), PendingNotice
	}
	if !allow {
		return mark(ctx), "已拦截：" + why
	}
	// A one-call approval covers this call only. It never widens the turn's
	// server scope: another call to the same unselected server asks again.
	next := mark(withCallGrant(ctx, newCallGrant(speaker.ID, rec.ID, call, label)))
	return next, ""
}

// authorizeWorkspace is the workspace branch: reads are exempt, writes and run
// need the shared decision.
func (a *Agent) authorizeWorkspace(ctx context.Context, speaker model.Bot, targetServer string, call toolCall, mark func(context.Context) context.Context) (context.Context, string) {
	// A bound local workspace always uses the local action grammar, whatever
	// the argument decode says about the SSH shape.
	if target := options(ctx).Workspace; target != nil && target.Kind == "local" {
		var lreq workspace.LocalRequest
		if err := json.Unmarshal([]byte(call.Function.Arguments), &lreq); err != nil {
			return ctx, "工作区参数无效，未执行"
		}
		return a.authorizeLocalWorkspace(ctx, speaker, targetServer, call, mark, lreq)
	}
	var req workspace.Request
	if json.Unmarshal([]byte(call.Function.Arguments), &req) != nil {
		return ctx, "工作区参数无效，未执行"
	}
	if req.Action != "write" && req.Action != "edit" && req.Action != "run" {
		return mark(ctx), ""
	}
	if err := req.Validate(); err != nil {
		return ctx, err.Error()
	}
	target := options(ctx).Workspace
	if target == nil {
		return ctx, "请先在代码工作区打开仓库"
	}
	rec, err := a.Workspace.Server(ctx, *target)
	if err != nil {
		return ctx, err.Error()
	}
	if rec.ID != targetServer {
		return ctx, "工作区目标与当前调用不一致，未执行"
	}
	label := req.Action
	if target.Root != "" {
		label = target.Root + "\n" + label
	}
	v := callVerdict{Level: "medium", Reasons: []string{"将修改 SSH 工作区内的源文件"}, Args: call.Function.Arguments}
	if req.Action == "run" {
		label = target.Root + "\n" + req.Command
		findings, level, untrusted := a.evidenceFor(ctx, req.Command)
		v = callVerdict{Level: level, Findings: findings, Untrusted: untrusted, Args: call.Function.Arguments}
		for _, f := range findings {
			v.Reasons = append(v.Reasons, "规则："+f.Label+"："+f.Detail)
		}
		v.Reasons = append([]string{"命令以 SSH 账号执行，能够访问该账号可访问的文件和服务"}, v.Reasons...)
		if untrusted != "" {
			v.Reasons = append(v.Reasons, untrusted)
		}
	} else if req.Path != "" {
		label += " " + req.Path
	}
	allow, pending, why := a.decideCall(ctx, speaker.ID, rec, label, v, a.approvalMode(ctx), true, req.Command)
	if pending {
		return mark(ctx), PendingNotice
	}
	if !allow {
		return mark(ctx), "未执行：" + why
	}
	return mark(withCallGrant(ctx, newCallGrant(speaker.ID, rec.ID, call, label))), ""
}

// authorizeLocalWorkspace is the workspace branch for native local roots.
// Reads are exempt; write/edit/run follow the same three-state decision as the
// SSH branch. The root itself was already validated against the operator
// allowlist when the task's workspace option was bound; the handler re-checks
// it in the engine.
func (a *Agent) authorizeLocalWorkspace(ctx context.Context, speaker model.Bot, targetServer string, call toolCall, mark func(context.Context) context.Context, req workspace.LocalRequest) (context.Context, string) {
	target := options(ctx).Workspace
	if target == nil || target.Kind != "local" {
		return ctx, "本地工作区未绑定，未执行"
	}
	if targetServer != "local" {
		return ctx, "工作区目标与当前调用不一致，未执行"
	}
	if a.LocalWorkspace == nil {
		return ctx, "本地工作区未启用，未执行"
	}
	if _, err := a.LocalWorkspace.Roots().Pick(target.Root); err != nil {
		return ctx, "工作区根目录不在允许列表内，未执行"
	}
	switch req.Action {
	case "list", "status", "diff", "read", "search":
		return mark(ctx), ""
	case "write", "edit", "run":
	default:
		return ctx, "未知的本地工作区操作，未执行"
	}
	label := req.Action
	if req.Action == "run" {
		label = "在本地工作区运行命令"
	} else if req.Path != "" {
		label += " " + req.Path
	}
	v := callVerdict{Level: "medium", Reasons: []string{"将修改本地工作区内的文件"}, Args: call.Function.Arguments}
	if req.Action == "run" {
		v = callVerdict{Level: "medium", Reasons: []string{"命令以 OneFrog 进程的系统账号权限执行（不是沙箱），需要确认"}, Args: call.Function.Arguments}
		findings, level, untrusted := a.evidenceFor(ctx, req.Command)
		v.Level = level
		v.Findings = findings
		v.Untrusted = untrusted
		for _, f := range findings {
			v.Reasons = append(v.Reasons, "规则："+f.Label+"："+f.Detail)
		}
		if untrusted != "" {
			v.Reasons = append(v.Reasons, untrusted)
		}
		if a.LocalWorkspace != nil && !a.LocalWorkspace.Roots().CommandExecution {
			return ctx, "本地命令执行未启用，未执行"
		}
	}
	allow, pending, why := a.decideCall(ctx, speaker.ID, store.ServerRec{Server: model.Server{ID: "local", Name: "本地工作区 " + target.Root}}, label, v, a.approvalMode(ctx), true, req.Command)
	if pending {
		return mark(ctx), PendingNotice
	}
	if !allow {
		return mark(ctx), "未执行：" + why
	}
	return mark(withCallGrant(ctx, newCallGrant(speaker.ID, "local", call, label))), ""
}

// panelAskReason explains why one panel mutation needs confirmation.
var panelAskReason = map[string]string{
	"set_module":          "会改变面板模块的启用状态",
	"set_bot":             "会启停一个 Bot",
	"confirm_fingerprint": "首次信任这台机器的 SSH 主机指纹",
	"reset_fingerprint":   "清除后必须重新核对主机指纹",
	"delete_server":       "删除后服务器从面板消失，登录信息一并清除",
	"apply_update":        "升级会重启面板，进行中的回复会中断",
	"set_api_key":         "会替换渠道保存的 API Key",
	"set_approval_mode":   "审批模式决定之后的命令还要不要你确认",
	"set_bot_prompt":      "会改变这个 Bot 之后怎么工作",
	"create_bot":          "会新建一个可以执行任务的 Bot",
	"delete_bot":          "会删掉这个 Bot 和它的记忆",
	"delete_chat":         "会删掉这条对话记录和项目记忆",
}

// authorizePanel is the panel branch. Unknown actions are refused, not run.
// precheckPanel applies the handler's cheap, non-destructive argument checks so
// the operator is never asked to approve a call that would be rejected anyway.
func (a *Agent) precheckPanel(ctx context.Context, action, raw string) error {
	if msg := a.panelPlanBlocked(ctx); msg != "" {
		return errors.New(msg)
	}
	var args struct {
		ID           string  `json:"id"`
		Name         string  `json:"name"`
		Enabled      *bool   `json:"enabled"`
		Fingerprint  string  `json:"fingerprint"`
		SecretName   string  `json:"secret_name"`
		Mode         string  `json:"mode"`
		SystemPrompt *string `json:"system_prompt"`
		Description  *string `json:"description"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return errors.New("panel 参数不是有效的 JSON，未执行")
	}
	switch action {
	case "set_module", "set_bot":
		if args.Enabled == nil {
			return errors.New("panel " + action + " 需要 enabled，未执行")
		}
	case "confirm_fingerprint":
		if _, ok := cleanFingerprint(args.Fingerprint); !ok {
			return errors.New("confirm_fingerprint 需要 test_server 汇报的主机指纹，形如 ssh-ed25519 SHA256:… 或 SHA256:…")
		}
	case "set_api_key":
		if panelHasRawSecret(raw) {
			return errors.New("不要在参数或附件里写 API Key 明文。先用 request_secret 放进保险箱，再把 secret_name 设成那个名称。")
		}
		if _, err := vaultName(args.SecretName); err != nil {
			return errors.New("set_api_key 需要 secret_name，填保险箱里的名称，不要填明文。")
		}
	case "set_approval_mode":
		if next, ok := preferences.NormalizeMode(args.Mode); !ok || next == preferences.ModeUnset {
			return errors.New("审批模式只能是 off、smart、manual、model、plan")
		}
	case "set_bot_prompt":
		if args.SystemPrompt == nil && args.Description == nil {
			return errors.New("set_bot_prompt 需要 system_prompt 或 description，未执行")
		}
	case "create_bot":
		if panelRejectsSecretFields(raw) {
			return errors.New("create_bot 不接受 API Key、私钥或保险箱明文。新 Bot 跟随默认模型，密钥只按名称调用。")
		}
		if _, ok := cleanBotName(args.Name); !ok {
			return errors.New("create_bot 需要 1–60 字的 name，不能包含换行")
		}
	}
	return nil
}

func (a *Agent) authorizePanel(ctx context.Context, speaker model.Bot, targetServer string, call toolCall, mark func(context.Context) context.Context) (context.Context, string) {
	if !a.commandsPanel(ctx, speaker) {
		return ctx, "协作任务里不能直接改管理面板，把需要改的内容写进汇报，由对话里的 Bot 来改"
	}
	var args struct {
		Action      string `json:"action"`
		ID          string `json:"id"`
		Name        string `json:"name"`
		Enabled     *bool  `json:"enabled"`
		Fingerprint string `json:"fingerprint"`
		SecretName  string `json:"secret_name"`
		Mode        string `json:"mode"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return ctx, "panel 参数不是有效的 JSON，未执行"
	}
	action := strings.TrimSpace(args.Action)
	reason, ok := panelAskReason[action]
	if !ok {
		return ctx, "panel 的 action 无效，未执行"
	}
	// Cheap argument checks must run before the approval card: never ask the
	// operator to approve something the handler is going to refuse anyway.
	if err := a.precheckPanel(ctx, action, call.Function.Arguments); err != nil {
		return ctx, err.Error()
	}
	label := panelCommand(action, strings.TrimSpace(args.ID), strings.TrimSpace(args.Name), args.Enabled, strings.TrimSpace(args.Fingerprint), strings.TrimSpace(args.SecretName), strings.TrimSpace(args.Mode))
	if targetServer != "panel" {
		return ctx, "面板操作的目标与当前调用不一致，未执行"
	}
	rec := store.ServerRec{Server: model.Server{ID: "panel", Name: "面板"}}
	v := callVerdict{Level: "medium", Reasons: []string{reason}, Args: call.Function.Arguments}
	allow, pending, why := a.decideCall(ctx, speaker.ID, rec, label, v, a.approvalMode(ctx), true, "")
	if pending {
		return mark(ctx), PendingNotice
	}
	if !allow {
		return mark(ctx), "没有执行：" + why
	}
	return mark(withCallGrant(ctx, newCallGrant(speaker.ID, rec.ID, call, label))), ""
}

// authorizeInstallProbe is the install_probe branch.
func (a *Agent) authorizeInstallProbe(ctx context.Context, speaker model.Bot, targetServer string, call toolCall, mark func(context.Context) context.Context) (context.Context, string) {
	if !a.commandsPanel(ctx, speaker) {
		return ctx, "协作任务里不能安装探针，把需要安装的服务器写进汇报，由对话里的 Bot 来装"
	}
	var args struct {
		Action   string `json:"action"`
		ServerID string `json:"server_id"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return ctx, "install_probe 参数无效，未执行"
	}
	action := strings.TrimSpace(args.Action)
	if action == "" {
		action = "install"
	}
	var label string
	var reason string
	switch action {
	case "install":
		label = "安装只读探针 " + strings.TrimSpace(args.ServerID)
		reason = "会在节点上安装或重装只读探针服务"
	case "confirm":
		label = "确认主机指纹"
		reason = "首次信任这台机器的 SSH 主机指纹"
	default:
		return ctx, "install_probe 的 action 无效，未执行"
	}
	rec := store.ServerRec{Server: model.Server{ID: "panel", Name: "面板"}}
	if id := strings.TrimSpace(args.ServerID); id != "" {
		if saved, err := a.serverByName(ctx, id); err == nil {
			rec = saved
		}
	}
	if action == "install" && rec.ID != targetServer {
		return ctx, "探针安装的目标与当前调用不一致，未执行"
	}
	v := callVerdict{Level: "medium", Reasons: []string{reason}, Args: call.Function.Arguments}
	allow, pending, why := a.decideCall(ctx, speaker.ID, rec, label, v, a.approvalMode(ctx), true, "")
	if pending {
		return mark(ctx), PendingNotice
	}
	if !allow {
		return mark(ctx), "没有执行：" + why
	}
	return mark(withCallGrant(ctx, newCallGrant(speaker.ID, rec.ID, call, label))), ""
}

// authorizeInspectServices gates the read-only SSH scan. Listing the stored
// snapshot stays exempt.
func (a *Agent) authorizeInspectServices(ctx context.Context, speaker model.Bot, fallback string, call toolCall, mark func(context.Context) context.Context) (context.Context, string) {
	var args struct {
		ServerID string `json:"server_id"`
	}
	_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
	id := args.ServerID
	if id == "" {
		id = fallback
	}
	rec, err := a.serverByName(ctx, id)
	if err != nil {
		return ctx, "找不到服务器，请选择要检测的服务器"
	}
	selected := selectedServer(ctx)
	unselected := options(ctx).RestrictServers && (selected == "" || rec.ID != selected) && !serverGranted(ctx, rec.ID)
	label := "只读检测服务器服务、容器与监听端口"
	v := callVerdict{Level: "low", Reasons: []string{"只读检测 systemd、Docker 与监听端口"}, Args: call.Function.Arguments}
	if unselected {
		v.Level = "high"
		v.Reasons = append(v.Reasons, "这台服务器不是本轮手动选中的")
	}
	mode := a.approvalMode(ctx)
	forceAsk := unselected || mode == preferences.ModeManual
	allow, pending, why := a.decideCall(ctx, speaker.ID, rec, label, v, mode, forceAsk, "")
	if pending {
		return mark(ctx), PendingNotice
	}
	if !allow {
		return mark(ctx), "没有执行检测：" + why
	}
	// A one-call approval does not widen the server scope for the turn.
	return mark(withCallGrant(ctx, newCallGrant(speaker.ID, rec.ID, call, label))), ""
}

// authorizeAssetRecord gates the asset ledger mutations.
func (a *Agent) authorizeAssetRecord(ctx context.Context, speaker model.Bot, targetServer string, call toolCall, mark func(context.Context) context.Context) (context.Context, string) {
	if !a.managesAssets(speaker) && isWorker(ctx) {
		return ctx, "普通协作 Bot 不能直接修改资产与订阅"
	}
	var args struct {
		Action string `json:"action"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return ctx, "manage_module_record 参数不是有效的 JSON，未执行"
	}
	action := strings.ToLower(strings.TrimSpace(args.Action))
	switch action {
	case "", "list":
		return mark(ctx), ""
	case "create", "update", "delete":
	default:
		return ctx, "manage_module_record 的 action 无效，未执行"
	}
	rec := store.ServerRec{Server: model.Server{ID: "panel", Name: "面板"}}
	if targetServer != rec.ID {
		return ctx, "资产操作的目标与当前调用不一致，未执行"
	}
	label := "资产与订阅 " + action
	v := callVerdict{Level: "low", Reasons: []string{"会写入资产与订阅台账"}, Args: call.Function.Arguments}
	allow, pending, why := a.decideCall(ctx, speaker.ID, rec, label, v, a.approvalMode(ctx), a.approvalMode(ctx) == preferences.ModeManual, "")
	if pending {
		return mark(ctx), PendingNotice
	}
	if !allow {
		return mark(ctx), "没有执行：" + why
	}
	return mark(withCallGrant(ctx, newCallGrant(speaker.ID, rec.ID, call, label))), ""
}

// authorizeManageModules gates module toggles and custom-module records.
func (a *Agent) authorizeManageModules(ctx context.Context, speaker model.Bot, targetServer string, call toolCall, mark func(context.Context) context.Context) (context.Context, string) {
	if !a.managesAssets(speaker) && isWorker(ctx) {
		return ctx, "普通协作 Bot 不能直接使用 manage_modules"
	}
	var args struct {
		Action string `json:"action"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return ctx, "manage_modules 参数不是有效的 JSON，未执行"
	}
	action := strings.ToLower(strings.TrimSpace(args.Action))
	switch action {
	case "", "list", "list_records":
		return mark(ctx), ""
	case "enable", "disable", "add_record", "delete_record":
	default:
		return ctx, "manage_modules 的 action 无效，未执行"
	}
	rec := store.ServerRec{Server: model.Server{ID: "panel", Name: "面板"}}
	if targetServer != rec.ID {
		return ctx, "模块操作的目标与当前调用不一致，未执行"
	}
	label := "manage_modules " + action
	v := callVerdict{Level: "low", Reasons: []string{"会改变面板模块或自定义模块的记录"}, Args: call.Function.Arguments}
	allow, pending, why := a.decideCall(ctx, speaker.ID, rec, label, v, a.approvalMode(ctx), a.approvalMode(ctx) == preferences.ModeManual, "")
	if pending {
		return mark(ctx), PendingNotice
	}
	if !allow {
		return mark(ctx), "没有执行：" + why
	}
	return mark(withCallGrant(ctx, newCallGrant(speaker.ID, rec.ID, call, label))), ""
}

// authorizeDevelopModule gates custom-module creation and changes.
func (a *Agent) authorizeDevelopModule(ctx context.Context, speaker model.Bot, targetServer string, call toolCall, mark func(context.Context) context.Context) (context.Context, string) {
	if isWorker(ctx) && !a.developsModules(speaker) {
		return ctx, "普通协作 Bot 不能直接修改自定义模块"
	}
	if a.managesAssets(speaker) {
		return ctx, "模块管理 Bot 不能新建自定义模块，请使用自定义模块 Bot"
	}
	var args struct {
		Action string `json:"action"`
		ID     string `json:"id"`
		Name   string `json:"name"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return ctx, "develop_module 参数不是有效的 JSON，未执行"
	}
	action := strings.ToLower(strings.TrimSpace(args.Action))
	if action == "" {
		action = "list"
	}
	switch action {
	case "list", "inspect":
		return mark(ctx), ""
	case "create", "update", "delete":
	default:
		return ctx, "develop_module 的 action 无效，未执行"
	}
	if msg := a.panelPlanBlocked(ctx); msg != "" {
		return ctx, msg
	}
	if panelRejectsSecretFields(call.Function.Arguments) {
		return ctx, "develop_module 不接受密码、私钥、API Key 等敏感字段，自定义模块不能保存密钥"
	}
	target := strings.TrimSpace(args.ID)
	if target == "" {
		target = strings.TrimSpace(args.Name)
	}
	rec := store.ServerRec{Server: model.Server{ID: "panel", Name: "面板"}}
	if targetServer != rec.ID {
		return ctx, "自定义模块操作的目标与当前调用不一致，未执行"
	}
	label := action + " " + target
	v := callVerdict{Level: "medium", Reasons: []string{"会写入或删除自定义模块的界面代码"}, Args: call.Function.Arguments}
	// create is additive and reversible; update/delete replace or drop code and
	// records, so they always need a decision.
	askHuman := action != "create"
	allow, pending, why := a.decideCall(ctx, speaker.ID, rec, label, v, a.approvalMode(ctx), askHuman, "")
	if pending {
		return mark(ctx), PendingNotice
	}
	if !allow {
		return mark(ctx), "没有执行：" + why
	}
	return mark(withCallGrant(ctx, newCallGrant(speaker.ID, rec.ID, call, label))), ""
}

// authorizeGenerateSecret gates new vault entries.
func (a *Agent) authorizeGenerateSecret(ctx context.Context, speaker model.Bot, targetServer string, call toolCall, mark func(context.Context) context.Context) (context.Context, string) {
	if a.managesAssets(speaker) || a.developsModules(speaker) {
		return ctx, "这个 Bot 不能生成密钥"
	}
	var args struct {
		Name string `json:"name"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return ctx, "generate_secret 参数不是有效的 JSON，未执行"
	}
	rec := store.ServerRec{Server: model.Server{ID: "panel", Name: "面板"}}
	if targetServer != rec.ID {
		return ctx, "保险箱操作的目标与当前调用不一致，未执行"
	}
	label := "在保险箱新建密钥 " + strings.TrimSpace(args.Name)
	v := callVerdict{Level: "low", Reasons: []string{"会在保险箱里新建一项随机密钥，明文不经过模型"}, Args: call.Function.Arguments}
	allow, pending, why := a.decideCall(ctx, speaker.ID, rec, label, v, a.approvalMode(ctx), a.approvalMode(ctx) == preferences.ModeManual, "")
	if pending {
		return mark(ctx), PendingNotice
	}
	if !allow {
		return mark(ctx), "没有执行：" + why
	}
	return mark(withCallGrant(ctx, newCallGrant(speaker.ID, rec.ID, call, label))), ""
}

// authorizeMessageAgent gates outbound bot-to-bot messages. Project content may
// never leave the project.
func (a *Agent) authorizeMessageAgent(ctx context.Context, speaker model.Bot, targetServer string, call toolCall, mark func(context.Context) context.Context) (context.Context, string) {
	if projectID(ctx) != "" {
		return ctx, "项目内容不能发送到全局收件箱。请用 ask_agent 在本项目内派工，或用 remember 留下结论。"
	}
	if !a.canMessage(ctx, speaker) {
		return ctx, "当前不能发消息"
	}
	var args struct {
		BotID   string `json:"bot_id"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return ctx, "message_agent 参数不是有效的 JSON，未执行"
	}
	rec := store.ServerRec{Server: model.Server{ID: "panel", Name: "面板"}}
	if targetServer != rec.ID {
		return ctx, "发消息的目标与当前调用不一致，未执行"
	}
	label := "给 " + strings.TrimSpace(args.BotID) + " 发消息"
	v := callVerdict{Level: "low", Reasons: []string{"会向另一个 Bot 的收件箱投递一条消息"}, Args: call.Function.Arguments}
	allow, pending, why := a.decideCall(ctx, speaker.ID, rec, label, v, a.approvalMode(ctx), a.approvalMode(ctx) == preferences.ModeManual, "")
	if pending {
		return mark(ctx), PendingNotice
	}
	if !allow {
		return mark(ctx), "没有发送：" + why
	}
	return mark(withCallGrant(ctx, newCallGrant(speaker.ID, rec.ID, call, label))), ""
}

// authorizeConnectServer gates new server records. The handler still requires
// the host, user and secret to appear in the user's own message.
func (a *Agent) authorizeConnectServer(ctx context.Context, speaker model.Bot, targetServer string, call toolCall, mark func(context.Context) context.Context) (context.Context, string) {
	var args struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return ctx, "连接信息不完整，未执行"
	}
	port := args.Port
	if port == 0 {
		port = 22
	}
	rec := store.ServerRec{Server: model.Server{ID: "panel", Name: "面板"}}
	if targetServer != rec.ID {
		return ctx, "接入操作的目标与当前调用不一致，未执行"
	}
	label := fmt.Sprintf("接入 %s@%s:%d", firstNonEmpty(strings.TrimSpace(args.Username), "root"), strings.TrimSpace(args.Host), port)
	v := callVerdict{Level: "medium", Reasons: []string{"会新增或更新服务器接入信息并尝试 SSH 登录"}, Args: call.Function.Arguments}
	allow, pending, why := a.decideCall(ctx, speaker.ID, rec, label, v, a.approvalMode(ctx), a.approvalMode(ctx) == preferences.ModeManual, "")
	if pending {
		return mark(ctx), PendingNotice
	}
	if !allow {
		return mark(ctx), "没有接入：" + why
	}
	return mark(withCallGrant(ctx, newCallGrant(speaker.ID, rec.ID, call, label))), ""
}
