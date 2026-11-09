package onefrog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Tool 是一个工具。Risk 根据参数给出风险等级，审批在 Execute 之前进行。
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Risk(args json.RawMessage) Risk
	Execute(ctx context.Context, env ToolEnv, args json.RawMessage) (string, error)
}

// ToolEnv 是执行工具时的环境。
type ToolEnv struct {
	Workspace string
	TaskID    string
	Env       map[string]string // 额外环境变量（shell 引用密钥时由执行层注入）
}

// FuncTool 用几个函数快速定义一个工具，自己加工具时推荐用它。
type FuncTool struct {
	ToolName, Desc, JSONSchema string
	RiskFn                     func(args json.RawMessage) Risk
	Fn                         func(ctx context.Context, env ToolEnv, args json.RawMessage) (string, error)
}

func (f FuncTool) Name() string            { return f.ToolName }
func (f FuncTool) Description() string     { return f.Desc }
func (f FuncTool) Schema() json.RawMessage { return json.RawMessage(f.JSONSchema) }
func (f FuncTool) Risk(a json.RawMessage) Risk {
	if f.RiskFn == nil {
		return RiskExec
	}
	return f.RiskFn(a)
}
func (f FuncTool) Execute(ctx context.Context, env ToolEnv, a json.RawMessage) (string, error) {
	return f.Fn(ctx, env, a)
}

// Registry 工具注册表。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: map[string]Tool{}}
	for _, t := range tools {
		r.Register(t)
	}
	return r
}

func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[t.Name()]; !ok {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

func (r *Registry) Get(name string) Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

func (r *Registry) Specs() []ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []ToolSpec
	for _, n := range r.order {
		t := r.tools[n]
		out = append(out, ToolSpec{t.Name(), t.Description(), t.Schema()})
	}
	return out
}

func insideWorkspace(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ResolvePath 把路径解析到工作区内。已存在的每一级目录都会展开符号链接，
// 最终位置必须仍在工作区里；指向工作区外的链接直接拒绝。
func ResolvePath(workspace, p string) (string, error) {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	if realRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = realRoot
	}
	abs := p
	if !filepath.IsAbs(p) {
		abs = filepath.Join(root, p)
	}
	abs = filepath.Clean(abs)
	if !insideWorkspace(root, abs) && !insideWorkspace(filepath.Clean(workspace), abs) {
		return "", fmt.Errorf("路径 %q 超出了工作区 %s", p, root)
	}
	current := abs
	rest := ""
	for {
		if real, err := filepath.EvalSymlinks(current); err == nil {
			joined := filepath.Join(real, rest)
			if !insideWorkspace(root, joined) {
				return "", fmt.Errorf("路径 %q 超出了工作区 %s", p, root)
			}
			return joined, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		rest = filepath.Join(filepath.Base(current), rest)
		current = parent
	}
	if !insideWorkspace(root, abs) {
		return "", fmt.Errorf("路径 %q 超出了工作区 %s", p, root)
	}
	return abs, nil
}

func parseArgs[T any](raw json.RawMessage) (T, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("参数格式错误: %w", err)
	}
	return v, nil
}

func readOnly(json.RawMessage) Risk { return RiskRead }
func writes(json.RawMessage) Risk   { return RiskWrite }

// DefaultTools 返回内置的基础工具。
func DefaultTools() []Tool {
	return []Tool{readFileTool, writeFileTool, editFileTool, listDirTool, grepTool, ShellTool}
}

var readFileTool = FuncTool{"read_file", "读取工作区内的文本文件。可用 offset(从1开始的行号)和 limit(行数)读取大文件的一部分。",
	`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}},"required":["path"]}`,
	readOnly, func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		a, err := parseArgs[struct {
			Path          string
			Offset, Limit int
		}](raw)
		if err != nil {
			return "", err
		}
		p, err := ResolvePath(env.Workspace, a.Path)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		if a.Offset <= 0 && a.Limit <= 0 {
			return string(data), nil
		}
		lines := strings.Split(string(data), "\n")
		start := max(a.Offset-1, 0)
		end := len(lines)
		if a.Limit > 0 && start+a.Limit < end {
			end = start + a.Limit
		}
		if start >= len(lines) {
			return "", fmt.Errorf("offset 超出文件行数 %d", len(lines))
		}
		return strings.Join(lines[start:end], "\n"), nil
	}}

var writeFileTool = FuncTool{"write_file", "写入（覆盖）工作区内的文件，会自动创建上级目录。",
	`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`,
	writes, func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		a, err := parseArgs[struct{ Path, Content string }](raw)
		if err != nil {
			return "", err
		}
		p, err := ResolvePath(env.Workspace, a.Path)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(a.Content), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("已写入 %s（%d 字节）", a.Path, len(a.Content)), nil
	}}

var editFileTool = FuncTool{"edit_file", "精确替换文件中的一段文字。old_string 必须在文件中唯一出现，除非 replace_all=true。",
	`{"type":"object","properties":{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}},"required":["path","old_string","new_string"]}`,
	writes, func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		a, err := parseArgs[struct {
			Path       string
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		}](raw)
		if err != nil {
			return "", err
		}
		p, err := ResolvePath(env.Workspace, a.Path)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		n := strings.Count(string(data), a.OldString)
		switch {
		case a.OldString == "" || n == 0:
			return "", errors.New("old_string 在文件中没有找到")
		case n > 1 && !a.ReplaceAll:
			return "", fmt.Errorf("old_string 出现了 %d 次，请提供更多上下文让它唯一，或设置 replace_all", n)
		}
		out := strings.Replace(string(data), a.OldString, a.NewString, map[bool]int{true: -1, false: 1}[a.ReplaceAll])
		if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("已修改 %s（替换 %d 处）", a.Path, map[bool]int{true: n, false: 1}[a.ReplaceAll]), nil
	}}

var listDirTool = FuncTool{"list_dir", "列出目录内容，目录名以 / 结尾。",
	`{"type":"object","properties":{"path":{"type":"string","description":"默认为工作区根目录"}}}`,
	readOnly, func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		a, _ := parseArgs[struct{ Path string }](raw)
		p, err := ResolvePath(env.Workspace, a.Path)
		if err != nil {
			return "", err
		}
		ents, err := os.ReadDir(p)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, e := range ents {
			if e.IsDir() {
				fmt.Fprintf(&b, "%s/\n", e.Name())
			} else if info, err := e.Info(); err == nil {
				fmt.Fprintf(&b, "%s  (%d B)\n", e.Name(), info.Size())
			}
		}
		if b.Len() == 0 {
			return "(空目录)", nil
		}
		return b.String(), nil
	}}

var grepTool = FuncTool{"grep", "在工作区里用正则搜索文件内容，返回 文件:行号: 内容（最多 200 条）。",
	`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"]}`,
	readOnly, func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		a, err := parseArgs[struct{ Pattern, Path string }](raw)
		if err != nil {
			return "", err
		}
		re, err := regexp.Compile(a.Pattern)
		if err != nil {
			return "", err
		}
		root, err := ResolvePath(env.Workspace, a.Path)
		if err != nil {
			return "", err
		}
		var hits []string
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil || len(hits) >= 200 {
				return filepath.SkipAll
			}
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if n := d.Name(); n == ".git" || n == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if info, err := d.Info(); err != nil || info.Size() > 2<<20 {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil || bytes.IndexByte(data[:min(len(data), 512)], 0) >= 0 {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			sc := bufio.NewScanner(bytes.NewReader(data))
			sc.Buffer(nil, 1<<20)
			for i := 1; sc.Scan() && len(hits) < 200; i++ {
				if re.MatchString(sc.Text()) {
					hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, i, sc.Text()))
				}
			}
			return nil
		})
		sort.Strings(hits)
		if len(hits) == 0 {
			return "没有匹配", nil
		}
		return strings.Join(hits, "\n"), nil
	}}

var safeShell = regexp.MustCompile(`^\s*(ls|pwd|cat|head|tail|wc|grep|rg|find|echo|which|file|stat|du|tree|git\s+(status|diff|log|show|branch))(\s|$)`)

// ShellRisk 只把简单的只读命令（不含管道、重定向、绝对路径、..）算作只读。
func ShellRisk(cmd string) Risk {
	if safeShell.MatchString(cmd) && !strings.ContainsAny(cmd, "><;&|`$~") &&
		!strings.Contains(cmd, "..") && !strings.Contains(cmd, " /") &&
		!strings.Contains(cmd, "-exec") && !strings.Contains(cmd, "-delete") {
		return RiskRead
	}
	return RiskExec
}

// ShellTool 用 bash -c 在工作区目录里执行命令；超时或取消会杀掉整个进程组。
var ShellTool = FuncTool{"shell", "在工作区目录中用 bash 执行命令，返回输出和退出码。timeout_sec 默认 120，最大 600。",
	`{"type":"object","properties":{"command":{"type":"string"},"timeout_sec":{"type":"integer"}},"required":["command"]}`,
	func(raw json.RawMessage) Risk {
		a, _ := parseArgs[struct{ Command string }](raw)
		return ShellRisk(a.Command)
	},
	func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		a, err := parseArgs[struct {
			Command    string
			TimeoutSec int `json:"timeout_sec"`
		}](raw)
		if err != nil {
			return "", err
		}
		to := time.Duration(a.TimeoutSec) * time.Second
		if to <= 0 {
			to = 120 * time.Second
		}
		ctx, cancel := context.WithTimeout(ctx, min(to, 600*time.Second))
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "-c", a.Command)
		cmd.Dir = env.Workspace
		if len(env.Env) > 0 {
			cmd.Env = os.Environ()
			for k, v := range env.Env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
		}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		setProcessGroup(cmd)
		cmd.WaitDelay = 2 * time.Second
		err = cmd.Run()
		res := out.String()
		if ctx.Err() != nil {
			return res, fmt.Errorf("命令被终止: %v", ctx.Err())
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Sprintf("%s\n[exit code %d]", res, ee.ExitCode()), nil
		}
		if err != nil {
			return res, err
		}
		return res + "\n[exit code 0]", nil
	}}
