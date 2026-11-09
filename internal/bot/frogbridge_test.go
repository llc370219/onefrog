package bot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
)

func frogRun(t *testing.T, a *Agent, c FrogCall) (string, int) {
	t.Helper()
	asked := 0
	c.Owner, c.BotID, c.Name = "u1", "main", "run_command"
	if c.Ask == nil {
		c.Ask = func(ApprovalRequest) (bool, string) { asked++; return true, "" }
	}
	out, err := a.ExecFrogTool(context.Background(), c)
	if err != nil {
		return "error: " + err.Error(), asked
	}
	return out, asked
}

func frogArgs(command, risk string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"server_id": "srv", "command": command, "risk": risk, "reason": "测试"})
	return raw
}

// The bridge keeps OneFrog's rules: a selected server runs plain commands per
// the approval mode, any other server asks every time, catastrophic commands
// never run, and an automation's plan mode or unattended "off" are honoured.
func TestFrogBridgeAuthorization(t *testing.T) {
	a, ssh, mem := modeAgent(t, preferences.ModeSmart)
	if _, err := mem.CreateBot(context.Background(), model.Bot{ID: "main", Name: "主控"}); err != nil {
		t.Fatal(err)
	}
	out, asked := frogRun(t, a, FrogCall{Selected: []string{"srv"}, Args: frogArgs("df -h", "low")})
	if asked != 0 || ssh.command != "df -h" || !strings.Contains(out, "seen:df -h") {
		t.Fatalf("selected plain read: asked %d ran %q out %q", asked, ssh.command, out)
	}
	ssh.command = ""
	denied := func(ApprovalRequest) (bool, string) { return false, "" }
	_, _ = frogRun(t, a, FrogCall{Args: frogArgs("df -h", "low"), Ask: denied})
	if ssh.command != "" {
		t.Fatal("unselected server ran without approval")
	}
	var req ApprovalRequest
	_, _ = frogRun(t, a, FrogCall{Args: frogArgs("uptime", "low"), Ask: func(r ApprovalRequest) (bool, string) { req = r; return true, "" }})
	if ssh.command != "uptime" || req.Risk != "high" || req.ServerID != "srv" {
		t.Fatalf("unselected server must ask (high) then run: %+v ran %q", req, ssh.command)
	}
	ssh.command = ""
	out, _ = frogRun(t, a, FrogCall{Selected: []string{"srv"}, Args: frogArgs("rm -rf /", "high")})
	if ssh.command != "" || !strings.HasPrefix(out, "error:") {
		t.Fatalf("catastrophic command: ran %q out %q", ssh.command, out)
	}
	out, asked = frogRun(t, a, FrogCall{Selected: []string{"srv"}, Args: frogArgs("df -h", "low"), ApprovalMode: preferences.ModePlan, Background: true})
	if ssh.command != "" || asked != 0 || !strings.Contains(out, "仅规划") {
		t.Fatalf("plan mode: ran %q asked %d out %q", ssh.command, asked, out)
	}
	_, asked = frogRun(t, a, FrogCall{Selected: []string{"srv"}, Args: frogArgs("systemctl restart nginx", "medium"), ApprovalMode: preferences.ModeManual})
	if asked != 1 {
		t.Fatalf("manual mode must ask, asked %d", asked)
	}
	if _, err := a.ExecFrogTool(context.Background(), FrogCall{Owner: "u1", BotID: "main", Name: "develop_module", Args: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("a tool outside the bridge must be refused")
	}
}
