package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"onefrog/internal/bot"
	"onefrog/internal/model"
	"onefrog/internal/task"
)

// taskRequest is the accepted task body. Options carry the chat settings; the
// id and content fields belong to this endpoint only.
type taskRequest struct {
	chatReq
	ClientRequestID string `json:"client_request_id"`
	OrderedChat     bool   `json:"ordered_chat"`
}

// TaskView is the only task shape the API returns. Checkpoint messages,
// stored options, journal arguments and secrets never leave the runtime.
type TaskView struct {
	ID        string `json:"id"`
	BotID     string `json:"bot_id"`
	SessionID string `json:"session_id"`
	ProjectID string `json:"project_id,omitempty"`
	// ParentID links a child task to the task that delegated it, so the UI can
	// show a child's approvals under its parent.
	ParentID        string             `json:"parent_id,omitempty"`
	ParentTurn      int                `json:"parent_turn,omitempty"`
	ParentCallID    string             `json:"parent_call_id,omitempty"`
	Goal            string             `json:"goal"`
	UserMessageID   string             `json:"user_message_id,omitempty"`
	ChatAttachments []model.Attachment `json:"chat_attachments,omitempty"`
	Plan            []string           `json:"plan"`
	// PlanCompleted is the persisted count of leading plan steps actually
	// finished (0..len(Plan)). The frontend renders the segmented progress
	// strip from it; undefined/0 means nothing completed — never inferred.
	PlanCompleted int            `json:"plan_completed"`
	Progress      []string       `json:"progress"`
	Next          string         `json:"next,omitempty"`
	Status        string         `json:"status"`
	TotalSteps    int            `json:"total_steps"`
	MaxSteps      int            `json:"max_steps"`
	Deadline      *time.Time     `json:"deadline,omitempty"`
	Result        string         `json:"result,omitempty"`
	LastError     string         `json:"last_error,omitempty"`
	Pending       *TaskPending   `json:"pending,omitempty"`
	Approvals     []TaskApproval `json:"approvals"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	// Activity card fields. ModelLabel is the routed model name; WorkspaceLabel
	// is a safe workspace tag (never credentials). Activities is a bounded,
	// safe event trail (tool start/end, plan/verification boundaries); labels
	// never contain raw arguments, output or secrets.
	ModelLabel     string         `json:"model_label,omitempty"`
	WorkspaceLabel string         `json:"workspace_label,omitempty"`
	StartedAt      *time.Time     `json:"started_at,omitempty"`
	FinishedAt     *time.Time     `json:"finished_at,omitempty"`
	Activities     []TaskActivity `json:"activities,omitempty"`
}

// TaskActivity is one safe activity event for the task card.
type TaskActivity struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Label      string `json:"label"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type TaskPending struct {
	Kind       string `json:"kind"`
	ApprovalID string `json:"approval_id,omitempty"`
}

type TaskApproval struct {
	ID        string    `json:"id"`
	Command   string    `json:"command,omitempty"`
	Reasons   []string  `json:"reasons,omitempty"`
	Status    string    `json:"status"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	DecidedAt time.Time `json:"decided_at,omitempty"`
}

func taskView(it task.Task) TaskView {
	parentID, parentTurn, parentCallID := taskParent(it)
	v := TaskView{
		ID: it.ID, BotID: it.BotID, SessionID: it.SessionID, ProjectID: it.ProjectID,
		ParentID: parentID, ParentTurn: parentTurn, ParentCallID: parentCallID,
		Goal: clipText(it.Goal, 4000), Status: it.Status,
		TotalSteps: it.TotalSteps, MaxSteps: it.MaxSteps,
		Result: clipText(it.Result, 4000), LastError: clipText(it.LastError, 1000),
		CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt,
		Plan: append([]string{}, it.Plan...), PlanCompleted: it.PlanCompleted, Progress: append([]string{}, it.Progress...),
		Next: clipText(it.Next, 2000), Approvals: []TaskApproval{},
	}
	v.UserMessageID, v.ChatAttachments = bot.ChatQueueMessage(it)
	if v.UserMessageID != "" {
		// This is also the queued chat bubble, so keep the full accepted text.
		v.Goal = it.Goal
	}
	if !it.StartedAt.IsZero() {
		s := it.StartedAt
		v.StartedAt = &s
	}
	if !it.FinishedAt.IsZero() {
		f := it.FinishedAt
		v.FinishedAt = &f
	}
	if it.Plan == nil {
		v.Plan = []string{}
	}
	if v.PlanCompleted < 0 {
		v.PlanCompleted = 0
	}
	if v.PlanCompleted > len(v.Plan) {
		v.PlanCompleted = len(v.Plan)
	}
	if it.Progress == nil {
		v.Progress = []string{}
	}
	if !it.Deadline.IsZero() {
		d := it.Deadline
		v.Deadline = &d
	}
	if it.Pending != nil {
		v.Pending = &TaskPending{Kind: it.Pending.Kind, ApprovalID: it.Pending.ApprovalID}
	}
	for _, a := range it.Approvals {
		v.Approvals = append(v.Approvals, TaskApproval{
			ID: a.ID, Command: clipText(a.Command, 2000), Reasons: append([]string{}, a.Reasons...),
			Status: a.Status, Note: clipText(a.Note, 1000), CreatedAt: a.CreatedAt, DecidedAt: a.DecidedAt,
		})
	}
	return v
}

// taskParent reads the delegation link from the task's serialized shape, so
// this transport keeps compiling while the registry adds the fields.
func taskParent(it task.Task) (parentID string, parentTurn int, parentCallID string) {
	raw, err := json.Marshal(it)
	if err != nil {
		return "", 0, ""
	}
	var shape struct {
		ParentID     string `json:"parent_id"`
		ParentTurn   int    `json:"parent_turn"`
		ParentCallID string `json:"parent_call_id"`
	}
	if json.Unmarshal(raw, &shape) != nil {
		return "", 0, ""
	}
	return shape.ParentID, shape.ParentTurn, shape.ParentCallID
}

// redactTaskView masks vault secrets and provider keys in the fields the API
// returns. Approval commands and reasons are already redacted by the runtime
// before they are stored; this second pass covers goals, results and any text
// that reached the task through another path.
func (s *Server) redactTaskView(c *gin.Context, v TaskView) TaskView {
	ctx := c.Request.Context()
	v.Goal = s.Agent.Redact(ctx, v.Goal)
	v.Result = s.Agent.Redact(ctx, v.Result)
	v.LastError = s.Agent.Redact(ctx, v.LastError)
	v.Next = s.Agent.Redact(ctx, v.Next)
	for i := range v.Plan {
		v.Plan[i] = s.Agent.Redact(ctx, v.Plan[i])
	}
	for i := range v.Progress {
		v.Progress[i] = s.Agent.Redact(ctx, v.Progress[i])
	}
	for i := range v.Approvals {
		v.Approvals[i].Command = s.Agent.Redact(ctx, v.Approvals[i].Command)
		v.Approvals[i].Note = s.Agent.Redact(ctx, v.Approvals[i].Note)
		for j := range v.Approvals[i].Reasons {
			v.Approvals[i].Reasons[j] = s.Agent.Redact(ctx, v.Approvals[i].Reasons[j])
		}
	}
	return v
}

func (s *Server) taskViewFor(c *gin.Context, it task.Task) TaskView {
	v := taskView(it)
	// Activity trail and labels come from the runner (process memory rebuilt
	// from the durable journal). Redact like every other free-text field.
	if s.Tasks != nil {
		acts := bot.ActivitiesFor(s.Tasks, it)
		v.Activities = make([]TaskActivity, 0, len(acts))
		for _, ev := range acts {
			v.Activities = append(v.Activities, TaskActivity{
				ID: ev.ID, Name: ev.Name, Label: s.Agent.Redact(c.Request.Context(), ev.Label),
				Status: ev.Status, StartedAt: ev.StartedAt, FinishedAt: ev.FinishedAt, DurationMS: ev.DurationMS,
			})
		}
		if v.ModelLabel == "" {
			v.ModelLabel = bot.TaskModelLabel(s.Tasks, it)
		}
	}
	if v.WorkspaceLabel == "" {
		v.WorkspaceLabel = taskWorkspaceLabel(it)
	}
	return s.redactTaskView(c, v)
}

// taskWorkspaceLabel builds the safe workspace tag from the task's frozen
// options. It never includes credentials; SSH targets show server id + root.
func taskWorkspaceLabel(it task.Task) string {
	if it.OptionsJSON == "" {
		return ""
	}
	var opts struct {
		Workspace *struct {
			ServerID string `json:"server_id"`
			Root     string `json:"root"`
			Kind     string `json:"kind"`
		} `json:"workspace"`
	}
	if json.Unmarshal([]byte(it.OptionsJSON), &opts) != nil || opts.Workspace == nil {
		return ""
	}
	w := opts.Workspace
	if w.Kind == "local" {
		return "local:" + strings.TrimSuffix(w.Root, "/")
	}
	if w.ServerID == "" || w.Root == "" {
		return ""
	}
	return "ssh:" + w.ServerID + strings.TrimSuffix(w.Root, "/")
}

func (s *Server) tasksReady(c *gin.Context) bool {
	if s.Tasks == nil || s.Agent == nil || s.Agent.Tasks == nil {
		fail(c, http.StatusServiceUnavailable, "任务运行时未启用")
		return false
	}
	return true
}

func (s *Server) listTasks(c *gin.Context) {
	if !s.tasksReady(c) {
		return
	}
	user := c.MustGet("user").(model.User)
	sessionID := strings.TrimSpace(c.Query("session_id"))
	rows := []TaskView{}
	for _, it := range s.Agent.Tasks.List(user.ID) {
		if sessionID != "" && it.SessionID != sessionID {
			continue
		}
		rows = append(rows, s.taskViewFor(c, it))
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"items": rows})
}

func (s *Server) getTask(c *gin.Context) {
	if !s.tasksReady(c) {
		return
	}
	user := c.MustGet("user").(model.User)
	it, err := s.Agent.Tasks.Get(c.Param("id"))
	if errors.Is(err, task.ErrNotFound) || (err == nil && it.OwnerID != user.ID) {
		fail(c, http.StatusNotFound, "任务不存在")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "任务读取失败")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, s.taskViewFor(c, it))
}

// createTask accepts the ordinary chat request and returns immediately; the
// task runs in the background, independent of this connection.
func (s *Server) createTask(c *gin.Context) {
	if !s.tasksReady(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2*1024*1024)
	var req taskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	user := c.MustGet("user").(model.User)
	ctx := c.Request.Context()

	// A card answer resumes the waiting task in this session instead of
	// starting another one.
	if req.Options.ChoiceAnswer != nil || req.Options.SecretAnswer != nil {
		s.resumeTaskCard(c, user, req.chatReq)
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		fail(c, http.StatusBadRequest, "任务目标不能为空")
		return
	}
	// Request identity is owner + client_request_id alone, and it names exactly
	// one task id. A retry must land on that task; it is never resolved by
	// scanning the session, because two different request ids in one session
	// are two different tasks even with the same goal.
	//
	// The whole acceptance goes through the one shared submission pipeline
	// (PrepareTaskRequest -> AcceptMessages -> AcceptTask), so this transport
	// never re-implements freezing, validation or the two-phase queue. A retry
	// with the identical payload returns the stored task and writes nothing; a
	// changed payload under the same id is refused as a conflict.
	requestID := strings.TrimSpace(req.ClientRequestID)
	if requestID != "" {
		if existing, found := s.Agent.RequestTask(user.ID, requestID); found &&
			existing.Goal != strings.TrimSpace(req.Content) {
			fail(c, http.StatusConflict, bot.ErrRequestPayloadChanged.Error())
			return
		}
	}
	it, err := s.Agent.SubmitTask(ctx, bot.SubmitRequest{
		OwnerID: user.ID, BotID: req.BotID, SessionID: req.SessionID,
		ClientRequestID: requestID, ServerID: req.ServerID, Content: req.Content,
		Title: clipText(req.Content, 60), Options: req.Options,
		OrderedChat: req.OrderedChat,
	})
	if err != nil {
		if errors.Is(err, bot.ErrNoRunner) {
			fail(c, http.StatusServiceUnavailable, err.Error())
			return
		}
		// A request id that already names a task but did not return that task
		// was reused with a different payload: refuse it instead of letting the
		// user think the new content was accepted.
		if requestID != "" {
			if _, found := s.Agent.RequestTask(user.ID, requestID); found {
				fail(c, http.StatusConflict, err.Error())
				return
			}
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, s.taskViewFor(c, it))
}

func (s *Server) resumeTaskCard(c *gin.Context, user model.User, req chatReq) {
	it, err := s.ResumeTaskCard(c.Request.Context(), user.ID, req.SessionID, req.Options)
	if err != nil {
		taskCardError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, s.taskViewFor(c, it))
}

// ResumeTaskCard is the shared card-resume path for every ingress (POST
// /api/tasks, /api/chat and the WebSocket relay). It returns the task the answer
// belongs to, so no transport reimplements the card identity rules.
func (s *Server) ResumeTaskCard(ctx context.Context, ownerID, sessionID string, opt bot.Options) (task.Task, error) {
	messageID, isChoice, hasCard := bot.TaskCardIdentity(opt)
	if !hasCard || messageID == "" {
		return task.Task{}, errCardBadRequest("缺少要回答的卡片")
	}
	it, ok := s.Agent.WaitingTask(ctx, ownerID, sessionID)
	if !ok {
		// No waiting task: either the answer already resumed its own task (a
		// retry) or this card belongs to nobody. Only the durable card→task
		// binding can prove a retry; any other task is not an acknowledgement.
		if owner, found := s.Agent.CardTask(ctx, ownerID, sessionID, messageID); found {
			return owner, nil
		}
		return task.Task{}, errCardConflict("这个对话没有等待回答的任务")
	}
	// A card bound to an earlier task must never acknowledge the current one,
	// and a retry of that earlier card must not resume this task either.
	if owner, found := s.Agent.CardTask(ctx, ownerID, sessionID, messageID); found && owner.ID != it.ID {
		return task.Task{}, errCardConflict(bot.ErrCardStaleReplay.Error())
	}
	// The reply must target the exact card this task recorded when it parked.
	// This is checked before the answered-repair path: an answered card from an
	// earlier turn must never be mistaken for this task's answer.
	if err := s.Agent.CheckTaskCard(ctx, it, messageID, isChoice); err != nil {
		if errors.Is(err, bot.ErrCardNotPending) {
			return task.Task{}, errCardConflict(err.Error())
		}
		return task.Task{}, errCardBadRequest(err.Error())
	}
	// Cross-storage gap: the store recorded the answer but the task is still
	// waiting (the process stopped between the two writes). Finish the task
	// transition from the stored answer instead of rejecting a valid reply and
	// leaving the task stuck forever.
	if s.Agent.CardAnswered(ctx, sessionID, messageID) {
		answer, ok, err := s.Agent.ResumableCardAnswer(ctx, sessionID, messageID)
		if err != nil {
			return task.Task{}, errCardInternal("读取卡片回答失败")
		}
		if !ok {
			return task.Task{}, errCardConflict(bot.ErrCardNotPending.Error())
		}
		return s.Tasks.AnswerUser(ctx, ownerID, it.ID, answer)
	}
	answer, done, err := s.Agent.AnswerTaskCard(ctx, sessionID, opt)
	if err != nil {
		return task.Task{}, errCardBadRequest(err.Error())
	}
	if done {
		// A retried answer: report the task without running it again.
		return it, nil
	}
	return s.Tasks.AnswerUser(ctx, ownerID, it.ID, answer)
}

// cardError carries the HTTP status a card failure maps to.
type cardError struct {
	status int
	msg    string
}

func (e cardError) Error() string { return e.msg }
func errCardBadRequest(msg string) error {
	return cardError{status: http.StatusBadRequest, msg: msg}
}
func errCardConflict(msg string) error {
	return cardError{status: http.StatusConflict, msg: msg}
}
func errCardInternal(msg string) error {
	return cardError{status: http.StatusInternalServerError, msg: msg}
}

func taskCardError(c *gin.Context, err error) {
	var ce cardError
	if errors.As(err, &ce) {
		fail(c, ce.status, ce.msg)
		return
	}
	taskError(c, err)
}

func (s *Server) pauseTask(c *gin.Context) {
	s.taskAction(c, func(userID, id string) (task.Task, error) {
		return s.Tasks.Pause(c.Request.Context(), userID, id)
	})
}

func (s *Server) resumeTask(c *gin.Context) {
	s.taskAction(c, func(userID, id string) (task.Task, error) {
		return s.Tasks.Resume(c.Request.Context(), userID, id)
	})
}

func (s *Server) cancelTask(c *gin.Context) {
	s.taskAction(c, func(userID, id string) (task.Task, error) {
		if c.Query("queued_only") == "true" {
			return s.Tasks.WithdrawChat(userID, id)
		}
		return s.Tasks.Cancel(c.Request.Context(), userID, id)
	})
}

func (s *Server) taskAction(c *gin.Context, fn func(userID, id string) (task.Task, error)) {
	if !s.tasksReady(c) {
		return
	}
	user := c.MustGet("user").(model.User)
	it, err := fn(user.ID, c.Param("id"))
	if err != nil {
		taskError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, s.taskViewFor(c, it))
}

func (s *Server) guideTask(c *gin.Context) {
	if !s.tasksReady(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var in struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	user := c.MustGet("user").(model.User)
	it, err := s.Tasks.Guide(c.Request.Context(), user.ID, c.Param("id"), in.Content)
	if err != nil {
		taskError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, s.taskViewFor(c, it))
}

func (s *Server) decideTaskApproval(c *gin.Context) {
	if !s.tasksReady(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var in struct {
		Allow  *bool  `json:"allow"`
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Allow == nil {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	user := c.MustGet("user").(model.User)
	it, err := s.Tasks.Decide(c.Request.Context(), user.ID, c.Param("id"), c.Param("approvalID"), *in.Allow, in.Reason)
	if err != nil {
		taskError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, s.taskViewFor(c, it))
}

func taskError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, task.ErrNotFound):
		fail(c, http.StatusNotFound, "任务不存在")
	case errors.Is(err, task.ErrInvalid):
		fail(c, http.StatusConflict, err.Error())
	default:
		fail(c, http.StatusConflict, err.Error())
	}
}
