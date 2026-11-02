package api

import (
	"github.com/gin-gonic/gin"
	"onefrog/internal/model"
	"onefrog/internal/store"
)

func (s *Server) serviceViews(c *gin.Context) {
	servers, err := s.Store.ListServers(c.Request.Context())
	if mapStore(c, err) {
		return
	}
	ids := make([]string, 0, len(servers))
	for _, h := range servers {
		ids = append(ids, h.ID)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"nodes": s.Services.Views(ids)})
}
func (s *Server) scanServices(c *gin.Context) {
	v, err := s.Services.Scan(c.Request.Context(), c.Param("id"))
	if mapStore(c, err) {
		return
	}
	status := "success"
	if v.Status == "failed" {
		status = "failed"
	}
	_, _ = s.Store.AddHistory(c.Request.Context(), store.HistoryRec{History: model.History{ServerID: v.ServerID, Action: "只读检测服务、容器与监听端口", Status: status}})
	c.Header("Cache-Control", "no-store")
	c.JSON(200, v)
}
