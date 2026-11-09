package onefrog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Policy 审批模式，参考 Codex。
type Policy string

const (
	PolicyUntrusted Policy = "untrusted"  // 只读自动放行，其余全部问用户
	PolicyOnRequest Policy = "on-request" // 工作区内读写、普通命令自动放行；危险操作问用户
	PolicyAuto      Policy = "auto"       // 写/执行交给审核模型判断；危险操作问用户
	PolicyNever     Policy = "never"      // 全自动，从不问用户；危险操作直接拒绝
)

// Risk 风险等级。
type Risk int

const (
	RiskRead Risk = iota
	RiskWrite
	RiskExec
	RiskDanger
)

func (r Risk) String() string {
	return [...]string{"read", "write", "exec", "danger"}[r]
}

func (r Risk) MarshalJSON() ([]byte, error) { return json.Marshal(r.String()) }

func (r *Risk) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		var n int
		err = json.Unmarshal(b, &n)
		*r = Risk(n)
		return err
	}
	for i := RiskRead; i <= RiskDanger; i++ {
		if i.String() == s {
			*r = i
		}
	}
	return nil
}

type Decision string

const (
	Allow   Decision = "allow"
	Deny    Decision = "deny"
	AskUser Decision = "ask_user"
	review  Decision = "review" // 内部用：交给审核模型
)

// Verdict 是一次审批判断。By 说明是谁做的决定：rule / policy / reviewer / user / approval:<id>。
type Verdict struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason"`
	By       string   `json:"by,omitempty"`
}

type Scope string

const (
	ScopeOnce    Scope = "once"    // 只能用一次
	ScopeSession Scope = "session" // 本任务内，同样的参数可以反复用，直到过期
)

// ApprovalRequest 是发给用户的审批请求（前端据此渲染卡片）。
type ApprovalRequest struct {
	ID       string          `json:"id"`
	TaskID   string          `json:"task_id"`
	BotID    string          `json:"bot_id,omitempty"`
	ToolName string          `json:"tool_name"`
	Args     json.RawMessage `json:"args"`
	ArgsHash string          `json:"args_hash"`
	Risk     Risk            `json:"risk"`
	Reason   string          `json:"reason"`
	// OnceOnly: the host asked for this exact call; the card offers no
	// "approve for this task" option.
	OnceOnly  bool      `json:"once_only,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// UserDecision 是用户点按钮的结果。只能由后端在收到用户本人的点击后产生。
type UserDecision struct {
	Approve   bool   `json:"approve"`
	Scope     Scope  `json:"scope,omitempty"`
	DecidedBy string `json:"decided_by,omitempty"`
	Note      string `json:"note,omitempty"`
}

// Approver 把审批请求交给用户。必须阻塞到用户决定或 ctx 结束（超时/取消 = 拒绝）。
type Approver interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (UserDecision, error)
}

// ApprovalRecord 是后端保存的审批记录，绑定到具体任务 + 工具 + 参数哈希。
type ApprovalRecord struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	ToolName  string    `json:"tool_name"`
	ArgsHash  string    `json:"args_hash"`
	Decision  Decision  `json:"decision"`
	Scope     Scope     `json:"scope"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	DecidedBy string    `json:"decided_by"`
	Used      bool      `json:"used"`
}

// Matches 判断这条记录能否放行这次调用：参数哈希完全一致、未过期、once 的还没用过。
func (r *ApprovalRecord) Matches(taskID, tool, hash string, now time.Time) bool {
	return r.Decision == Allow && r.TaskID == taskID && r.ToolName == tool &&
		r.ArgsHash == hash && now.Before(r.ExpiresAt) && !(r.Scope == ScopeOnce && r.Used)
}

// ArgsHash 对参数做规范化（键排序）后取 sha256。
func ArgsHash(args json.RawMessage) string {
	var v any
	canon := []byte(args)
	if json.Unmarshal(args, &v) == nil {
		canon, _ = json.Marshal(v)
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])
}

// DefaultDanger 是默认的危险命令规则：命中后一律升级给用户（never 模式下直接拒绝）。
var DefaultDanger = []string{
	`\brm\s+(-\S*\s+)*-\S*[rR]`,           // 递归删除
	`\brm\s+(-\S+\s+)*(/|~|\$HOME)(\s|$)`, // 删根目录 / 家目录
	`\bsudo\b|\bsu\s|\bdoas\b`,
	`\b(curl|wget)\b.*\|\s*(ba|z)?sh\b`,                // curl | sh
	`\b(curl|wget|nc|ncat|scp|rsync|ssh|ftp|telnet)\b`, // 网络访问（可能外传数据）
	`\bmkfs|\bdd\s+if=|>\s*/dev/sd`,
	`:\(\)\s*\{`, // fork 炸弹
	`\bchmod\s+(-R\s+)?777|\bchown\s+-R`,
	`\b(shutdown|reboot|halt|poweroff)\b`,
	`\bgit\s+push\b|\bgit\s+reset\s+--hard`,
	`\.ssh/|id_rsa|\.aws/|\.env\b|\bprintenv\b`, // 读密钥
}

// Rules 是可配置的确定性规则（正则）。
type Rules struct {
	AllowShell  []string // 命中则直接放行（危险规则优先）
	DenyShell   []string // 命中则直接拒绝（最高优先）
	ExtraDanger []string // 追加的危险规则
	DenyTools   []string // 完全禁用的工具
}

// Guard 是每次工具调用执行前的分层审批：规则 -> 审核模型 -> 用户。
type Guard struct {
	Reviewer        Reviewer
	Approver        Approver
	Store           ApprovalRecorder // 可选：保存审批记录
	ApprovalTimeout time.Duration    // 等用户多久，默认 10 分钟
	GrantTTL        time.Duration    // 批准记录有效期，默认 30 分钟
	Now             func() time.Time
	Secrets         Secrets // 可选：密钥库，负责 {{secret:NAME}} 替换和输出脱敏

	denyTools           map[string]bool
	allow, deny, danger []*regexp.Regexp
	mu                  sync.Mutex
	records             []*ApprovalRecord
}

func NewGuard(r Rules, rev Reviewer, ap Approver) (*Guard, error) {
	g := &Guard{Reviewer: rev, Approver: ap, denyTools: map[string]bool{}}
	for _, t := range r.DenyTools {
		g.denyTools[t] = true
	}
	var err error
	if g.allow, err = compileAll(r.AllowShell); err != nil {
		return nil, err
	}
	if g.deny, err = compileAll(r.DenyShell); err != nil {
		return nil, err
	}
	if g.danger, err = compileAll(append(append([]string{}, DefaultDanger...), r.ExtraDanger...)); err != nil {
		return nil, err
	}
	return g, nil
}

func compileAll(pats []string) ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for _, p := range pats {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("bad rule %q: %w", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func anyMatch(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func (g *Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Classify 只跑确定性规则和审批模式（不调模型、不问用户），返回判断和风险等级。
func (g *Guard) Classify(t Task, tool Tool, args json.RawMessage) (Verdict, Risk) {
	name := tool.Name()
	if g.denyTools[name] {
		return Verdict{Deny, "该工具已被禁用", "rule"}, RiskDanger
	}
	var m map[string]any
	_ = json.Unmarshal(args, &m)
	if p, ok := m["path"].(string); ok {
		if _, err := ResolvePath(t.Workspace, p); err != nil {
			return Verdict{Deny, err.Error(), "rule"}, RiskDanger
		}
	}
	risk := tool.Risk(args)
	cmd, isCmd := m["command"].(string)
	if isCmd && anyMatch(g.deny, cmd) {
		return Verdict{Deny, "命令命中拒绝名单", "rule"}, RiskDanger
	}
	if refs := SecretRefs(args); len(refs) > 0 && !handlesSecrets(tool) {
		// 用到密钥的调用一律要用户确认（哪怕是 auto 策略）；policy=never 时 askUser 会直接拒绝。
		if g.Secrets == nil {
			return Verdict{Deny, "没有配置密钥库，不能引用 {{secret:…}}", "rule"}, risk
		}
		if err := g.Secrets.Allowed(tool.Name(), args, refs); err != nil {
			return Verdict{Deny, err.Error(), "rule"}, risk
		}
		if isCmd && anyMatch(g.danger, cmd) {
			risk = RiskDanger
		}
		return Verdict{AskUser, "要使用密钥 " + strings.Join(refs, "、") + "（真实值只在执行时替换，不会给模型看）", "rule"}, max(risk, RiskExec)
	}
	if isCmd {
		switch {
		case anyMatch(g.danger, cmd):
			risk = RiskDanger
		case anyMatch(g.allow, cmd):
			return Verdict{Allow, "命令命中允许名单", "rule"}, risk
		}
	}
	if risk == RiskRead {
		return Verdict{Allow, "工作区内只读操作", "rule"}, risk
	}
	switch t.Policy {
	case PolicyNever:
		if risk == RiskDanger {
			return Verdict{Deny, "危险操作，且当前模式不询问用户", "policy"}, risk
		}
		return Verdict{Allow, "never 模式自动放行", "policy"}, risk
	case PolicyAuto:
		if risk == RiskDanger {
			return Verdict{AskUser, "危险操作需要用户确认", "policy"}, risk
		}
		return Verdict{review, "交给审核模型", "policy"}, risk
	case PolicyUntrusted:
		return Verdict{AskUser, "当前模式下，改文件和执行命令都要先经过你确认", "policy"}, risk
	default: // on-request
		if risk == RiskDanger {
			return Verdict{AskUser, "危险操作需要用户确认", "policy"}, risk
		}
		return Verdict{Allow, "工作区内的普通操作", "policy"}, risk
	}
}

// Grant 添加一条审批记录（例如从数据库恢复）。
func (g *Guard) Grant(r ApprovalRecord) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.records = append(g.records, &r)
}

// Consume 查找能放行这次调用的记录；once 记录会被标记为已用。
func (g *Guard) Consume(taskID, tool, hash string) (ApprovalRecord, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for _, r := range g.records {
		if r.Matches(taskID, tool, hash, now) {
			if r.Scope == ScopeOnce {
				r.Used = true
			}
			return *r, true
		}
	}
	return ApprovalRecord{}, false
}

// Check 是完整的审批流程，在工具执行前调用。emit 用来发出审批事件。
func (g *Guard) Check(ctx context.Context, t Task, tool Tool, args json.RawMessage, emit func(EventType, any)) Verdict {
	v, risk := g.Classify(t, tool, args)
	if v.Decision == Allow || v.Decision == Deny {
		return v
	}
	hash := ArgsHash(args)
	if rec, ok := g.Consume(t.ID, tool.Name(), hash); ok { // 之前批过（session 范围）
		return Verdict{Allow, "已有用户批准记录", "approval:" + rec.ID}
	}
	if v.Decision == review {
		v = Verdict{AskUser, "没有配置审核模型", "policy"}
		if g.Reviewer != nil {
			rctx, cancel := context.WithTimeout(ctx, time.Minute)
			rv, err := g.Reviewer.Review(rctx, ReviewRequest{TaskID: t.ID, TaskInput: t.Input,
				ToolName: tool.Name(), Args: args, Risk: risk, Workspace: t.Workspace})
			cancel()
			rv.By = "reviewer"
			if err != nil {
				rv = Verdict{AskUser, "审核模型出错：" + err.Error(), "reviewer"}
			}
			if rv.Decision != AskUser {
				return rv
			}
			v = rv
		}
	}
	return g.askUser(ctx, t, tool.Name(), args, hash, risk, v.Reason, false, emit)
}

func (g *Guard) askUser(ctx context.Context, t Task, tool string, args json.RawMessage, hash string, risk Risk, reason string, onceOnly bool, emit func(EventType, any)) Verdict {
	if t.Policy == PolicyNever || g.Approver == nil {
		return Verdict{Deny, reason + "（无法询问用户）", "policy"}
	}
	timeout := g.ApprovalTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	now := g.now()
	req := ApprovalRequest{ID: NewID("appr"), TaskID: t.ID, BotID: t.BotID, ToolName: tool, Args: args,
		ArgsHash: hash, Risk: risk, Reason: reason, OnceOnly: onceOnly, CreatedAt: now, ExpiresAt: now.Add(timeout)}
	emit(EvApprovalRequested, req)
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d, err := g.Approver.RequestApproval(actx, req) // 阻塞等待，不消耗 token
	rec := ApprovalRecord{ID: req.ID, TaskID: t.ID, ToolName: tool, ArgsHash: hash, Decision: Deny,
		Scope: ScopeOnce, CreatedAt: g.now(), DecidedBy: d.DecidedBy}
	verdict := Verdict{Deny, "用户拒绝了这个操作", "user"}
	switch {
	case err != nil:
		verdict.Reason = "等待用户确认超时或任务被取消"
		rec.DecidedBy = "timeout"
	case d.Approve:
		rec.Decision = Allow
		if d.Scope == ScopeSession && !onceOnly {
			rec.Scope = ScopeSession
		}
	}
	if rec.DecidedBy == "" {
		rec.DecidedBy = "user"
	}
	ttl := g.GrantTTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	rec.ExpiresAt = rec.CreatedAt.Add(ttl)
	g.Grant(rec)
	if g.Store != nil {
		_ = g.Store.SaveApproval(context.WithoutCancel(ctx), rec)
	}
	if rec.Decision == Allow {
		if r, ok := g.Consume(t.ID, tool, hash); ok { // 用记录本身去放行，保证哈希绑定
			verdict = Verdict{Allow, "用户已批准", "approval:" + r.ID}
		}
	}
	emit(EvApprovalDecided, map[string]any{"approval_id": req.ID, "decision": verdict.Decision,
		"scope": rec.Scope, "decided_by": rec.DecidedBy, "reason": verdict.Reason})
	return verdict
}

// DeniedMessage 是被拒绝时返回给模型的工具结果。
func DeniedMessage(v Verdict) string {
	return fmt.Sprintf("DENIED (%s): %s\n这个操作没有被批准。不要重试，也不要换别的方法达到同样效果；"+
		"请向用户说明情况，然后继续做其他部分或结束任务。", v.By, strings.TrimSpace(v.Reason))
}

// QueueApprover 把审批请求挂起，等外部（例如 HTTP 接口收到用户点击）调用 Resolve。
type QueueApprover struct {
	mu      sync.Mutex
	pending map[string]pendingApproval
}

type pendingApproval struct {
	req ApprovalRequest
	ch  chan UserDecision
}

func NewQueueApprover() *QueueApprover {
	return &QueueApprover{pending: map[string]pendingApproval{}}
}

func (q *QueueApprover) RequestApproval(ctx context.Context, req ApprovalRequest) (UserDecision, error) {
	p := pendingApproval{req, make(chan UserDecision, 1)}
	q.mu.Lock()
	q.pending[req.ID] = p
	q.mu.Unlock()
	defer func() { q.mu.Lock(); delete(q.pending, req.ID); q.mu.Unlock() }()
	select {
	case d := <-p.ch:
		return d, nil
	case <-ctx.Done():
		return UserDecision{}, ctx.Err()
	}
}

// DenyAll 拒绝当前挂起的全部审批。停止或关闭时调用，避免批准在任务取消后仍然生效。
func (q *QueueApprover) DenyAll(reason string) {
	q.mu.Lock()
	pending := q.pending
	q.pending = map[string]pendingApproval{}
	q.mu.Unlock()
	for _, p := range pending {
		p.ch <- UserDecision{DecidedBy: "stop", Note: reason}
	}
}

// Resolve 提交用户的决定。只应在后端确认是用户本人点击后调用。
func (q *QueueApprover) Resolve(id string, d UserDecision) error {
	q.mu.Lock()
	p, ok := q.pending[id]
	delete(q.pending, id)
	q.mu.Unlock()
	if !ok {
		return fmt.Errorf("approval %s not pending (expired or already decided)", id)
	}
	p.ch <- d
	return nil
}

func (q *QueueApprover) Pending() []ApprovalRequest {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]ApprovalRequest, 0, len(q.pending))
	for _, p := range q.pending {
		out = append(out, p.req)
	}
	return out
}
