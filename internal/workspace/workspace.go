// Package workspace gives OneFrog a Git checkout on a pinned SSH server.
package workspace

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"

	"onefrog/internal/remote"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

//go:embed runner.py
var runner string

const MaxFile = 256 << 10

type Target struct {
	ServerID string `json:"server_id"`
	Root     string `json:"root"`
	// Kind selects the workspace type: "ssh" (default, backward compatible)
	// or "local" (native, operator-allowlisted root on the OneFrog server
	// itself). For local, ServerID is always "local".
	Kind string `json:"kind,omitempty"`
}

// IsLocal reports whether the target is a native local workspace.
func (t Target) IsLocal() bool { return t.Kind == "local" }

func (t Target) Validate() error {
	if t.Kind == "local" {
		if t.ServerID != "local" {
			return errors.New("本地工作区的 server_id 必须是 local")
		}
		if len(t.Root) > 1024 || !path.IsAbs(t.Root) || t.Root == "/" || path.Clean(t.Root) != t.Root || strings.ContainsFunc(t.Root, unicode.IsControl) {
			return errors.New("本地工作区需要允许列表内的绝对路径")
		}
		return nil
	}
	if t.ServerID == "" || len(t.ServerID) > 100 || strings.ContainsFunc(t.ServerID, unicode.IsControl) {
		return errors.New("请选择工作区所在的服务器")
	}
	if len(t.Root) > 1024 || !path.IsAbs(t.Root) || t.Root == "/" || path.Clean(t.Root) != t.Root || strings.ContainsFunc(t.Root, unicode.IsControl) {
		return errors.New("请输入 Git 仓库根目录的绝对路径（用 pwd -P 查看，不含末尾斜线）")
	}
	return nil
}

type Request struct {
	Action   string `json:"action"`
	Path     string `json:"path,omitempty"`
	Revision string `json:"revision,omitempty"`
	Content  string `json:"content"`
	Old      string `json:"old,omitempty"`
	New      string `json:"new,omitempty"`
	Command  string `json:"command,omitempty"`
	FromLine int    `json:"from_line,omitempty"`
	Lines    int    `json:"lines,omitempty"`
}

type Result struct {
	Files     []string `json:"files,omitempty"`
	Text      string   `json:"text,omitempty"`
	Revision  string   `json:"revision,omitempty"`
	ExitCode  *int     `json:"exit_code,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

var revision = regexp.MustCompile("^[0-9a-f]{64}$")

func validRelative(p string) bool {
	if p == "" || len(p) > 1024 || p == "." || path.IsAbs(p) || path.Clean(p) != p || strings.ContainsFunc(p, unicode.IsControl) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

func (r Request) Validate() error {
	switch r.Action {
	case "list", "status", "diff", "read", "write", "edit", "run":
	default:
		return errors.New("未知的工作区操作")
	}
	if r.Path != "" && !validRelative(r.Path) {
		return errors.New("路径必须相对于仓库根目录，不能访问 .git 或上级目录")
	}
	switch r.Action {
	case "read", "write", "edit":
		if r.Path == "" {
			return errors.New("请指定文件路径")
		}
	}
	if r.Revision != "" && !revision.MatchString(r.Revision) {
		return errors.New("文件版本无效，请重新读取")
	}
	if r.Action == "edit" && (r.Revision == "" || r.Old == "") {
		return errors.New("编辑需要读取时的文件版本和要替换的原文")
	}
	if len(r.Content)+len(r.Old)+len(r.New) > MaxFile {
		return errors.New("一次最多写入或替换 256 KiB 文本")
	}
	if r.FromLine < 0 || r.Lines < 0 || r.Lines > 300 {
		return errors.New("行范围无效，每次最多读取 300 行")
	}
	if r.Action == "run" && (strings.TrimSpace(r.Command) == "" || len(r.Command) > 4000 || strings.ContainsRune(r.Command, 0)) {
		return errors.New("命令不能为空、含 NUL 字符或超过 4000 字节")
	}
	return nil
}

type Service struct {
	Store store.Store
	Box   *secret.Box
	Run   func(context.Context, string, int, string, string, string, string, string, []byte) (string, string, error)
}

func (s *Service) Server(ctx context.Context, t Target) (store.ServerRec, error) {
	if s == nil || s.Run == nil {
		return store.ServerRec{}, errors.New("代码工作区未启用")
	}
	if err := t.Validate(); err != nil {
		return store.ServerRec{}, err
	}
	rec, err := s.Store.GetServer(ctx, t.ServerID)
	if err != nil {
		return rec, errors.New("工作区服务器不存在")
	}
	if rec.HostKey == "" {
		return rec, errors.New("请先在服务器页面确认 SSH 主机指纹")
	}
	if rec.Username == "root" {
		return rec, errors.New("代码工作区需要非 root 的 SSH 开发账号")
	}
	return rec, nil
}

func (s *Service) Do(ctx context.Context, t Target, r Request) (Result, error) {
	return s.DoChecked(ctx, t, r, nil)
}

// DoChecked refuses a change of SSH identity or destination while an approval was pending.
func (s *Service) DoChecked(ctx context.Context, t Target, r Request, expected *store.ServerRec) (Result, error) {
	if err := r.Validate(); err != nil {
		return Result{}, err
	}
	rec, err := s.Server(ctx, t)
	if err != nil {
		return Result{}, err
	}
	if expected != nil && (rec.Host != expected.Host || rec.Port != expected.Port || rec.Username != expected.Username ||
		rec.HostKey != expected.HostKey || rec.AuthType != expected.AuthType || rec.EncryptedKey != expected.EncryptedKey) {
		return Result{}, errors.New("审批期间服务器连接设置已改变，请重试")
	}
	if s.Box == nil || rec.EncryptedKey == "" {
		return Result{}, errors.New("请为工作区服务器设置 SSH 凭据")
	}
	password, err := s.Box.Decrypt(rec.EncryptedKey)
	if err != nil {
		return Result{}, errors.New("无法读取 SSH 凭据")
	}
	timeout := 35 * time.Second
	if r.Action == "run" {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	payload, err := json.Marshal(struct {
		Request
		Root string `json:"root"`
	}{r, t.Root})
	if err != nil {
		return Result{}, err
	}
	cmd := "python3 -I -c '" + strings.ReplaceAll(runner, "'", "'\\''") + "'"
	out, _, err := s.Run(ctx, rec.Host, rec.Port, rec.Username, rec.AuthType, password, rec.HostKey, cmd, payload)
	if err != nil {
		if errors.Is(err, remote.ErrHostKeyMismatch) || errors.Is(err, remote.ErrHostKeyUnknown) {
			return Result{}, err
		}
		return Result{}, errors.New("无法运行工作区工具：请检查 SSH 连接、账号和宿主机的 Python 3")
	}
	var answer struct {
		Result
		Error string `json:"error"`
	}
	if err = json.Unmarshal([]byte(out), &answer); err != nil {
		return Result{}, fmt.Errorf("宿主机工作区未返回有效结果，请检查 SSH 登录环境")
	}
	if answer.Error != "" {
		return Result{}, errors.New(answer.Error)
	}
	return answer.Result, nil
}
