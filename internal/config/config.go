package config

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
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
	}
	if cfg.DatabaseURL == "" && cfg.Driver == "postgres" {
		cfg.DatabaseURL = "postgres://onefrog:onefrog@127.0.0.1:5432/onefrog?sslmode=disable"
	}
	key, err := masterKey()
	if err != nil {
		return Config{}, err
	}
	cfg.MasterKey = key
	return cfg, nil
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
