package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"onefrog/internal/model"
)

func (s *Server) listProjectNotes(c *gin.Context) {
	notes, err := s.Store.ListProjectNotes(c.Request.Context(), c.Param("id"))
	if mapStore(c, err) {
		return
	}
	if notes == nil {
		notes = []model.ProjectNote{}
	}
	c.JSON(http.StatusOK, notes)
}

func (s *Server) saveProjectNote(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	var input struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if c.ShouldBindJSON(&input) != nil {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	ctx := c.Request.Context()
	if _, err := s.Store.ListProjectNotes(ctx, c.Param("id")); mapStore(c, err) {
		return
	}
	note, err := s.Agent.RememberProject(ctx, c.Param("id"), input.Title, input.Body, "user")
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusCreated, note)
}

func (s *Server) deleteProjectNote(c *gin.Context) {
	if mapStore(c, s.Store.DeleteProjectNote(c.Request.Context(), c.Param("id"), c.Param("noteID"))) {
		return
	}
	c.Status(http.StatusNoContent)
}
