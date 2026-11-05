package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"onefrog/internal/automation"
	"onefrog/internal/model"
	"onefrog/internal/store"
)

func (s *Server) listAutomations(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"items": s.Automations.List()})
}

func (s *Server) bindAutomation(c *gin.Context) (automation.Automation, bool) {
	var in automation.Automation
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "自动化格式无效")
		return in, false
	}
	ctx := c.Request.Context()
	in.BotID = strings.TrimSpace(in.BotID)
	in.ServerID = strings.TrimSpace(in.ServerID)
	if in.BotID == "" {
		fail(c, http.StatusBadRequest, "请选择 Bot")
		return in, false
	}
	if _, err := s.Store.GetBot(ctx, in.BotID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, http.StatusBadRequest, "Bot 不存在")
		} else {
			mapStore(c, err)
		}
		return in, false
	}
	if in.ServerID != "" {
		if _, err := s.Store.GetServer(ctx, in.ServerID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				fail(c, http.StatusBadRequest, "服务器不存在")
			} else {
				mapStore(c, err)
			}
			return in, false
		}
	}
	return in, true
}

func (s *Server) createAutomation(c *gin.Context) {
	in, ok := s.bindAutomation(c)
	if !ok {
		return
	}
	user := c.MustGet("user").(model.User)
	it, err := s.Automations.Create(in, user.ID)
	if automationError(c, err) {
		return
	}
	c.JSON(http.StatusCreated, it)
}

func (s *Server) updateAutomation(c *gin.Context) {
	if _, err := s.Automations.Get(c.Param("id")); mapStore(c, err) {
		return
	}
	in, ok := s.bindAutomation(c)
	if !ok {
		return
	}
	it, err := s.Automations.Update(c.Param("id"), in)
	if automationError(c, err) {
		return
	}
	c.JSON(http.StatusOK, it)
}

func (s *Server) deleteAutomation(c *gin.Context) {
	if automationError(c, s.Automations.Delete(c.Param("id"))) {
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) runAutomation(c *gin.Context) {
	it, err := s.Automations.RunNow(c.Param("id"))
	if automationError(c, err) {
		return
	}
	c.JSON(http.StatusAccepted, it)
}

func automationError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	var bad *automation.ValidationError
	switch {
	case errors.As(err, &bad):
		fail(c, http.StatusBadRequest, bad.Msg)
	case errors.Is(err, automation.ErrFull), errors.Is(err, automation.ErrRunning):
		fail(c, http.StatusConflict, err.Error())
	default:
		mapStore(c, err)
	}
	return true
}
