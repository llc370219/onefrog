package bot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"onefrog/internal/asset"
	"onefrog/internal/model"
)

func TestModuleBuilderUpdateKeepsFields(t *testing.T) {
	ledger, err := asset.New("", nil)
	if err != nil {
		t.Fatal(err)
	}
	row, err := ledger.Create(asset.Input{Name: "web-01", Provider: "搬瓦工", Cycle: "yearly", Currency: "USD", AmountCents: 36500, StartsAt: "2026-01-01", ExpiresAt: "2026-12-31", Region: "洛杉矶"})
	if err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Assets: ledger}
	out := agent.manageAsset(toolCall{Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Name: "manage_module_record", Arguments: `{"action":"update","id":"` + row.ID + `","notes":"明年续费"}`}})
	if !strings.Contains(out, "已更新") || !strings.Contains(out, "剩余价值") {
		t.Fatal(out)
	}
	got, err := ledger.Get(row.ID)
	if err != nil || got.Provider != "搬瓦工" || got.AmountCents != 36500 || got.Notes != "明年续费" || got.Region != "洛杉矶" {
		t.Fatalf("%+v %v", got, err)
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(`{"amount_yuan":12.5}`), &raw) != nil {
		t.Fatal("json")
	}
	in := asset.Input{}
	if err = applyAsset(&in, raw); err != nil || in.AmountCents != 1250 {
		t.Fatal(in.AmountCents, err)
	}
}

func TestModuleBuilderRefusesServerCommands(t *testing.T) {
	agent := &Agent{}
	call := toolCall{}
	call.Function.Name = "run_command"
	call.Function.Arguments = "{}"
	out := agent.execCall(context.Background(), model.Bot{ID: "module-builder", Name: "模块构建"}, credentials{}, "", call, true, nil)
	if !strings.Contains(out, "不能执行") {
		t.Fatal(out)
	}
}
