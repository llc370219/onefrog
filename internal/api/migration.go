package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"onefrog/internal/devmodule"
	"onefrog/internal/maintenance"
	"onefrog/internal/migration"
	"onefrog/internal/modulebuilder"
	"onefrog/internal/store"
	"onefrog/internal/version"
)

type Migration struct {
	Root, Dir, Driver, PublicURL string
	Key                          []byte
	Gate                         *maintenance.Gate
	Restart                      func()
	mu                           sync.Mutex
	busy                         bool
	phase                        string
	message                      string
	restartRequired              bool
	code                         string
	expires                      time.Time
}

func (m *Migration) begin(phase string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy || m.restartRequired {
		return false
	}
	m.busy = true
	m.phase = phase
	m.message = ""
	return true
}
func (m *Migration) finish(phase, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.busy = false
	m.phase = phase
	m.message = msg
}

func (s *Server) migrationReady(c *gin.Context) bool {
	if s.Migration == nil {
		fail(c, 503, "当前部署未启用备份与迁移")
		return false
	}
	return true
}

func (s *Server) migrationStatus(c *gin.Context) {
	if !s.migrationReady(c) {
		return
	}
	m := s.Migration
	m.mu.Lock()
	defer m.mu.Unlock()
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"phase": m.phase, "message": m.message, "busy": m.busy, "paused": m.Gate.Paused(), "restart_required": m.restartRequired, "receiving": m.code != "" && time.Now().Before(m.expires), "expires_at": m.expires, "driver": m.Driver, "version": version.Version, "max_bytes": migration.MaxBytes, "public_url": m.PublicURL})
}

func (s *Server) migrationResume(c *gin.Context) {
	if !s.migrationReady(c) {
		return
	}
	m := s.Migration
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if c.ShouldBindJSON(&req) != nil || !req.Confirm {
		fail(c, 400, "请确认另一台服务器已暂停，再恢复运行")
		return
	}
	if !m.begin("resuming") {
		fail(c, 409, "操作正在进行或等待重启")
		return
	}
	if err := m.Gate.Resume(); err != nil {
		m.finish("failed", "恢复运行失败")
		fail(c, 500, "恢复运行失败")
		return
	}
	m.mu.Lock()
	m.code = ""
	m.mu.Unlock()
	m.finish("idle", "工作台已恢复运行")
	c.JSON(200, gin.H{"ok": true})
}

func (s *Server) exportSnapshot(ctx context.Context, password string) ([]byte, error) {
	m := s.Migration
	files, err := migration.ReadFiles(m.Dir, m.Key)
	if err != nil {
		return nil, err
	}
	if pg, ok := s.Store.(*store.Postgres); ok {
		dump, err := pg.ExportDatabase(ctx)
		if err != nil {
			return nil, errors.New("数据库备份失败")
		}
		raw, err := json.Marshal(dump)
		if err != nil {
			return nil, err
		}
		files[migration.DatabaseName] = raw
	}
	return migration.Seal(files, migration.Manifest{Version: version.Version, Driver: m.Driver, PublicURL: m.PublicURL}, password)
}

func (s *Server) backupExport(c *gin.Context) {
	if !s.migrationReady(c) {
		return
	}
	m := s.Migration
	var req struct {
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.Password) < 12 || len(req.Password) > 256 {
		fail(c, 400, "请输入至少 12 位备份密码")
		return
	}
	if !m.begin("exporting") {
		fail(c, 409, "已有迁移操作正在进行")
		return
	}
	wasPaused := m.Gate.Paused()
	if err := m.Gate.Pause(); err != nil {
		m.finish("failed", err.Error())
		fail(c, 409, err.Error())
		return
	}
	defer func() {
		if !wasPaused {
			if err := m.Gate.Resume(); err != nil {
				m.finish("failed", "备份后恢复运行失败，请手动恢复")
			}
		}
	}()
	raw, err := s.exportSnapshot(c.Request.Context(), req.Password)
	if err != nil {
		m.finish("failed", err.Error())
		fail(c, 400, err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Disposition", `attachment; filename="onefrog-`+time.Now().UTC().Format("20060102-150405")+`.ofbackup"`)
	c.Data(200, "application/octet-stream", raw)
	m.finish("exported", "备份已生成，请保管备份密码")
}

// Fresh target check is re-run under the global pause at the moment of restore.
func (s *Server) freshTarget(ctx context.Context) error {
	m := s.Migration
	if _, err := os.Stat(filepath.Join(m.Dir, migration.ManifestName)); err == nil {
		return errors.New("此实例已恢复过数据，请使用全新安装的实例")
	}
	servers, err := s.Store.ListServers(ctx)
	if err != nil {
		return err
	}
	chats, err := s.Store.ListChatSessions(ctx)
	if err != nil {
		return err
	}
	secrets, err := s.Store.ListSecrets(ctx)
	if err != nil {
		return err
	}
	history, err := s.Store.ListHistory(ctx, 1)
	if err != nil {
		return err
	}
	if len(servers)+len(chats)+len(secrets)+len(history)+len(s.Settings.Snapshot().Providers)+len(s.Automations.List())+len(s.Assets.List())+len(s.DevModules.List()) != 0 {
		return errors.New("恢复仅支持全新实例；目标已有业务数据，不能覆盖")
	}
	bots, err := s.Store.ListBots(ctx)
	if err != nil {
		return err
	}
	for _, b := range bots {
		if b.ID != modulebuilder.BotID && b.ID != devmodule.BotID {
			return errors.New("目标已有 Bot，请使用全新实例")
		}
		mem, err := s.Store.ListBotMemories(ctx, b.ID)
		if err != nil {
			return err
		}
		if len(mem) > 0 {
			return errors.New("目标已有 Bot 记忆")
		}
	}
	entries, err := os.ReadDir(filepath.Join(m.Dir, "uploads"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(entries) > 0 {
		return errors.New("目标已有附件")
	}
	return nil
}

func (s *Server) restoreSnapshot(ctx context.Context, raw []byte, password string) error {
	m := s.Migration
	if err := s.freshTarget(ctx); err != nil {
		return err
	}
	files, manifest, err := migration.Open(raw, password)
	if err != nil {
		return err
	}
	if manifest.Version != version.Version {
		return errors.New("请将新机安装为与备份相同的 OneFrog 版本后恢复")
	}
	if manifest.Driver != m.Driver {
		return errors.New("新旧实例的存储类型必须一致")
	}
	if m.Driver == "memory" && files["accounts.json"] == nil {
		return errors.New("备份缺少账号数据")
	}
	if m.Driver == "postgres" && files[migration.DatabaseName] == nil {
		return errors.New("备份缺少数据库")
	}
	dir, err := migration.Stage(m.Root, files, manifest)
	if err != nil {
		return errors.New("无法写入恢复数据，请检查磁盘空间")
	}
	// Load through production decoders before changing the active generation.
	if err = s.validateRestore(ctx, dir, files); err != nil {
		os.RemoveAll(dir)
		return err
	}
	if err = migration.Activate(m.Root, dir); err != nil {
		return errors.New("切换恢复数据失败，原数据已保留")
	}
	m.mu.Lock()
	m.restartRequired = true
	m.code = ""
	m.mu.Unlock()
	return nil
}

func (s *Server) backupRestore(c *gin.Context) {
	if !s.migrationReady(c) {
		return
	}
	m := s.Migration
	if !m.begin("restoring") {
		fail(c, 409, "已有迁移操作正在进行")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, migration.MaxBytes+(1<<20))
	if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
		m.finish("failed", "备份上传失败或超过大小限制")
		fail(c, 400, "备份上传失败或超过大小限制")
		return
	}
	defer c.Request.MultipartForm.RemoveAll()
	password := c.PostForm("password")
	if c.PostForm("confirm") != "true" {
		m.finish("failed", "需要确认恢复")
		fail(c, 400, "请确认恢复到全新实例")
		return
	}
	f, _, err := c.Request.FormFile("backup")
	if err != nil {
		m.finish("failed", "请选择备份包")
		fail(c, 400, "请选择备份包")
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, migration.MaxBytes+1025))
	if err != nil {
		m.finish("failed", "读取备份失败")
		fail(c, 400, "读取备份失败")
		return
	}
	wasPaused := m.Gate.Paused()
	if err = m.Gate.Pause(); err != nil {
		m.finish("failed", err.Error())
		fail(c, 409, err.Error())
		return
	}
	if err = s.restoreSnapshot(c.Request.Context(), raw, password); err != nil {
		if !wasPaused {
			_ = m.Gate.Resume()
		}
		m.finish("failed", err.Error())
		fail(c, 400, err.Error())
		return
	}
	m.finish("restored", "恢复完成，正在重新加载；请使用旧机账号登录并检查后恢复运行")
	c.JSON(200, gin.H{"ok": true, "restart_required": true})
	if m.Restart != nil {
		m.Restart()
	}
}

func (s *Server) receiveCode(c *gin.Context) {
	if !s.migrationReady(c) {
		return
	}
	m := s.Migration
	if !m.begin("preparing") {
		fail(c, 409, "已有迁移操作正在进行")
		return
	}
	wasPaused := m.Gate.Paused()
	if err := m.Gate.Pause(); err != nil {
		m.finish("failed", err.Error())
		fail(c, 409, err.Error())
		return
	}
	if err := s.freshTarget(c.Request.Context()); err != nil {
		if !wasPaused {
			_ = m.Gate.Resume()
		}
		m.finish("failed", err.Error())
		fail(c, 409, err.Error())
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		m.finish("failed", "无法生成迁移码")
		fail(c, 500, "无法生成迁移码")
		return
	}
	code := base64.RawURLEncoding.EncodeToString(b)
	m.mu.Lock()
	m.code = code
	m.expires = time.Now().Add(15 * time.Minute)
	expires := m.expires
	m.mu.Unlock()
	m.finish("receiving", "等待旧机连接；迁移码 15 分钟有效，仅能使用一次")
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"code": code, "expires_at": expires})
}

func (s *Server) transferReceive(c *gin.Context) {
	if !s.migrationReady(c) {
		return
	}
	m := s.Migration
	// No cookie authorization: receiver must first be armed by its logged-in
	// owner. Invalid/replayed transfers cannot activate a restore.
	m.mu.Lock()
	code := m.code
	if code == "" || time.Now().After(m.expires) || m.busy || m.restartRequired {
		m.mu.Unlock()
		fail(c, 409, "接收未开启、迁移码已过期或正在处理")
		return
	}
	m.busy = true
	m.phase = "receiving"
	m.mu.Unlock()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, migration.MaxBytes+1024)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil || !migration.Verify(code, "upload", raw, c.GetHeader("X-OneFrog-Migration-Proof")) {
		m.finish("receiving", "迁移请求验证失败")
		fail(c, 403, "迁移请求验证失败")
		return
	}
	m.mu.Lock()
	expired := time.Now().After(m.expires)
	m.code = ""
	m.mu.Unlock()
	if expired {
		m.finish("failed", "迁移码已过期")
		fail(c, 403, "迁移码已过期")
		return
	}
	if err = s.restoreSnapshot(c.Request.Context(), raw, code); err != nil {
		m.finish("failed", err.Error())
		fail(c, 400, err.Error())
		return
	}
	m.finish("restored", "已收到并恢复迁移数据，正在重新加载；请用旧机账号登录")
	ack := []byte(migration.Proof(code, "received", raw))
	c.Header("Cache-Control", "no-store")
	c.Data(200, "text/plain", ack)
	if m.Restart != nil {
		m.Restart()
	}
}

func (s *Server) transferSend(c *gin.Context) {
	if !s.migrationReady(c) {
		return
	}
	m := s.Migration
	var req struct {
		Address string `json:"address"`
		Code    string `json:"code"`
		Confirm bool   `json:"confirm"`
	}
	if c.ShouldBindJSON(&req) != nil || !req.Confirm {
		fail(c, 400, "请确认目标地址以及暂停旧机")
		return
	}
	key, err := base64.RawURLEncoding.DecodeString(req.Code)
	if err != nil || len(key) != 32 {
		fail(c, 400, "迁移码格式不正确")
		return
	}
	client, address, err := migration.TransferClient(c.Request.Context(), req.Address)
	if err != nil {
		fail(c, 400, err.Error())
		return
	}
	if strings.TrimRight(m.PublicURL, "/") == address {
		client.CloseIdleConnections()
		fail(c, 400, "目标地址不能是当前实例")
		return
	}
	if !m.begin("packing") {
		client.CloseIdleConnections()
		fail(c, 409, "已有迁移操作正在进行")
		return
	}
	if err = m.Gate.Pause(); err != nil {
		client.CloseIdleConnections()
		m.finish("failed", err.Error())
		fail(c, 409, err.Error())
		return
	}
	go func() {
		defer client.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		raw, err := s.exportSnapshot(ctx, req.Code)
		if err != nil {
			m.finish("failed", err.Error()+"；旧机保持暂停，可重试或恢复运行")
			return
		}
		m.mu.Lock()
		m.phase = "transferring"
		m.mu.Unlock()
		r, err := http.NewRequestWithContext(ctx, "POST", address+"/api/migration/receive", bytes.NewReader(raw))
		if err != nil {
			m.finish("failed", "迁移地址无效")
			return
		}
		r.Header.Set("Content-Type", "application/octet-stream")
		r.Header.Set("X-OneFrog-Migration-Proof", migration.Proof(req.Code, "upload", raw))
		resp, err := client.Do(r)
		if err != nil {
			m.finish("uncertain", "连接中断，请查看新机是否已收到数据；旧机保持暂停，确认新机未接管后才能恢复运行")
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if err != nil || resp.StatusCode != 200 || !migration.Verify(req.Code, "received", raw, string(body)) {
			m.finish("uncertain", "未收到有效恢复确认，请检查新机结果、版本和迁移码；旧机保持暂停")
			return
		}
		m.finish("transferred", "新机已校验并恢复数据。请登录新机检查连接并恢复运行；旧机保持暂停，数据完整保留")
	}()
	c.JSON(202, gin.H{"ok": true})
}

// Middleware covers all HTTP operations (even GETs can cache/persist data).
// Authentication and migration management remain accessible while paused.
func (s *Server) migrationGate(c *gin.Context) {
	if s.Migration == nil {
		c.Next()
		return
	}
	path := c.Request.URL.Path
	if path == "/api/health" || strings.HasPrefix(path, "/api/migration/") || path == "/api/backup/export" || path == "/api/backup/restore" || path == "/api/auth/login" || path == "/api/auth/logout" || path == "/api/auth/me" || path == "/api/settings" && c.Request.Method == "GET" || path == "/api/bots" && c.Request.Method == "GET" {
		c.Next()
		return
	}
	done, err := s.Migration.Gate.Enter()
	if err != nil {
		fail(c, 503, err.Error())
		return
	}
	defer done()
	c.Next()
}
