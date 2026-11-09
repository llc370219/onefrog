package bot

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"

	"onefrog/internal/audit"
	"onefrog/internal/id"
	"onefrog/internal/model"
	"onefrog/internal/preferences"
)

// The Frog Bot runtime (internal/frogbot) is OneFrog's AI layer: chat layer,
// background agents, supervisor and event log. The OneFrog operations below
// are offered to its background agents as tools. Every call goes through the
// same shared authorization hook as before (approval mode, rule findings,
// selected-server scope, host-key pinning, secret expansion, audit), and the
// operator decision is shown as a Frog Bot approval card.

// frogBridged lists the OneFrog tools a Frog Bot agent may call. Chat-only
// tools (ask_user, request_secret, memory, delegation) are provided by Frog
// Bot itself; custom modules are built on their own page.
var frogBridged = []string{"run_command", "connect_server", "inspect_services", "panel", "install_probe",
	"manage_module_record", "manage_modules", "generate_secret"}

// FrogTool is a OneFrog tool offered to a Frog Bot agent.
type FrogTool struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// FrogTools returns the OneFrog tools the bot may use, honouring its role
// (the module bot only manages modules and records, and so on).
func (a *Agent) FrogTools(ctx context.Context, botID string) []FrogTool {
	speaker, err := a.Store.GetBot(ctx, botID)
	if err != nil {
		return nil
	}
	ctx = context.WithValue(ctx, optionsKey{}, Options{RestrictServers: true})
	var out []FrogTool
	seen := map[string]bool{}
	for _, def := range a.toolSpec(ctx, speaker, false) {
		fn, _ := def["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if !slices.Contains(frogBridged, name) || seen[name] {
			continue
		}
		if name == "panel" || name == "install_probe" || name == "connect_server" || name == "generate_secret" {
			if a.managesAssets(speaker) || a.developsModules(speaker) {
				continue
			}
		}
		schema, err := json.Marshal(fn["parameters"])
		if err != nil {
			continue
		}
		desc, _ := fn["description"].(string)
		seen[name] = true
		out = append(out, FrogTool{Name: name, Description: desc, Schema: schema})
	}
	return out
}

// FrogSession carries the per-task state the authorization hook relies on:
// the user's request (commands copied from it are not "untrusted"), content
// read from servers (commands copied from it ask first) and servers connected
// during the task.
type FrogSession struct {
	request string
	taint   *taintSet
	grants  *grants
}

func NewFrogSession(request string) *FrogSession {
	return &FrogSession{request: request, taint: &taintSet{}, grants: &grants{ids: map[string]struct{}{}}}
}

// FrogSessions keeps one session per running task.
type FrogSessions struct {
	mu   sync.Mutex
	byID map[string]*FrogSession
}

func (s *FrogSessions) Get(taskID, request string) *FrogSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID == nil {
		s.byID = map[string]*FrogSession{}
	}
	if x := s.byID[taskID]; x != nil {
		return x
	}
	if len(s.byID) > 512 { // finished tasks are not tracked; keep the map bounded
		s.byID = map[string]*FrogSession{}
	}
	x := NewFrogSession(request)
	s.byID[taskID] = x
	return x
}

// FrogCall is one tool call from a Frog Bot agent.
type FrogCall struct {
	Owner    string
	BotID    string
	Session  *FrogSession
	Selected []string // servers the user picked for this bot (or this automation run)
	Name     string
	Args     json.RawMessage
	// Ask shows an approval card and waits for the operator.
	Ask func(req ApprovalRequest) (bool, string)
	// ApprovalMode overrides the account setting (an automation's own mode).
	ApprovalMode string
	// Background marks an unattended run (automation): "off" is raised to
	// smart, as for classic background runs.
	Background bool
}

// ExecFrogTool runs one bridged OneFrog tool for a Frog Bot agent.
func (a *Agent) ExecFrogTool(ctx context.Context, c FrogCall) (string, error) {
	if !slices.Contains(frogBridged, c.Name) {
		return "", errors.New("未知工具")
	}
	speaker, err := a.Store.GetBot(ctx, c.BotID)
	if err != nil {
		return "", errors.New("Bot 不存在")
	}
	if !a.allowedBot(ctx, speaker.ID) {
		return "", errors.New("该 Bot 已停用")
	}
	allowed := false
	for _, t := range a.FrogTools(ctx, speaker.ID) {
		allowed = allowed || t.Name == c.Name
	}
	if !allowed {
		return "", errors.New("这个 Bot 不能使用 " + c.Name)
	}
	var args struct {
		ServerID string `json:"server_id"`
		Command  string `json:"command"`
	}
	_ = json.Unmarshal(c.Args, &args)
	if c.Name == "run_command" {
		// Catastrophic commands stay blocked whatever the approval mode:
		// approval never lifts a capability limit.
		if hard := audit.Decide(args.Command); !hard.Allow {
			return "", errors.New(hard.Reason)
		}
	}
	// The selected scope decides whether a server is "chosen by the user".
	// A call to a selected server follows the approval mode (only risky
	// commands ask); any other server always asks first.
	fallback := ""
	target := strings.TrimSpace(args.ServerID)
	if target == "" && len(c.Selected) == 1 {
		fallback = c.Selected[0]
	} else if target != "" {
		if slices.Contains(c.Selected, target) {
			fallback = target
		} else if rec, err := a.serverByName(ctx, target); err == nil && slices.Contains(c.Selected, rec.ID) {
			fallback = rec.ID
		}
	}
	opt := Options{OwnerID: c.Owner, RestrictServers: true}
	if mode, ok := preferences.NormalizeMode(c.ApprovalMode); ok && mode != preferences.ModeUnset {
		opt.ApprovalMode = mode
	}
	if c.Background && a.approvalMode(context.WithValue(ctx, optionsKey{}, opt)) == preferences.ModeOff {
		opt.ApprovalMode = preferences.ModeSmart
	}
	if c.Ask != nil {
		opt.Approver = func(_ context.Context, req ApprovalRequest) (bool, string) {
			ok, note := c.Ask(req)
			if ok {
				return true, "用户已在 AI 助理里批准"
			}
			if strings.TrimSpace(note) == "" {
				note = "用户拒绝执行"
			}
			return false, note
		}
	}
	ctx = context.WithValue(ctx, optionsKey{}, opt)
	sess := c.Session
	if sess == nil {
		sess = NewFrogSession("")
	}
	ctx = context.WithValue(ctx, taintKey{}, sess.taint)
	ctx = context.WithValue(ctx, grantKey{}, sess.grants)
	ctx = context.WithValue(ctx, taskKey{}, sess.request)
	ctx = usageContext(ctx, speaker.ID, "frog-agent", "")
	var call toolCall
	call.ID, call.Type = id.New(), "function"
	call.Function.Name, call.Function.Arguments = c.Name, string(c.Args)
	if len(c.Args) == 0 {
		call.Function.Arguments = "{}"
	}
	out := a.execCall(ctx, speaker, credentials{}, fallback, call, false, nil)
	return a.redact(ctx, out), nil
}

// FrogServers lists the servers a Frog Bot may see, marking the selected ones.
type FrogServer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Role     string `json:"role,omitempty"`
	Status   string `json:"status,omitempty"`
	Selected bool   `json:"selected"`
	Pinned   bool   `json:"fingerprint_confirmed"`
}

func (a *Agent) FrogServers(ctx context.Context, selected []string) ([]FrogServer, error) {
	servers, err := a.Store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]FrogServer, 0, len(servers))
	for _, s := range servers {
		row := FrogServer{ID: s.ID, Name: s.Name, Role: s.Role, Status: s.Status, Selected: slices.Contains(selected, s.ID)}
		if rec, err := a.Store.GetServer(ctx, s.ID); err == nil {
			row.Pinned = rec.HostKey != ""
		}
		out = append(out, row)
	}
	return out, nil
}

// FrogBotRow is a OneFrog bot that runs as a Frog Bot.
type FrogBotRow struct {
	model.Bot
	Leader bool
}

// FrogBotRows lists the bots that appear in the AI assistant: every enabled
// bot except the custom-module builder, which lives on its own page. The
// leader (主控) comes first.
func (a *Agent) FrogBotRows(ctx context.Context) ([]FrogBotRow, error) {
	bots, err := a.Store.ListBots(ctx)
	if err != nil {
		return nil, err
	}
	lead, _ := a.pickBot(ctx, "")
	var out []FrogBotRow
	for _, b := range bots {
		if a.developsModules(b) || !a.allowedBot(ctx, b.ID) {
			continue
		}
		row := FrogBotRow{Bot: b, Leader: b.ID == lead.ID}
		if row.Leader {
			out = append([]FrogBotRow{row}, out...)
		} else {
			out = append(out, row)
		}
	}
	return out, nil
}
