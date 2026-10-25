package api

import (
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"onefrog/internal/model"
	"onefrog/internal/onboarding"
	"onefrog/internal/store"
)

func (s *Server) onboardingDefaults(c *gin.Context) {
	c.JSON(200, gin.H{"report_base": s.PublicURL, "interval": 15, "configure": true})
}
func (s *Server) startOnboarding(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 384*1024)
	var r onboarding.Request
	if c.ShouldBindJSON(&r) != nil {
		fail(c, 400, "接入信息格式无效或私钥过大")
		return
	}
	v, e := s.Onboarding.Start(c.MustGet("user").(model.User).ID, r)
	if e != nil {
		fail(c, 400, e.Error())
		return
	}
	c.JSON(202, v)
}
func (s *Server) onboardingStatus(c *gin.Context) {
	v, e := s.Onboarding.Get(c.MustGet("user").(model.User).ID, c.Param("id"))
	if mapStore(c, e) {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, v)
}
func (s *Server) confirmOnboarding(c *gin.Context) {
	var r struct {
		Fingerprint string `json:"fingerprint"`
	}
	if c.ShouldBindJSON(&r) != nil || r.Fingerprint == "" {
		fail(c, 400, "请提供要确认的主机指纹")
		return
	}
	v, e := s.Onboarding.Confirm(c.MustGet("user").(model.User).ID, c.Param("id"), r.Fingerprint)
	if errors.Is(e, store.ErrNotFound) {
		fail(c, 404, "不存在")
		return
	}
	if e != nil {
		fail(c, 400, e.Error())
		return
	}
	c.JSON(200, v)
}
