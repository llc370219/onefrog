package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"onefrog/internal/bot"
	frog "onefrog/internal/frogbot/bot"
	kernel "onefrog/internal/frogbot/onefrog"
	"onefrog/internal/maintenance"
	"onefrog/internal/model"
	"onefrog/internal/secret"
)

// FrogBots embeds the upstream runtime once per authenticated account. Each
// instance owns its event stream, vault, schedules and a private workspace.
type FrogBots struct {
	dir       string
	key       []byte
	agent     *bot.Agent
	gate      *maintenance.Gate
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	closeOnce sync.Once
	instances map[string]http.Handler
	bots      []*frog.Bot
}

func NewFrogBots(dir string, key []byte, agent *bot.Agent, gate *maintenance.Gate) *FrogBots {
	ctx, cancel := context.WithCancel(context.Background())
	return &FrogBots{dir: dir, key: append([]byte(nil), key...), agent: agent, gate: gate,
		ctx: ctx, cancel: cancel, instances: make(map[string]http.Handler)}
}

func (f *FrogBots) StopStreams() { f.cancel() }

func (f *FrogBots) Close() {
	f.closeOnce.Do(func() {
		f.mu.Lock()
		f.cancel()
		bots := f.bots
		f.mu.Unlock()
		for _, b := range bots {
			b.Close()
		}
	})
}

// Restore starts persisted queues and schedules without waiting for a browser.
func (f *FrogBots) Restore() error {
	release, err := f.gate.Enter()
	if errors.Is(err, maintenance.ErrPaused) {
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	rows, err := os.ReadDir(f.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, row := range rows {
		owner, err := hex.DecodeString(row.Name())
		if !row.IsDir() || err != nil || len(owner) == 0 || len(owner) > 128 {
			continue
		}
		if _, err := f.handler(string(owner)); err != nil {
			return err
		}
	}
	return nil
}

func (f *FrogBots) handler(owner string) (http.Handler, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ctx.Err() != nil {
		return nil, f.ctx.Err()
	}
	if h := f.instances[owner]; h != nil {
		return h, nil
	}
	if owner == "" || len(owner) > 128 {
		return nil, errors.New("账户标识无效")
	}
	dir := filepath.Join(f.dir, hex.EncodeToString([]byte(owner)))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, f.key)
	mac.Write([]byte("onefrog/frog-bot/vault/" + owner))
	vaultKey := mac.Sum(nil)
	mac = hmac.New(sha256.New, f.key)
	mac.Write([]byte("onefrog/frog-bot/state/" + owner))
	codec, err := secret.New(mac.Sum(nil))
	if err != nil {
		return nil, err
	}
	cfg := frog.Config{
		Codec: codec,
		Name:  "Frog Bot", DataDir: dir, Workspace: filepath.Join(dir, "workspace"),
		MasterKey:     hex.EncodeToString(vaultKey),
		ChatProvider:  bot.FrogProvider{Agent: f.agent, Owner: owner},
		AgentProvider: bot.FrogProvider{Agent: f.agent, Owner: owner, Worker: true},
		Policy:        kernel.PolicyUntrusted, DisableShell: true, Enter: f.gate.Enter,
		AgentTools: f.hostTools(owner), Redact: func(text string) string { return f.agent.RedactServiceOutput(f.ctx, text) },
	}
	b, err := frog.New(cfg)
	if err != nil {
		return nil, err
	}
	h := b.Handler("")
	f.instances[owner] = h
	f.bots = append(f.bots, b)
	b.Start()
	return h, nil
}

func (f *FrogBots) hostTools(owner string) []kernel.Tool {
	if f.agent.SSH == nil {
		return nil
	}
	return []kernel.Tool{
		kernel.FuncTool{
			ToolName: "list_onefrog_servers", Desc: "列出当前账户可操作的 OneFrog 服务器及其 ID。",
			JSONSchema: `{"type":"object","properties":{}}`,
			RiskFn:     func(json.RawMessage) kernel.Risk { return kernel.RiskRead },
			Fn: func(ctx context.Context, _ kernel.ToolEnv, _ json.RawMessage) (string, error) {
				servers, err := f.agent.Store.ListServers(ctx)
				if err != nil {
					return "", err
				}
				rows := make([]map[string]string, 0)
				for _, server := range servers {
					rows = append(rows, map[string]string{"id": server.ID, "name": server.Name})
				}
				data, err := json.Marshal(rows)
				return string(data), err
			},
		},
		kernel.PassSecrets{FuncTool: kernel.FuncTool{
			ToolName:   "run_onefrog_command",
			Desc:       "经用户确认，在 OneFrog 登记且已确认 SSH 指纹的服务器上执行一条命令，并写入 OneFrog 审计。server_id 来自 list_onefrog_servers。",
			JSONSchema: `{"type":"object","properties":{"server_id":{"type":"string"},"command":{"type":"string"}},"required":["server_id","command"]}`,
			RiskFn:     func(json.RawMessage) kernel.Risk { return kernel.RiskDanger },
			Fn: func(ctx context.Context, _ kernel.ToolEnv, raw json.RawMessage) (string, error) {
				var args struct {
					ServerID string `json:"server_id"`
					Command  string `json:"command"`
				}
				if err := json.Unmarshal(raw, &args); err != nil || args.ServerID == "" || strings.TrimSpace(args.Command) == "" || len(args.Command) > 8000 {
					return "", errors.New("请提供服务器 ID 和命令（最多 8000 字节）")
				}
				return f.agent.RunFrogCommand(ctx, owner, args.ServerID, args.Command)
			},
		}},
	}
}

func (s *Server) frogBotAPI(c *gin.Context) {
	if s.FrogBots == nil {
		fail(c, 503, "Frog Bot 未启用")
		return
	}
	owner := c.MustGet("user").(model.User).ID
	// The event stream is read-only and must not hold the migration gate
	// open. Only opening/recovering the instance needs a short lease.
	release, err := s.FrogBots.gate.Enter()
	if err != nil {
		fail(c, 503, err.Error())
		return
	}
	h, err := s.FrogBots.handler(owner)
	release()
	if err != nil {
		fail(c, 503, "Frog Bot 启动失败")
		return
	}
	ctx := c.Request.Context()
	if c.Param("path") == "/stream" {
		// Reauthenticate on reconnect, including after logout or password change.
		bounded, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		stop := context.AfterFunc(s.FrogBots.ctx, cancel)
		defer stop()
		ctx = bounded
	}
	r := c.Request.Clone(ctx)
	r.URL.Path = "/api" + c.Param("path")
	r.URL.RawPath = ""
	c.Header("Cache-Control", "private, no-store")
	h.ServeHTTP(c.Writer, r)
}
