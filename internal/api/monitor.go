package api

import (
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"onefrog/internal/monitor"
	"strings"
	"time"
)

func (s *Server) monitorView(c *gin.Context) {
	servers, err := s.Store.ListServers(c.Request.Context())
	if mapStore(c, err) {
		return
	}
	ids := []string{}
	for _, h := range servers {
		ids = append(ids, h.ID)
	}
	c.JSON(200, gin.H{"nodes": s.Monitor.Views(ids, time.Now()), "stale_after_seconds": 90, "history_limit": monitor.MaxSamples})
}
func (s *Server) probeToken(c *gin.Context) {
	if _, err := s.Store.GetServer(c.Request.Context(), c.Param("id")); mapStore(c, err) {
		return
	}
	token, err := s.Monitor.Issue(c.Param("id"))
	if err != nil {
		fail(c, 500, "无法保存探针凭据")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"token": token, "server_id": c.Param("id")})
}
func (s *Server) probeReport(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	var req struct {
		ServerID string         `json:"server_id"`
		Sample   monitor.Sample `json:"sample"`
	}
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "指标格式无效")
		return
	}
	header := c.GetHeader("Authorization")
	if !strings.HasPrefix(header, "Bearer ") || !s.Monitor.Authorized(req.ServerID, strings.TrimPrefix(header, "Bearer ")) {
		fail(c, 401, "探针凭据无效")
		return
	}
	if _, err := s.Store.GetServer(c.Request.Context(), req.ServerID); err != nil {
		fail(c, 404, "服务器不存在")
		return
	}
	if err := s.Monitor.Report(req.ServerID, req.Sample, time.Now()); err != nil {
		if errors.Is(err, monitor.ErrRateLimit) {
			fail(c, 429, err.Error())
		} else {
			fail(c, 400, "指标无效或保存失败")
		}
		return
	}
	c.Status(204)
}
func (s *Server) probeScript(c *gin.Context) {
	c.Header("Content-Disposition", `attachment; filename="onefrog-probe.py"`)
	c.Data(200, "text/x-python; charset=utf-8", []byte(monitor.AgentScript))
}
