package api

import (
	"errors"

	"github.com/gin-gonic/gin"
	"onefrog/internal/model"
	"onefrog/internal/services"
	"onefrog/internal/store"
)

func (s *Server) writeServiceWatch(c *gin.Context) {
	cfg, status, err := s.ServiceWatch.Snapshot(c.Request.Context())
	if mapStore(c, err) {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"interval_seconds": cfg.IntervalSeconds, "servers": cfg.Servers, "status": status})
}
func (s *Server) serviceWatch(c *gin.Context) { s.writeServiceWatch(c) }
func (s *Server) saveServiceWatch(c *gin.Context) {
	var in services.WatchConfig
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, "监控配置格式无效")
		return
	}
	if err := s.ServiceWatch.Set(c.Request.Context(), in); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			mapStore(c, err)
			return
		}
		fail(c, 400, err.Error())
		return
	}
	s.writeServiceWatch(c)
}
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
