package boot

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"onefrog/internal/config"
	"onefrog/internal/model"
	"onefrog/internal/module"
	"onefrog/internal/modulebuilder"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func Prepare(ctx context.Context, db store.Store, cfg config.Config) error {
	n, err := db.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		password := cfg.AdminPassword
		if password == "" {
			buf := make([]byte, 12)
			if _, err := rand.Read(buf); err != nil {
				return err
			}
			password = base64.RawURLEncoding.EncodeToString(buf)
			log.Printf("初始管理员 %s 密码：%s", cfg.AdminUser, password)
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		created, err := db.CreateUser(ctx, cfg.AdminUser, string(hash))
		if err != nil {
			return err
		}
		if cfg.ForceCredentialSetup {
			created.MustChangeCredentials = true
			if _, err = db.UpdateUser(ctx, created, "", ""); err != nil {
				return err
			}
		}
	}
	for _, item := range module.Catalog() {
		if _, err := db.GetModule(ctx, item.ID); err == nil {
			continue
		}
		if _, err := db.SaveModule(ctx, item); err != nil {
			return err
		}
	}
	if err := modulebuilder.Ensure(
		func() ([]model.Bot, error) { return db.ListBots(ctx) },
		func(id string) (model.Bot, error) { return db.GetBot(ctx, id) },
		func(item model.Bot) (model.Bot, error) { return db.CreateBot(ctx, item) },
	); err != nil {
		return err
	}
	if !cfg.Seed {
		return nil
	}
	count, err := db.CountServers(ctx)
	if err != nil || count > 0 {
		return err
	}
	return seed(ctx, db)
}

func AttachModel(ctx context.Context, db store.Store, box *secret.Box, cfg config.Config) error {
	if cfg.ModelAPIKey == "" || cfg.ModelBaseURL == "" || cfg.ModelName == "" {
		return nil
	}
	enc, err := box.Encrypt(cfg.ModelAPIKey)
	if err != nil {
		return err
	}
	secretID := ""
	if existing, err := db.SecretByName(ctx, "model-key"); err == nil {
		if err := db.DeleteSecret(ctx, existing.ID); err != nil {
			return err
		}
	}
	created, err := db.CreateSecret(ctx, store.SecretRec{
		Secret:         model.Secret{Name: "model-key", Description: "主控模型接口"},
		EncryptedValue: enc,
	})
	if err != nil {
		return err
	}
	secretID = created.ID
	bots, err := db.ListBots(ctx)
	if err != nil {
		return err
	}
	if len(bots) == 0 {
		_, err = db.CreateBot(ctx, model.Bot{
			Name: "主控", Description: "接收指令，分派任务",
			ModelProvider: "openai", ModelName: cfg.ModelName, BaseURL: cfg.ModelBaseURL, APIKeySecretID: secretID,
		})
		return err
	}
	bot := bots[0]
	bot.ModelProvider = "openai"
	bot.ModelName = cfg.ModelName
	bot.BaseURL = cfg.ModelBaseURL
	bot.APIKeySecretID = secretID
	_, err = db.UpdateBot(ctx, bot)
	return err
}

func seed(ctx context.Context, db store.Store) error {
	now := time.Now().UTC()
	specs := []store.ServerRec{
		{Server: model.Server{ID: "web-01", Name: "web-01", Host: "192.168.1.11", Port: 22, Username: "root", AuthType: "password", Status: "online", Role: "nginx", DiskPercent: pin(71), Load: "0.42", CreatedAt: now}},
		{Server: model.Server{ID: "db-01", Name: "db-01", Host: "192.168.1.21", Port: 22, Username: "root", AuthType: "password", Status: "online", Role: "postgres", DiskPercent: pin(44), Load: "0.18", CreatedAt: now.Add(time.Second)}},
		{Server: model.Server{ID: "cache-01", Name: "cache-01", Host: "192.168.1.31", Port: 22, Username: "root", AuthType: "password", Status: "online", Role: "redis", DiskPercent: pin(28), Load: "0.07", CreatedAt: now.Add(2 * time.Second)}},
		{Server: model.Server{ID: "edge-02", Name: "edge-02", Host: "192.168.1.41", Port: 22, Username: "root", AuthType: "password", Status: "offline", Role: "caddy", Load: "—", CreatedAt: now.Add(3 * time.Second)}},
	}
	for _, rec := range specs {
		if _, err := db.CreateServer(ctx, rec); err != nil {
			return err
		}
	}
	mainBot, err := db.GetBot(ctx, "main")
	if errors.Is(err, store.ErrNotFound) {
		mainBot, err = db.CreateBot(ctx, model.Bot{ID: "main", Name: "主控", Description: "接收指令，分派任务", CreatedAt: now})
	}
	if err != nil {
		return err
	}
	checkBot, err := db.GetBot(ctx, "check")
	if errors.Is(err, store.ErrNotFound) {
		checkBot, err = db.CreateBot(ctx, model.Bot{ID: "check", Name: "巡检", Description: "定时看磁盘、证书和服务", CreatedAt: now.Add(time.Second)})
	}
	if err != nil {
		return err
	}
	rows := []store.HistoryRec{
		{History: model.History{BotID: mainBot.ID, ServerID: "web-01", Action: "systemctl reload nginx", Command: "systemctl reload nginx", Status: "success", CanRollback: true, CreatedAt: now.Add(-25 * time.Minute)}},
		{History: model.History{BotID: checkBot.ID, ServerID: "db-01", Action: "df -h /", Command: "df -h /", Status: "success", CreatedAt: now.Add(-44 * time.Minute)}},
		{History: model.History{BotID: mainBot.ID, ServerID: "web-01", Action: "rm -rf /var/log/nginx/*", Command: "rm -rf /var/log/nginx/*", Status: "blocked", CreatedAt: now.Add(-58 * time.Minute)}},
	}
	for _, row := range rows {
		if _, err := db.AddHistory(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

func pin(n int) *int { return &n }
