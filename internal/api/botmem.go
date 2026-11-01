package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"onefrog/internal/model"
	"onefrog/internal/store"
)

func (s *Server) listBotMemories(c *gin.Context) {
	if _, err := s.Store.GetBot(c.Request.Context(), c.Param("id")); mapStore(c, err) {
		return
	}
	items, err := s.Store.ListBotMemories(c.Request.Context(), c.Param("id"))
	if mapStore(c, err) {
		return
	}
	if items == nil {
		items = []model.BotMemory{}
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) createBotMemory(c *gin.Context) {
	var req struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if c.ShouldBindJSON(&req) != nil {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	if _, err := s.Store.GetBot(c.Request.Context(), c.Param("id")); mapStore(c, err) {
		return
	}
	item, err := s.Agent.Remember(c.Request.Context(), c.Param("id"), req.Title, req.Body, "user")
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrLimit) {
			mapStore(c, err)
			return
		}
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusCreated, item)
}

func (s *Server) deleteBotMemory(c *gin.Context) {
	if mapStore(c, s.Store.DeleteBotMemory(c.Request.Context(), c.Param("id"), c.Param("memoryID"))) {
		return
	}
	c.Status(http.StatusNoContent)
}
