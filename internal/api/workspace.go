package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"onefrog/internal/model"
	"onefrog/internal/store"
	"onefrog/internal/workspace"
)

func (s *Server) codeWorkspace(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
	var body struct {
		Target workspace.Target `json:"target"`
		workspace.Request
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, http.StatusBadRequest, "工作区请求无效")
		return
	}
	switch body.Action {
	case "list", "status", "diff", "read", "write":
	default:
		fail(c, http.StatusBadRequest, "此入口只支持浏览和手动保存文件")
		return
	}
	// kind 是显式选择：只接受空（沿用 SSH 兼容语义）、ssh、local。其他值必须
	// 被拒绝，绝不能静默回退到 SSH——否则一个拼错的 kind 会在错误的服务器上
	// 执行操作。
	if body.Target.Kind != "" && body.Target.Kind != "ssh" && body.Target.Kind != "local" {
		fail(c, http.StatusBadRequest, "target.kind 只支持 ssh 或 local")
		return
	}
	if body.Target.IsLocal() {
		s.codeWorkspaceLocal(c, body.Target, body.Request)
		return
	}
	ctx := c.Request.Context()
	if s.Agent == nil || s.Agent.Workspace == nil {
		fail(c, http.StatusServiceUnavailable, "工作区服务未启用")
		return
	}
	res, err := s.Agent.Workspace.Do(ctx, body.Target, body.Request)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if body.Action == "write" {
		_, _ = s.Store.AddHistory(ctx, store.HistoryRec{History: model.History{
			ServerID: body.Target.ServerID, Action: "手动保存工作区文件：" + body.Target.Root + "/" + body.Path, Status: "success",
		}})
	}
	c.JSON(http.StatusOK, res)
}

// codeWorkspaceLocal serves the flat /api/workspace shape for kind=local
// targets: browsing via the engine, full-content reads/writes via the editor's
// RawRead/RawSave with revision checking. Exec, edit and search are never
// offered over HTTP; targets must sit in the operator allowlist.
func (s *Server) codeWorkspaceLocal(c *gin.Context, t workspace.Target, r workspace.Request) {
	if s.Agent == nil || s.Agent.LocalWorkspace == nil {
		fail(c, http.StatusServiceUnavailable, "本地工作区未启用")
		return
	}
	if err := t.Validate(); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.Agent.LocalWorkspace.Roots().Pick(t.Root); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Action {
	case "list", "status", "diff":
		res, err := s.Agent.LocalWorkspace.Do(c.Request.Context(), t.Root, workspace.LocalRequest{Action: r.Action, Path: r.Path})
		if err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, res)
	case "read":
		raw, err := s.Agent.LocalWorkspace.RawRead(t.Root, r.Path)
		if err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, workspace.Result{Text: raw.Content, Revision: raw.Revision})
	case "write":
		if len(r.Content) > workspace.MaxLocalFile {
			fail(c, http.StatusBadRequest, "文件超过编辑器大小上限")
			return
		}
		raw, err := s.Agent.LocalWorkspace.RawSave(t.Root, r.Path, r.Content, r.Revision)
		if err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		if s.Store != nil {
			_, _ = s.Store.AddHistory(c.Request.Context(), store.HistoryRec{History: model.History{
				ServerID: t.ServerID, Action: "手动保存工作区文件：" + t.Root + "/" + r.Path, Status: "success",
			}})
		}
		c.JSON(http.StatusOK, workspace.Result{Text: raw.Content, Revision: raw.Revision})
	}
}
