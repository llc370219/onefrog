package bot

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"onefrog/internal/task"
)

func TestRunAutomationUsesBackgroundTurn(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"一切正常"}}]}`)
	}))
	defer srv.Close()
	mem, box := mailboxFixture(t, srv.URL+"/v1")
	agent := &Agent{Store: mem, Box: box, SSH: &scriptSSH{}, HTTP: srv.Client()}
	ctx := context.Background()

	res, err := agent.RunAutomation(ctx, "main", "", "auto-x-main", "每日巡检", "检查一下所有服务器", "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "一切正常" {
		t.Fatalf("text %q", res.Text)
	}
	mu.Lock()
	if len(bodies) == 0 {
		mu.Unlock()
		t.Fatal("no model request")
	}
	first := bodies[0]
	mu.Unlock()
	if !strings.Contains(first, "【自动化任务】") {
		t.Fatal("automation prompt missing")
	}
	if strings.Contains(first, `"ask_user"`) {
		t.Fatal("ask_user offered to a background run")
	}
	msgs, err := mem.ListMessages(ctx, "auto-x-main")
	if err != nil {
		t.Fatal(err)
	}
	var sawUser, sawReply bool
	for _, m := range msgs {
		if m.Role == "user" && m.Content == "检查一下所有服务器" {
			sawUser = true
		}
		if m.Role == "assistant" && m.Content == "一切正常" {
			sawReply = true
		}
	}
	if !sawUser || !sawReply {
		t.Fatalf("session messages %+v", msgs)
	}
}

func TestRunAutomationRejectsMissingOrDisabledBot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected model request")
	}))
	defer srv.Close()
	mem, box := mailboxFixture(t, srv.URL+"/v1")
	settings, err := preferences.New("", box)
	if err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Store: mem, Box: box, SSH: &scriptSSH{}, HTTP: srv.Client(), Settings: settings}
	ctx := context.Background()
	if _, err := agent.RunAutomation(ctx, "ghost", "", "auto-x-ghost", "x", "hi", "u1", ""); err == nil {
		t.Fatal("missing bot accepted")
	}
	_ = settings.SetBotEnabled("check", false)
	if _, err := agent.RunAutomation(ctx, "check", "", "auto-x-check", "x", "hi", "u1", ""); err == nil {
		t.Fatal("disabled bot accepted")
	}
}

// automationFixture wires a durable task runner so a scheduled run is
// persisted instead of run inline.
func automationFixture(t *testing.T, mode string, replies ...func(int) string) (*Agent, *task.Book, *sshCounter, *scriptedModel, *Runner, func()) {
	t.Helper()
	fake := &scriptedModel{replies: replies}
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)
	mem := store.NewMemory()
	box, err := secret.New(bytesRepeat())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	b := model.Bot{ID: "main", Name: "主控", BaseURL: srv.URL + "/v1", ModelName: "demo", CreatedAt: time.Now().UTC()}
	if _, err := mem.CreateBot(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.CreateServer(ctx, store.ServerRec{Server: model.Server{ID: "web", Name: "web", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online"}, HostKey: "SHA256:test"}); err != nil {
		t.Fatal(err)
	}
	settings, err := preferences.New("", box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settings.Save(preferences.Input{
		Approval:  preferences.Approval{Mode: mode},
		Execution: preferences.Execution{TimeoutSeconds: 30, CommandTimeoutSeconds: 30, MaxSteps: 4, ContextMessages: 24},
	}); err != nil {
		t.Fatal(err)
	}
	book, err := task.New(filepath.Join(t.TempDir(), "tasks.enc"), box)
	if err != nil {
		t.Fatal(err)
	}
	ssh := &sshCounter{}
	agent := &Agent{Store: mem, Box: box, Settings: settings, SettingsLive: settings, SSH: ssh, HTTP: srv.Client(), Tasks: book}
	agent.TasksRunner = NewRunner(agent, book)
	stop := runBackground(t, agent.TasksRunner)
	return agent, book, ssh, fake, agent.TasksRunner, stop
}

// TestSubmitAutomationPersistsTaskBeforeRunning: a scheduled run is accepted as
// a durable task with automation provenance, not executed inside the caller.
func TestSubmitAutomationPersistsTaskBeforeRunning(t *testing.T) {
	agent, book, _, fake, _, stop := automationFixture(t, preferences.ModeSmart,
		func(int) string { return plainReply("巡检完成") },
	)
	defer stop()
	it, err := agent.SubmitAutomation(context.Background(), AutomationRequest{
		OwnerID: "u1", BotID: "main", ServerID: "web", SessionID: "auto-sched-main",
		Title: "每日巡检", Prompt: "检查磁盘", ClientRequestID: "run-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if it.ID == "" || it.Purpose != "automation" {
		t.Fatalf("task not persisted with automation purpose: %+v", it)
	}
	// The task row exists before the model is called at all.
	if _, err := book.Get(it.ID); err != nil {
		t.Fatalf("task not durable: %v", err)
	}
	// A retry of the same run resolves to the same task.
	again, err := agent.SubmitAutomation(context.Background(), AutomationRequest{
		OwnerID: "u1", BotID: "main", ServerID: "web", SessionID: "auto-sched-main",
		Title: "每日巡检", Prompt: "检查磁盘", ClientRequestID: "run-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != it.ID {
		t.Fatalf("retry created a second task: %s vs %s", again.ID, it.ID)
	}
	out, err := agent.WaitAutomation(context.Background(), "u1", it.ID)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if out.Status != task.StatusCompleted {
		t.Fatalf("status %s", out.Status)
	}
	if fake.count() == 0 {
		t.Fatal("model was never called")
	}
}

// TestAutomationApprovalWaitsAndIsNotSuccess: a scheduled run that needs a
// human decision must return ErrAutomationWaiting, keep the task parked, and
// never report a successful run. Resuming the same task after the decision
// runs it, and the scheduler never submits the work again.
func TestAutomationApprovalWaitsAndIsNotSuccess(t *testing.T) {
	agent, _, ssh, _, runner, stop := automationFixture(t, preferences.ModeSmart,
		func(int) string {
			return toolReply("c1", "run_command", `{"server_id":"web","command":"systemctl restart nginx","risk":"medium","reason":"重启"}`)
		},
		func(int) string {
			return toolReply("c2", "finish", `{"summary":"已重启","verification":["ok"]}`)
		},
	)
	defer stop()
	out, err := agent.RunAutomationTask(context.Background(), AutomationRequest{
		OwnerID: "u1", BotID: "main", ServerID: "web", SessionID: "auto-approval-main",
		Title: "重启任务", Prompt: "重启 nginx", ClientRequestID: "run-approval",
	})
	if !errors.Is(err, ErrAutomationWaiting) {
		t.Fatalf("parked run was reported as %v (out=%+v)", err, out)
	}
	if out.Text != "" {
		t.Fatalf("parked run reported success text: %q", out.Text)
	}
	if out.Status != task.StatusWaitingApproval {
		t.Fatalf("status %s", out.Status)
	}
	if out.Pending == nil || out.Pending.ApprovalID == "" {
		t.Fatalf("missing pending approval: %+v", out.Pending)
	}
	if ssh.count() != 0 {
		t.Fatal("command ran before the human decision")
	}

	// The scheduler observes the parked run instead of resubmitting it, and
	// the observation is still not a success.
	observed, err := agent.ObserveAutomation(context.Background(), "u1", out.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Text != "" || observed.Status != task.StatusWaitingApproval {
		t.Fatalf("observation looked successful: %+v", observed)
	}
	if ssh.count() != 0 {
		t.Fatal("observation ran the command")
	}

	if _, err := runner.Decide(context.Background(), "u1", out.TaskID, out.Pending.ApprovalID, true, "同意"); err != nil {
		t.Fatal(err)
	}
	final, err := agent.WaitAutomation(context.Background(), "u1", out.TaskID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if final.Status != task.StatusCompleted || final.Text == "" {
		t.Fatalf("resumed run did not complete: %+v", final)
	}
	if ssh.count() != 1 {
		t.Fatalf("ssh %d", ssh.count())
	}
}

// TestAutomationFallbackOnlyWithoutRunner: the turn-bound path stays available
// for unit contexts with no runner, and is not taken when a runner exists.
func TestAutomationFallbackOnlyWithoutRunner(t *testing.T) {
	agent, _, _, _, _, stop := automationFixture(t, preferences.ModeSmart,
		func(int) string { return plainReply("ok") },
	)
	defer stop()
	noRunner := *agent
	noRunner.TasksRunner = nil
	res, err := noRunner.RunAutomation(context.Background(), "main", "web", "auto-fb-main", "t", "检查", "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "ok" {
		t.Fatalf("text %q", res.Text)
	}
	// With a runner wired, the same call must create a durable task instead.
	if _, err := agent.RunAutomation(context.Background(), "main", "web", "auto-fb2-main", "t", "检查", "u1", ""); err != nil {
		t.Fatal(err)
	}
	if len(agent.Tasks.List("u1")) == 0 {
		t.Fatal("runner wired but no durable task was created")
	}
}
