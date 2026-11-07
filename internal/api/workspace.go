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
	ctx := c.Request.Context()
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
