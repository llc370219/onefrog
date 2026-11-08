package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Native local workspace execution: read/list/search/write/edit/run directly
// in the OneFrog server process, on paths the operator explicitly allowlisted.
// This is NOT a sandbox — the process runs with its OS account's authority and
// every command executes with that account's full permissions. cwd confinement
// only bounds path resolution, not privileges. Command execution is opt-in via
// configuration and every exec still goes through the shared approval hook.

const (
	// MaxLocalFile bounds one write/edit payload, matching MaxFile for SSH.
	MaxLocalFile = 256 << 10
	// MaxLocalRead is the most bytes one read returns.
	MaxLocalRead = 128 << 10
	// MaxLocalList bounds one directory listing.
	MaxLocalList = 2000
	// MaxLocalOutput bounds command output captured from a run.
	MaxLocalOutput = 32 << 10
	// DefaultLocalRunTimeout bounds one command run.
	DefaultLocalRunTimeout = 2 * time.Minute
)

// LocalTarget identifies a local workspace: the operator-allowlisted root the
// task works inside.
type LocalTarget struct {
	Root string `json:"root"`
}

// Roots is the operator-configured allowlist of absolute directories local
// workspaces may operate in. It is configuration, never a per-request input:
// nothing the model or the browser sends may add to it at runtime.
type Roots struct {
	// Enabled gates the whole local workspace feature. Disabled means no root
	// is usable regardless of the list.
	Enabled bool `json:"enabled"`
	// CommandExecution gates the run action only. Reads/writes/edits are still
	// bound to the allowlisted roots; shell execution is a separate, explicit
	// operator decision because it runs with the OS account's authority.
	CommandExecution bool `json:"command_execution"`
	// Dirs are absolute paths. A task may work inside a dir or any
	// subdirectory of it.
	Dirs []string `json:"dirs"`
}

// Normalize trims and cleans one allowlist entry, rejecting anything that is
// not an absolute path.
func NormalizeRoot(raw string) (string, error) {
	root := strings.TrimSpace(raw)
	if root == "" || len(root) > 1024 {
		return "", errors.New("本地工作区根目录必须是 1–1024 字的绝对路径")
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("本地工作区根目录必须是绝对路径")
	}
	if strings.ContainsRune(root, 0) {
		return "", errors.New("本地工作区根目录不能包含 NUL")
	}
	clean := filepath.Clean(root)
	if clean == "/" {
		return "", errors.New("本地工作区根目录不能是文件系统根 /")
	}
	return clean, nil
}

// Validate checks the whole allowlist.
func (r Roots) Validate() error {
	if !r.Enabled && len(r.Dirs) > 0 {
		// Keeping a disabled list is allowed (operator may toggle back), so
		// only entries that would be active are validated strictly below.
	}
	seen := map[string]bool{}
	for _, d := range r.Dirs {
		clean, err := NormalizeRoot(d)
		if err != nil {
			return err
		}
		if seen[clean] {
			return fmt.Errorf("本地工作区根目录重复：%s", clean)
		}
		seen[clean] = true
	}
	return nil
}

// containsRoot reports whether target is the allowed root itself or a path
// strictly inside one (lexically, before symlink resolution).
func containsRoot(allowed, target string) bool {
	if allowed == target {
		return true
	}
	return strings.HasPrefix(target, allowed+string(filepath.Separator))
}

// Pick resolves the allowlist entry that covers root. It returns an error when
// the feature is off or no entry covers the target.
func (r Roots) Pick(root string) (string, error) {
	if !r.Enabled {
		return "", errors.New("本地工作区未启用：请由操作员在配置中开启并填写允许的根目录")
	}
	clean, err := NormalizeRoot(root)
	if err != nil {
		return "", err
	}
	for _, d := range r.Dirs {
		cleanAllowed, err := NormalizeRoot(d)
		if err != nil {
			continue
		}
		if containsRoot(cleanAllowed, clean) {
			return cleanAllowed, nil
		}
	}
	return "", errors.New("路径不在允许的本地工作区根目录内")
}

// Validate checks a local request target.
func (t LocalTarget) Validate() error {
	_, err := NormalizeRoot(t.Root)
	return err
}

// errEscapedRoot is returned when a resolved path escapes its requested root
// (including through a symlink). All confinement goes through os.Root, which
// resolves every component against the open directory itself, so a symlink
// swapped in after an earlier check cannot redirect an operation outside.
var errEscapedRoot = errors.New("路径越出了工作区根目录")

// openLocalRoot opens the exact requested root for confined access. The root
// itself may be behind a symlink (macOS /tmp → /private/tmp, mountpoints):
// OpenRoot follows the final path, and everything below it is resolved
// relative to the real directory.
func openLocalRoot(root string) (*os.Root, error) {
	clean, err := NormalizeRoot(root)
	if err != nil {
		return nil, err
	}
	return os.OpenRoot(clean)
}

// cleanRelPath lexically validates a relative path and rejects escapes
// before os.Root sees them (Root rejects ".." itself; this gives clearer
// errors). It returns the Root-relative name.
func cleanRelPath(rel string) (string, error) {
	if rel == "" {
		return ".", nil
	}
	if len(rel) > 1024 || filepath.IsAbs(rel) || strings.ContainsRune(rel, 0) {
		return "", errors.New("路径必须是不超过 1024 字的相对路径")
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("路径不能越出工作区根目录")
	}
	return clean, nil
}

// rejectSymlinkComponents verifies that every component of rel below the open
// root is a real filesystem entry, not a symlink. A symlinked parent could
// alias a protected directory (e.g. `cfg -> .ssh`) or redirect an operation
// before os.Root's per-component resolution is consulted, so direct
// read/write/edit/RawRead/RawSave/list refuse any such path outright.
// The root itself is exempt: openLocalRoot already resolved it and Do
// re-verified that the resolved root stays inside the allowlist.
func rejectSymlinkComponents(r *os.Root, rel string) error {
	if rel == "." || rel == "" {
		return nil
	}
	cur := ""
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		if cur == "" {
			cur = part
		} else {
			cur = cur + "/" + part
		}
		st, err := r.Lstat(cur)
		if err != nil {
			// A component that does not exist yet cannot be a symlink
			// alias; creation paths (write/RawSave) legitimately target
			// fresh files. os.Root re-resolves every component at use
			// time, so confinement is unaffected.
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return errors.New("路径包含符号链接组件，已拒绝访问")
		}
	}
	return nil
}

// isLocalProtectedName reports names that must never be read, written or
// traversed by agent tools in a local workspace. The check applies to every
// path component — direct requests and walked trees alike — so a direct read
// of ".env" and a walk that would descend into a "data" directory are both
// refused.
func isLocalProtectedName(name string) bool {
	base := filepath.Base(name)
	// Example/sample templates are legitimate project files, not secrets.
	low := strings.ToLower(base)
	if low == ".env.example" || low == ".env.sample" || low == ".env.template" {
		return false
	}
	switch strings.ToLower(base) {
	case ".git", "master.key", ".env", ".env.local", ".env.production", ".env.development",
		".install.env", "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa",
		"authorized_keys", "known_hosts", ".ssh", ".gnupg", ".aws", ".kube":
		return true
	}
	if strings.HasPrefix(strings.ToLower(base), ".env.") {
		return true
	}
	if strings.HasSuffix(strings.ToLower(base), ".pem") || strings.HasSuffix(strings.ToLower(base), ".p12") ||
		strings.HasSuffix(strings.ToLower(base), ".pfx") || strings.HasSuffix(strings.ToLower(base), ".keystore") {
		return true
	}
	return false
}

// protectedPath reports whether any component of a relative path is
// protected. Used by direct reads/writes/edits/walks so protection cannot be
// bypassed by naming a deeper path.
func protectedPath(rel string) bool {
	if rel == "." || rel == "" {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		if isLocalProtectedName(part) {
			return true
		}
	}
	return false
}

// localFileMode is the file mode agent-created files get.
const localFileMode = 0o600

// localDirMode is the mode agent-created directories get.
const localDirMode = 0o750

// maxWalkDepth bounds recursive listing.
const maxWalkDepth = 12

// walkTimeout bounds one list/search call.
const walkTimeout = 20 * time.Second
