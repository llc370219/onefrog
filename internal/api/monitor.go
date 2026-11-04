package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"onefrog/internal/monitor"
	"onefrog/internal/onboarding"
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
	// Optional body: {"province": "广东", "targets": ["1.1.1.1"]}. The response
	// carries the ping_targets to put in the node's config (三网 first).
	var req struct {
		Province string   `json:"province"`
		Targets  []string `json:"targets"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		fail(c, 400, "请求无效")
		return
	}
	province := strings.TrimSpace(req.Province)
	if province != "" {
		if _, ok := monitor.LookupProvince(province); !ok {
			fail(c, 400, "不认识的省份")
			return
		}
	}
	targets := onboarding.ProbeTargets(province, req.Targets)
	if len(targets) > 8 {
		fail(c, 400, "最多 8 个检测目标（三网占 3 个）")
		return
	}
	token, err := s.Monitor.Issue(c.Param("id"))
	if err != nil {
		fail(c, 500, "无法保存探针凭据")
		return
	}
	if err := s.Monitor.SetProvince(c.Param("id"), province); err != nil {
		fail(c, 500, "无法保存测速省份")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"token": token, "server_id": c.Param("id"), "ping_targets": targets})
}
func (s *Server) probeProvince(c *gin.Context) {
	var req struct {
		Province *string `json:"province"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	if c.ShouldBindJSON(&req) != nil || req.Province == nil {
		fail(c, 400, "请选择测速省份，或选择不测三网")
		return
	}
	if err := s.Onboarding.ConfigureProvince(c.Request.Context(), c.Param("id"), strings.TrimSpace(*req.Province)); err != nil {
		fail(c, 400, err.Error())
		return
	}
	c.JSON(200, gin.H{"message": "测速配置已应用，等待探针下一次上报"})
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
