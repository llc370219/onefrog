package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"onefrog/internal/bot"
	"onefrog/internal/model"
	"onefrog/internal/module"
	"onefrog/internal/preferences"
	"onefrog/internal/remote"
	"onefrog/internal/store"
)

type serverReq struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	AuthType string `json:"auth_type"`
	Secret   string `json:"secret"`
	Role     string `json:"role"`
	Status   string `json:"status"`
	Disk     *int   `json:"disk_percent"`
	Load     string `json:"load"`
}

func (s *Server) listServers(c *gin.Context) {
	items, err := s.Store.ListServers(c.Request.Context())
	if mapStore(c, err) {
		return
	}
	if items == nil {
		items = []model.Server{}
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) createServer(c *gin.Context) {
	var req serverReq
	if err := c.BindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Host) == "" {
		fail(c, http.StatusBadRequest, "名称和地址必填")
		return
	}
	rec, err := s.serverRec(req, store.ServerRec{})
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	item, err := s.Store.CreateServer(c.Request.Context(), rec)
	if mapStore(c, err) {
		return
	}
	c.JSON(http.StatusCreated, item)
}

func (s *Server) updateServer(c *gin.Context) {
	var req serverReq
	if err := c.BindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	old, err := s.Store.GetServer(c.Request.Context(), c.Param("id"))
	if mapStore(c, err) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = old.Name
	}
	if strings.TrimSpace(req.Host) == "" {
		req.Host = old.Host
	}
	if req.Port == 0 {
		req.Port = old.Port
	}
	if req.Username == "" {
		req.Username = old.Username
	}
	if req.AuthType == "" {
		req.AuthType = old.AuthType
	}
	if req.Role == "" {
		req.Role = old.Role
	}
	if req.Status == "" {
		req.Status = old.Status
	}
	if req.Disk == nil {
		req.Disk = old.DiskPercent
	}
	if req.Load == "" {
		req.Load = old.Load
	}
	rec, err := s.serverRec(req, old)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	rec.ID = old.ID
	rec.CreatedAt = old.CreatedAt
	rec.HostKey = old.HostKey
	if rec.Host != old.Host || effectivePort(rec.Port) != effectivePort(old.Port) {
		// A different endpoint is a different host: its key must be confirmed again.
		rec.HostKey, rec.ClearHostKey = "", true
	}
	item, err := s.Store.UpdateServer(c.Request.Context(), rec)
	if mapStore(c, err) {
		return
	}
	c.JSON(http.StatusOK, item)
}

func (s *Server) serverRec(req serverReq, old store.ServerRec) (store.ServerRec, error) {
	auth := req.AuthType
	if auth == "" {
		auth = "password"
	}
	if auth != "password" && auth != "private_key" {
		return store.ServerRec{}, errors.New("登录方式只能是 password 或 private_key")
	}
	enc := old.EncryptedKey
	if req.Secret != "" {
		var err error
		enc, err = s.Box.Encrypt(req.Secret)
		if err != nil {
			return store.ServerRec{}, err
		}
	}
	status := req.Status
	if status == "" {
		status = "unknown"
	}
	return store.ServerRec{Server: model.Server{Name: strings.TrimSpace(req.Name), Host: strings.TrimSpace(req.Host), Port: req.Port, Username: req.Username, AuthType: auth, Status: status, Role: req.Role, DiskPercent: req.Disk, Load: req.Load}, EncryptedKey: enc, HostKey: old.HostKey}, nil
}

func (s *Server) deleteServer(c *gin.Context) {
	if mapStore(c, s.Store.DeleteServer(c.Request.Context(), c.Param("id"))) {
		return
	}
	c.Status(http.StatusNoContent)
}

func effectivePort(p int) int {
	if p == 0 {
		return 22
	}
	return p
}

// testServer checks SSH connectivity. The host key is never trusted silently:
//   - no stored key and no "fingerprint" in the body: report the presented
//     fingerprint (status host_key_unknown) without sending credentials;
//   - no stored key and a confirmed "fingerprint": connect pinned to it and
//     store it only after the connection succeeds;
//   - stored key mismatch: fail (host_key_mismatch); the stored key is kept.
func (s *Server) testServer(c *gin.Context) {
	var req struct {
		Fingerprint string `json:"fingerprint"`
	}
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, "请求格式不对")
			return
		}
	}
	ctx := c.Request.Context()
	rec, err := s.Store.GetServer(ctx, c.Param("id"))
	if mapStore(c, err) {
		return
	}
	if s.Agent == nil || s.Agent.SSH == nil {
		fail(c, http.StatusBadRequest, "没有 SSH 执行器")
		return
	}
	plain := ""
	if rec.EncryptedKey != "" {
		plain, err = s.Box.Decrypt(rec.EncryptedKey)
		if err != nil {
			fail(c, http.StatusBadRequest, "登录密钥无法解密")
			return
		}
	}
	known := rec.HostKey
	confirming := false
	if known == "" && strings.TrimSpace(req.Fingerprint) != "" {
		known, confirming = strings.TrimSpace(req.Fingerprint), true
	}
	out, fp, err := s.Agent.SSH.Run(ctx, rec.Host, rec.Port, rec.Username, rec.AuthType, plain, known, "echo ok")
	switch {
	case errors.Is(err, remote.ErrHostKeyUnknown):
		c.JSON(http.StatusOK, gin.H{"ok": false, "status": "host_key_unknown", "fingerprint": fp, "detail": "首次连接：请核对服务器的 SSH 主机指纹后确认"})
		return
	case errors.Is(err, remote.ErrHostKeyMismatch):
		if confirming {
			c.JSON(http.StatusOK, gin.H{"ok": false, "status": "host_key_unknown", "fingerprint": fp, "detail": "服务器指纹在确认期间发生了变化，请重新核对"})
			return
		}
		rec.Status = "offline"
		_, _ = s.Store.UpdateServer(ctx, rec)
		c.JSON(http.StatusOK, gin.H{"ok": false, "status": "host_key_mismatch", "fingerprint": fp, "stored_fingerprint": rec.HostKey, "detail": err.Error()})
		return
	case err != nil:
		rec.Status = "offline"
		_, _ = s.Store.UpdateServer(ctx, rec)
		c.JSON(http.StatusOK, gin.H{"ok": false, "status": "offline", "detail": err.Error()})
		return
	}
	if confirming {
		rec.HostKey = known
		s.recordHostKey(c, rec, "host_key_confirmed", "已确认 SSH 主机指纹 "+known)
	}
	rec.Status = "online"
	_, _ = s.Store.UpdateServer(ctx, rec)
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": "online", "fingerprint": rec.HostKey, "detail": out})
}

// resetHostKey is the explicit admin action to forget a server's stored host
// key, e.g. after the server was reinstalled. The next connection must be
// confirmed again.
func (s *Server) resetHostKey(c *gin.Context) {
	ctx := c.Request.Context()
	rec, err := s.Store.GetServer(ctx, c.Param("id"))
	if mapStore(c, err) {
		return
	}
	old := rec.HostKey
	rec.HostKey, rec.ClearHostKey = "", true
	rec.Status = "unknown"
	if _, err := s.Store.UpdateServer(ctx, rec); mapStore(c, err) {
		return
	}
	s.recordHostKey(c, rec, "host_key_reset", "已重置 SSH 主机指纹（原指纹 "+old+"）")
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": "host_key_reset"})
}

func (s *Server) recordHostKey(c *gin.Context, rec store.ServerRec, status, output string) {
	actor := ""
	if u, ok := c.Get("user"); ok {
		if user, ok := u.(model.User); ok {
			actor = user.Username
		}
	}
	_, _ = s.Store.AddHistory(c.Request.Context(), store.HistoryRec{History: model.History{ServerID: rec.ID, ServerName: rec.Name, BotName: actor, Action: output, Command: "ssh host key", Status: status}})
}

type botReq struct {
	ModelRoute     *preferences.Route `json:"model_route"`
	Name           string             `json:"name"`
	Description    string             `json:"description"`
	ModelProvider  string             `json:"model_provider"`
	ModelName      string             `json:"model_name"`
	SystemPrompt   string             `json:"system_prompt"`
	BaseURL        string             `json:"base_url"`
	APIKeySecretID string             `json:"api_key_secret_id"`
}

func (s *Server) listBots(c *gin.Context) {
	items, err := s.Store.ListBots(c.Request.Context())
	if mapStore(c, err) {
		return
	}
	if items == nil {
		items = []model.Bot{}
	}
	views := make([]botView, 0, len(items))
	state := s.Settings.Snapshot()
	for _, item := range items {
		route := s.Settings.RouteFor(item, false)
		if route.Model == "" {
			route.Model = item.ModelName
		}
		views = append(views, botView{Bot: item, Enabled: s.Settings.BotEnabled(item.ID), ModelRoute: state.BotRoutes[item.ID], EffectiveRoute: route, Usage: s.Usage.ForBot(item.ID)})
	}
	c.JSON(http.StatusOK, views)
}

func (s *Server) createBot(c *gin.Context) {
	var req botReq
	if err := c.BindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		fail(c, http.StatusBadRequest, "名称必填")
		return
	}
	item, err := s.Store.CreateBot(c.Request.Context(), model.Bot{Name: strings.TrimSpace(req.Name), Description: req.Description, ModelProvider: req.ModelProvider, ModelName: req.ModelName, SystemPrompt: req.SystemPrompt, BaseURL: req.BaseURL, APIKeySecretID: req.APIKeySecretID})
	if mapStore(c, err) {
		return
	}
	if req.ModelRoute != nil {
		if err := s.Settings.BindBot(item.ID, *req.ModelRoute); err != nil {
			if cleanup := s.Store.DeleteBot(c.Request.Context(), item.ID); cleanup != nil {
				fail(c, 500, "模型绑定失败，清理 Bot 失败，请刷新检查")
				return
			}
			fail(c, 400, err.Error())
			return
		}
	}
	c.JSON(http.StatusCreated, item)
}

func (s *Server) updateBot(c *gin.Context) {
	old, err := s.Store.GetBot(c.Request.Context(), c.Param("id"))
	if mapStore(c, err) {
		return
	}
	var req struct {
		ModelProvider  *string            `json:"model_provider"`
		ModelName      *string            `json:"model_name"`
		BaseURL        *string            `json:"base_url"`
		APIKeySecretID *string            `json:"api_key_secret_id"`
		Name           *string            `json:"name"`
		Description    *string            `json:"description"`
		SystemPrompt   *string            `json:"system_prompt"`
		ModelRoute     *preferences.Route `json:"model_route"`
	}
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求无效")
		return
	}
	next := old
	if req.ModelProvider != nil {
		next.ModelProvider = *req.ModelProvider
	}
	if req.ModelName != nil {
		next.ModelName = *req.ModelName
	}
	if req.BaseURL != nil {
		next.BaseURL = *req.BaseURL
	}
	if req.APIKeySecretID != nil {
		next.APIKeySecretID = *req.APIKeySecretID
	}
	if req.Name != nil {
		next.Name = strings.TrimSpace(*req.Name)
		if next.Name == "" || len([]rune(next.Name)) > 60 {
			fail(c, 400, "Bot 名称需要 1–60 字")
			return
		}
	}
	if req.Description != nil {
		next.Description = *req.Description
	}
	if req.SystemPrompt != nil {
		next.SystemPrompt = *req.SystemPrompt
	}
	previous := s.Settings.Snapshot().BotRoutes[old.ID]
	changeRoute := req.ModelRoute != nil && *req.ModelRoute != previous
	if changeRoute {
		if err = s.Settings.ValidateSelectable(*req.ModelRoute); err != nil {
			fail(c, 400, err.Error())
			return
		}
	}
	saved, err := s.Store.UpdateBot(c.Request.Context(), next)
	if mapStore(c, err) {
		return
	}
	if changeRoute {
		if err = s.Settings.BindBot(old.ID, *req.ModelRoute); err != nil {
			_, rollbackErr := s.Store.UpdateBot(c.Request.Context(), old)
			if rollbackErr != nil {
				fail(c, 500, "模型保存失败，请刷新检查 Bot 信息")
				return
			}
			fail(c, 400, err.Error())
			return
		}
	}
	c.JSON(200, saved)
}

func (s *Server) deleteBot(c *gin.Context) {
	if mapStore(c, s.Store.DeleteBot(c.Request.Context(), c.Param("id"))) {
		return
	}
	c.Status(http.StatusNoContent)
}

type secretReq struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description"`
}

func (s *Server) listSecrets(c *gin.Context) {
	items, err := s.Store.ListSecrets(c.Request.Context())
	if mapStore(c, err) {
		return
	}
	if items == nil {
		items = []model.Secret{}
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) createSecret(c *gin.Context) {
	var req secretReq
	if err := c.BindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" || req.Value == "" {
		fail(c, http.StatusBadRequest, "名称和内容必填")
		return
	}
	enc, err := s.Box.Encrypt(req.Value)
	if err != nil {
		fail(c, http.StatusInternalServerError, "加密失败")
		return
	}
	item, err := s.Store.CreateSecret(c.Request.Context(), store.SecretRec{Secret: model.Secret{Name: strings.TrimSpace(req.Name), Description: req.Description}, EncryptedValue: enc})
	if mapStore(c, err) {
		return
	}
	c.JSON(http.StatusCreated, item)
}

func (s *Server) deleteSecret(c *gin.Context) {
	if mapStore(c, s.Store.DeleteSecret(c.Request.Context(), c.Param("id"))) {
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) listModules(c *gin.Context) {
	items, err := s.Store.ListModules(c.Request.Context())
	if mapStore(c, err) {
		return
	}
	if items == nil {
		items = []model.Module{}
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) installModule(c *gin.Context) {
	var req struct {
		ID string `json:"id"`
	}
	if err := c.BindJSON(&req); err != nil || req.ID == "" {
		fail(c, http.StatusBadRequest, "要指定模块")
		return
	}
	item, ok := module.Find(req.ID)
	if !ok {
		fail(c, http.StatusNotFound, "没有这个模块")
		return
	}
	if old, err := s.Store.GetModule(c.Request.Context(), item.ID); err == nil {
		c.JSON(http.StatusOK, old)
		return
	}
	saved, err := s.Store.SaveModule(c.Request.Context(), item)
	if mapStore(c, err) {
		return
	}
	c.JSON(http.StatusCreated, saved)
}

func (s *Server) enableModule(c *gin.Context) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.BindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	item, err := s.Store.SetModuleEnabled(c.Request.Context(), c.Param("id"), req.Enabled)
	if mapStore(c, err) {
		return
	}
	c.JSON(http.StatusOK, item)
}

func (s *Server) listHistory(c *gin.Context) {
	items, err := s.Store.ListHistory(c.Request.Context(), 100)
	if mapStore(c, err) {
		return
	}
	if items == nil {
		items = []model.History{}
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) rollback(c *gin.Context) {
	rec, err := s.Store.GetHistory(c.Request.Context(), c.Param("id"))
	if mapStore(c, err) {
		return
	}
	if !rec.CanRollback {
		fail(c, http.StatusConflict, "这条不能撤回")
		return
	}
	if err := s.Store.FinishRollback(c.Request.Context(), rec.ID); mapStore(c, err) {
		return
	}
	_, _ = s.Store.AddHistory(c.Request.Context(), store.HistoryRec{History: model.History{BotID: rec.BotID, ServerID: rec.ServerID, Action: "撤回 " + rec.Action, Command: rec.Command, Status: "rolled_back"}})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type chatReq struct {
	bot.Options
	BotID     string `json:"bot_id"`
	ServerID  string `json:"server_id"`
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
}

func (s *Server) chat(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2*1024*1024)
	var req chatReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	req.OwnerID = c.MustGet("user").(model.User).ID
	req.RestrictServers = true
	result, err := s.Agent.ReplyWithOptions(c.Request.Context(), req.BotID, req.ServerID, req.SessionID, req.Content, req.Options, nil)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) messages(c *gin.Context) {
	items, err := s.Store.ListMessages(c.Request.Context(), c.Param("id"))
	if mapStore(c, err) {
		return
	}
	if items == nil {
		items = []model.Message{}
	}
	c.JSON(http.StatusOK, items)
}

func stepEvent(step bot.Step) gin.H {
	switch step.Kind {
	case "session":
		return gin.H{"type": "session", "session_id": step.Output}
	case "delegate":
		return gin.H{"type": "delegate", "bot": step.Bot, "target": step.Target, "task": step.Task}
	default:
		return gin.H{"type": "tool", "bot": step.Bot, "name": step.Name, "command": step.Command, "server_id": step.ServerID, "ok": step.OK, "output": step.Output}
	}
}
