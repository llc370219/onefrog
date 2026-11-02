package bot

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"onefrog/internal/devmodule"
	"onefrog/internal/model"
	"onefrog/internal/module"
	"onefrog/internal/modulebuilder"
	"onefrog/internal/preferences"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

func TestModuleBuilderMigrationAndDevelopLifecycle(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	box, err := secret.New(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range module.Catalog() {
		_, _ = mem.SaveModule(ctx, m)
	}
	// Seed legacy "模块构建" bot and verify Ensure renames it to "模块管理".
	if _, err := mem.CreateBot(ctx, model.Bot{
		ID:           modulebuilder.BotID,
		Name:         "模块构建",
		Description:  "登记和维护模块数据，当前负责资产与订阅",
		SystemPrompt: "你是模块构建 Bot。",
	}); err != nil {
		t.Fatal(err)
	}
	if err := modulebuilder.Ensure(
		func() ([]model.Bot, error) { return mem.ListBots(ctx) },
		func(id string) (model.Bot, error) { return mem.GetBot(ctx, id) },
		func(b model.Bot) (model.Bot, error) { return mem.CreateBot(ctx, b) },
		func(b model.Bot) (model.Bot, error) { return mem.UpdateBot(ctx, b) },
	); err != nil {
		t.Fatal(err)
	}
	migrated, err := mem.GetBot(ctx, modulebuilder.BotID)
	if err != nil || migrated.Name != "模块管理" || strings.Contains(migrated.SystemPrompt, "模块构建") {
		t.Fatalf("migration failed: %+v %v", migrated, err)
	}
	if err := devmodule.Ensure(
		func() ([]model.Bot, error) { return mem.ListBots(ctx) },
		func(id string) (model.Bot, error) { return mem.GetBot(ctx, id) },
		func(b model.Bot) (model.Bot, error) { return mem.CreateBot(ctx, b) },
	); err != nil {
		t.Fatal(err)
	}
	devBot, err := mem.GetBot(ctx, devmodule.BotID)
	if err != nil || devBot.Name != "二次开发" {
		t.Fatalf("dev bot missing: %+v %v", devBot, err)
	}

	// Store a vault secret to ensure custom module creation redacts vault values.
	enc, _ := box.Encrypt("super-secret-token-42")
	_, _ = mem.CreateSecret(ctx, store.SecretRec{
		Secret:         model.Secret{Name: "api-tok"},
		EncryptedValue: enc,
	})

	book, err := devmodule.New("", box)
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := preferences.New("", box)
	agent := &Agent{
		Store:        mem,
		Box:          box,
		DevModules:   book,
		Settings:     settings,
		SettingsLive: settings,
	}

	// Reject secret fields in develop_module arguments.
	badCall := toolCall{}
	badCall.Function.Name = "develop_module"
	badCall.Function.Arguments = `{"action":"create","name":"bad","html":"<div>x</div>","api_key":"123"}`
	if out := agent.execCall(ctx, devBot, credentials{}, "", badCall, false, nil); !strings.Contains(out, "不接受") {
		t.Fatalf("expected secret field rejection, got %q", out)
	}

	// Create a check-in module via 二次开发 Bot; vault secret must be redacted.
	createCall := toolCall{}
	createCall.Function.Name = "develop_module"
	createCall.Function.Arguments = `{"action":"create","name":"打卡","description":"点击记录打卡时间","html":"<div class=\"of-card\">打卡 super-secret-token-42</div>"}`
	out := agent.execCall(ctx, devBot, credentials{}, "", createCall, false, nil)
	if !strings.Contains(out, "已创建二次开发模块「打卡」") || strings.Contains(out, "super-secret-token-42") {
		t.Fatalf("unexpected create output: %q", out)
	}
	saved, err := book.Get("打卡")
	if err != nil || strings.Contains(saved.HTML, "super-secret-token-42") {
		t.Fatalf("vault secret leaked into custom module HTML: %+v %v", saved, err)
	}
	modInStore, err := mem.GetModule(ctx, saved.ID)
	if err != nil || modInStore.Type != "custom" || !modInStore.Enabled {
		t.Fatalf("custom module not synced to store: %+v %v", modInStore, err)
	}

	// 模块管理 Bot can list/toggle modules and add/read records of the custom module.
	addRec := toolCall{}
	addRec.Function.Name = "manage_modules"
	addRec.Function.Arguments = `{"action":"add_record","id":"打卡","data":{"note":"首次打卡"}}`
	if out := agent.execCall(ctx, migrated, credentials{}, "", addRec, false, nil); !strings.Contains(out, "共 1 条") {
		t.Fatalf("manage_modules add_record: %q", out)
	}
	toggleMod := toolCall{}
	toggleMod.Function.Name = "manage_modules"
	toggleMod.Function.Arguments = `{"action":"disable","id":"打卡"}`
	if out := agent.execCall(ctx, migrated, credentials{}, "", toggleMod, false, nil); !strings.Contains(out, "已关闭模块 打卡") {
		t.Fatalf("manage_modules disable: %q", out)
	}

	// Update requires approval and preserves the existing record.
	deniedCtx := context.WithValue(ctx, optionsKey{}, Options{
		Approver: func(context.Context, ApprovalRequest) (bool, string) { return false, "暂不修改" },
	})
	updCall := toolCall{}
	updCall.Function.Name = "develop_module"
	updCall.Function.Arguments = `{"action":"update","id":"打卡","html":"<div>v2</div>"}`
	if out := agent.execCall(deniedCtx, devBot, credentials{}, "", updCall, false, nil); !strings.Contains(out, "没有修改模块") {
		t.Fatalf("expected denial, got %q", out)
	}
	approvedCtx := context.WithValue(ctx, optionsKey{}, Options{
		Approver: func(context.Context, ApprovalRequest) (bool, string) { return true, "同意" },
	})
	if out := agent.execCall(approvedCtx, devBot, credentials{}, "", updCall, false, nil); !strings.Contains(out, "原有 1 条记录已保留") {
		t.Fatalf("expected update keeping record, got %q", out)
	}

	// 二次开发 Bot must not execute server commands.
	sshCall := toolCall{}
	sshCall.Function.Name = "run_command"
	sshCall.Function.Arguments = `{"command":"id","risk":"low","reason":"test"}`
	if out := agent.execCall(ctx, devBot, credentials{}, "", sshCall, false, nil); !strings.Contains(out, "不能执行服务器命令") {
		t.Fatalf("expected ssh refusal, got %q", out)
	}

	// Delete requires approval and removes from both Book and Store.
	delCall := toolCall{}
	delCall.Function.Name = "develop_module"
	delCall.Function.Arguments = `{"action":"delete","id":"打卡"}`
	if out := agent.execCall(approvedCtx, devBot, credentials{}, "", delCall, false, nil); !strings.Contains(out, "已删除二次开发模块") {
		t.Fatalf("delete failed: %q", out)
	}
	if _, err := mem.GetModule(ctx, saved.ID); err == nil {
		t.Fatal("deleted custom module still in store")
	}
}
