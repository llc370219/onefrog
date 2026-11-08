package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/store"
	"onefrog/internal/task"
)

// PrepareTask establishes or reuses the chat session a task writes into, saves
// the per-task options, and records the user's goal as the first message. It
// returns the project scope: a group chat uses its stable session id, an
// ordinary direct chat has none.
//
// The transport always passes the authenticated user's id: Preflight needs it
// to check attachment ownership, and the runtime stamps it on the task.
func (a *Agent) PrepareTask(ctx context.Context, ownerID, botID, sessionID, title string, opt Options) (model.Session, string, Options, error) {
	sess, project, opt, _, err := a.PrepareTaskRefs(ctx, ownerID, botID, sessionID, title, opt)
	return sess, project, opt, err
}

// StableSessionID derives the session id a retried acceptance must land in when
// the client did not have a session yet. Without it, a retry with a fresh
// session would create a second session and a second task, even though the
// runtime derives task ids from the session.
func StableSessionID(ownerID, clientRequestID string) string {
	ownerID = strings.TrimSpace(ownerID)
	clientRequestID = strings.TrimSpace(clientRequestID)
	if ownerID == "" || clientRequestID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ownerID + "\x00" + clientRequestID))
	return "req-" + hex.EncodeToString(sum[:16])
}

// ErrRequestPayloadChanged means a client request id was reused with a
// different goal, bot or scope. Retrying the same id with new content would
// silently run something the user never approved under that id.
var ErrRequestPayloadChanged = errors.New("这个请求编号已经用于另一个任务，请刷新后重试")

// RequestTaskID is the durable task id a client request id resolves to. It is
// derived from the owner and the request id only (the same derivation
// AcceptTask uses), so a retried acceptance lands on the same task even when
// the transport assigned a fresh session. It is never a session scan: two
// different request ids in one session are two different tasks.
func RequestTaskID(ownerID, clientRequestID string) string {
	ownerID = strings.TrimSpace(ownerID)
	clientRequestID = strings.TrimSpace(clientRequestID)
	if ownerID == "" || clientRequestID == "" {
		return ""
	}
	return "task-" + acceptID(ownerID, clientRequestID)
}

// RequestIdentity is the immutable payload of one accepted request: what the
// user asked for and the authorization it was accepted under. A retry must
// match every part, otherwise it is a different request wearing the same id.
type RequestIdentity struct {
	Content       string
	BotID         string
	SessionID     string
	ProjectID     string
	ServerID      string
	OptionsJSON   string
	AttachmentIDs []string
}

// AcceptanceOptionsJSON canonicalizes the durable per-task options exactly as
// AcceptTask stores them, so a retry can be compared byte for byte. It includes
// the validated attachment ids (part of the frozen authorization) and excludes
// plaintext secret answers and card replies.
func AcceptanceOptionsJSON(opt Options) string {
	attachments := append([]string(nil), opt.AttachmentIDs...)
	sort.Strings(attachments)
	raw, err := json.Marshal(struct {
		Workspace      any                `json:"workspace,omitempty"`
		ApprovalRoute  *preferences.Route `json:"approval_route,omitempty"`
		ModelRoute     *preferences.Route `json:"model_route,omitempty"`
		ExcludedBotIDs []string           `json:"excluded_bot_ids,omitempty"`
		Mode           string             `json:"mode,omitempty"`
		ApprovalMode   string             `json:"approval_mode,omitempty"`
		AttachmentIDs  []string           `json:"attachment_ids,omitempty"`
	}{opt.Workspace, opt.ApprovalRoute, opt.ModelRoute, opt.ExcludedBotIDs, opt.Mode, opt.ApprovalMode, attachments})
	if err != nil {
		return ""
	}
	return string(raw)
}

// AttachmentIDsOf returns the validated attachment ids of an accepted request.
func AttachmentIDsOf(refs []model.Attachment) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.ID)
	}
	return out
}

// RequestTask returns the task already accepted for this exact request id,
// without looking at any other task in the session.
func (a *Agent) RequestTask(ownerID, clientRequestID string) (task.Task, bool) {
	if a.Tasks == nil {
		return task.Task{}, false
	}
	id := RequestTaskID(ownerID, clientRequestID)
	if id == "" {
		return task.Task{}, false
	}
	it, err := a.Tasks.Get(id)
	if err != nil {
		return task.Task{}, false
	}
	if it.OwnerID != strings.TrimSpace(ownerID) {
		return task.Task{}, false
	}
	return it, true
}

// ExistingRequestTask resolves a reused request id against the one task that id
// derives, and compares the full accepted payload. A different payload is a
// conflict; an identical one is the idempotent retry and is returned as-is, so
// no second transcript entry and no second task are created.
func (a *Agent) ExistingRequestTask(ctx context.Context, ownerID, clientRequestID string, want RequestIdentity) (task.Task, bool, error) {
	it, found := a.RequestTask(ownerID, clientRequestID)
	if !found {
		return task.Task{}, false, nil
	}
	if !sameRequest(it, want) {
		return task.Task{}, false, ErrRequestPayloadChanged
	}
	ok, err := a.requestAttachmentsMatch(ctx, it, want)
	if err != nil {
		return task.Task{}, false, err
	}
	if !ok {
		return task.Task{}, false, ErrRequestPayloadChanged
	}
	return it, true, nil
}

func sameRoute(a, b *preferences.Route) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func sameRequest(it task.Task, want RequestIdentity) bool {
	return it.Goal == strings.TrimSpace(want.Content) &&
		it.BotID == strings.TrimSpace(want.BotID) &&
		it.SessionID == strings.TrimSpace(want.SessionID) &&
		it.ProjectID == strings.TrimSpace(want.ProjectID) &&
		it.ServerID == strings.TrimSpace(want.ServerID) &&
		it.OptionsJSON == want.OptionsJSON
}

// requestAttachmentsMatch checks the accepted goal entry kept its exact
// attachment set. The ids are only in the transcript, so the message written by
// acceptance is matched by goal text, attachment ids and the fact that it was
// written at or after the task row. With no attachments the goal text alone is
// the request.
func (a *Agent) requestAttachmentsMatch(ctx context.Context, it task.Task, want RequestIdentity) (bool, error) {
	if len(want.AttachmentIDs) == 0 {
		return true, nil
	}
	msgs, err := a.Store.ListMessages(ctx, it.SessionID)
	if err != nil {
		return false, err
	}
	for _, m := range msgs {
		if m.Role != "user" || m.Content != strings.TrimSpace(want.Content) {
			continue
		}
		if m.CreatedAt.Before(it.CreatedAt) || len(m.Attachments) != len(want.AttachmentIDs) {
			continue
		}
		match := true
		for i := range m.Attachments {
			if m.Attachments[i].ID != want.AttachmentIDs[i] {
				match = false
				break
			}
		}
		if match {
			return true, nil
		}
	}
	return false, nil
}

// PrepareTaskRefs is PrepareTask plus the validated attachment references the
// acceptance step stores in the transcript, so a task never loses its files.
func (a *Agent) PrepareTaskRefs(ctx context.Context, ownerID, botID, sessionID, title string, opt Options) (model.Session, string, Options, []model.Attachment, error) {
	return a.PrepareTaskRequest(ctx, ownerID, botID, sessionID, "", title, opt)
}

// PrepareTaskRequest resolves the stable session for a request id before the
// session is created, so an ambiguous retry is idempotent end to end.
func (a *Agent) PrepareTaskRequest(ctx context.Context, ownerID, botID, sessionID, clientRequestID, title string, opt Options) (model.Session, string, Options, []model.Attachment, error) {
	if strings.TrimSpace(ownerID) == "" {
		return model.Session{}, "", opt, nil, errors.New("缺少任务所有者")
	}
	if strings.TrimSpace(sessionID) == "" {
		sessionID = StableSessionID(ownerID, clientRequestID)
	}
	// Ownership and server restriction come from the authenticated request only;
	// a client-supplied value never widens them.
	opt.OwnerID = ownerID
	opt.RestrictServers = true
	opt.TaskID = ""
	b, err := a.pickBot(ctx, botID)
	if err != nil {
		return model.Session{}, "", opt, nil, err
	}
	if strings.TrimSpace(title) == "" {
		title = "任务"
	}
	sess, err := a.Store.EnsureSession(ctx, sessionID, b.ID, title)
	if err != nil {
		return model.Session{}, "", opt, nil, err
	}
	if sess.BotID != b.ID {
		return model.Session{}, "", opt, nil, errors.New("切换 Bot 时请新建任务")
	}
	// A session belongs to the owner who first used it. Reusing another user's
	// session would let a task inherit its transcript and pending cards.
	if a.Tasks != nil {
		for _, other := range a.Tasks.List("") {
			if other.SessionID == sess.ID && other.OwnerID != ownerID {
				return model.Session{}, "", opt, nil, errors.New("这个对话不属于当前用户，请新建对话")
			}
		}
	}
	if len(sess.Options) > 0 {
		var saved Options
		if json.Unmarshal(sess.Options, &saved) != nil {
			return model.Session{}, "", opt, nil, errors.New("无法读取对话设置")
		}
		// A session has one owner, recorded in its saved options. A legacy
		// session without a marker is claimed by the first task user; a session
		// owned by someone else is never reused.
		if savedOwner := sessionOwner(sess.Options); savedOwner != "" && savedOwner != ownerID {
			return model.Session{}, "", opt, nil, errors.New("这个对话不属于当前用户，请新建对话")
		}
		// Mode and workspace are fixed when the session is created. A later
		// request may omit them but never change them. An empty mode and "team"
		// are the same scope, so direct still cannot be switched back and forth.
		savedMode := normalizeMode(saved.Mode)
		if opt.Mode == "" {
			opt.Mode = saved.Mode
		} else if normalizeMode(opt.Mode) != savedMode {
			return model.Session{}, "", opt, nil, errors.New("不能更改对话模式，请新建对话")
		}
		if opt.Workspace == nil {
			opt.Workspace = saved.Workspace
		} else if saved.Workspace == nil || *opt.Workspace != *saved.Workspace {
			return model.Session{}, "", opt, nil, errors.New("不能更改对话绑定的工作区，请新建任务")
		}
		if opt.ApprovalMode == "" || opt.ApprovalMode == "default" {
			if saved.ApprovalMode != "" {
				opt.ApprovalMode = saved.ApprovalMode
			}
		}
		if opt.ApprovalRoute == nil {
			opt.ApprovalRoute = saved.ApprovalRoute
		}
		if opt.ModelRoute == nil {
			opt.ModelRoute = saved.ModelRoute
		}
		if opt.ExcludedBotIDs == nil {
			opt.ExcludedBotIDs = saved.ExcludedBotIDs
		}
	}
	if opt.Mode == "direct" {
		opt.ExcludedBotIDs = nil
	}
	// Validate exactly like an interactive turn: mode, model/approval routes,
	// excluded bots, attachment ownership and total size, workspace access.
	opt, refs, err := a.Preflight(ctx, opt)
	if err != nil {
		return model.Session{}, "", opt, nil, err
	}
	// Freeze the accepted authorization. The saved options never carry
	// attachments, plaintext secret answers or the client's card reply.
	opt.AttachmentIDs = make([]string, 0, len(refs))
	for _, ref := range refs {
		opt.AttachmentIDs = append(opt.AttachmentIDs, ref.ID)
	}
	saved := opt
	saved.ProjectTitle = ""
	saved.AttachmentIDs = nil
	saved.ChoiceAnswer = nil
	saved.SecretAnswer = nil
	raw, err := json.Marshal(struct {
		Options
		// OwnerID is the session's owner marker. The Options type hides its own
		// OwnerID from JSON, so this sibling field is what persists it.
		OwnerID string `json:"owner_id,omitempty"`
	}{Options: saved, OwnerID: ownerID})
	if err != nil {
		return model.Session{}, "", opt, nil, err
	}
	if err := a.Store.SaveChatOptions(ctx, sess.ID, raw); err != nil {
		return model.Session{}, "", opt, nil, err
	}
	project := ""
	if opt.Mode != "direct" && !isBackgroundSession(sess.ID) && store.ProjectSession(sess) {
		project = sess.ID
	}
	return sess, project, opt, refs, nil
}

func isBackgroundSession(id string) bool {
	return strings.HasPrefix(id, "inbox-") || strings.HasPrefix(id, "auto-") || strings.HasPrefix(id, "task-")
}

// AddTaskGoal stores the goal as the user's message so the chat transcript and
// the checkpoint agree.
func (a *Agent) AddTaskGoal(ctx context.Context, sessionID, content string) error {
	return a.AddTaskGoalRefs(ctx, sessionID, content, nil)
}

// AddTaskGoalRefs stores the goal with its validated attachments so the
// transcript keeps a recoverable reference to every file of this request.
func (a *Agent) AddTaskGoalRefs(ctx context.Context, sessionID, content string, refs []model.Attachment) error {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("任务需要先建立对话")
	}
	_, err := a.Store.AddMessage(ctx, model.Message{SessionID: sessionID, Role: "user", Content: content, Attachments: refs})
	return err
}

// WaitingTask returns the task in a session that waits for the operator, so a
// card answer resumes that task instead of creating another one. Only a
// waiting_user task may consume a card answer: a task parked on an approval
// must be decided through its own approval id, never by a card reply.
func (a *Agent) WaitingTask(ctx context.Context, ownerID, sessionID string) (task.Task, bool) {
	if a.Tasks == nil {
		return task.Task{}, false
	}
	for _, it := range a.Tasks.List(ownerID) {
		if it.SessionID != sessionID {
			continue
		}
		if it.Status == task.StatusWaitingUser && it.Pending != nil && it.Pending.Kind == task.PendingUser {
			return it, true
		}
	}
	return task.Task{}, false
}

// AnswerTaskCard records a choice or secret answer and returns the text that
// becomes the pending tool result. Secret plaintext goes to the vault only;
// the task stores the acknowledgement.
func (a *Agent) AnswerTaskCard(ctx context.Context, sessionID string, opt Options) (string, bool, error) {
	if opt.ChoiceAnswer != nil {
		text, done, err := a.answerChoice(ctx, sessionID, *opt.ChoiceAnswer)
		return text, done, err
	}
	if opt.SecretAnswer != nil {
		text, done, err := a.answerSecret(ctx, sessionID, *opt.SecretAnswer)
		return text, done, err
	}
	return "", false, errors.New("没有要回答的卡片")
}

// TaskCardIdentity returns the card message a reply targets. The transport
// binds the answer to that exact message; the store then refuses an answer for
// a card that is already answered or no longer pending.
func TaskCardIdentity(opt Options) (messageID string, isChoice bool, ok bool) {
	if opt.ChoiceAnswer != nil {
		return strings.TrimSpace(opt.ChoiceAnswer.MessageID), true, true
	}
	if opt.SecretAnswer != nil {
		return strings.TrimSpace(opt.SecretAnswer.MessageID), false, true
	}
	return "", false, false
}

// ErrCardNotPending means the reply does not target the card the task is
// actually waiting on.
var ErrCardNotPending = errors.New("这个回答不是任务正在等待的卡片")

// ErrCardStaleReplay means a retried answer cannot be proven to belong to the
// task it would resume. Returning a guess is worse than asking the client to
// reload the task list.
var ErrCardStaleReplay = errors.New("这个卡片回答已经处理过，请刷新任务状态")

// CheckTaskCard proves that a reply targets the exact card the task is
// suspended on. A task parked on an approval is never a card match, an
// arbitrary active task is never an acknowledgement, and a card from an
// earlier task in the same session is rejected.
func (a *Agent) CheckTaskCard(ctx context.Context, it task.Task, messageID string, isChoice bool) error {
	if it.Status != task.StatusWaitingUser || it.Pending == nil || it.Pending.Kind != task.PendingUser {
		return ErrCardNotPending
	}
	if strings.TrimSpace(messageID) == "" {
		return errString("缺少要回答的卡片")
	}
	// The runtime persists the exact card on Pending when it parks the call.
	// Without that binding the task cannot prove which card it waits on, and a
	// "latest card" guess would let an older answered card acknowledge it.
	if bound := strings.TrimSpace(it.Pending.CardMessageID); bound != messageID {
		return ErrCardNotPending
	}
	msgs, err := a.Store.ListMessages(ctx, it.SessionID)
	if err != nil {
		return err
	}
	// The bound card must still exist in this task's session, be an assistant
	// card of the right kind, and still be the newest assistant message.
	var found *model.Message
	latest := true
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.ID == messageID {
			found = &msgs[i]
			break
		}
		if m.Role == "user" {
			latest = false
		}
	}
	if found == nil || found.Role != "assistant" || found.SessionID != it.SessionID || !latest {
		return ErrCardNotPending
	}
	if isChoice && found.Choice == nil || !isChoice && found.SecretRequest == nil {
		return ErrCardNotPending
	}
	return nil
}

// Redact masks vault secrets and provider keys in text leaving the runtime, so
// the task API never echoes a credential that reached a goal, result or
// approval reason.
func (a *Agent) Redact(ctx context.Context, text string) string {
	return a.redact(ctx, text)
}

// CardTask finds the task that owns a card message, using the durable binding
// the runtime records: Pending.CardMessageID while waiting, and the journal
// entry after it resumes. It never guesses from "the latest task".
func (a *Agent) CardTask(ctx context.Context, ownerID, sessionID, messageID string) (task.Task, bool) {
	messageID = strings.TrimSpace(messageID)
	if a.Tasks == nil || messageID == "" || sessionID == "" {
		return task.Task{}, false
	}
	for _, it := range a.Tasks.List(ownerID) {
		if it.SessionID != sessionID {
			continue
		}
		if it.Pending != nil && strings.TrimSpace(it.Pending.CardMessageID) == messageID {
			return it, true
		}
		for _, entry := range it.Journal {
			if strings.TrimSpace(entry.CardMessageID) == messageID {
				return it, true
			}
		}
	}
	return task.Task{}, false
}

// CardAnswered reports whether the assistant card message was already answered
// or saved. It is the cross-storage truth: the card and the task live in
// different stores, so one can commit while the other does not.
func (a *Agent) CardAnswered(ctx context.Context, sessionID, messageID string) bool {
	if sessionID == "" || messageID == "" {
		return false
	}
	msgs, err := a.Store.ListMessages(ctx, sessionID)
	if err != nil {
		return false
	}
	for _, m := range msgs {
		if m.ID != messageID || m.Role != "assistant" {
			continue
		}
		if m.Choice != nil {
			return m.Choice.Answer != nil
		}
		if m.SecretRequest != nil {
			return m.SecretRequest.ProvidedAt != nil
		}
		return false
	}
	return false
}

// ResumableCardAnswer returns the stored answer text for a card that is already
// answered, so a retry can finish the task-side transition without asking the
// user again. It returns the literal answer text the card recorded.
func (a *Agent) ResumableCardAnswer(ctx context.Context, sessionID, messageID string) (string, bool, error) {
	msgs, err := a.Store.ListMessages(ctx, sessionID)
	if err != nil {
		return "", false, err
	}
	for _, m := range msgs {
		if m.ID != messageID || m.Role != "assistant" {
			continue
		}
		switch {
		case m.Choice != nil && m.Choice.Answer != nil:
			return answerText(*m.Choice, *m.Choice.Answer), true, nil
		case m.SecretRequest != nil && m.SecretRequest.ProvidedAt != nil:
			return secretAck(m.SecretRequest.Name), true, nil
		default:
			return "", false, nil
		}
	}
	return "", false, nil
}

// AcceptMessages builds the frozen conversation a task is accepted with: the
// bot's system prompt, the session history as it is right now, and this
// request's goal with its validated attachments. It runs before the task is
// queued, so a later message in the same session can never change what the
// task was authorized to do.
//
// The prompt is built under the accepted scope, not the transport's: a group
// session gets the isolated project scope (project notes only, never bot
// private memory) and a direct session keeps its own private memory. Freezing
// the prompt under the wrong scope would embed private notes in the checkpoint.
func (a *Agent) AcceptMessages(ctx context.Context, sess model.Session, botID, serverID, content string, refs []model.Attachment, opt Options) ([]map[string]any, error) {
	b, err := a.Store.GetBot(ctx, botID)
	if err != nil {
		return nil, err
	}
	// The system prompt depends on the chat scope, exactly like a live turn.
	promptCtx := context.WithValue(ctx, optionsKey{}, opt)
	if opt.Mode != "direct" && !isBackgroundSession(sess.ID) && store.ProjectSession(sess) {
		promptCtx = withProject(promptCtx, sess.ID)
	}
	var system string
	switch {
	case opt.Workspace != nil:
		system = a.workspacePrompt(promptCtx, b)
	case opt.Mode == "direct":
		if a.managesAssets(b) {
			system = a.assetPrompt(promptCtx, b)
		} else if a.developsModules(b) {
			system = a.developPrompt(promptCtx, b)
		} else {
			system = a.directPrompt(promptCtx, b, serverID)
		}
	default:
		if a.managesAssets(b) {
			system = a.assetPrompt(promptCtx, b)
		} else if a.developsModules(b) {
			system = a.developPrompt(promptCtx, b)
		} else {
			system = a.leaderPrompt(promptCtx, b, serverID)
		}
	}
	messages, err := a.sessionMessages(promptCtx, sess.ID, system, b.Name)
	if err != nil {
		return nil, err
	}
	message := model.Message{SessionID: sess.ID, Role: "user", Content: content, Attachments: refs}
	userContent, err := a.messageContent(promptCtx, message)
	if err != nil {
		return nil, err
	}
	messages = append(messages, map[string]any{"role": "user", "content": userContent})
	return messages, nil
}
