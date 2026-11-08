package workspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// LocalResult is the shape returned by local workspace actions, mirroring the
// SSH Result JSON so transports can share the model-facing contract.
type LocalResult struct {
	Files     []string `json:"files,omitempty"`
	Text      string   `json:"text,omitempty"`
	Revision  string   `json:"revision,omitempty"`
	ExitCode  *int     `json:"exit_code,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

// LocalEngine executes native workspace actions for one process. It holds the
// operator allowlist (ENV-only policy) and runs commands with the OS account's
// authority (no sandbox). A nil engine or disabled allowlist refuses every
// action.
type LocalEngine struct {
	mu     sync.RWMutex
	roots  Roots
	runner func(ctx context.Context, dir, command string) (stdout, stderr string, exitCode int, err error)
	// runTimeout bounds one foreground command.
	runTimeout time.Duration
	// isContainer lets tests inject container detection (finding 16).
	isContainer func() bool
}

// NewLocalEngine builds an engine; runner is injectable for tests. A nil
// runner uses the native process executor (still gated by CommandExecution).
func NewLocalEngine(roots Roots, runner func(ctx context.Context, dir, command string) (string, string, int, error)) *LocalEngine {
	e := &LocalEngine{roots: roots, runner: runner, runTimeout: DefaultLocalRunTimeout}
	// Deep-copy the allowlist: the engine is boot-time policy and must stay
	// immune to later mutation of the caller's slice (e.g. a config reload
	// reusing the same backing array to widen access at runtime).
	e.roots.Dirs = append([]string(nil), roots.Dirs...)
	return e
}

// The allowlist is boot-time operator policy: it is set once at construction
// and never replaced at runtime. There is deliberately no SetRoots — a
// settings save or a model call must not be able to widen the policy.

// Roots returns a copy of the current allowlist.
func (e *LocalEngine) Roots() Roots {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return Roots{Enabled: e.roots.Enabled, CommandExecution: e.roots.CommandExecution, Dirs: append([]string(nil), e.roots.Dirs...)}
}

// Capabilities is what the API reports to the UI.
func (e *LocalEngine) Capabilities() LocalCapabilities {
	r := e.Roots()
	return LocalCapabilities{Enabled: r.Enabled, Roots: r.Dirs, CommandExecution: r.CommandExecution}
}

// LocalCapabilities describes the native workspace feature truthfully.
type LocalCapabilities struct {
	Enabled          bool     `json:"enabled"`
	Roots            []string `json:"roots"`
	CommandExecution bool     `json:"command_execution"`
}

// allowed resolves and authorizes root for a target action.
func (e *LocalEngine) allowed(root string) (string, error) {
	e.mu.RLock()
	roots := e.roots
	e.mu.RUnlock()
	return roots.Pick(root)
}

// LocalRequest is one local action. It shares field names with Request so the
// model tool schema can stay uniform.
type LocalRequest struct {
	Action   string `json:"action"`
	Path     string `json:"path,omitempty"`
	Revision string `json:"revision,omitempty"`
	Content  string `json:"content,omitempty"`
	Old      string `json:"old,omitempty"`
	New      string `json:"new,omitempty"`
	Command  string `json:"command,omitempty"`
	FromLine int    `json:"from_line,omitempty"`
	Lines    int    `json:"lines,omitempty"`
}

// ValidateLocalRequest checks one action's fields. Run is only valid when the
// engine's operator allowlist turns command execution on.
func ValidateLocalRequest(r LocalRequest) error {
	switch r.Action {
	case "list", "status", "diff", "read", "write", "edit", "run", "search":
	default:
		return errors.New("未知的本地工作区操作")
	}
	if r.Action == "search" {
		if strings.TrimSpace(r.Content) == "" || len(r.Content) > 200 {
			return errors.New("search 需要一个 1–200 字的搜索词")
		}
		return nil
	}
	if r.Path != "" && (len(r.Path) > 1024 || filepath.IsAbs(r.Path) || strings.ContainsRune(r.Path, 0)) {
		return errors.New("路径必须是相对路径")
	}
	switch r.Action {
	case "read", "write", "edit":
		if strings.TrimSpace(r.Path) == "" {
			return errors.New("请指定文件路径")
		}
	}
	if r.Action == "edit" && (r.Revision == "" || r.Old == "") {
		return errors.New("编辑需要读取时的文件版本和要替换的原文")
	}
	if len(r.Content)+len(r.Old)+len(r.New) > MaxLocalFile {
		return errors.New("一次最多写入或替换 256 KiB 文本")
	}
	if r.FromLine < 0 || r.Lines < 0 || r.Lines > 500 {
		return errors.New("行范围无效，每次最多读取 500 行")
	}
	if r.Action == "run" {
		cmd := strings.TrimSpace(r.Command)
		if cmd == "" || len(cmd) > 4000 || strings.ContainsRune(cmd, 0) {
			return errors.New("命令不能为空、含 NUL 字符或超过 4000 字节")
		}
	}
	return nil
}

// Do executes one local action.
//
// Authorization and execution use different roots on purpose: the allowlist
// decides whether the requested root may be used at all (Pick), and the action
// then runs against the exact canonical requested target — never the allowlist
// ancestor. A task scoped to /allowed/project cannot see or change sibling
// projects under /allowed. All file confinement goes through os.Root.
func (e *LocalEngine) Do(ctx context.Context, root string, req LocalRequest) (LocalResult, error) {
	if err := ValidateLocalRequest(req); err != nil {
		return LocalResult{}, err
	}
	// Authorize the requested target against the operator policy.
	if _, err := e.allowed(root); err != nil {
		return LocalResult{}, err
	}
	target, err := NormalizeRoot(root)
	if err != nil {
		return LocalResult{}, err
	}
	if st, serr := os.Stat(target); serr != nil || !st.IsDir() {
		return LocalResult{}, errors.New("工作区根目录不存在或不是目录")
	}
	// The resolved real location must still sit inside the allowlist: a
	// symlinked root is fine when it lands in an allowed dir; a symlink into
	// /etc is not. Resolved-path comparison preserves /tmp → /private/tmp.
	if resolved, rerr := filepath.EvalSymlinks(target); rerr == nil {
		if err := e.assertCovered(resolved); err != nil {
			return LocalResult{}, err
		}
	}
	switch req.Action {
	case "list":
		return e.list(ctx, target, req.Path)
	case "read":
		return e.read(target, req)
	case "write":
		return e.write(target, req)
	case "edit":
		return e.edit(target, req)
	case "search":
		return e.search(ctx, target, req.Content)
	case "run":
		e.mu.RLock()
		execOK := e.roots.CommandExecution
		e.mu.RUnlock()
		if !execOK {
			return LocalResult{}, errors.New("本地命令执行未启用：命令执行以当前进程的操作系统账号权限运行，需要操作员显式开启")
		}
		return e.run(ctx, target, req.Command)
	case "status":
		return e.gitStatus(target)
	case "diff":
		return e.gitDiff(target)
	}
	return LocalResult{}, errors.New("未知的本地工作区操作")
}

// assertCovered verifies a resolved target sits inside one allowlisted dir
// (comparing resolved paths so symlinked allowlist entries still match).
func (e *LocalEngine) assertCovered(resolved string) error {
	e.mu.RLock()
	dirs := append([]string(nil), e.roots.Dirs...)
	e.mu.RUnlock()
	for _, d := range dirs {
		rd, err := filepath.EvalSymlinks(d)
		if err != nil {
			continue
		}
		if containsRoot(rd, resolved) {
			return nil
		}
	}
	return errEscapedRoot
}

// revisionFor hashes file content for optimistic-concurrency checks.
func revisionFor(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// withRoot opens the exact target root once for a confined operation. Every
// file operation uses Root-relative names, so no symlink can redirect an
// operation outside the tree between check and use.
func withRoot(root string, fn func(r *os.Root) error) error {
	r, err := openLocalRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	return fn(r)
}

func (e *LocalEngine) read(root string, req LocalRequest) (LocalResult, error) {
	rel, err := cleanRelPath(req.Path)
	if err != nil {
		return LocalResult{}, err
	}
	if protectedPath(rel) {
		return LocalResult{}, errors.New("不能访问敏感文件")
	}
	var text string
	var rev string
	var total, from, end int
	err = withRoot(root, func(r *os.Root) error {
		// No symlink may appear as a path component: a symlinked parent can
		// alias a protected directory and bypass component-level checks.
		if err := rejectSymlinkComponents(r, rel); err != nil {
			return err
		}
		// Lstat first: a symlink inside the tree is refused rather than
		// followed (Root.Read would follow it within the tree, which is
		// confined, but aliasing a protected file must be caught).
		st, err := r.Lstat(rel)
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			dst, err := r.Readlink(rel)
			if err != nil {
				return errors.New("只能读取普通文件")
			}
			if protectedPath(dst) || filepath.IsAbs(dst) {
				return errors.New("不能访问敏感文件")
			}
			if sub, err := cleanRelPath(dst); err != nil || protectedPath(sub) {
				return errors.New("不能访问敏感文件")
			}
			rel = filepath.Clean(dst)
			st, err = r.Lstat(rel)
			if err != nil {
				return err
			}
		}
		if st.IsDir() {
			return errors.New("目标是一个目录，请用 list")
		}
		if !st.Mode().IsRegular() {
			return errors.New("只能读取普通文件")
		}
		if st.Size() > MaxLocalRead {
			return errors.New("文件超过单次读取上限，请用行范围分段读取")
		}
		raw, err := r.ReadFile(rel)
		if err != nil {
			return err
		}
		if strings.ContainsRune(string(raw), 0) {
			return errors.New("这是一个二进制文件，无法作为文本读取")
		}
		lines := strings.Split(string(raw), "\n")
		total = len(lines)
		from = req.FromLine
		if from <= 0 {
			from = 1
		}
		if from > total {
			return fmt.Errorf("起始行 %d 超过文件总行数 %d", from, total)
		}
		count := req.Lines
		if count <= 0 {
			count = 160
		}
		end = from + count
		if end > total {
			end = total
		}
		text = strings.Join(lines[from-1:end], "\n")
		rev = revisionFor(raw)
		return nil
	})
	if err != nil {
		return LocalResult{}, err
	}
	truncated := end < total
	if len([]rune(text)) > 32000 {
		text = clipRunesLocal(text, 32000)
		truncated = true
	}
	head := fmt.Sprintf("文件 %s（共 %d 行，显示 %d–%d）\nrevision: %s\n\n", req.Path, total, from, end-1, rev)
	return LocalResult{Text: head + text, Revision: rev, Truncated: truncated}, nil
}

// write creates a NEW file (O_EXCL semantics via Root) or, with a correct
// revision, replaces an existing one atomically. Overwriting an existing file
// without its current revision is refused: a stale save must never clobber a
// concurrent edit.
func (e *LocalEngine) write(root string, req LocalRequest) (LocalResult, error) {
	if strings.TrimSpace(req.Path) == "" {
		return LocalResult{}, errors.New("请指定文件路径")
	}
	rel, err := cleanRelPath(req.Path)
	if err != nil {
		return LocalResult{}, err
	}
	if protectedPath(rel) {
		return LocalResult{}, errors.New("不能写入敏感文件")
	}
	err = withRoot(root, func(r *os.Root) error {
		// No symlink may appear as a path component.
		if err := rejectSymlinkComponents(r, rel); err != nil {
			return err
		}
		st, err := r.Lstat(rel)
		switch {
		case err == nil:
			if st.Mode()&os.ModeSymlink != 0 {
				return errors.New("目标路径是符号链接")
			}
			if st.IsDir() {
				return errors.New("目标路径是一个目录")
			}
			if !st.Mode().IsRegular() {
				return errors.New("只能写入普通文件")
			}
			// Existing file: require the exact current revision and write
			// atomically. The revision check and the rename run under the
			// engine's write lock, so a concurrent save cannot interleave.
			if req.Revision == "" {
				return errors.New("文件已存在：请用 read 取得 revision 后用 edit 修改，避免覆盖无关内容")
			}
			return writeExistingLocked(r, e, rel, req.Revision, []byte(req.Content))
		case errors.Is(err, os.ErrNotExist):
			// New file: MkdirAll the parent inside the Root (refuses symlink
			// escapes by construction), then create with O_EXCL so a file
			// created concurrently is not silently overwritten.
			if dir := filepath.Dir(rel); dir != "." {
				if err := r.MkdirAll(dir, localDirMode); err != nil {
					return err
				}
			}
			f, err := r.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, localFileMode)
			if err != nil {
				if errors.Is(err, os.ErrExist) {
					return errors.New("文件已被并发创建：请用 read 取得 revision 后用 edit 修改")
				}
				return err
			}
			defer f.Close()
			if _, err := f.Write([]byte(req.Content)); err != nil {
				return err
			}
			return nil
		default:
			return err
		}
	})
	if err != nil {
		return LocalResult{}, err
	}
	return LocalResult{Text: "已写入 " + req.Path + "（" + fmt.Sprint(len(req.Content)) + " 字节）", Revision: revisionFor([]byte(req.Content))}, nil
}

// writeMu serializes revisioned full-file saves across the process (finding
// 18: two editors saving the same file must not interleave).
var writeMu sync.Mutex

// randomTempName generates a fresh, unguessable temp name in the same
// directory as rel. Predictable names (`rel + ".onefrog-tmp"`) would let any
// process sharing the account pre-create or race the sentinel between create
// and rename.
func randomTempName(rel string) string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failing means the process CSPRNG is broken; refuse
		// rather than fall back to a guessable name.
		panic("workspace: crypto/rand unavailable: " + err.Error())
	}
	dir := filepath.Dir(rel)
	name := "." + filepath.Base(rel) + ".onefrog-tmp-" + hex.EncodeToString(buf[:])
	if dir == "." {
		return name
	}
	return dir + string(filepath.Separator) + name
}

// writeExistingLocked replaces rel's content only while its bytes still hash
// to expected. The temp file and rename are Root-confined.
func writeExistingLocked(r *os.Root, e *LocalEngine, rel, expected string, content []byte) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	st, err := r.Lstat(rel)
	if err != nil {
		return err
	}
	// Preserve the original file's permission bits across the rename.
	perm := st.Mode().Perm()
	raw, err := r.ReadFile(rel)
	if err != nil {
		return err
	}
	if revisionFor(raw) != expected {
		return errors.New("文件已被其他人修改（revision 不匹配），请重新读取后再保存")
	}
	// Same-directory O_EXCL temp with a random name: no O_TRUNC on a
	// predictable path, and the sentinel cannot be hijacked mid-save.
	tmp := randomTempName(rel)
	f, err := r.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if errors.Is(err, os.ErrExist) {
		// Astronomically unlikely with a CSPRNG; a single retry keeps the
		// invariant without making the name predictable.
		tmp = randomTempName(rel)
		f, err = r.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		_ = r.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	if err := r.Rename(tmp, rel); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	return nil
}

func (e *LocalEngine) edit(root string, req LocalRequest) (LocalResult, error) {
	rel, err := cleanRelPath(req.Path)
	if err != nil {
		return LocalResult{}, err
	}
	if protectedPath(rel) {
		return LocalResult{}, errors.New("不能编辑敏感文件")
	}
	updated := ""
	err = withRoot(root, func(r *os.Root) error {
		// No symlink may appear as a path component.
		if err := rejectSymlinkComponents(r, rel); err != nil {
			return err
		}
		st, err := r.Lstat(rel)
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return errors.New("目标路径是符号链接")
		}
		if st.IsDir() || !st.Mode().IsRegular() {
			return errors.New("只能编辑普通文件")
		}
		raw, err := r.ReadFile(rel)
		if err != nil {
			return err
		}
		if rev := revisionFor(raw); req.Revision != rev {
			return errors.New("文件已被修改，revision 不匹配；请重新读取后再编辑")
		}
		content := string(raw)
		count := strings.Count(content, req.Old)
		if count == 0 {
			return errors.New("没有找到要替换的原文片段")
		}
		if count > 1 {
			return fmt.Errorf("原文片段出现了 %d 次，必须提供唯一片段", count)
		}
		next := strings.Replace(content, req.Old, req.New, 1)
		// Serialize read-check-replace-rename so a concurrent edit cannot
		// slip between revision check and write.
		writeMu.Lock()
		defer writeMu.Unlock()
		check, err := r.ReadFile(rel)
		if err != nil {
			return err
		}
		if revisionFor(check) != req.Revision {
			return errors.New("文件已被修改，revision 不匹配；请重新读取后再编辑")
		}
		// Preserve the original file's permissions; the temp file is
		// created O_EXCL with a random same-directory name.
		tmp := randomTempName(rel)
		f, err := r.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, st.Mode().Perm())
		if errors.Is(err, os.ErrExist) {
			tmp = randomTempName(rel)
			f, err = r.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, st.Mode().Perm())
		}
		if err != nil {
			return err
		}
		if _, err := f.Write([]byte(next)); err != nil {
			f.Close()
			_ = r.Remove(tmp)
			return err
		}
		if err := f.Close(); err != nil {
			_ = r.Remove(tmp)
			return err
		}
		if err := r.Rename(tmp, rel); err != nil {
			_ = r.Remove(tmp)
			return err
		}
		updated = next
		return nil
	})
	if err != nil {
		return LocalResult{}, err
	}
	return LocalResult{Text: "已编辑 " + req.Path, Revision: revisionFor([]byte(updated))}, nil
}

func (e *LocalEngine) list(ctx context.Context, root, rel string) (LocalResult, error) {
	base, err := cleanRelPath(rel)
	if err != nil {
		return LocalResult{}, err
	}
	// "." (the root itself) is allowed for listing; any protected component
	// deeper is refused.
	if protectedPath(base) {
		return LocalResult{}, errors.New("不能访问敏感文件")
	}
	var files []string
	truncated := false
	deadline := time.Now().Add(walkTimeout)
	err = withRoot(root, func(r *os.Root) error {
		// A symlinked parent inside the tree could alias a protected
		// directory; refuse symlink components in the requested path.
		if err := rejectSymlinkComponents(r, base); err != nil {
			return err
		}
		st, err := r.Stat(base)
		if err != nil {
			return err
		}
		if !st.IsDir() {
			return errors.New("list 的目标必须是目录")
		}
		return walkRoot(ctx, r, base, 0, &deadline, func(name string, d os.DirEntry, depth int) error {
			if len(files) >= MaxLocalList {
				truncated = true
				return errStopWalk
			}
			if d.IsDir() {
				if isLocalProtectedName(name) {
					return filepath.SkipDir
				}
				files = append(files, name+"/")
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				files = append(files, name+" ->")
				return nil
			}
			files = append(files, name)
			return nil
		})
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return LocalResult{}, err
	}
	sort.Strings(files)
	out := LocalResult{Files: files, Truncated: truncated}
	if rel == "" || rel == "." {
		out.Text = fmt.Sprintf("工作区根目录共列出 %d 项", len(files))
	}
	return out, nil
}

var errStopWalk = errors.New("stop")

// walkRoot is a depth-bounded walk inside an os.Root. It never follows
// symlinks (DirEntry.Type reports them without resolution) and skips
// protected names wherever they appear.
func walkRoot(ctx context.Context, r *os.Root, dir string, depth int, deadline *time.Time, fn func(name string, d os.DirEntry, depth int) error) error {
	if depth > maxWalkDepth {
		return nil
	}
	if time.Now().After(*deadline) || ctx.Err() != nil {
		return errStopWalk
	}
	f, err := r.Open(dir)
	if err != nil {
		return nil // unreadable dirs are skipped, not fatal
	}
	entries, err := f.ReadDir(-1)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if ctx.Err() != nil {
			return errStopWalk
		}
		path := filepath.Join(dir, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 {
			if err := fn(path, entry, depth); err != nil {
				if errors.Is(err, filepath.SkipDir) {
					continue
				}
				return err
			}
			continue
		}
		if err := fn(path, entry, depth); err != nil {
			if errors.Is(err, errStopWalk) {
				return err
			}
			if errors.Is(err, filepath.SkipDir) {
				continue
			}
			return err
		}
		if entry.IsDir() {
			if isLocalProtectedName(entry.Name()) {
				continue
			}
			if err := walkRoot(ctx, r, path, depth+1, deadline, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *LocalEngine) search(ctx context.Context, root, needle string) (LocalResult, error) {
	if strings.TrimSpace(needle) == "" {
		return LocalResult{}, errors.New("搜索词不能为空")
	}
	var files []string
	truncated := false
	deadline := time.Now().Add(walkTimeout)
	lower := strings.ToLower(needle)
	err := withRoot(root, func(r *os.Root) error {
		return walkRoot(ctx, r, ".", 0, &deadline, func(name string, d os.DirEntry, depth int) error {
			if len(files) >= 200 {
				truncated = true
				return errStopWalk
			}
			if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if isLocalProtectedName(name) || isLocalProtectedName(filepath.Dir(name)) {
				return nil
			}
			st, err := d.Info()
			if err != nil || st.Size() > 1<<20 {
				return nil
			}
			raw, err := r.ReadFile(name)
			if err != nil {
				return nil
			}
			if bytes.Contains(bytes.ToLower(raw), []byte(lower)) {
				files = append(files, name)
			}
			return nil
		})
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return LocalResult{}, err
	}
	sort.Strings(files)
	out := LocalResult{Files: files, Truncated: truncated}
	out.Text = fmt.Sprintf("包含 %q 的文件：%d 个", clipRunesLocal(needle, 40), len(files))
	return out, nil
}

// gitStatus/gitDiff run the real git commands for a git-backed local root.
// Output is bounded and secrets inside .git stay unreachable to file tools;
// git porcelain output only exposes what git itself reports.
func (e *LocalEngine) gitStatus(root string) (LocalResult, error) {
	res, err := e.run(context.Background(), root, "git status --porcelain -b")
	if err != nil {
		return LocalResult{}, err
	}
	if res.ExitCode != nil && *res.ExitCode != 0 {
		return LocalResult{ExitCode: res.ExitCode, Text: "git status 失败：" + strings.TrimSpace(res.Text)}, nil
	}
	res.Text = strings.TrimPrefix(res.Text, "stdout:\n")
	return res, nil
}

func (e *LocalEngine) gitDiff(root string) (LocalResult, error) {
	res, err := e.run(context.Background(), root, "git --no-pager diff --stat && git --no-pager diff -- . ':(exclude)*.pem' ':(exclude)*.key'")
	if err != nil {
		return LocalResult{}, err
	}
	if res.ExitCode != nil && *res.ExitCode != 0 {
		return LocalResult{ExitCode: res.ExitCode, Text: "git diff 失败：" + strings.TrimSpace(res.Text)}, nil
	}
	res.Text = strings.TrimPrefix(res.Text, "stdout:\n")
	return res, nil
}

// run executes one command in the exact target root with the process's OS
// account authority. This is explicitly NOT a sandbox. Output is captured
// through bounded writers at collection time (not clipped afterwards), the
// process GROUP is killed on timeout/cancel so children never outlive the
// call, and the environment is sanitized.
func (e *LocalEngine) run(ctx context.Context, root, command string) (LocalResult, error) {
	if strings.TrimSpace(command) == "" {
		return LocalResult{}, errors.New("命令不能为空")
	}
	timeout := e.runTimeout
	if timeout <= 0 {
		timeout = DefaultLocalRunTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr, code, truncated, err := e.exec(runCtx, root, command)
	res := LocalResult{ExitCode: &code, Truncated: truncated}
	var b strings.Builder
	if stdout != "" {
		b.WriteString("stdout:\n" + stdout + "\n")
	}
	if stderr != "" {
		b.WriteString("stderr:\n" + stderr + "\n")
	}
	if truncated {
		b.WriteString(fmt.Sprintf("（输出超过 %d 字节，已截断）\n", MaxLocalOutput))
	}
	if err != nil {
		res.Text = b.String() + "执行失败：" + err.Error()
		return res, nil // the failure IS the result; not a transport error
	}
	res.Text = b.String()
	if res.Text == "" {
		res.Text = fmt.Sprintf("命令执行完成，退出码 %d", code)
	}
	return res, nil
}

// exec runs the command via the injected runner or the native process
// executor. Returns bounded stdout/stderr, the exit code and whether output
// was truncated.
func (e *LocalEngine) exec(ctx context.Context, dir, command string) (string, string, int, bool, error) {
	if e.runner != nil {
		stdout, stderr, code, err := e.runner(ctx, dir, command)
		return stdout, stderr, code, false, err
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	// New process group: on timeout/cancel the whole group (children included)
	// is killed, so no descendant holds the output pipes forever.
	cmd.SysProcAttr = procGroupAttr()
	// Sanitized environment: only PATH/HOME/TERM/LANG pass through, so
	// commands cannot inherit OneFrog's own process secrets.
	cmd.Env = sanitizedEnv()
	stdout := newBoundedWriter(MaxLocalOutput)
	stderr := newBoundedWriter(MaxLocalOutput / 2)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = nil
	runErr := cmd.Run()
	code := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			code = ee.ExitCode()
		} else {
			return stdout.String(), stderr.String(), -1, stdout.truncated || stderr.truncated, runErr
		}
	}
	return stdout.String(), stderr.String(), code, stdout.truncated || stderr.truncated, nil
}

// boundedWriter collects at most n bytes and discards the rest immediately,
// so an unbounded command cannot balloon memory before clipping.
type boundedWriter struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
	dropped   int64
}

func newBoundedWriter(limit int) *boundedWriter { return &boundedWriter{limit: limit} }

func (w *boundedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	space := w.limit - w.buf.Len()
	if space <= 0 {
		w.truncated = true
		w.dropped += int64(len(p))
		return len(p), nil
	}
	if len(p) > space {
		w.buf.Write(p[:space])
		w.truncated = true
		w.dropped += int64(len(p) - space)
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

func (w *boundedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func firstNonEmptyLocal(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func clipRunesLocal(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// keep io imported for future streaming work
var _ = io.Discard

// RawReadResult is the editor-facing raw file shape: the exact bytes on disk
// plus the revision needed to save them. No excerpt header, no truncation.
type RawReadResult struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Revision  string `json:"revision"`
	Size      int    `json:"size"`
	Truncated bool   `json:"truncated,omitempty"`
}

// RawRead returns the FULL raw file for the workspace editor. It is confined
// by the same os.Root rules as every other action and refuses protected
// names; oversized files are refused rather than truncated so the editor
// never saves a header or a clipped file back.
func (e *LocalEngine) RawRead(root, path string) (RawReadResult, error) {
	if _, err := e.allowed(root); err != nil {
		return RawReadResult{}, err
	}
	target, err := NormalizeRoot(root)
	if err != nil {
		return RawReadResult{}, err
	}
	// Same guarantee as Do: the resolved real location must still sit inside
	// the allowlist, so a symlinked root that lands outside it is refused.
	if st, serr := os.Stat(target); serr != nil || !st.IsDir() {
		return RawReadResult{}, errors.New("工作区根目录不存在或不是目录")
	}
	if resolved, rerr := filepath.EvalSymlinks(target); rerr == nil {
		if err := e.assertCovered(resolved); err != nil {
			return RawReadResult{}, err
		}
	}
	rel, err := cleanRelPath(path)
	if err != nil {
		return RawReadResult{}, err
	}
	if protectedPath(rel) {
		return RawReadResult{}, errors.New("不能读取敏感文件")
	}
	out := RawReadResult{Path: path}
	err = withRoot(target, func(r *os.Root) error {
		// No symlink may appear as a path component.
		if err := rejectSymlinkComponents(r, rel); err != nil {
			return err
		}
		st, err := r.Lstat(rel)
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 || st.IsDir() || !st.Mode().IsRegular() {
			return errors.New("只能读取普通文件")
		}
		if st.Size() > MaxLocalFile {
			return errors.New("文件超过编辑器大小上限，请在工作区中编辑")
		}
		raw, err := r.ReadFile(rel)
		if err != nil {
			return err
		}
		if strings.ContainsRune(string(raw), 0) {
			return errors.New("这是一个二进制文件，无法在编辑器中打开")
		}
		out.Content = string(raw)
		out.Size = len(raw)
		out.Revision = revisionFor(raw)
		return nil
	})
	if err != nil {
		return RawReadResult{}, err
	}
	return out, nil
}

// RawSave replaces a file's full content with revision checking. An existing
// file requires the exact current revision (stale editor saves are refused);
// a new file is created with O_EXCL. Both paths write atomically via a
// Root-confined temp file and rename.
func (e *LocalEngine) RawSave(root, path, content, revision string) (RawReadResult, error) {
	if _, err := e.allowed(root); err != nil {
		return RawReadResult{}, err
	}
	target, err := NormalizeRoot(root)
	if err != nil {
		return RawReadResult{}, err
	}
	// Same guarantee as Do: the resolved real location must still sit inside
	// the allowlist, so a symlinked root that lands outside it is refused.
	if st, serr := os.Stat(target); serr != nil || !st.IsDir() {
		return RawReadResult{}, errors.New("工作区根目录不存在或不是目录")
	}
	if resolved, rerr := filepath.EvalSymlinks(target); rerr == nil {
		if err := e.assertCovered(resolved); err != nil {
			return RawReadResult{}, err
		}
	}
	if strings.TrimSpace(path) == "" {
		return RawReadResult{}, errors.New("请指定文件路径")
	}
	if len(content) > MaxLocalFile {
		return RawReadResult{}, errors.New("文件超过编辑器大小上限")
	}
	rel, err := cleanRelPath(path)
	if err != nil {
		return RawReadResult{}, err
	}
	if protectedPath(rel) {
		return RawReadResult{}, errors.New("不能写入敏感文件")
	}
	err = withRoot(target, func(r *os.Root) error {
		// No symlink may appear as a path component.
		if err := rejectSymlinkComponents(r, rel); err != nil {
			return err
		}
		st, err := r.Lstat(rel)
		switch {
		case err == nil:
			if st.Mode()&os.ModeSymlink != 0 {
				return errors.New("目标路径是符号链接")
			}
			if st.IsDir() || !st.Mode().IsRegular() {
				return errors.New("只能保存普通文件")
			}
			if revision == "" {
				return errors.New("缺少 revision：请重新打开文件后再保存，避免覆盖他人的修改")
			}
			return writeExistingLocked(r, e, rel, revision, []byte(content))
		case errors.Is(err, os.ErrNotExist):
			if dir := filepath.Dir(rel); dir != "." {
				if err := r.MkdirAll(dir, localDirMode); err != nil {
					return err
				}
			}
			f, err := r.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, localFileMode)
			if err != nil {
				if errors.Is(err, os.ErrExist) {
					return errors.New("文件已被其他人创建：请重新打开后再保存")
				}
				return err
			}
			defer f.Close()
			_, err = f.Write([]byte(content))
			return err
		default:
			return err
		}
	})
	if err != nil {
		return RawReadResult{}, err
	}
	return RawReadResult{Path: path, Content: content, Size: len(content), Revision: revisionFor([]byte(content))}, nil
}
