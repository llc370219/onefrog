package api

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"onefrog/internal/bot"
	"onefrog/internal/model"
)

func (s *Server) listApprovals(c *gin.Context) {
	items, err := s.Store.ListHistory(c.Request.Context(), 200)
	if mapStore(c, err) {
		return
	}
	rows := []any{}
	for _, h := range items {
		if h.Status != "approval_allowed" && h.Status != "approval_denied" {
			continue
		}
		rec, err := s.Store.GetHistory(c.Request.Context(), h.ID)
		if err != nil {
			continue
		}
		var review bot.Review
		if json.Unmarshal([]byte(rec.Snapshot), &review) != nil {
			continue
		}
		rows = append(rows, struct {
			model.History
			Review bot.Review `json:"review"`
		}{h, review})
	}
	c.JSON(200, rows)
}
