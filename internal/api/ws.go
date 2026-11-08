package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"onefrog/internal/bot"
	"onefrog/internal/model"
	"onefrog/internal/task"
)

// manualApprovalTimeout bounds how long a command waits for the operator.
var manualApprovalTimeout = 5 * time.Minute

type wsWriter struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (w *wsWriter) send(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteJSON(v)
}

type approvalReply struct {
	allow  bool
	reason string
}

// wsChat streams agent steps. Client → server messages are either chat
// requests or {"type":"approval","id":...,"allow":bool} answers to an
// approval_request (smart or manual approval mode). No answer within
// manualApprovalTimeout counts as a rejection.
func (s *Server) wsChat(c *gin.Context) {
	if !sameOrigin(c.Request, s.PublicURL) {
		fail(c, http.StatusForbidden, "来源不被允许")
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return sameOrigin(r, s.PublicURL) }}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(2 * 1024 * 1024)
	user := c.MustGet("user").(model.User)
	out := &wsWriter{conn: conn}
	connCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var pmu sync.Mutex
	pending := map[string]chan approvalReply{}
	requests := make(chan chatReq, 1)
	go func() {
		defer cancel()
		defer close(requests)
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var head struct {
				Type   string `json:"type"`
				ID     string `json:"id"`
				TaskID string `json:"task_id"`
				Allow  bool   `json:"allow"`
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(raw, &head)
			if head.Type == "approval" {
				if s.durable() {
					// A durable decision must name both the task and the exact
					// approval it decides. Guessing "the only pending task" would
					// let a stale card decide a different parked call, so an
					// incomplete reply is refused instead.
					taskID := strings.TrimSpace(head.TaskID)
					approvalID := strings.TrimSpace(head.ID)
					if taskID == "" || approvalID == "" {
						_ = out.send(gin.H{"type": "error", "message": "审批需要同时指定任务和审批编号"})
						continue
					}
					if _, err := s.Tasks.Decide(connCtx, user.ID, taskID, approvalID, head.Allow, clipText(head.Reason, 200)); err != nil {
						_ = out.send(gin.H{"type": "error", "message": err.Error()})
						continue
					}
					// The task wakes and the observer loop relays the outcome;
					// no channel hand-off is involved, so a reconnect can decide
					// the same approval instead of losing it with this socket.
					continue
				}
				pmu.Lock()
				ch := pending[head.ID]
				delete(pending, head.ID)
				pmu.Unlock()
				if ch != nil {
					ch <- approvalReply{allow: head.Allow, reason: clipText(head.Reason, 200)}
				}
				continue
			}
			var req chatReq
			if err := json.Unmarshal(raw, &req); err != nil {
				_ = out.send(gin.H{"type": "error", "message": "请求无效"})
				continue
			}
			select {
			case requests <- req:
			default:
				_ = out.send(gin.H{"type": "error", "message": "上一条消息还在处理中"})
			}
		}
	}()

	approver := func(ctx context.Context, r bot.ApprovalRequest) (bool, string) {
		ch := make(chan approvalReply, 1)
		pmu.Lock()
		pending[r.ID] = ch
		pmu.Unlock()
		defer func() { pmu.Lock(); delete(pending, r.ID); pmu.Unlock() }()
		// This approver only serves the turn-bound fallback (no durable runner).
		// A durable task parks its approval on the task itself and is decided
		// through Runner.Decide, so the decision survives a reconnect.
		if err := out.send(gin.H{"type": "approval_request", "id": r.ID, "single_use": r.SingleUse, "bot": r.BotID, "bot_name": r.BotName, "server_id": r.ServerID, "server_name": r.ServerName, "command": r.Command, "warning": r.Warning, "mode": r.Mode, "risk": r.Risk, "reasons": r.Reasons, "findings": r.Findings, "model_risk": r.ModelRisk, "model_reason": r.ModelReason, "untrusted": r.Untrusted, "timeout_seconds": int(manualApprovalTimeout.Seconds())}); err != nil {
			return false, "连接已断开，未执行"
		}
		timer := time.NewTimer(manualApprovalTimeout)
		defer timer.Stop()
		select {
		case rep := <-ch:
			return rep.allow, rep.reason
		case <-timer.C:
			_ = out.send(gin.H{"type": "approval_timeout", "id": r.ID})
			return false, "等待人工审批超时，未执行"
		case <-connCtx.Done():
			return false, "连接已断开，未执行"
		case <-ctx.Done():
			return false, "对话已中断，未执行"
		}
	}

	for req := range requests {
		req.OwnerID = user.ID
		req.RestrictServers = true
		req.Approver = approver
		if s.durable() {
			s.durableWS(c, user, req, out, connCtx)
			continue
		}
		result, err := s.Agent.ReplyWithOptions(c.Request.Context(), req.BotID, req.ServerID, req.SessionID, req.Content, req.Options, func(step bot.Step) {
			_ = out.send(stepEvent(step))
		})
		if err != nil {
			_ = out.send(gin.H{"type": "error", "message": err.Error()})
			continue
		}
		_ = out.send(gin.H{"type": "assistant", "content": result.Text, "message_id": result.MessageID, "choice": result.Choice, "secret_request": result.SecretRequest, "timeline": result.Timeline, "answered": result.Answered})
		_ = out.send(gin.H{"type": "done"})
	}
}

// durableWS submits a socket request as a durable task and relays its progress.
// The task is never bound to the socket: closing the tab leaves it running, and
// a reconnect can read the same task instead of starting over.
func (s *Server) durableWS(c *gin.Context, user model.User, req chatReq, out *wsWriter, connCtx context.Context) {
	ctx := context.Background()
	var taskID string
	if req.Options.ChoiceAnswer != nil || req.Options.SecretAnswer != nil {
		it, err := s.ResumeTaskCard(ctx, user.ID, req.SessionID, req.Options)
		if err != nil {
			_ = out.send(gin.H{"type": "error", "message": err.Error()})
			return
		}
		taskID = it.ID
	} else {
		it, err := s.Agent.SubmitTask(ctx, bot.SubmitRequest{
			OwnerID: user.ID, BotID: req.BotID, SessionID: req.SessionID,
			ClientRequestID: strings.TrimSpace(req.ClientRequestID), ServerID: req.ServerID,
			Content: req.Content, Title: clipText(req.Content, 60), Options: req.Options,
		})
		if err != nil {
			_ = out.send(gin.H{"type": "error", "message": err.Error()})
			return
		}
		taskID = it.ID
	}
	if it, err := s.Tasks.Book.Get(taskID); err == nil {
		_ = out.send(gin.H{"type": "session", "session_id": it.SessionID})
		_ = out.send(gin.H{"type": "task", "task": s.taskViewFor(c, it)})
	}
	outcome, err := s.Tasks.WaitTask(connCtx, user.ID, taskID, 100*time.Millisecond)
	if err != nil {
		// The socket closed, not the task. Say so and leave the task parked.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			_ = out.send(gin.H{"type": "detached", "task_id": taskID, "message": "连接已断开，任务继续在后台运行"})
			return
		}
		_ = out.send(gin.H{"type": "error", "message": err.Error()})
		return
	}
	if outcome.Done {
		if outcome.Task.Status == task.StatusFailed || outcome.Task.Status == task.StatusBudgetExhausted {
			// A failed task is not a completion. Relay the durable view so the
			// UI shows the real status and error instead of an empty answer.
			_ = out.send(gin.H{"type": "task", "task": s.taskViewFor(c, outcome.Task)})
			return
		}
		_ = out.send(gin.H{"type": "assistant", "content": outcome.Result.Text, "message_id": outcome.Result.MessageID, "timeline": outcome.Result.Timeline, "task_id": taskID})
		_ = out.send(gin.H{"type": "done"})
		return
	}
	// A durable approval is announced with the exact task and approval id, so
	// the client can decide it through Runner.Decide and a reconnect can
	// replay the same decision. The pending record is read from the task, never
	// inferred from the session.
	if p := outcome.Task.Pending; p != nil && p.Kind == task.PendingApproval && p.ApprovalID != "" {
		card := gin.H{"type": "approval_request", "id": p.ApprovalID, "task_id": outcome.Task.ID, "bot": outcome.Task.BotID, "single_use": true, "timeout_seconds": int(manualApprovalTimeout.Seconds())}
		for _, a := range outcome.Task.Approvals {
			if a.ID != p.ApprovalID {
				continue
			}
			card["command"] = a.Command
			card["reasons"] = a.Reasons
			break
		}
		_ = out.send(card)
	}
	// Waiting on the operator: relay the exact durable state instead of a
	// fabricated completion.
	_ = out.send(gin.H{"type": "task", "task": s.taskViewFor(c, outcome.Task)})
}

// durable reports whether this server runs durable tasks. Both the legacy HTTP
// chat and the WebSocket relay use it to choose between the durable path and
// the turn-bound fallback used by tests and by builds without a runner.
func (s *Server) durable() bool {
	return s.Tasks != nil && s.Agent != nil && s.Agent.TasksRunner != nil
}

func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
