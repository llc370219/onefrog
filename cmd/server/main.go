package main

import (
	"context"
	"log"
	"net/http"
	"onefrog/internal/api"
	"onefrog/internal/asset"
	"onefrog/internal/attachment"
	"onefrog/internal/boot"
	"onefrog/internal/bot"
	"onefrog/internal/config"
	"onefrog/internal/monitor"
	"onefrog/internal/onboarding"
	"onefrog/internal/preferences"
	"onefrog/internal/providerlogin"
	"onefrog/internal/remote"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"onefrog/internal/usage"
	"os"
	"path/filepath"
	"time"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	box, err := secret.New(cfg.MasterKey)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	db, err := openStore(ctx, cfg, box)
	if err != nil {
		log.Fatal(err)
	}
	if err := boot.Prepare(ctx, db, cfg); err != nil {
		log.Fatal(err)
	}
	settings, err := preferences.New(filepath.Join(cfg.DataDir, "settings.enc"), box)
	if err != nil {
		log.Fatal(err)
	}
	if len(settings.Snapshot().Providers) == 0 {
		bots, _ := db.ListBots(ctx)
		if len(bots) == 0 || bots[0].BaseURL == "" {
			if err := boot.AttachModel(ctx, db, box, cfg); err != nil {
				log.Fatal(err)
			}
		}
		if err := settings.Seed(ctx, db); err != nil {
			log.Fatal(err)
		}
	}
	probe, err := monitor.New(filepath.Join(cfg.DataDir, "monitor.json"))
	if err != nil {
		log.Fatal(err)
	}
	usageLog, err := usage.New(filepath.Join(cfg.DataDir, "usage.enc"), box)
	if err != nil {
		log.Fatal(err)
	}
	onboard, err := onboarding.New(filepath.Join(cfg.DataDir, "onboarding.json"), db, box, probe, remoteExec{})
	if err != nil {
		log.Fatal(err)
	}
	oauth, err := providerlogin.New(filepath.Join(cfg.DataDir, "oauth.enc"), box)
	if err != nil {
		log.Fatal(err)
	}
	ledger, err := asset.New(filepath.Join(cfg.DataDir, "assets.enc"), box)
	if err != nil {
		log.Fatal(err)
	}
	app := &api.Server{
		OAuth:      oauth,
		Onboarding: onboard,
		Assets:     ledger,
		PublicURL:  cfg.PublicURL,
		Files:      attachment.New(filepath.Join(cfg.DataDir, "uploads"), box),
		Usage:      usageLog,
		Monitor:    probe,
		Store:      db,
		Settings:   settings,
		Box:        box,
		Secure:     cfg.CookieSecure,
		WebDir:     cfg.WebDir,
		// OF_TRUSTED_PROXIES: reverse proxies whose X-Forwarded-For is trusted for login rate limiting.
		TrustedProxies: os.Getenv("OF_TRUSTED_PROXIES"),
		Agent: &bot.Agent{
			Store:    db,
			Settings: settings,
			Box:      box,
			SSH:      remoteExec{},
			HTTP:     &http.Client{Timeout: 60 * time.Second},
		},
	}
	log.Printf("listening on %s", cfg.Addr)
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No ReadTimeout/WriteTimeout: chat requests and WebSocket sessions may legitimately
		// run for minutes; request bodies are bounded with MaxBytesReader instead.
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 64 << 10,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func openStore(ctx context.Context, cfg config.Config, box *secret.Box) (store.Store, error) {
	if cfg.Driver == "memory" {
		m, err := store.NewMemoryAccounts(filepath.Join(cfg.DataDir, "accounts.json"))
		if err != nil {
			return nil, err
		}
		if err = m.EnableServerPersistence(filepath.Join(cfg.DataDir, "servers.enc"), box); err != nil {
			return nil, err
		}
		if err = m.EnableBotPersistence(filepath.Join(cfg.DataDir, "bots.enc"), box); err != nil {
			return nil, err
		}
		return m, nil
	}
	pg, err := store.NewPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := pg.Migrate(ctx, store.SchemaSQL); err != nil {
		pg.Close()
		return nil, err
	}
	return pg, nil
}

type remoteExec struct{}

func (remoteExec) Run(ctx context.Context, host string, port int, user, authType, secret, knownKey, command string) (string, string, error) {
	return remote.Run(ctx, host, port, user, authType, secret, knownKey, command)
}
