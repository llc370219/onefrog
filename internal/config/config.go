package config

import (
	"crypto/rand"
	"encoding/base64"
	"onefrog/internal/migration"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	DataRoot             string
	PublicURL            string
	Addr                 string
	DatabaseURL          string
	Driver               string
	MasterKey            []byte
	WebDir               string
	AdminUser            string
	AdminPassword        string
	ForceCredentialSetup bool
	Seed                 bool
	CookieSecure         bool
	ModelBaseURL         string
	ModelName            string
	DataDir              string
	ModelAPIKey          string
	// ApprovalMode (OF_APPROVAL_MODE) presets the approval mode for headless
	// installs. It only applies while no mode has been chosen.
	ApprovalMode string
	// UpdateCheck is false when OF_UPDATE_CHECK=0 (no requests to GitHub).
	UpdateCheck bool
	// UpdateRepo (OF_UPDATE_REPO) is the owner/name whose releases are checked.
	UpdateRepo string
	// Channel (OF_CHANNEL) is the release channel: "stable" or "beta".
	Channel string
	// LocalWorkspace is the ENV-only operator policy for native local
	// workspaces: an explicit allowlist of project roots plus an opt-in for
	// command execution. It is loaded once at boot and cannot be widened from
	// the settings API, the model or a running task.
	LocalWorkspace LocalWorkspacePolicy
}

// LocalWorkspacePolicy is the immutable, boot-time local workspace policy.
type LocalWorkspacePolicy struct {
	// Roots are absolute project directories. Empty means the feature is off.
	Roots []string
	// CommandExecution opts in to shell/command runs (still with the process's
	// OS account authority; never a sandbox).
	CommandExecution bool
}

func Load() (Config, error) {
	loadEnvFile(".env")
	cfg := Config{
		PublicURL:            strings.TrimRight(os.Getenv("OF_PUBLIC_URL"), "/"),
		DataDir:              env("OF_DATA_DIR", "data"),
		Addr:                 env("OF_HTTP_ADDR", ":8080"),
		DatabaseURL:          os.Getenv("DATABASE_URL"),
		Driver:               env("OF_DRIVER", "postgres"),
		WebDir:               env("OF_WEB_DIR", "web/dist"),
		AdminUser:            env("OF_ADMIN_USER", "admin"),
		AdminPassword:        os.Getenv("OF_ADMIN_PASSWORD"),
		ForceCredentialSetup: os.Getenv("OF_FORCE_CREDENTIAL_SETUP") == "1",
		Seed:                 os.Getenv("OF_SEED") == "1",
		CookieSecure:         os.Getenv("OF_COOKIE_SECURE") == "1",
		ModelBaseURL:         strings.TrimSpace(os.Getenv("OF_MODEL_BASE_URL")),
		ModelName:            strings.TrimSpace(os.Getenv("OF_MODEL_NAME")),
		ModelAPIKey:          strings.TrimSpace(os.Getenv("OF_MODEL_API_KEY")),
		ApprovalMode:         strings.ToLower(strings.TrimSpace(os.Getenv("OF_APPROVAL_MODE"))),
		UpdateCheck:          !isOff(os.Getenv("OF_UPDATE_CHECK")),
		UpdateRepo:           strings.TrimSpace(os.Getenv("OF_UPDATE_REPO")),
		Channel:              strings.TrimSpace(os.Getenv("OF_CHANNEL")),
	}
	// OF_LOCAL_WORKSPACE_ROOTS: a colon-separated allowlist of absolute
	// project roots ("": feature off). Paths are normalized; a bad entry is a
	// boot error rather than a silently narrowed allowlist.
	for _, raw := range strings.Split(os.Getenv("OF_LOCAL_WORKSPACE_ROOTS"), ":") {
		root := strings.TrimSpace(raw)
		if root == "" {
			continue
		}
		if !filepath.IsAbs(root) || strings.ContainsRune(root, 0) {
			return Config{}, errString("OF_LOCAL_WORKSPACE_ROOTS 中的路径必须是绝对路径：" + root)
		}
		clean := filepath.Clean(root)
		if clean == "/" {
			return Config{}, errString("OF_LOCAL_WORKSPACE_ROOTS 不能包含文件系统根 /")
		}
		dup := false
		for _, have := range cfg.LocalWorkspace.Roots {
			if have == clean {
				dup = true
				break
			}
		}
		if !dup {
			cfg.LocalWorkspace.Roots = append(cfg.LocalWorkspace.Roots, clean)
		}
	}
	// OF_LOCAL_COMMAND_EXECUTION=1: shell runs are a separate operator
	// decision because they execute with the server process's OS account
	// authority (NOT a sandbox).
	cfg.LocalWorkspace.CommandExecution = os.Getenv("OF_LOCAL_COMMAND_EXECUTION") == "1" && len(cfg.LocalWorkspace.Roots) > 0
	root, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return Config{}, err
	}
	cfg.DataRoot = root
	cfg.DataDir, err = migration.Resolve(root)
	if err != nil {
		return Config{}, err
	}
	if cfg.DatabaseURL == "" && cfg.Driver == "postgres" {
		cfg.DatabaseURL = "postgres://onefrog:onefrog@127.0.0.1:5432/onefrog?sslmode=disable"
	}
	var key []byte
	if cfg.DataDir != cfg.DataRoot {
		key, err = os.ReadFile(filepath.Join(cfg.DataDir, "master.key"))
		if err == nil && len(key) != 32 {
			err = errString("恢复数据主密钥无效")
		}
	} else {
		key, err = masterKey()
	}
	if err != nil {
		return Config{}, err
	}
	cfg.MasterKey = key
	return cfg, nil
}

// isOff reports whether an on/off environment value means off.
func isOff(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func loadEnvFile(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		key, val, _ := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if key != "" && os.Getenv(key) == "" {
			_ = os.Setenv(key, val)
		}
	}
}

func masterKey() ([]byte, error) {
	if raw := strings.TrimSpace(os.Getenv("OF_MASTER_KEY")); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(key) != 32 {
			return nil, errString("OF_MASTER_KEY 需要是 32 字节的 base64")
		}
		return key, nil
	}
	dir := env("OF_DATA_DIR", "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "master.key")
	if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
		return b, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

type errString string

func (e errString) Error() string { return string(e) }
