package bot

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"onefrog/internal/asset"
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
		func(b model.Bot) (model.Bot, error) { return mem.UpdateBot(ctx, b) },
	); err != nil {
		t.Fatal(err)
	}
	devBot, err := mem.GetBot(ctx, devmodule.BotID)
	if err != nil || devBot.Name != "自定义模块" {
		t.Fatalf("dev bot missing: %+v %v", devBot, err)
	}
	// Verify legacy "二次开发" name migrates to "自定义模块".
	devBot.Name = "二次开发"
	_, _ = mem.UpdateBot(ctx, devBot)
	_ = devmodule.Ensure(
		func() ([]model.Bot, error) { return mem.ListBots(ctx) },
		func(id string) (model.Bot, error) { return mem.GetBot(ctx, id) },
		func(b model.Bot) (model.Bot, error) { return mem.CreateBot(ctx, b) },
		func(b model.Bot) (model.Bot, error) { return mem.UpdateBot(ctx, b) },
	)
	devBot, _ = mem.GetBot(ctx, devmodule.BotID)
	if devBot.Name != "自定义模块" {
		t.Fatalf("legacy develop bot not renamed: %+v", devBot)
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

	// Create a check-in module via 自定义模块 Bot; vault secret must be redacted.
	createCall := toolCall{}
	createCall.Function.Name = "develop_module"
	createCall.Function.Arguments = `{"action":"create","name":"打卡","description":"点击记录打卡时间","html":"<div class=\"of-card\">打卡 super-secret-token-42</div>"}`
	out := agent.execCall(ctx, devBot, credentials{}, "", createCall, false, nil)
	if !strings.Contains(out, "已创建自定义模块「打卡」") || strings.Contains(out, "super-secret-token-42") {
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
	if out := agent.execCall(deniedCtx, devBot, credentials{}, "", updCall, false, nil); !strings.Contains(out, "没有修改模块") && !strings.Contains(out, "暂不修改") {
		t.Fatalf("expected denial, got %q", out)
	}
	approvedCtx := context.WithValue(ctx, optionsKey{}, Options{
		Approver: func(context.Context, ApprovalRequest) (bool, string) { return true, "同意" },
	})
	if out := agent.execCall(approvedCtx, devBot, credentials{}, "", updCall, false, nil); !strings.Contains(out, "原有 1 条记录已保留") {
		t.Fatalf("expected update keeping record, got %q", out)
	}

	// 自定义模块 Bot must not execute server commands.
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
	if out := agent.execCall(approvedCtx, devBot, credentials{}, "", delCall, false, nil); !strings.Contains(out, "已删除自定义模块") {
		t.Fatalf("delete failed: %q", out)
	}
	if _, err := mem.GetModule(ctx, saved.ID); err == nil {
		t.Fatal("deleted custom module still in store")
	}
}

func TestConversationalTimelineAndInlineApproval(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	box, _ := secret.New(bytes.Repeat([]byte{4}, 32))
	book, _ := devmodule.New("", box)
	_, _ = book.Create(devmodule.Input{Name: "打卡", HTML: "<div>v1</div>"})

	var turn atomic.Int32
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if turn.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"收到，正在为你升级打卡模块界面。","tool_calls":[{"id":"c1","type":"function","function":{"name":"develop_module","arguments":"{\"action\":\"update\",\"id\":\"打卡\",\"html\":\"<div>v2</div>\"}"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"打卡模块已升级完成，原有记录保留不变。"}}]}`))
	}))
	defer llm.Close()

	enc, _ := box.Encrypt("test-key")
	sec, _ := mem.CreateSecret(ctx, store.SecretRec{Secret: model.Secret{Name: "k"}, EncryptedValue: enc})
	leader, _ := mem.CreateBot(ctx, model.Bot{
		ID: "main", Name: "主控", ModelProvider: "openai", ModelName: "gpt-test", BaseURL: llm.URL, APIKeySecretID: sec.ID,
	})

	agent := &Agent{Store: mem, Box: box, DevModules: book, HTTP: llm.Client()}
	var streamed []string
	res, err := agent.ReplyWithOptions(ctx, leader.ID, "", "", "升级打卡模块", Options{
		Approver: func(context.Context, ApprovalRequest) (bool, string) { return true, "always" },
	}, func(s Step) {
		streamed = append(streamed, s.Kind)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Timeline) < 3 {
		t.Fatalf("expected say + approval + step in timeline, got %+v", res.Timeline)
	}
	hasSay, hasAlwaysApproval := false, false
	for _, item := range res.Timeline {
		if item.Kind == "say" && strings.Contains(item.Text, "正在为你升级") {
			hasSay = true
		}
		if item.Kind == "approval" && item.Approval != nil && item.Approval.Status == "always" {
			hasAlwaysApproval = true
		}
	}
	if !hasSay || !hasAlwaysApproval {
		t.Fatalf("missing say=%v or always approval=%v in %+v (streamed=%v)", hasSay, hasAlwaysApproval, res.Timeline, streamed)
	}
	msgs, _ := mem.ListMessages(ctx, res.SessionID)
	if len(msgs) != 2 || len(msgs[1].Timeline) != len(res.Timeline) {
		t.Fatalf("persisted messages missing timeline: %+v", msgs)
	}
}

func TestCrossBotLinkageAndAutoLinkAssets(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	box, _ := secret.New(bytes.Repeat([]byte{6}, 32))
	ledger, _ := asset.New("", box)
	book, _ := devmodule.New("", box)

	// Register an asset before the server exists, then add the server and verify AutoLink.
	createdAsset, err := ledger.Create(asset.Input{
		Name:        "la-node",
		IP:          "198.51.100.25",
		Cycle:       "yearly",
		Currency:    "USD",
		AmountCents: 4900,
		StartsAt:    "2026-01-01",
		ExpiresAt:   "2026-12-31",
	})
	if err != nil {
		t.Fatal(err)
	}
	srvRec, err := mem.CreateServer(ctx, store.ServerRec{
		Server: model.Server{ID: "srv-la", Name: "la-node", Host: "198.51.100.25", Port: 22, Username: "root", AuthType: "password"},
	})
	if err != nil {
		t.Fatal(err)
	}
	servers, _ := mem.ListServers(ctx)
	if n := ledger.AutoLink(servers); n != 1 {
		t.Fatalf("expected 1 auto-linked asset, got %d", n)
	}
	linked, _ := ledger.Get(createdAsset.ID)
	if linked.ServerID != srvRec.ID {
		t.Fatalf("asset ServerID=%q want %q", linked.ServerID, srvRec.ID)
	}

	// Verify 模块管理 Bot answers the simple question first in a say bubble and delegates module creation to 自定义模块 Bot.
	var stepNum atomic.Int32
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch stepNum.Add(1) {
		case 1:
			// 模块管理 Bot answers the simple question first and hands off custom module build to develop.
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"目前在册资产共 1 台（la-node）。我这就协同自定义模块 Bot 帮你创建巡检打卡卡片。","tool_calls":[{"id":"d1","type":"function","function":{"name":"ask_agent","arguments":"{\"bot_id\":\"develop\",\"task\":\"创建巡检打卡模块\"}"}}]}}]}`))
		case 2:
			// 自定义模块 Bot creates the custom module.
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"develop_module","arguments":"{\"action\":\"create\",\"name\":\"巡检打卡\",\"html\":\"<div>ok</div>\"}"}}]}}]}`))
		case 3:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"已创建巡检打卡模块并启用。"}}]}`))
		default:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"巡检打卡模块已经建好并展示在面板上了。"}}]}`))
		}
	}))
	defer llm.Close()

	enc, _ := box.Encrypt("test-key")
	sec, _ := mem.CreateSecret(ctx, store.SecretRec{Secret: model.Secret{Name: "k"}, EncryptedValue: enc})
	modMgr, _ := mem.CreateBot(ctx, model.Bot{
		ID: modulebuilder.BotID, Name: "模块管理", ModelProvider: "openai", ModelName: "gpt-test", BaseURL: llm.URL, APIKeySecretID: sec.ID,
	})
	_, _ = mem.CreateBot(ctx, model.Bot{
		ID: devmodule.BotID, Name: "自定义模块", ModelProvider: "openai", ModelName: "gpt-test", BaseURL: llm.URL, APIKeySecretID: sec.ID,
	})

	agent := &Agent{Store: mem, Box: box, Assets: ledger, DevModules: book, HTTP: llm.Client()}
	res, err := agent.ReplyWithOptions(ctx, modMgr.ID, "", "", "现在有几台在册资产？顺便创建一个巡检打卡模块", Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := book.Get("巡检打卡"); err != nil {
		t.Fatalf("delegated custom module was not created: %v", err)
	}
	if len(res.Timeline) == 0 || res.Timeline[0].Kind != "say" || !strings.Contains(res.Timeline[0].Text, "共 1 台") {
		t.Fatalf("expected simple question answered first in timeline[0], got %+v", res.Timeline)
	}
}
