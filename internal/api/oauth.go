package api

import (
	"github.com/gin-gonic/gin"
	"onefrog/internal/model"
	"onefrog/internal/providerlogin"
	"onefrog/internal/store"
	"strings"
	"time"
)

func (s *Server) oauthView(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	owner := c.MustGet("user").(model.User).ID
	c.JSON(200, gin.H{"definitions": providerlogin.Definitions(), "accounts": s.OAuth.Accounts(owner), "consent_version": providerlogin.ConsentVersion, "acknowledgements": s.OAuth.Acknowledgements(owner)})
}

// oauthStart refuses unless the request carries the explicit risk
// acknowledgement (checkbox + confirm in the UI) for the current notice. The
// acknowledgement is stored per provider in oauth.enc and in history.
func (s *Server) oauthStart(c *gin.Context) {
	var req struct {
		Kind         string `json:"kind"`
		Consent      string `json:"consent_version"`
		Acknowledged bool   `json:"risk_acknowledged"`
	}
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求无效")
		return
	}
	user := c.MustGet("user").(model.User)
	if !req.Acknowledged || req.Consent != providerlogin.ConsentVersion {
		fail(c, 400, "请先阅读第三方登录风险说明，勾选“我已了解风险并自行承担后果”后再继续")
		return
	}
	ack, e := s.OAuth.Acknowledge(user.ID, req.Kind)
	if e != nil {
		fail(c, 400, e.Error())
		return
	}
	_, _ = s.Store.AddHistory(c.Request.Context(), store.HistoryRec{History: model.History{BotName: user.Username, Command: "oauth login " + req.Kind, Action: "已确认第三方登录风险说明 " + ack.Version + " @ " + ack.At.Format(time.RFC3339), Status: "risk_acknowledged"}})
	login, e := s.OAuth.Start(user.ID, req.Kind, req.Consent)
	if e != nil {
		fail(c, 400, e.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, login)
}
func (s *Server) oauthStatus(c *gin.Context) {
	v, e := s.OAuth.Status(c.MustGet("user").(model.User).ID, c.Param("id"))
	if e != nil {
		fail(c, 404, "登录请求不存在")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, v)
}
func (s *Server) oauthCallback(c *gin.Context) {
	var req struct {
		URL string `json:"url"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.URL) > 16384 {
		fail(c, 400, "回调地址无效")
		return
	}
	if e := s.OAuth.Complete(c.MustGet("user").(model.User).ID, c.Param("id"), req.URL); e != nil {
		fail(c, 400, e.Error())
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) oauthCancel(c *gin.Context) {
	if e := s.OAuth.Cancel(c.MustGet("user").(model.User).ID, c.Param("id")); e != nil {
		fail(c, 404, "登录请求不存在")
		return
	}
	c.Status(204)
}
func (s *Server) oauthDisconnect(c *gin.Context) {
	if _, e := providerlogin.DefinitionFor(c.Param("kind")); e != nil {
		fail(c, 400, e.Error())
		return
	}
	if e := s.OAuth.Disconnect(c.MustGet("user").(model.User).ID, c.Param("kind")); e != nil {
		fail(c, 500, "解除本机连接失败")
		return
	}
	c.Status(204)
}
func (s *Server) validOAuthProbe(c *gin.Context, req providerProbe) bool {
	d, e := providerlogin.DefinitionFor(req.AuthKind)
	if e != nil || strings.TrimRight(req.BaseURL, "/") != d.BaseURL {
		fail(c, 400, "OAuth 接口地址不可修改")
		return false
	}
	return true
}
