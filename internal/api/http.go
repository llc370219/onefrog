package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"onefrog/internal/asset"
	"onefrog/internal/attachment"
	"onefrog/internal/bot"
	"onefrog/internal/devmodule"
	"onefrog/internal/id"
	"onefrog/internal/model"
	"onefrog/internal/monitor"
	"onefrog/internal/onboarding"
	"onefrog/internal/preferences"
	"onefrog/internal/providerlogin"
	"onefrog/internal/secret"
	"onefrog/internal/services"
	"onefrog/internal/store"
	"onefrog/internal/update"
	"onefrog/internal/usage"
	"onefrog/internal/version"
)

type Server struct {
	Services   *services.Scanner
	OAuth      *providerlogin.Manager
	Onboarding *onboarding.Manager
	PublicURL  string
	Files      *attachment.Manager
	Usage      *usage.Manager
	Store      store.Store
	Box        *secret.Box
	Agent      *bot.Agent
	Secure     bool
	WebDir     string
	Settings   *preferences.Manager
	Monitor    *monitor.Manager
	Assets     *asset.Ledger
	DevModules *devmodule.Book
	// Updates checks GitHub for newer releases; nil reports a dev build.
	Updates *update.Checker
	// TrustedProxies is OF_TRUSTED_PROXIES (comma separated CIDRs/IPs, or "none").
	TrustedProxies string
	limiter        *loginLimiter
}

func (s *Server) Handler() http.Handler {
	if s.Settings == nil {
		s.Settings, _ = preferences.New("", s.Box)
	}
	if s.Monitor == nil {
		s.Monitor, _ = monitor.New("")
	}
	if s.Files == nil {
		s.Files = attachment.New("", s.Box)
	}
	if s.Usage == nil {
		s.Usage, _ = usage.New("", s.Box)
	}
	if s.Updates == nil {
		s.Updates = &update.Checker{Current: version.Version, EnvDisabled: true}
	}
	if s.Assets == nil {
		s.Assets, _ = asset.New("", s.Box)
	}
	if s.DevModules == nil {
		s.DevModules, _ = devmodule.New("", s.Box)
	}
	if s.Agent != nil {
		s.Agent.Settings = s.Settings
		s.Agent.Files = s.Files
		s.Agent.Usage = s.Usage
		s.Agent.Assets = s.Assets
		s.Agent.DevModules = s.DevModules
	}
	if s.OAuth == nil {
		s.OAuth, _ = providerlogin.New("", s.Box)
	}
	if s.Agent != nil {
		s.Agent.OAuth = s.OAuth
	}
	if s.Onboarding == nil {
		var executor onboarding.Executor
		if s.Agent != nil {
			executor = s.Agent.SSH
		}
		s.Onboarding, _ = onboarding.New("", s.Store, s.Box, s.Monitor, executor)
	}
	if s.Services == nil {
		var executor services.Executor
		var sanitize func(context.Context, string) string
		if s.Agent != nil {
			executor = s.Agent.SSH
			sanitize = s.Agent.RedactServiceOutput
		}
		s.Services = services.New(s.Store, s.Box, executor, sanitize)
	}
	if s.Agent != nil {
		s.Agent.Services = s.Services
	}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	_ = r.SetTrustedProxies(trustedProxies(s.TrustedProxies))
	r.Use(gin.Recovery(), securityHeaders)
	if s.limiter == nil {
		s.limiter = newLoginLimiter()
	}
	api := r.Group("/api")
	api.Use(limitBody, requireCSRF)
	api.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	api.POST("/probe/report", s.probeReport)
	api.GET("/probe/agent.py", s.probeScript)
	api.POST("/auth/login", s.login)
	api.POST("/auth/logout", s.logout)
	authed := api.Group("")
	authed.Use(s.requireAuth)
	authed.Use(s.requireFreshCredentials)
	authed.GET("/auth/me", s.me)
	authed.PUT("/auth/profile", s.updateProfile)
	authed.GET("/monitor", s.monitorView)
	authed.GET("/services", s.serviceViews)
	authed.POST("/servers/:id/services/scan", s.scanServices)
	authed.POST("/servers/:id/probe/token", s.probeToken)
	authed.GET("/servers", s.listServers)
	authed.GET("/servers/onboarding/defaults", s.onboardingDefaults)
	authed.POST("/servers/onboarding", s.startOnboarding)
	authed.GET("/servers/onboarding/:id", s.onboardingStatus)
	authed.POST("/servers/onboarding/:id/confirm", s.confirmOnboarding)
	authed.POST("/servers", s.createServer)
	authed.PUT("/servers/:id", s.updateServer)
	authed.DELETE("/servers/:id", s.deleteServer)
	authed.POST("/servers/:id/test", s.testServer)
	authed.POST("/servers/:id/host-key/reset", s.resetHostKey)
	authed.GET("/bots", s.listBots)
	authed.POST("/attachments", s.uploadAttachment)
	authed.GET("/attachments/:id/content", s.attachmentContent)
	authed.GET("/usage", s.usageView)
	authed.PUT("/bots/:id/enabled", s.botEnabled)
	authed.POST("/bots", s.createBot)
	authed.PUT("/bots/:id", s.updateBot)
	authed.DELETE("/bots/:id", s.deleteBot)
	authed.GET("/bots/:id/memories", s.listBotMemories)
	authed.POST("/bots/:id/memories", s.createBotMemory)
	authed.DELETE("/bots/:id/memories/:memoryID", s.deleteBotMemory)
	authed.GET("/settings", s.getSettings)
	authed.GET("/oauth", s.oauthView)
	authed.POST("/oauth/start", s.oauthStart)
	authed.GET("/oauth/login/:id", s.oauthStatus)
	authed.POST("/oauth/login/:id/callback", s.oauthCallback)
	authed.POST("/oauth/login/:id/cancel", s.oauthCancel)
	authed.POST("/oauth/:kind/disconnect", s.oauthDisconnect)
	authed.PUT("/settings", s.putSettings)
	authed.GET("/settings/approval", s.getApproval)
	authed.PUT("/settings/approval", s.putApproval)
	authed.GET("/update", s.updateStatus)
	authed.POST("/update/check", s.updateCheck)
	authed.POST("/update/apply", s.updateApply)
	authed.PUT("/settings/update", s.putUpdateSetting)
	authed.POST("/settings/providers/models", s.providerModels)
	authed.POST("/settings/providers/test", s.testProvider)
	authed.GET("/approvals", s.listApprovals)
	authed.GET("/secrets", s.listSecrets)
	authed.POST("/secrets", s.createSecret)
	authed.PUT("/secrets/:id", s.updateSecret)
	authed.DELETE("/secrets/:id", s.deleteSecret)
	authed.GET("/modules", s.listModules)
	authed.POST("/modules/install", s.installModule)
	authed.POST("/modules/:id/enable", s.enableModule)
	authed.GET("/assets", s.listAssets)
	authed.POST("/assets", s.createAsset)
	authed.PUT("/assets/:id", s.updateAsset)
	authed.DELETE("/assets/:id", s.deleteAsset)
	authed.GET("/dev/modules", s.listDevModules)
	authed.POST("/dev/modules", s.createDevModule)
	authed.GET("/dev/modules/:id", s.getDevModule)
	authed.PUT("/dev/modules/:id", s.updateDevModule)
	authed.DELETE("/dev/modules/:id", s.deleteDevModule)
	authed.POST("/dev/modules/:id/data", s.devModuleData)
	authed.GET("/dev/modules/:id/frame", s.devModuleFrame)
	authed.GET("/history", s.listHistory)
	authed.POST("/history/:id/rollback", s.rollback)
	authed.POST("/chat", s.chat)
	authed.GET("/sessions", s.listSessions)
	authed.DELETE("/sessions/:id", s.deleteChatSession)
	authed.GET("/sessions/:id/messages", s.messages)
	r.GET("/ws/chat", s.requireAuth, s.requireFreshCredentials, s.wsChat)
	if s.WebDir != "" {
		if info, err := os.Stat(s.WebDir); err == nil && info.IsDir() {
			r.Static("/assets", s.WebDir+"/assets")
			for _, name := range rootFiles {
				if _, err := os.Stat(filepath.Join(s.WebDir, name)); err == nil {
					r.StaticFile("/"+name, filepath.Join(s.WebDir, name))
				}
			}
			index := s.WebDir + "/index.html"
			r.NoRoute(func(c *gin.Context) {
				if strings.HasPrefix(c.Request.URL.Path, "/api") || strings.HasPrefix(c.Request.URL.Path, "/ws") {
					c.Status(http.StatusNotFound)
					return
				}
				c.File(index)
			})
		}
	}
	return r
}

func (s *Server) requireAuth(c *gin.Context) {
	cookie, err := c.Cookie("of_session")
	if err != nil || cookie == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	user, err := s.Store.UserBySession(c.Request.Context(), hashToken(cookie))
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	c.Set("user", user)
	c.Next()
}

func (s *Server) requireFreshCredentials(c *gin.Context) {
	user, _ := c.Get("user")
	account, ok := user.(model.User)
	if !ok || !account.MustChangeCredentials {
		c.Next()
		return
	}
	switch c.Request.URL.Path {
	case "/api/auth/me", "/api/auth/profile", "/api/auth/logout":
		c.Next()
		return
	}
	fail(c, http.StatusForbidden, "请先修改用户名和密码")
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) login(c *gin.Context) {
	var req loginReq
	if err := c.BindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	ip := c.ClientIP()
	if wait := s.limiter.blocked(ip); wait > 0 {
		c.Header("Retry-After", retryAfter(wait))
		fail(c, http.StatusTooManyRequests, "登录失败次数过多，请稍后再试")
		return
	}
	user, hash, err := s.Store.UserByName(c.Request.Context(), strings.TrimSpace(req.Username))
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		s.limiter.fail(ip)
		fail(c, http.StatusUnauthorized, "账号或密码不对")
		return
	}
	s.limiter.succeed(ip)
	token := id.New() + id.New()
	if err := s.Store.CreateSession(c.Request.Context(), user.ID, hashToken(token), time.Now().UTC().Add(7*24*time.Hour)); err != nil {
		fail(c, http.StatusInternalServerError, "会话创建失败")
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: "of_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.Secure, Expires: time.Now().Add(7 * 24 * time.Hour)})
	c.JSON(http.StatusOK, s.profileJSON(user))
}

func (s *Server) logout(c *gin.Context) {
	if cookie, err := c.Cookie("of_session"); err == nil {
		_ = s.Store.DeleteSession(c.Request.Context(), hashToken(cookie))
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: "of_session", Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(0, 0), HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.Secure})
	c.Status(http.StatusNoContent)
}

func (s *Server) me(c *gin.Context) {
	user, _ := c.Get("user")
	c.JSON(http.StatusOK, s.profileJSON(user))
}

// profileJSON is the user plus approval_choice_required, which tells the web
// UI to ask the operator to pick an approval mode once.
func (s *Server) profileJSON(user any) map[string]any {
	out := map[string]any{}
	raw, _ := json.Marshal(user)
	_ = json.Unmarshal(raw, &out)
	out["approval_choice_required"] = s.Settings.Snapshot().Approval.Mode == preferences.ModeUnset
	return out
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func fail(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(code, gin.H{"error": msg})
}

func mapStore(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "不存在")
		return true
	}
	if errors.Is(err, store.ErrConflict) {
		fail(c, http.StatusConflict, "名称已存在")
		return true
	}
	if errors.Is(err, store.ErrLimit) {
		fail(c, http.StatusConflict, err.Error())
		return true
	}
	fail(c, http.StatusInternalServerError, "服务器错误")
	return true
}
