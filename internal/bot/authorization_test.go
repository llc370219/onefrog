package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

func TestParseReviewThreeState(t *testing.T) {
	for _, tc := range []struct {
		name, reply, decision, risk string
		wantErr                     bool
	}{
		{"allow", `{"decision":"allow","risk":"low","reason":"只读检查"}`, "allow", "low", false},
		{"deny", `{"decision":"deny","risk":"medium","reason":"超出范围"}`, "deny", "medium", false},
		{"ask", `{"decision":"ask","risk":"high","reason":"需要用户确认"}`, "ask", "high", false},
		{"allow high is not forced deny", `{"decision":"allow","risk":"high","reason":"用户要求重启"}`, "allow", "high", false},
		{"legacy allow", `{"allow":true,"risk":"high","reason":"兼容旧格式"}`, "allow", "high", false},
		{"legacy deny", `{"allow":false,"risk":"low","reason":"兼容旧格式"}`, "deny", "low", false},
		{"fenced", "```\n{\"decision\":\"deny\",\"risk\":\"low\",\"reason\":\"x\"}\n```", "deny", "low", false},
		{"invalid json", `maybe`, "", "", true},
		{"missing reason", `{"decision":"allow","risk":"low"}`, "", "", true},
		{"invalid decision", `{"decision":"maybe","risk":"low","reason":"x"}`, "", "", true},
		{"invalid risk", `{"decision":"allow","risk":"extreme","reason":"x"}`, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseReview(tc.reply)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Decision != tc.decision || got.Risk != tc.risk {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

// TestCentralHookDefaultsToAuthorization proves runTool never bypasses the
// shared hook when AuthorizeCall is nil: a risky command without an operator
// becomes a durable pending, not a silent allow.
func TestCentralHookDefaultsToAuthorization(t *testing.T) {
	a, ssh, mem := modeAgent(t, "smart")
	ev := a.runTool(context.Background(), "bot", "srv", runCall("systemctl restart nginx", "medium"))
	if ev.Output != PendingNotice {
		t.Fatalf("expected pending notice, got %q", ev.Output)
	}
	if ssh.command != "" {
		t.Fatalf("parked command executed: %q", ssh.command)
	}
	h, _ := mem.ListHistory(context.Background(), 20)
	for _, row := range h {
		if row.Status == "approval_denied" {
			t.Fatal("pending must not be recorded as a completed denial")
		}
	}
}

// TestUserAuthorizedDangerousWordRuns: delete/restart keywords are evidence,
// not an automatic denial.
func TestUserAuthorizedDangerousWordRuns(t *testing.T) {
	a, ssh, _ := modeAgent(t, "smart")
	asked := 0
	ctx := context.WithValue(context.Background(), optionsKey{}, Options{
		Approver: func(context.Context, ApprovalRequest) (bool, string) {
			asked++
			return true, "用户已批准本次操作"
		},
	})
	ctx = context.WithValue(ctx, taskKey{}, "把 web-1 的旧缓存删掉并重启 nginx")
	ev := a.runTool(ctx, "bot", "srv", runCall("rm -rf /var/tmp/cache && systemctl restart nginx", "medium"))
	if !ev.OK || ssh.command == "" {
		t.Fatalf("user-authorized command did not run: %s", ev.Output)
	}
	if asked != 1 {
		t.Fatalf("expected exactly one approval card, got %d", asked)
	}
}

// TestNestedSameBotSameServerCallNotAuthorized: a second call for the same bot
// and server must not inherit the first call's grant.
func TestNestedSameBotSameServerCallNotAuthorized(t *testing.T) {
	a, ssh, _ := modeAgent(t, "smart")
	approvals := 0
	ctx := context.WithValue(context.Background(), optionsKey{}, Options{
		Approver: func(context.Context, ApprovalRequest) (bool, string) {
			approvals++
			// Only the first call is allowed; the nested call must ask again and
			// be refused on its own merits.
			return approvals == 1, ""
		},
	})
	first := runCall("systemctl restart nginx", "medium")
	first.ID = "call-1"
	next, refusal := a.authorizeCall(ctx, model.Bot{ID: "bot"}, "srv", first)
	if refusal != "" {
		t.Fatalf("first call refused: %s", refusal)
	}
	second := runCall("rm -rf /var/tmp/x", "medium")
	second.ID = "call-2"
	if grantForCall(next, "bot", "srv", second) {
		t.Fatal("second call inherited the first call's grant")
	}
	if hookAlreadyRan(next, "bot", second) {
		t.Fatal("second call inherited the first call's hook mark")
	}
	// The second call must ask again (no inherited grant) and, being refused on
	// its own, must not run.
	ssh.command = ""
	ev := a.runTool(next, "bot", "srv", second)
	if ssh.command != "" || ev.OK {
		t.Fatalf("nested call ran without its own approval: %q / %q", ev.Output, ssh.command)
	}
	if approvals != 2 {
		t.Fatalf("nested call reused the first approval: %d cards", approvals)
	}
}

// TestPermitIsStrictlyScoped: a permit needs every identity field and is
// consumed once; wildcard/empty fields never match.
func TestPermitIsStrictlyScoped(t *testing.T) {
	call := runCall("uptime", "low")
	call.ID = "c-1"
	base := &callPermit{BotID: "bot", ServerID: "srv", Tool: "run_command", CallID: "c-1", Hash: argsHash(call), Command: "uptime"}
	if !permitMatches(base, "bot", "srv", call) {
		t.Fatal("exact permit should match")
	}
	for _, tc := range []struct {
		name   string
		permit *callPermit
		bot    string
		server string
		call   toolCall
	}{
		{"other bot", base, "other", "srv", call},
		{"other server", base, "bot", "other", call},
		{"empty bot", &callPermit{ServerID: "srv", Tool: "run_command", CallID: "c-1", Hash: argsHash(call)}, "bot", "srv", call},
		{"empty server", &callPermit{BotID: "bot", Tool: "run_command", CallID: "c-1", Hash: argsHash(call)}, "bot", "srv", call},
		{"empty hash", &callPermit{BotID: "bot", ServerID: "srv", Tool: "run_command", CallID: "c-1"}, "bot", "srv", call},
		{"other call id", &callPermit{BotID: "bot", ServerID: "srv", Tool: "run_command", CallID: "c-2", Hash: argsHash(call)}, "bot", "srv", call},
		{"other tool", &callPermit{BotID: "bot", ServerID: "srv", Tool: "panel", CallID: "c-1", Hash: argsHash(call)}, "bot", "srv", call},
	} {
		if permitMatches(tc.permit, tc.bot, tc.server, tc.call) {
			t.Fatalf("%s: permit matched but should not", tc.name)
		}
	}
	// Consumed permits never match twice.
	ctx := withCallPermit(withCurrentCall(context.Background(), "c-1"), base)
	if !consumePermitForCall(ctx, "bot", "srv", call) {
		t.Fatal("first consumption should match")
	}
	if consumePermitForCall(ctx, "bot", "srv", call) {
		t.Fatal("permit was consumed twice")
	}
}

// TestGrantRequiresEffectiveServer: the grant must carry the effective target,
// not just any server the model named.
func TestGrantRequiresEffectiveServer(t *testing.T) {
	a, _, _ := modeAgent(t, "smart")
	if _, err := a.Store.CreateServer(context.Background(), store.ServerRec{Server: model.Server{ID: "srv2", Name: "db-2"}, HostKey: "SHA256:test"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), optionsKey{}, Options{
		Approver: func(context.Context, ApprovalRequest) (bool, string) { return true, "" },
	})
	call := runCall("uptime", "low")
	call.ID = "c-1"
	next, refusal := a.authorizeCall(ctx, model.Bot{ID: "bot"}, "srv", call)
	if refusal != "" {
		t.Fatalf("unexpected refusal: %s", refusal)
	}
	if grantForCall(next, "bot", "srv2", call) {
		t.Fatal("grant matched a different effective server")
	}
	if !grantForCall(next, "bot", "srv", call) {
		t.Fatal("grant lost its own effective server")
	}
}

// TestReviewerFailureAsksOperator: reviewer errors and invalid JSON become an
// ask, never a silent allow, and allow+high is honoured.
func TestReviewerFailureAsksOperator(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reply   string
		status  int
		allow   bool
		wantRun bool
	}{
		{"reviewer allows high risk", `{"decision":"allow","risk":"high","reason":"用户要求重启"}`, 200, true, true},
		{"reviewer denies", `{"decision":"deny","risk":"medium","reason":"超出范围"}`, 200, false, false},
		{"reviewer asks", `{"decision":"ask","risk":"medium","reason":"需要用户确认"}`, 200, true, true},
		{"reviewer invalid json", `not json`, 200, true, true},
		{"reviewer error", `{}`, 500, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			box, _ := secret.New(bytesRepeat())
			settings, err := preferences.New("", box)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := settings.Save(preferences.Input{
				Providers: []preferences.ProviderInput{{Provider: preferences.Provider{ID: "review", Name: "review", BaseURL: "http://review.example/v1", Enabled: true, AuthRequired: true}, APIKey: "review-key"}},
				Default:   preferences.Route{ProviderID: "review", Model: "worker"},
				Approval:  preferences.Approval{Mode: "model", Route: preferences.Route{ProviderID: "review", Model: "review-only"}},
				Execution: preferences.Defaults().Execution,
			}); err != nil {
				t.Fatal(err)
			}
			mem := store.NewMemory()
			ssh := &scriptSSH{}
			_, _ = mem.CreateServer(context.Background(), store.ServerRec{Server: model.Server{ID: "srv", Name: "web-1"}, HostKey: "SHA256:test"})
			a := &Agent{Store: mem, Box: box, SSH: ssh, Settings: settings}
			transport := reviewTransport(func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), `\"user_request\"`) || !strings.Contains(string(body), `\"scope\"`) {
					t.Fatalf("reviewer payload missing original authorization context: %s", body)
				}
				if strings.Contains(string(body), `\"model_risk\"`) || strings.Contains(string(body), `\"executor_risk\"`) {
					t.Fatalf("reviewer payload must not carry the executor's risk self-report: %s", body)
				}
				if !strings.Contains(string(body), `\"candidate_args\"`) || !strings.Contains(string(body), `systemctl restart nginx`) {
					t.Fatalf("reviewer payload missing the candidate's full arguments: %s", body)
				}
				if r.Header.Get("Authorization") != "Bearer review-key" {
					t.Fatal("reviewer used the wrong key")
				}
				reply := `{"choices":[{"message":{"content":` + jsonString(tc.reply) + `}}]}`
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(reply)), Header: make(http.Header)}, nil
			})
			a.HTTP = &http.Client{Transport: transport}
			route := preferences.Route{ProviderID: "review", Model: "review-only"}
			asked := 0
			ctx := context.WithValue(context.Background(), optionsKey{}, Options{
				ApprovalRoute: &route,
				Approver: func(context.Context, ApprovalRequest) (bool, string) {
					asked++
					return tc.allow, ""
				},
			})
			ssh.command = ""
			ev := a.runTool(ctx, "bot", "srv", runCall("systemctl restart nginx", "medium"))
			ran := ssh.command != ""
			if ran != tc.wantRun || ev.OK != tc.wantRun {
				t.Fatalf("ran=%v ok=%v output=%s", ran, ev.OK, ev.Output)
			}
			switch tc.reply {
			case `{"decision":"allow","risk":"high","reason":"用户要求重启"}`:
				if asked != 0 {
					t.Fatalf("reviewer allow should not ask the operator, asked=%d", asked)
				}
			case `{"decision":"deny","risk":"medium","reason":"超出范围"}`:
				if asked != 0 {
					t.Fatalf("reviewer deny must not ask the operator, asked=%d", asked)
				}
			default:
				if asked == 0 {
					t.Fatal("unavailable/ambiguous reviewer must fall back to the operator")
				}
			}
		})
	}
}

// TestReadOnlyExceptionsDoNotAsk: read/recall/project remember and read-only
// workspace/panel actions stay approval-exempt.
func TestReadOnlyExceptionsDoNotAsk(t *testing.T) {
	call := func(name, args string) toolCall {
		c := toolCall{ID: name + "-1", Type: "function"}
		c.Function.Name = name
		c.Function.Arguments = args
		return c
	}
	for _, c := range []toolCall{
		call("recall", `{"query":""}`),
		call("remember", `{"title":"x","body":"y"}`),
		call("workspace", `{"action":"read","path":"main.go"}`),
		call("panel", `{"action":"overview"}`),
		call("inspect_services", `{"action":"list"}`),
		call("manage_modules", `{"action":"list"}`),
		call("develop_module", `{"action":"list"}`),
	} {
		if !readOnlyCall(c.Function.Name, c.Function.Arguments) {
			t.Fatalf("%s should be approval-exempt", c.Function.Name)
		}
	}
	for _, c := range []toolCall{
		call("panel", `{"action":"delete_server","id":"srv"}`),
		call("workspace", `{"action":"write","path":"a.go","content":"x"}`),
		call("develop_module", `{"action":"delete","id":"m"}`),
		call("run_command", `{"command":"uptime"}`),
	} {
		if readOnlyCall(c.Function.Name, c.Function.Arguments) {
			t.Fatalf("%s must not be approval-exempt", c.Function.Name)
		}
	}
}

// TestIllegalActionNotExecuted: unknown actions are refused by the hook.
func TestIllegalActionNotExecuted(t *testing.T) {
	a, _, _ := modeAgent(t, "smart")
	for _, tc := range []struct{ name, args string }{
		{"panel", `{"action":"destroy_everything"}`},
		{"develop_module", `{"action":"nuke"}`},
		{"manage_modules", `{"action":"drop"}`},
		{"install_probe", `{"action":"format"}`},
	} {
		c := toolCall{ID: "x", Type: "function"}
		c.Function.Name = tc.name
		c.Function.Arguments = tc.args
		if _, refusal := a.authorizeCall(context.Background(), model.Bot{ID: "bot"}, "srv", c); refusal == "" {
			t.Fatalf("%s/%s was not refused", tc.name, tc.args)
		}
	}
}

// TestApprovedCallSkipsSecondCard: the operator's allow for one call is reused
// by that same call's handler without a second card.
func TestApprovedCallSkipsSecondCard(t *testing.T) {
	a, ssh, _ := modeAgent(t, "smart")
	asked := 0
	ctx := context.WithValue(context.Background(), optionsKey{}, Options{
		Approver: func(context.Context, ApprovalRequest) (bool, string) { asked++; return true, "" },
	})
	call := runCall("systemctl restart nginx", "medium")
	call.ID = "c-1"
	next, refusal := a.authorizeCall(ctx, model.Bot{ID: "bot"}, "srv", call)
	if refusal != "" {
		t.Fatalf("refused: %s", refusal)
	}
	ev := a.runTool(next, "bot", "srv", call)
	if !ev.OK || ssh.command == "" {
		t.Fatalf("authorized call did not run: %s", ev.Output)
	}
	if asked != 1 {
		t.Fatalf("handler asked again: %d cards", asked)
	}
}

// TestHookMarkIsScoped keeps the runtime hook deduplication from leaking to
// another server or tool.
func TestHookMarkIsScoped(t *testing.T) {
	a, _, _ := modeAgent(t, "smart")
	call := runCall("uptime", "low")
	call.ID = "c-1"
	next, _ := a.authorizeCall(context.Background(), model.Bot{ID: "bot"}, "srv", call)
	if !hookAlreadyRan(next, "bot", call) {
		t.Fatal("hook mark missing for its own call")
	}
	other := runCall("uptime", "low")
	other.ID = "c-2"
	if hookAlreadyRan(next, "bot", other) {
		t.Fatal("hook mark leaked to another call")
	}
	mark := next.Value(hookMarkKey{}).(hookMark)
	if mark.ServerID != "srv" || mark.Tool != "run_command" {
		t.Fatalf("hook mark missing effective identity: %+v", mark)
	}
	if _, ok := json.Marshal(mark); ok != nil {
		t.Fatal("hook mark should be marshalable for diagnostics")
	}
}

// TestSameCallIDDifferentToolCannotInheritGrant: a nested call that reuses the
// call id but changes the tool or arguments must not reuse the handler grant.
func TestSameCallIDDifferentToolCannotInheritGrant(t *testing.T) {
	a, ssh, _ := modeAgent(t, "smart")
	approvals := 0
	ctx := context.WithValue(context.Background(), optionsKey{}, Options{
		Approver: func(context.Context, ApprovalRequest) (bool, string) {
			approvals++
			return true, ""
		},
	})
	first := runCall("systemctl restart nginx", "medium")
	first.ID = "same-id"
	next, refusal := a.authorizeCall(ctx, model.Bot{ID: "bot"}, "srv", first)
	if refusal != "" {
		t.Fatalf("first call refused: %s", refusal)
	}
	if !grantForCall(next, "bot", "srv", first) {
		t.Fatal("first call lost its own grant")
	}
	// Same call id, same bot and server, but another tool and arguments.
	panel := toolCall{ID: "same-id", Type: "function"}
	panel.Function.Name = "panel"
	panel.Function.Arguments = `{"action":"delete_server","id":"srv"}`
	if grantForCall(next, "bot", "srv", panel) {
		t.Fatal("different tool inherited the grant via grantForCall")
	}
	// The handler grant still refers to the run_command call that was actually
	// authorized. Once a different tool runs through the hook, the identity is
	// replaced and the old grant no longer matches.
	panelNext, _ := a.authorizeCall(next, model.Bot{ID: "bot"}, "srv", panel)
	if grantForHandler(panelNext, "bot", "srv") && grantForCall(panelNext, "bot", "srv", first) {
		t.Fatal("old run_command grant survived the new panel call")
	}
	// A nested run_command with other arguments must ask again. Its own approval
	// then covers that nested call only.
	nested := runCall("rm -rf /var/tmp/y", "medium")
	nested.ID = "same-id"
	ssh.command = ""
	ev := a.runTool(next, "bot", "srv", nested)
	if approvals < 2 {
		t.Fatalf("nested call reused the approval: %d cards", approvals)
	}
	if !ev.OK || ssh.command == "" {
		t.Fatalf("nested call did not run after its own approval: %s", ev.Output)
	}
	if grantForCall(next, "bot", "srv", nested) {
		t.Fatal("nested call reused the outer call's grant")
	}
}

// TestPermitCannotBeBypassedByCallIDOnly: an ID-only "already approved" check
// must not let a permit cover a different tool or different arguments.
func TestPermitCannotBeBypassedByCallIDOnly(t *testing.T) {
	approved := runCall("systemctl restart nginx", "medium")
	approved.ID = "c-1"
	permit := PermitForApprovedCall("bot", "srv", approved)

	// The exact call matches.
	ctx := withCallPermit(withCurrentCall(context.Background(), "c-1"), permit)
	ctx = withCallIdentity(ctx, callIdentity{CallID: "c-1", Tool: "run_command", Hash: argsHash(approved)})
	req := ApprovalRequest{BotID: "bot", ServerID: "srv", Command: approved.Function.Arguments}
	if !consumePermit(ctx, req) {
		t.Fatal("exact permit should be consumable")
	}

	for _, tc := range []struct {
		name string
		id   callIdentity
		req  ApprovalRequest
	}{
		{"other tool same id", callIdentity{CallID: "c-1", Tool: "panel", Hash: argsHash(approved)}, ApprovalRequest{BotID: "bot", ServerID: "srv", Command: approved.Function.Arguments}},
		{"other args same id", callIdentity{CallID: "c-1", Tool: "run_command", Hash: "deadbeef"}, ApprovalRequest{BotID: "bot", ServerID: "srv", Command: approved.Function.Arguments}},
		{"other server", callIdentity{CallID: "c-1", Tool: "run_command", Hash: argsHash(approved)}, ApprovalRequest{BotID: "bot", ServerID: "srv2", Command: approved.Function.Arguments}},
		{"other command text", callIdentity{CallID: "c-1", Tool: "run_command", Hash: argsHash(approved)}, ApprovalRequest{BotID: "bot", ServerID: "srv", Command: "rm -rf /"}},
	} {
		p := PermitForApprovedCall("bot", "srv", approved)
		c := withCallPermit(withCurrentCall(context.Background(), "c-1"), p)
		c = withCallIdentity(c, tc.id)
		if consumePermit(c, tc.req) {
			t.Fatalf("%s: permit was consumed for a different call", tc.name)
		}
	}
}

// TestEveryMutableFamilyAsksOnceAndRuns: each side-effecting tool family gets
// exactly one card, and the handler does not ask a second time.
func TestEveryMutableFamilyAsksOnceAndRuns(t *testing.T) {
	for _, tc := range []struct {
		name string
		call toolCall
	}{
		{"run_command", func() toolCall { c := runCall("systemctl restart nginx", "medium"); c.ID = "c1"; return c }()},
		{"panel", func() toolCall {
			c := toolCall{ID: "c1", Type: "function"}
			c.Function.Name = "panel"
			c.Function.Arguments = `{"action":"set_module","id":"probe","enabled":false}`
			return c
		}()},
		{"install_probe", func() toolCall {
			c := toolCall{ID: "c1", Type: "function"}
			c.Function.Name = "install_probe"
			c.Function.Arguments = `{"action":"install","server_id":"srv"}`
			return c
		}()},
		{"manage_modules", func() toolCall {
			c := toolCall{ID: "c1", Type: "function"}
			c.Function.Name = "manage_modules"
			c.Function.Arguments = `{"action":"add_record","id":"checkin","data":{"note":"早班"}}`
			return c
		}()},
		{"develop_module", func() toolCall {
			c := toolCall{ID: "c1", Type: "function"}
			c.Function.Name = "develop_module"
			c.Function.Arguments = `{"action":"create","name":"打卡","html":"<div>x</div>"}`
			return c
		}()},
		{"generate_secret", func() toolCall {
			c := toolCall{ID: "c1", Type: "function"}
			c.Function.Name = "generate_secret"
			c.Function.Arguments = `{"name":"db-pass"}`
			return c
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := modeAgent(t, "manual")
			asked := 0
			ctx := context.WithValue(context.Background(), optionsKey{}, Options{
				Approver: func(context.Context, ApprovalRequest) (bool, string) { asked++; return true, "" },
			})
			next, refusal := a.authorizeCall(ctx, model.Bot{ID: "bot"}, "srv", tc.call)
			if refusal != "" {
				t.Fatalf("refused: %s", refusal)
			}
			if asked != 1 {
				t.Fatalf("expected one card, got %d", asked)
			}
			if !grantForCall(next, "bot", effectiveServerForTest(t, a, next, "srv", tc.call), tc.call) {
				t.Fatal("grant missing or not exact for the authorized call")
			}
			// The handler for the same call must not ask again.
			if _, again := a.authorizeCall(next, model.Bot{ID: "bot"}, "srv", tc.call); again != "" {
				t.Fatalf("second pass refused: %s", again)
			}
			if asked != 1 {
				t.Fatalf("handler asked again: %d cards", asked)
			}
		})
	}
}

// effectiveServerForTest exposes the resolved target for assertions.
func effectiveServerForTest(t *testing.T, a *Agent, ctx context.Context, fallback string, call toolCall) string {
	t.Helper()
	target, ok := a.effectiveServer(ctx, fallback, call)
	if !ok {
		t.Fatal("target did not resolve")
	}
	return target
}

// TestOneCallApprovalDoesNotWidenServerScope: approving one call on an
// unselected server must not authorize the next call on that server.
func TestOneCallApprovalDoesNotWidenServerScope(t *testing.T) {
	a, ssh, _ := modeAgent(t, "smart")
	asked := 0
	ctx := context.WithValue(context.Background(), optionsKey{}, Options{
		RestrictServers: true,
		Approver:        func(context.Context, ApprovalRequest) (bool, string) { asked++; return true, "" },
	})
	first := runCall("uptime", "low")
	first.ID = "c1"
	next, refusal := a.authorizeCall(ctx, model.Bot{ID: "bot"}, "", first)
	if refusal != "" {
		t.Fatalf("first call refused: %s", refusal)
	}
	ev := a.runTool(next, "bot", "", first)
	if !ev.OK || ssh.command == "" {
		t.Fatalf("approved call did not run: %s", ev.Output)
	}
	// A second, different call on the same unselected server must ask again.
	second := runCall("df -h", "low")
	second.ID = "c2"
	ssh.command = ""
	ev = a.runTool(next, "bot", "", second)
	if asked != 2 {
		t.Fatalf("second call reused the first approval: %d cards", asked)
	}
	if ssh.command == "" && ev.OK {
		t.Fatal("second call reported success without running")
	}
}
