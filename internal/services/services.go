// Package services collects a read-only inventory without process arguments,
// environment variables, container inspect data, logs, or configuration files.
package services

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"onefrog/internal/remote"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

//go:embed collect.py
var script string

// Command has no user-controlled shell fragments.
var Command = "python3 - <<'ONEFROG_SERVICES'\n" + script + "\nONEFROG_SERVICES"

type Item struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	Image  string `json:"image,omitempty"`
	Ports  string `json:"ports,omitempty"`
}
type Source struct {
	Status    string `json:"status"`
	Detail    string `json:"detail"`
	Truncated bool   `json:"truncated"`
}
type View struct {
	ServerID    string            `json:"server_id"`
	Status      string            `json:"status"`
	CollectedAt *time.Time        `json:"collected_at,omitempty"`
	AttemptedAt *time.Time        `json:"attempted_at,omitempty"`
	Error       string            `json:"error,omitempty"`
	Items       []Item            `json:"items"`
	Sources     map[string]Source `json:"sources"`
}
type Executor interface {
	Run(context.Context, string, int, string, string, string, string, string) (string, string, error)
}
type Scanner struct {
	store    store.Store
	box      *secret.Box
	ssh      Executor
	sanitize func(context.Context, string) string
	mu       sync.Mutex
	views    map[string]View
	jobs     map[string]chan struct{}
	slots    chan struct{}
}

func New(st store.Store, box *secret.Box, ssh Executor, sanitize func(context.Context, string) string) *Scanner {
	return &Scanner{store: st, box: box, ssh: ssh, sanitize: sanitize, views: map[string]View{}, jobs: map[string]chan struct{}{}, slots: make(chan struct{}, 4)}
}
func empty(id string) View {
	return View{ServerID: id, Status: "pending", Items: []Item{}, Sources: map[string]Source{}}
}
func clone(v View) View {
	if v.CollectedAt != nil {
		stamp := *v.CollectedAt
		v.CollectedAt = &stamp
	}
	if v.AttemptedAt != nil {
		stamp := *v.AttemptedAt
		v.AttemptedAt = &stamp
	}
	v.Items = append([]Item{}, v.Items...)
	sources := map[string]Source{}
	for k, s := range v.Sources {
		sources[k] = s
	}
	v.Sources = sources
	return v
}

// Views only returns IDs supplied by the current server list, including pending nodes.
func (s *Scanner) Views(ids []string) []View {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]View, 0, len(ids))
	for _, id := range ids {
		v, ok := s.views[id]
		if !ok {
			v = empty(id)
		}
		out = append(out, clone(v))
	}
	return out
}
func Parse(raw string) (View, error) {
	v := empty("")
	if len(raw) > 2<<20 || json.Unmarshal([]byte(strings.TrimSpace(raw)), &v) != nil {
		return v, errors.New("采集结果无效（服务器需安装 Python 3）")
	}
	if len(v.Items) > 1500 || len(v.Sources) != 3 {
		return v, errors.New("采集结果超出范围")
	}
	good := 0
	for _, kind := range []string{"systemd", "docker", "ports"} {
		source, ok := v.Sources[kind]
		if !ok {
			return v, errors.New("采集来源缺失")
		}
		switch source.Status {
		case "ok":
			good++
		case "error", "unavailable":
		default:
			return v, errors.New("采集来源状态无效")
		}
	}
	for _, item := range v.Items {
		if item.Kind != "systemd" && item.Kind != "docker" && item.Kind != "ports" {
			return v, errors.New("服务类型无效")
		}
		if len(item.Name) > 800 || len(item.Detail) > 800 || len(item.State) > 160 || len(item.Image) > 1200 || len(item.Ports) > 4000 {
			return v, errors.New("服务字段过长")
		}
	}
	v.Status = "ok"
	if good < 3 {
		v.Status = "partial"
	}
	if good == 0 {
		v.Status = "failed"
		v.Error = "所有采集来源均不可用，请检查命令和权限"
	}
	return v, nil
}
func (s *Scanner) Scan(ctx context.Context, id string) (View, error) {
	// Validate existence even for an in-flight or cached entry after server deletion.
	rec, err := s.store.GetServer(ctx, id)
	if err != nil {
		return View{}, err
	}
	s.mu.Lock()
	if job := s.jobs[id]; job != nil {
		s.mu.Unlock()
		select {
		case <-job:
			return s.Views([]string{id})[0], nil
		case <-ctx.Done():
			return View{}, ctx.Err()
		}
	}
	job := make(chan struct{})
	s.jobs[id] = job
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.jobs, id); close(job); s.mu.Unlock() }()
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	v := empty(id)
	now := time.Now().UTC()
	v.AttemptedAt = &now
	failure := func(msg string) (View, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		old, ok := s.views[id]
		if ok {
			v = clone(old)
		}
		v.Status = "failed"
		v.Error = msg
		v.AttemptedAt = &now
		s.views[id] = v
		return clone(v), nil
	}
	if rec.HostKey == "" {
		return failure("SSH 主机指纹尚未确认，请先在服务器页测试连接并核对指纹")
	}
	if rec.EncryptedKey == "" || s.box == nil {
		return failure("未配置 SSH 登录凭据")
	}
	if s.ssh == nil {
		return failure("没有 SSH 执行器")
	}
	plain, err := s.box.Decrypt(rec.EncryptedKey)
	if err != nil {
		return failure("SSH 登录凭据无法解密")
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-runCtx.Done():
		return failure("服务检测超时或已取消")
	}
	raw, _, err := s.ssh.Run(runCtx, rec.Host, rec.Port, rec.Username, rec.AuthType, plain, rec.HostKey, Command)
	if errors.Is(err, remote.ErrHostKeyMismatch) {
		return failure("SSH 主机指纹不一致，已停止检测，请核对服务器身份")
	}
	if err != nil {
		return failure("SSH 检测失败：请检查连接、登录权限与 Python 3 是否可用")
	}
	// Fail closed: caller supplies the same redaction used by AI tool output.
	if s.sanitize == nil {
		return failure("未配置敏感信息过滤器")
	}
	raw = s.sanitize(runCtx, raw)
	parsed, err := Parse(raw)
	if err != nil {
		return failure(err.Error())
	}
	if parsed.Status == "failed" {
		return failure(parsed.Error)
	}
	stamp := time.Now().UTC()
	parsed.ServerID = id
	parsed.CollectedAt = &stamp
	parsed.AttemptedAt = &now
	parsed.Error = ""
	// A node may have been deleted while SSH was running. Never revive its data.
	if _, err = s.store.GetServer(ctx, id); err != nil {
		return View{}, err
	}
	s.mu.Lock()
	s.views[id] = parsed
	s.mu.Unlock()
	return clone(parsed), nil
}
