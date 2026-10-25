package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"onefrog/internal/bot"
	"onefrog/internal/model"
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
// approval_request (manual approval mode).
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
				Allow  bool   `json:"allow"`
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(raw, &head)
			if head.Type == "approval" {
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
		if err := out.send(gin.H{"type": "approval_request", "id": r.ID, "bot": r.BotID, "server_id": r.ServerID, "server_name": r.ServerName, "command": r.Command, "warning": r.Warning, "timeout_seconds": int(manualApprovalTimeout.Seconds())}); err != nil {
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
		result, err := s.Agent.ReplyWithOptions(c.Request.Context(), req.BotID, req.ServerID, req.SessionID, req.Content, req.Options, func(step bot.Step) {
			_ = out.send(stepEvent(step))
		})
		if err != nil {
			_ = out.send(gin.H{"type": "error", "message": err.Error()})
			continue
		}
		_ = out.send(gin.H{"type": "assistant", "content": result.Text})
		_ = out.send(gin.H{"type": "done"})
	}
}

func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
