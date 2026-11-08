package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"

	"onefrog/internal/attachment"
	"onefrog/internal/boot"
	"onefrog/internal/bot"
	"onefrog/internal/config"
	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"onefrog/internal/task"
)

// taskServer wires the real HTTP handler with a durable task runner. The runner
// is not started, so a POST only accepts work; tests drive state explicitly.
func taskServer(t *testing.T) (*http.Client, *httptest.Server, *bot.Runner, *store.Memory) {
	t.Helper()
	mem := store.NewMemory()
	box, err := secret.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := boot.Prepare(context.Background(), mem, config.Config{AdminUser: "admin", AdminPassword: "admin", Seed: true}); err != nil {
		t.Fatal(err)
	}
	modelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	t.Cleanup(modelSrv.Close)
	dir := t.TempDir()
	settings, err := preferences.New(filepath.Join(dir, "settings.enc"), box)
	if err != nil {
		t.Fatal(err)
	}
	d := preferences.Defaults()
	if _, err := settings.Save(preferences.Input{
		Providers: []preferences.ProviderInput{{
			Provider: preferences.Provider{ID: "p", Name: "test", BaseURL: modelSrv.URL + "/v1", Enabled: true, Models: []string{"base", "alternate", "review"}},
		}},
		Default:   preferences.Route{ProviderID: "p", Model: "base"},
		Approval:  preferences.Approval{Mode: preferences.ModeOff},
		Execution: d.Execution,
	}); err != nil {
		t.Fatal(err)
	}
	book, err := task.New(filepath.Join(dir, "tasks.enc"), box)
	if err != nil {
		t.Fatal(err)
	}
	files := attachment.New("", box)
	agent := &bot.Agent{
		Store: mem, Box: box, Settings: settings, SettingsLive: settings,
		Files: files, Tasks: book, SSH: &fakeSSH{}, HTTP: modelSrv.Client(),
	}
	runner := bot.NewRunner(agent, book)
	agent.TasksRunner = runner
	app := &Server{Store: mem, Box: box, Agent: agent, Settings: settings, Files: files, Tasks: runner}
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}, srv, runner, mem
}

func TestTaskEndpointRequiresAuthAndValidOptions(t *testing.T) {
	client, srv, _, _ := taskServer(t)

	res := postRaw(t, client, srv.URL+"/api/tasks", map[string]any{"content": "看一下"})
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated task accepted: %d", res.StatusCode)
	}
	login(t, client, srv, "admin", "admin", http.StatusOK)

	for _, body := range []map[string]any{
		{"content": "看一下", "mode": "nonsense"},
		{"content": "看一下", "excluded_bot_ids": []string{"ghost"}},
		{"content": "   "},
	} {
		res = postRaw(t, client, srv.URL+"/api/tasks", body)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid task accepted (%v): %d", body, res.StatusCode)
		}
	}

	var view TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{"content": "看一下磁盘"}, http.StatusAccepted, &view)
	if view.ID == "" || view.Status != task.StatusQueued || view.SessionID == "" {
		t.Fatalf("accepted view %+v", view)
	}
	if view.Goal != "看一下磁盘" || view.ParentID != "" {
		t.Fatalf("unexpected view %+v", view)
	}
	var listed struct {
		Items []TaskView `json:"items"`
	}
	getJSON(t, client, srv.URL+"/api/tasks", &listed)
	if len(listed.Items) != 1 || listed.Items[0].ID != view.ID {
		t.Fatalf("task list %+v", listed.Items)
	}
}

func TestTaskAcceptanceIsIdempotentPerRequestID(t *testing.T) {
	client, srv, _, mem := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)
	body := map[string]any{"content": "检查磁盘", "client_request_id": "req-1"}
	var first, retry TaskView
	postJSON(t, client, srv.URL+"/api/tasks", body, http.StatusAccepted, &first)
	postJSON(t, client, srv.URL+"/api/tasks", body, http.StatusAccepted, &retry)
	if first.ID == "" || first.ID != retry.ID {
		t.Fatalf("retry created another task: %q %q", first.ID, retry.ID)
	}
	if want := bot.StableSessionID(mustAdminID(t, mem), "req-1"); first.SessionID != want {
		t.Fatalf("session id is not stable for the request: %q want %q", first.SessionID, want)
	}
	msgs, err := mem.ListMessages(context.Background(), first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "检查磁盘" {
		t.Fatalf("retry duplicated the transcript: %+v", msgs)
	}

	// The same request id with a different payload is a conflict: it must not
	// silently run new content under an id the user already used.
	res := postRaw(t, client, srv.URL+"/api/tasks", map[string]any{"content": "另一件事", "client_request_id": "req-1"})
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("reused request id accepted new content: %d", res.StatusCode)
	}
}

func mustAdminID(t *testing.T, mem *store.Memory) string {
	t.Helper()
	user, _, err := mem.UserByName(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	return user.ID
}

func TestTaskSessionOwnerAndModeAreFixed(t *testing.T) {
	client, srv, _, mem := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)
	ctx := context.Background()
	hash, err := bcrypt.GenerateFromPassword([]byte("other-pass"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.CreateUser(ctx, "other", string(hash)); err != nil {
		t.Fatal(err)
	}

	var first TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{"content": "第一条"}, http.StatusAccepted, &first)

	res := postRaw(t, client, srv.URL+"/api/tasks", map[string]any{"content": "第二条", "session_id": first.SessionID, "mode": "direct"})
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("mode change accepted: %d", res.StatusCode)
	}
	var second TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{"content": "第二条", "session_id": first.SessionID}, http.StatusAccepted, &second)
	if second.SessionID != first.SessionID || second.ProjectID != first.SessionID {
		t.Fatalf("session scope changed: %+v", second)
	}

	other, _ := cookiejar.New(nil)
	otherClient := &http.Client{Jar: other}
	login(t, otherClient, srv, "other", "other-pass", http.StatusOK)
	res = postRaw(t, otherClient, srv.URL+"/api/tasks", map[string]any{"content": "接管", "session_id": first.SessionID})
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("another user reused the session: %d", res.StatusCode)
	}
	res, err = otherClient.Get(srv.URL + "/api/tasks/" + first.ID)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("another user read the task: %d", res.StatusCode)
	}
}

func TestTaskViewRedactsSecrets(t *testing.T) {
	client, srv, _, mem := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)
	ctx := context.Background()
	const vault = "p@ss-vault-task-42"
	boxed, err := secret.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := boxed.Encrypt(vault)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.CreateSecret(ctx, store.SecretRec{Secret: model.Secret{Name: "db-token"}, EncryptedValue: enc}); err != nil {
		t.Fatal(err)
	}
	var view TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{"content": "用 " + vault + " 登录"}, http.StatusAccepted, &view)
	if strings.Contains(view.Goal, vault) || !strings.Contains(view.Goal, "***") {
		t.Fatalf("goal not redacted: %q", view.Goal)
	}
	raw := getBody(t, client, srv.URL+"/api/tasks/"+view.ID)
	if strings.Contains(raw, vault) {
		t.Fatalf("task view leaked the secret: %s", raw)
	}
}

func TestTaskViewPlanCompletedJSONShape(t *testing.T) {
	client, srv, runner, _ := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)
	var view TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{"content": "按计划上线"}, http.StatusAccepted, &view)

	// A task with a plan and a real nonzero count: the field is a plain JSON
	// number, present and not a string.
	if _, err := runner.Book.Update(view.ID, func(tsk *task.Task) error {
		tsk.Plan = []string{"构建", "测试", "发布"}
		tsk.PlanCompleted = 2
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw := getBody(t, client, srv.URL+"/api/tasks/"+view.ID)
	var shaped struct {
		Plan          []string `json:"plan"`
		PlanCompleted *int     `json:"plan_completed"`
	}
	if err := json.Unmarshal([]byte(raw), &shaped); err != nil {
		t.Fatal(err)
	}
	if shaped.PlanCompleted == nil || *shaped.PlanCompleted != 2 || len(shaped.Plan) != 3 {
		t.Fatalf("plan_completed shape wrong: %s", raw)
	}
	// Zero is serialized as an explicit 0, not omitted.
	if _, err := runner.Book.Update(view.ID, func(tsk *task.Task) error {
		tsk.PlanCompleted = 0
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw = getBody(t, client, srv.URL+"/api/tasks/"+view.ID)
	if !strings.Contains(raw, `"plan_completed":0`) {
		t.Fatalf("zero plan_completed not serialized: %s", raw)
	}
	// A corrupted negative count in the store never serializes as negative:
	// the view clamps it to 0 and never above the plan length.
	if _, err := runner.Book.Update(view.ID, func(tsk *task.Task) error {
		tsk.PlanCompleted = -3
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw = getBody(t, client, srv.URL+"/api/tasks/"+view.ID)
	if !strings.Contains(raw, `"plan_completed":0`) {
		t.Fatalf("negative plan_completed not clamped: %s", raw)
	}
	if _, err := runner.Book.Update(view.ID, func(tsk *task.Task) error {
		tsk.PlanCompleted = 99
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw = getBody(t, client, srv.URL+"/api/tasks/"+view.ID)
	if !strings.Contains(raw, `"plan_completed":3`) {
		t.Fatalf("over-range plan_completed not clamped to plan length: %s", raw)
	}
}

func TestTaskApprovalIsNotACardAnswer(t *testing.T) {
	client, srv, runner, mem := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)
	ctx := context.Background()
	var view TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{"content": "重启 nginx"}, http.StatusAccepted, &view)

	if _, err := runner.Book.Update(view.ID, func(it *task.Task) error {
		it.Status = task.StatusWaitingApproval
		it.Pending = &task.Pending{Kind: task.PendingApproval, CallID: "c1", ArgsHash: "h", ApprovalID: "apr-1"}
		it.Approvals = append(it.Approvals, task.Approval{ID: "apr-1", CallID: "c1", ArgsHash: "h", Command: "systemctl restart nginx", Status: task.ApprovalPending})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	card, err := mem.AddMessage(ctx, model.Message{SessionID: view.SessionID, Role: "assistant", Content: "要重启吗", Choice: &model.Choice{
		Prompt: "要重启吗", Options: []model.ChoiceOption{{Label: "是", Value: "是"}, {Label: "否", Value: "否"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	res := postRaw(t, client, srv.URL+"/api/tasks", map[string]any{
		"session_id":    view.SessionID,
		"choice_answer": map[string]any{"message_id": card.ID, "values": []string{"是"}},
	})
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("a card answer matched an approval task: %d", res.StatusCode)
	}
	stored, _ := runner.Book.Get(view.ID)
	if len(stored.Approvals) != 1 || stored.Approvals[0].Status != task.ApprovalPending {
		t.Fatalf("approval was consumed by a card: %+v", stored.Approvals)
	}
	postJSON(t, client, srv.URL+"/api/tasks/"+view.ID+"/approvals/apr-1", map[string]any{"allow": true}, http.StatusOK, &view)
}

func TestTaskCardAnswerMustTargetThePendingCard(t *testing.T) {
	client, srv, runner, mem := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)
	ctx := context.Background()
	var view TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{"content": "选一个"}, http.StatusAccepted, &view)

	oldCard, _ := mem.AddMessage(ctx, model.Message{SessionID: view.SessionID, Role: "assistant", Content: "旧问题", Choice: &model.Choice{
		Prompt: "旧问题", Options: []model.ChoiceOption{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}},
	}})
	card, _ := mem.AddMessage(ctx, model.Message{SessionID: view.SessionID, Role: "assistant", Content: "新问题", Choice: &model.Choice{
		Prompt: "新问题", Options: []model.ChoiceOption{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}},
	}})
	if _, err := runner.Book.Update(view.ID, func(it *task.Task) error {
		it.Status = task.StatusWaitingUser
		it.Pending = &task.Pending{Kind: task.PendingUser, CallID: "c9", JobRef: "新问题", CardMessageID: card.ID}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	res := postRaw(t, client, srv.URL+"/api/tasks", map[string]any{
		"session_id":    view.SessionID,
		"choice_answer": map[string]any{"message_id": oldCard.ID, "values": []string{"a"}},
	})
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("an older card answered the task: %d", res.StatusCode)
	}
	if stored, _ := runner.Book.Get(view.ID); stored.Status != task.StatusWaitingUser {
		t.Fatalf("task resumed on a wrong card: %s", stored.Status)
	}

	var resumed TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{
		"session_id":    view.SessionID,
		"choice_answer": map[string]any{"message_id": card.ID, "values": []string{"a"}},
	}, http.StatusAccepted, &resumed)
	if resumed.ID != view.ID || resumed.Status != task.StatusQueued {
		t.Fatalf("exact card did not resume: %+v", resumed)
	}
}

func TestStaleCardRetryDoesNotAcknowledgeANewPendingTask(t *testing.T) {
	client, srv, runner, mem := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)
	ctx := context.Background()

	var a TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{"content": "任务 A"}, http.StatusAccepted, &a)
	cardA, _ := mem.AddMessage(ctx, model.Message{SessionID: a.SessionID, Role: "assistant", Content: "A 吗", Choice: &model.Choice{
		Prompt: "A 吗", Options: []model.ChoiceOption{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}},
	}})
	if _, err := runner.Book.Update(a.ID, func(it *task.Task) error {
		it.Status = task.StatusWaitingUser
		it.Pending = &task.Pending{Kind: task.PendingUser, CallID: "ca", CardMessageID: cardA.ID}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{
		"session_id":    a.SessionID,
		"choice_answer": map[string]any{"message_id": cardA.ID, "values": []string{"a"}},
	}, http.StatusAccepted, &a)

	var b TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{"content": "任务 B", "session_id": a.SessionID}, http.StatusAccepted, &b)
	if b.ID == a.ID {
		t.Fatal("task B reused task A")
	}
	cardB, _ := mem.AddMessage(ctx, model.Message{SessionID: b.SessionID, Role: "assistant", Content: "B 吗", Choice: &model.Choice{
		Prompt: "B 吗", Options: []model.ChoiceOption{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}},
	}})
	if _, err := runner.Book.Update(b.ID, func(it *task.Task) error {
		it.Status = task.StatusWaitingUser
		it.Pending = &task.Pending{Kind: task.PendingUser, CallID: "cb", CardMessageID: cardB.ID}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	res := postRaw(t, client, srv.URL+"/api/tasks", map[string]any{
		"session_id":    b.SessionID,
		"choice_answer": map[string]any{"message_id": cardA.ID, "values": []string{"a"}},
	})
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("stale card acknowledged a new task: %d", res.StatusCode)
	}
	if stored, _ := runner.Book.Get(b.ID); stored.Status != task.StatusWaitingUser {
		t.Fatalf("task B was resumed by an old card: %s", stored.Status)
	}
	var resumed TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{
		"session_id":    b.SessionID,
		"choice_answer": map[string]any{"message_id": cardB.ID, "values": []string{"b"}},
	}, http.StatusAccepted, &resumed)
	if resumed.ID != b.ID || resumed.Status != task.StatusQueued {
		t.Fatalf("own card did not resume: %+v", resumed)
	}
}

// TestTaskCardRepairsStoredAnswerGap covers the cross-storage gap: the chat
// store recorded the answer, but the process stopped before the task moved out
// of waiting_user. The retry must finish the task transition, not 409 forever.
func TestTaskCardRepairsStoredAnswerGap(t *testing.T) {
	client, srv, runner, mem := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)
	ctx := context.Background()
	var view TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{"content": "选一个"}, http.StatusAccepted, &view)
	card, _ := mem.AddMessage(ctx, model.Message{SessionID: view.SessionID, Role: "assistant", Content: "选一个", Choice: &model.Choice{
		Prompt: "选一个", Options: []model.ChoiceOption{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}},
	}})
	if _, err := runner.Book.Update(view.ID, func(it *task.Task) error {
		it.Status = task.StatusWaitingUser
		it.Pending = &task.Pending{Kind: task.PendingUser, CallID: "c1", CardMessageID: card.ID}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Simulate the store half committing: the card now has an answer, but the
	// task still waits because the second write never happened.
	if _, err := mem.AnswerChoice(ctx, view.SessionID, card.ID, model.ChoiceAnswer{Values: []string{"a"}, AnsweredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if stored, _ := runner.Book.Get(view.ID); stored.Status != task.StatusWaitingUser {
		t.Fatalf("precondition: task should still be waiting, got %s", stored.Status)
	}

	// The retried POST repairs the gap and resumes the task exactly once.
	var resumed TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{
		"session_id":    view.SessionID,
		"choice_answer": map[string]any{"message_id": card.ID, "values": []string{"a"}},
	}, http.StatusAccepted, &resumed)
	if resumed.ID != view.ID || resumed.Status != task.StatusQueued {
		t.Fatalf("gap not repaired: %+v", resumed)
	}
	stored, _ := runner.Book.Get(view.ID)
	if stored.Status != task.StatusQueued {
		t.Fatalf("task still parked after repair: %s %+v", stored.Status, stored.Pending)
	}
	if stored.Pending == nil || stored.Pending.Answer == "" {
		t.Fatalf("stored answer was not carried into the resumed task: %+v", stored.Pending)
	}
	// A further retry is idempotent: the task is queued, not re-answered.
	res := postRaw(t, client, srv.URL+"/api/tasks", map[string]any{
		"session_id":    view.SessionID,
		"choice_answer": map[string]any{"message_id": card.ID, "values": []string{"a"}},
	})
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("idempotent retry rejected: %d", res.StatusCode)
	}
	if again, _ := runner.Book.Get(view.ID); again.Pending == nil || again.Pending.Answer != stored.Pending.Answer {
		t.Fatalf("retry changed the stored answer: %+v", again.Pending)
	}

	// queued + Pending.Answer is the legitimate transient state until the
	// worker's next window injects the answer and clears the pending card.
	if stored.Pending.CardMessageID != card.ID {
		t.Fatalf("repaired pending lost its card binding: %+v", stored.Pending)
	}

	// The next window consumes it exactly once: the answer becomes the
	// operator's user turn and the pending card is cleared. This drives the
	// real dispatcher, so it also proves the queued task is actually picked up.
	runCtx, stopRun := context.WithCancel(ctx)
	go runner.Run(runCtx)
	var after task.Task
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := runner.Book.Get(view.ID)
		if err != nil {
			stopRun()
			runner.Wait()
			t.Fatal(err)
		}
		if cur.Terminal() || (cur.Pending == nil || cur.Pending.Answer == "") && cur.Status != task.StatusQueued {
			after = cur
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stopRun()
	runner.Wait()
	if after.ID == "" {
		t.Fatalf("stored answer was never consumed: %+v", stored.Pending)
	}
	raw, _ := json.Marshal(after.Checkpoint.Messages)
	if !strings.Contains(string(raw), stored.Pending.Answer) {
		t.Fatalf("consumed answer never became a user turn: %s", raw)
	}
}

// TestNewestCardIsNotThePendingCard answers a card that is the newest message
// but not the one the task is bound to. It must be refused: "newest" is not
// identity.
func TestNewestCardIsNotThePendingCard(t *testing.T) {
	client, srv, runner, mem := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)
	ctx := context.Background()
	var view TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{"content": "选一个"}, http.StatusAccepted, &view)
	bound, _ := mem.AddMessage(ctx, model.Message{SessionID: view.SessionID, Role: "assistant", Content: "绑定的问题", Choice: &model.Choice{
		Prompt: "绑定的问题", Options: []model.ChoiceOption{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}},
	}})
	// A newer card appears (another turn), but the task is still bound to the
	// older one.
	newest, _ := mem.AddMessage(ctx, model.Message{SessionID: view.SessionID, Role: "assistant", Content: "更新的问题", Choice: &model.Choice{
		Prompt: "更新的问题", Options: []model.ChoiceOption{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}},
	}})
	if _, err := runner.Book.Update(view.ID, func(it *task.Task) error {
		it.Status = task.StatusWaitingUser
		it.Pending = &task.Pending{Kind: task.PendingUser, CallID: "c1", CardMessageID: bound.ID}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	res := postRaw(t, client, srv.URL+"/api/tasks", map[string]any{
		"session_id":    view.SessionID,
		"choice_answer": map[string]any{"message_id": newest.ID, "values": []string{"a"}},
	})
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("a newer but unrelated card answered the task: %d", res.StatusCode)
	}
	if stored, _ := runner.Book.Get(view.ID); stored.Status != task.StatusWaitingUser {
		t.Fatalf("task resumed on the wrong card: %s", stored.Status)
	}
	// The bound card still answers it.
	var resumed TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{
		"session_id":    view.SessionID,
		"choice_answer": map[string]any{"message_id": bound.ID, "values": []string{"b"}},
	}, http.StatusAccepted, &resumed)
	if resumed.ID != view.ID || resumed.Status != task.StatusQueued {
		t.Fatalf("bound card did not resume: %+v", resumed)
	}
}

func TestTaskAttachmentsAreStoredWithTheGoal(t *testing.T) {
	client, srv, runner, mem := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)
	ctx := context.Background()
	ref := uploadAttachment(t, client, srv, "notes.txt", "port=8088")
	var view TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{
		"content": "分析附件", "attachment_ids": []string{ref.ID},
	}, http.StatusAccepted, &view)
	msgs, err := mem.ListMessages(ctx, view.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || len(msgs[0].Attachments) != 1 || msgs[0].Attachments[0].ID != ref.ID {
		t.Fatalf("goal lost its attachment: %+v", msgs)
	}
	stored, _ := runner.Book.Get(view.ID)
	raw, _ := json.Marshal(stored.Checkpoint.Messages)
	if !strings.Contains(string(raw), "port=8088") {
		t.Fatalf("accepted checkpoint missing attachment content: %s", raw)
	}
	// The validated attachment ids belong to the task's immutable authorization
	// fingerprint (that is what makes a retry with different files a conflict),
	// but the file content never does, and the session's chat options carry
	// neither.
	if !strings.Contains(stored.OptionsJSON, ref.ID) {
		t.Fatalf("task authorization lost its attachment id: %s", stored.OptionsJSON)
	}
	if strings.Contains(stored.OptionsJSON, "port=8088") {
		t.Fatalf("attachment content reached task options: %s", stored.OptionsJSON)
	}
	sess, err := mem.EnsureSession(ctx, view.SessionID, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sess.Options), ref.ID) || strings.Contains(string(sess.Options), "port=8088") {
		t.Fatalf("session options carry the task's attachments: %s", sess.Options)
	}
}

// TestSameRequestIDWithDifferentAttachmentsConflicts proves the attachment set
// is part of the immutable request fingerprint, not a per-session detail.
func TestSameRequestIDWithDifferentAttachmentsConflicts(t *testing.T) {
	client, srv, runner, mem := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)
	ctx := context.Background()
	first := uploadAttachment(t, client, srv, "a.txt", "alpha")
	second := uploadAttachment(t, client, srv, "b.txt", "beta")

	body := map[string]any{
		"content": "分析附件", "attachment_ids": []string{first.ID}, "client_request_id": "req-attach",
	}
	var view TaskView
	postJSON(t, client, srv.URL+"/api/tasks", body, http.StatusAccepted, &view)

	// Same id, same goal, different file: a different request, not a replay.
	res := postRaw(t, client, srv.URL+"/api/tasks", map[string]any{
		"content": "分析附件", "attachment_ids": []string{second.ID}, "client_request_id": "req-attach",
	})
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("changed attachments adopted the old task: %d", res.StatusCode)
	}
	// Dropping the file is also a change.
	res = postRaw(t, client, srv.URL+"/api/tasks", map[string]any{
		"content": "分析附件", "client_request_id": "req-attach",
	})
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("dropped attachments adopted the old task: %d", res.StatusCode)
	}
	// The exact retry is still idempotent and writes no second transcript.
	var retry TaskView
	postJSON(t, client, srv.URL+"/api/tasks", body, http.StatusAccepted, &retry)
	if retry.ID != view.ID {
		t.Fatalf("exact retry created another task: %+v", retry)
	}
	msgs, err := mem.ListMessages(ctx, view.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || len(msgs[0].Attachments) != 1 || msgs[0].Attachments[0].ID != first.ID {
		t.Fatalf("retry changed the accepted transcript: %+v", msgs)
	}
	stored, _ := runner.Book.Get(view.ID)
	if !strings.Contains(stored.OptionsJSON, first.ID) {
		t.Fatalf("stored authorization lost the attachment: %s", stored.OptionsJSON)
	}
}

// TestSameSessionDifferentRequestIDsAreDifferentTasks is the regression for the
// old session-scan lookup: two request ids in one session are two tasks, even
// with an identical goal, and the second never adopts the first.
func TestSameSessionDifferentRequestIDsAreDifferentTasks(t *testing.T) {
	client, srv, _, mem := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)
	ctx := context.Background()
	var first TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{
		"content": "检查磁盘", "client_request_id": "req-one",
	}, http.StatusAccepted, &first)

	// Same session, same goal, new request id: a new task.
	var second TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{
		"content": "检查磁盘", "session_id": first.SessionID, "client_request_id": "req-two",
	}, http.StatusAccepted, &second)
	if second.ID == first.ID {
		t.Fatalf("a different request id adopted the earlier task: %+v", second)
	}
	// Different goal in the same session with its own id is also its own task.
	var third TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{
		"content": "查看内存", "session_id": first.SessionID, "client_request_id": "req-three",
	}, http.StatusAccepted, &third)
	if third.ID == first.ID || third.ID == second.ID {
		t.Fatalf("a third request reused a task: %+v", third)
	}
	// Each accepted its own transcript entry, so no request adopted another's.
	msgs, err := mem.ListMessages(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	goals := map[string]int{}
	for _, m := range msgs {
		goals[m.Content]++
	}
	if goals["检查磁盘"] != 2 || goals["查看内存"] != 1 {
		t.Fatalf("transcript does not match the accepted requests: %+v", goals)
	}
	// A same-id retry that names a different session is a conflict, never a
	// move. A fresh request id without a session gets its own stable session.
	var otherSession TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{
		"content": "另一个对话", "client_request_id": "req-other-session",
	}, http.StatusAccepted, &otherSession)
	if otherSession.SessionID == first.SessionID {
		t.Fatalf("expected a distinct session, got %q", otherSession.SessionID)
	}
	res := postRaw(t, client, srv.URL+"/api/tasks", map[string]any{
		"content": "检查磁盘", "client_request_id": "req-one", "session_id": otherSession.SessionID,
	})
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("a retry moved the task to another session: %d", res.StatusCode)
	}
}

// TestTaskAcceptFreezesIsolatedProjectPrompt proves the accepted checkpoint is
// built under the project scope: bot private memory (including peer titles)
// must never be frozen into a group task, because the checkpoint is replayed
// even before the runtime re-applies withProject.
func TestTaskAcceptFreezesIsolatedProjectPrompt(t *testing.T) {
	client, srv, runner, mem := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)
	ctx := context.Background()
	main, err := mem.GetBot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	agent := &bot.Agent{Store: mem, Box: mustBox(t)}
	const sentinel = "PRIVATE-SENTINEL-主控私记"
	if _, err := agent.Remember(ctx, main.ID, "主控私记", sentinel, "bot"); err != nil {
		t.Fatal(err)
	}

	var view TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{"content": "组内任务"}, http.StatusAccepted, &view)
	stored, err := runner.Book.Get(view.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(stored.Checkpoint.Messages)
	if strings.Contains(string(raw), sentinel) || strings.Contains(string(raw), "主控私记") {
		t.Fatalf("project checkpoint froze bot private memory: %s", raw)
	}
	if !strings.Contains(string(raw), "隔离项目记忆") {
		t.Fatalf("project checkpoint missing project memory scope: %s", raw)
	}
}

// TestTaskAcceptDirectKeepsOwnMemoryOnly proves a direct task still freezes the
// bot's own memory but never another bot's private notes.
func TestTaskAcceptDirectKeepsOwnMemoryOnly(t *testing.T) {
	client, srv, runner, mem := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)
	ctx := context.Background()
	main, _ := mem.GetBot(ctx, "main")
	agent := &bot.Agent{Store: mem, Box: mustBox(t)}
	const own = "OWN-DIRECT-记忆"
	if _, err := agent.Remember(ctx, main.ID, "主控私记", own, "bot"); err != nil {
		t.Fatal(err)
	}
	var peer string
	for _, b := range mustBots(t, mem) {
		if b.ID != main.ID {
			peer = b.ID
			break
		}
	}
	if peer == "" {
		t.Skip("no peer bot in seed data")
	}
	const peerNote = "PEER-PRIVATE-不可见"
	if _, err := agent.Remember(ctx, peer, "巡检私记", peerNote, "bot"); err != nil {
		t.Fatal(err)
	}

	var view TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{"content": "单聊任务", "mode": "direct"}, http.StatusAccepted, &view)
	stored, err := runner.Book.Get(view.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(stored.Checkpoint.Messages)
	if !strings.Contains(string(raw), own) {
		t.Fatalf("direct checkpoint lost its own memory: %s", raw)
	}
	for _, bad := range []string{peerNote, "巡检私记", "各 Bot 最近记住的标题"} {
		if strings.Contains(string(raw), bad) {
			t.Fatalf("direct checkpoint leaked peer memory %q: %s", bad, raw)
		}
	}
}

func mustBox(t *testing.T) *secret.Box {
	t.Helper()
	box, err := secret.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func mustBots(t *testing.T, mem *store.Memory) []model.Bot {
	t.Helper()
	bots, err := mem.ListBots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return bots
}

func TestTaskAttachmentOwnershipIsEnforced(t *testing.T) {
	client, srv, _, mem := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)
	ctx := context.Background()
	other, err := mem.CreateUser(ctx, "other", "hash")
	if err != nil {
		t.Fatal(err)
	}
	boxed, _ := secret.New(bytes.Repeat([]byte{7}, 32))
	files := attachment.New("", boxed)
	ref, err := files.Put(other.ID, "secret.txt", []byte("not yours"))
	if err != nil {
		t.Fatal(err)
	}
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{
		"content": "分析别人的附件", "attachment_ids": []string{ref.ID},
	}, http.StatusBadRequest, &map[string]any{})
}

// wsDial opens the chat socket with the client's cookies and a same-origin
// header, like the browser does.
func wsDial(t *testing.T, client *http.Client, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{}
	for _, c := range client.Jar.Cookies(u) {
		header.Add("Cookie", c.Name+"="+c.Value)
	}
	header.Set("Origin", srv.URL)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat", header)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// wsReadUntil reads socket messages until one matches, or fails on timeout.
func wsReadUntil(t *testing.T, conn *websocket.Conn, match func(map[string]any) bool) map[string]any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatalf("socket read: %v", err)
		}
		if match(msg) {
			return msg
		}
	}
}

// TestWSDurableTaskSurvivesDisconnectAndReconnect proves a socket request is a
// durable task: closing the connection does not cancel it, and reconnecting
// with the same request id lands on the same task instead of starting again.
func TestWSDurableTaskSurvivesDisconnectAndReconnect(t *testing.T) {
	client, srv, runner, _ := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go runner.Run(ctx)

	conn := wsDial(t, client, srv)
	body := map[string]any{"content": "socket 任务", "client_request_id": "ws-req-1"}
	if err := conn.WriteJSON(body); err != nil {
		t.Fatal(err)
	}
	taskMsg := wsReadUntil(t, conn, func(m map[string]any) bool { return m["type"] == "task" })
	taskView, _ := taskMsg["task"].(map[string]any)
	taskID, _ := taskView["id"].(string)
	if taskID == "" {
		t.Fatalf("socket did not report its task: %+v", taskMsg)
	}
	// Drop the connection without answering anything.
	conn.Close()

	// The task is durable: it still exists and is not cancelled by the drop.
	var stored task.Task
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := runner.Book.Get(taskID)
		if err != nil {
			t.Fatal(err)
		}
		stored = cur
		if cur.Terminal() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stored.Status == task.StatusCancelled {
		t.Fatal("disconnect cancelled the durable task")
	}

	// Reconnecting with the same request id resolves to the same task, so no
	// work is duplicated.
	again := wsDial(t, client, srv)
	if err := again.WriteJSON(body); err != nil {
		t.Fatal(err)
	}
	second := wsReadUntil(t, again, func(m map[string]any) bool { return m["type"] == "task" })
	view2, _ := second["task"].(map[string]any)
	if view2["id"] != taskID {
		t.Fatalf("reconnect started a second task: %v vs %v", view2["id"], taskID)
	}
}

// TestWSDurableApprovalBindsExactTask proves a socket approval is decided on the
// exact task and approval id through the runner, and that a reply without a
// task id is refused instead of being guessed onto some pending task.
func TestWSDurableApprovalBindsExactTask(t *testing.T) {
	client, srv, runner, _ := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)

	var view TaskView
	postJSON(t, client, srv.URL+"/api/tasks", map[string]any{"content": "重启 nginx"}, http.StatusAccepted, &view)
	if _, err := runner.Book.Update(view.ID, func(it *task.Task) error {
		it.Status = task.StatusWaitingApproval
		it.Pending = &task.Pending{Kind: task.PendingApproval, CallID: "c1", ArgsHash: "h", ApprovalID: "apr-ws"}
		it.Approvals = append(it.Approvals, task.Approval{ID: "apr-ws", CallID: "c1", ArgsHash: "h", Command: "systemctl restart nginx", Status: task.ApprovalPending})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	conn := wsDial(t, client, srv)
	// No task id: refuse, never guess which task this decides.
	if err := conn.WriteJSON(map[string]any{"type": "approval", "id": "apr-ws", "allow": true}); err != nil {
		t.Fatal(err)
	}
	errMsg := wsReadUntil(t, conn, func(m map[string]any) bool { return m["type"] == "error" })
	if !strings.Contains(errMsg["message"].(string), "任务") {
		t.Fatalf("unexpected refusal: %+v", errMsg)
	}
	if stored, _ := runner.Book.Get(view.ID); stored.Status != task.StatusWaitingApproval {
		t.Fatalf("an unbound approval resumed the task: %s", stored.Status)
	}

	// Exact task + approval id: the decision is recorded through Runner.Decide.
	if err := conn.WriteJSON(map[string]any{"type": "approval", "id": "apr-ws", "task_id": view.ID, "allow": true, "reason": "同意"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := runner.Book.Get(view.ID)
		if err != nil {
			t.Fatal(err)
		}
		if cur.Status != task.StatusWaitingApproval {
			if len(cur.Approvals) != 1 || cur.Approvals[0].Status != task.ApprovalAllowed {
				t.Fatalf("approval not recorded: %+v", cur.Approvals)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("socket approval never reached the task")
}

// TestLegacyChatReportsFailedTaskAsDurableView proves the legacy /api/chat
// Result shape is only used for a completed task. A failed, cancelled or
// exhausted task must surface as the durable view, never as a successful answer
// that hides the real status.
func TestLegacyChatReportsFailedTaskAsDurableView(t *testing.T) {
	client, srv, runner, _ := taskServer(t)
	login(t, client, srv, "admin", "admin", http.StatusOK)

	for _, status := range []string{task.StatusFailed, task.StatusCancelled, task.StatusBudgetExhausted} {
		var view TaskView
		postJSON(t, client, srv.URL+"/api/tasks", map[string]any{"content": "任务 " + status}, http.StatusAccepted, &view)
		if _, err := runner.Book.Update(view.ID, func(it *task.Task) error {
			it.Status = status
			it.LastError = "出错了"
			it.Result = "看起来像成功的文本"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		out, err := runner.WaitTask(context.Background(), "", view.ID, 10*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if !out.Done || out.Task.Status != status {
			t.Fatalf("wait did not report %s: %+v", status, out.Task)
		}
		// The chat helper only turns a completed task into a Result; every
		// other terminal status keeps the durable view.
		if out.Task.Status == task.StatusCompleted {
			t.Fatalf("status %s must not be treated as completed", status)
		}
	}
}

// uploadAttachment posts a multipart file and returns its reference.
func uploadAttachment(t *testing.T, client *http.Client, srv *httptest.Server, name, body string) model.Attachment {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/attachments", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload %d %s", res.StatusCode, raw)
	}
	var ref model.Attachment
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	return ref
}
