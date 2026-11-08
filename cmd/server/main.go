package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"onefrog/internal/api"
	"onefrog/internal/asset"
	"onefrog/internal/attachment"
	"onefrog/internal/automation"
	"onefrog/internal/boot"
	"onefrog/internal/bot"
	"onefrog/internal/config"
	"onefrog/internal/devmodule"
	"onefrog/internal/maintenance"
	"onefrog/internal/migration"
	"onefrog/internal/monitor"
	"onefrog/internal/onboarding"
	"onefrog/internal/preferences"
	"onefrog/internal/providerlogin"
	"onefrog/internal/remote"
	"onefrog/internal/secret"
	"onefrog/internal/services"
	"onefrog/internal/store"
	"onefrog/internal/task"
	"onefrog/internal/update"
	"onefrog/internal/usage"
	"onefrog/internal/version"
	"onefrog/internal/workspace"
	"os"
	"path/filepath"
	"sync"
	"time"
)

func main() {
	for {
		run()
	}
}

func run() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	box, err := secret.New(cfg.MasterKey)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := openStore(ctx, cfg, box)
	if err != nil {
		log.Fatal(err)
	}
	if pg, ok := db.(*store.Postgres); ok {
		defer pg.Close()
	}
	gate, err := maintenance.New(cfg.DataDir)
	if err != nil {
		log.Fatal(err)
	}
	reload := make(chan struct{})
	var reloadOnce sync.Once
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
	if cfg.ApprovalMode != "" && settings.Snapshot().Approval.Mode == preferences.ModeUnset {
		switch cfg.ApprovalMode {
		case preferences.ModeOff, preferences.ModeSmart, preferences.ModeManual, preferences.ModePlan:
			if _, err := settings.SetApprovalMode(cfg.ApprovalMode); err != nil {
				log.Fatal(err)
			}
			log.Printf("approval mode preset from OF_APPROVAL_MODE: %s", cfg.ApprovalMode)
		default:
			log.Fatalf("OF_APPROVAL_MODE must be off, smart, manual or plan, got %q", cfg.ApprovalMode)
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
	devModules, err := devmodule.New(filepath.Join(cfg.DataDir, "devmodules.enc"), box)
	if err != nil {
		log.Fatal(err)
	}
	if err := devmodule.SyncStore(ctx, db, devModules); err != nil {
		log.Fatal(err)
	}
	channel, ok := update.NormalizeChannel(cfg.Channel)
	if !ok {
		log.Fatalf("OF_CHANNEL must be stable or beta, got %q", cfg.Channel)
	}
	updates := &update.Checker{
		Repo:             cfg.UpdateRepo,
		Channel:          channel,
		Current:          version.Version,
		EnvDisabled:      !cfg.UpdateCheck,
		SettingsDisabled: func() bool { return settings.Snapshot().UpdateCheckDisabled },
		SettingsChannel:  func() string { return settings.Snapshot().UpdateChannel },
		HTTP:             &http.Client{Timeout: 15 * time.Second},
	}
	if !update.ValidRepo(updates.Repo) && updates.Repo != "" {
		log.Fatalf("OF_UPDATE_REPO must look like owner/name, got %q", updates.Repo)
	}
	go updates.Run(ctx)
	log.Printf("OneFrog %s", version.Version)
	files := attachment.New(filepath.Join(cfg.DataDir, "uploads"), box)
	watch, err := services.NewWatch(filepath.Join(cfg.DataDir, "servicewatch.enc"), box, db)
	if err != nil {
		log.Fatal(err)
	}
	autos, err := automation.New(filepath.Join(cfg.DataDir, "automations.enc"), box, db)
	if err != nil {
		log.Fatal(err)
	}
	tasks, err := task.New(filepath.Join(cfg.DataDir, "tasks.enc"), box)
	if err != nil {
		log.Fatal(err)
	}
	app := &api.Server{
		Migration:    &api.Migration{Root: cfg.DataRoot, Dir: cfg.DataDir, Key: cfg.MasterKey, Driver: cfg.Driver, PublicURL: cfg.PublicURL, Gate: gate, Restart: func() { reloadOnce.Do(func() { close(reload) }) }},
		ServiceWatch: watch,
		Automations:  autos,
		Tasks:        nil,
		Updates:      updates,
		OAuth:        oauth,
		Onboarding:   onboard,
		Assets:       ledger,
		DevModules:   devModules,
		PublicURL:    cfg.PublicURL,
		Files:        files,
		Usage:        usageLog,
		Monitor:      probe,
		Store:        db,
		Settings:     settings,
		Box:          box,
		Secure:       cfg.CookieSecure,
		WebDir:       cfg.WebDir,
		// OF_TRUSTED_PROXIES: reverse proxies whose X-Forwarded-For is trusted for login rate limiting.
		TrustedProxies: os.Getenv("OF_TRUSTED_PROXIES"),
		Agent: &bot.Agent{
			Workspace: &workspace.Service{Store: db, Box: box, Run: remote.RunInput},
			// Native local workspaces: the allowlist is ENV-only operator
			// policy (OF_LOCAL_WORKSPACE_ROOTS), loaded once at boot. Nothing
			// in the settings API, the model or a task may widen it.
			LocalWorkspace: workspace.NewLocalEngine(workspace.Roots{
				Enabled:          len(cfg.LocalWorkspace.Roots) > 0,
				CommandExecution: cfg.LocalWorkspace.CommandExecution,
				Dirs:             cfg.LocalWorkspace.Roots,
			}, nil),
			Store:        db,
			Settings:     settings,
			SettingsLive: settings,
			Box:          box,
			SSH:          remoteExec{},
			HTTP:         &http.Client{Timeout: 60 * time.Second},
			OAuth:        oauth,
			Files:        files,
			Usage:        usageLog,
			Assets:       ledger,
			DevModules:   devModules,
			Updates:      updates,
			Onboarding:   onboard,
			Probes:       probe,
			PublicURL:    cfg.PublicURL,
			Mail:         bot.NewMailbox(),
			Tasks:        tasks,
		},
	}
	runner := bot.NewRunner(app.Agent, tasks)
	// Durable background callers (mailbox, automation, submission) find the
	// runner through this field. Without it they silently fall back to the
	// non-durable synchronous path.
	app.Agent.TasksRunner = runner
	app.Tasks = runner
	log.Printf("listening on %s", cfg.Addr)
	// Handler attaches the shared inventory scanner before polling starts.
	handler := app.Handler()
	go watch.Run(ctx)
	go autos.Run(ctx)
	go runner.Run(ctx)
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No ReadTimeout/WriteTimeout: chat requests and WebSocket sessions may legitimately
		// run for minutes; request bodies are bounded with MaxBytesReader instead.
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 64 << 10,
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- srv.ListenAndServe() }()
	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	case <-reload:
		cancel()
		stop, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer stopCancel()
		if err := srv.Shutdown(stop); err != nil {
			log.Fatal(err)
		}
		<-serverErr
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
		if err = m.EnablePersistence(cfg.DataDir, box); err != nil {
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
	if cfg.DataDir != cfg.DataRoot {
		raw, e := os.ReadFile(filepath.Join(cfg.DataDir, migration.DatabaseName))
		if e != nil {
			pg.Close()
			return nil, e
		}
		var dump store.DatabaseBackup
		if e = json.Unmarshal(raw, &dump); e != nil {
			pg.Close()
			return nil, e
		}
		if e = pg.ImportDatabaseOnce(ctx, filepath.Base(cfg.DataDir), dump); e != nil {
			pg.Close()
			return nil, e
		}
	}
	return pg, nil
}

type remoteExec struct{}

func (remoteExec) Run(ctx context.Context, host string, port int, user, authType, secret, knownKey, command string) (string, string, error) {
	return remote.Run(ctx, host, port, user, authType, secret, knownKey, command)
}
