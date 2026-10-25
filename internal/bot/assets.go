package bot

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"onefrog/internal/asset"
	"onefrog/internal/model"
	"onefrog/internal/modulebuilder"
)

func (a *Agent) managesAssets(item model.Bot) bool {
	return item.ID == modulebuilder.BotID || item.Name == "模块构建"
}

func (a *Agent) writeAssets(b *strings.Builder, item model.Bot) {
	if !a.managesAssets(item) || a.Assets == nil {
		return
	}
	rows := a.Assets.List()
	if len(rows) > 30 {
		b.WriteString("资产记录超过 30 条，这里只给出最早到期的 30 条。完整内容用 manage_module_record 的 list。\n")
		rows = rows[:30]
	}
	b.WriteString(modulebuilder.Format(rows))
	b.WriteString("\n修改资产时调用 manage_module_record。update 只提交变化字段。不要执行服务器命令，也不要派工。\n")
}

func (a *Agent) manageAsset(call toolCall) string {
	if a.Assets == nil {
		return "资产模块未就绪"
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(call.Function.Arguments), &raw); err != nil {
		return "参数不是 JSON"
	}
	action := strings.ToLower(strings.TrimSpace(jsonText(raw["action"])))
	if action == "" {
		action = "list"
	}
	idv := strings.TrimSpace(jsonText(raw["id"]))
	in := asset.Input{Currency: "CNY", Cycle: "monthly", Status: asset.StatusActive}
	if action == "update" {
		old, err := a.Assets.Get(idv)
		if err != nil {
			return err.Error()
		}
		in = inputFrom(old)
	}
	if err := applyAsset(&in, raw); err != nil {
		return err.Error()
	}
	res, err := modulebuilder.Act(a.Assets, action, idv, in)
	if err != nil {
		return err.Error()
	}
	if !res.OK {
		return res.Message
	}
	if res.ID == "" {
		return res.Message
	}
	row, err := a.Assets.Get(res.ID)
	if err != nil {
		return res.Message
	}
	return fmt.Sprintf("%s。剩余 %d 天，剩余价值 %s，状态 %s", res.Message, row.DaysLeft, moneyText(row.ResidualCents, row.Currency), statusText(row.DerivedStatus))
}

func inputFrom(row asset.View) asset.Input {
	tags := append([]string{}, row.Tags...)
	return asset.Input{
		ServerID: row.ServerID, Name: row.Name, Provider: row.Provider, Region: row.Region, Plan: row.Plan,
		Cycle: row.Cycle, Currency: row.Currency, AmountCents: row.AmountCents, PaidAt: row.PaidAt,
		StartsAt: row.StartsAt, ExpiresAt: row.ExpiresAt, AutoRenew: row.AutoRenew, Status: row.Status,
		IP: row.IP, Specs: row.Specs, OrderNo: row.OrderNo, Account: row.Account, InvoiceURL: row.InvoiceURL,
		Contact: row.Contact, Tags: tags, Notes: row.Notes,
	}
}

func applyAsset(in *asset.Input, raw map[string]json.RawMessage) error {
	set := func(key string, dest *string) {
		if v, ok := raw[key]; ok {
			*dest = jsonText(v)
		}
	}
	set("server_id", &in.ServerID)
	set("name", &in.Name)
	set("provider", &in.Provider)
	set("region", &in.Region)
	set("plan", &in.Plan)
	set("cycle", &in.Cycle)
	set("currency", &in.Currency)
	set("paid_at", &in.PaidAt)
	set("starts_at", &in.StartsAt)
	set("expires_at", &in.ExpiresAt)
	set("status", &in.Status)
	set("ip", &in.IP)
	set("specs", &in.Specs)
	set("order_no", &in.OrderNo)
	set("account", &in.Account)
	set("invoice_url", &in.InvoiceURL)
	set("contact", &in.Contact)
	set("notes", &in.Notes)
	if v, ok := raw["auto_renew"]; ok {
		var flag bool
		if err := json.Unmarshal(v, &flag); err != nil {
			return fmt.Errorf("auto_renew 必须是布尔值")
		}
		in.AutoRenew = flag
	}
	if v, ok := raw["amount_yuan"]; ok {
		cents, err := yuanToCents(v)
		if err != nil {
			return err
		}
		in.AmountCents = cents
	} else if v, ok := raw["amount_cents"]; ok {
		var cents int64
		if err := json.Unmarshal(v, &cents); err != nil {
			return fmt.Errorf("amount_cents 必须是整数")
		}
		in.AmountCents = cents
	}
	if v, ok := raw["tags"]; ok {
		tags, err := tagList(v)
		if err != nil {
			return err
		}
		in.Tags = tags
	}
	return nil
}

func yuanToCents(raw json.RawMessage) (int64, error) {
	var number float64
	if err := json.Unmarshal(raw, &number); err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		text := strings.TrimSpace(jsonText(raw))
		parsed, err2 := strconv.ParseFloat(text, 64)
		if err2 != nil {
			return 0, fmt.Errorf("amount_yuan 必须是数字")
		}
		number = parsed
	}
	cents := int64(math.Round(number * 100))
	if math.Abs(number*100-float64(cents)) > 0.001 {
		return 0, fmt.Errorf("金额最多两位小数")
	}
	return cents, nil
}

func tagList(raw json.RawMessage) ([]string, error) {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list, nil
	}
	text := jsonText(raw)
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '，' || r == ';' || r == '；' })
	return parts, nil
}

func jsonText(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	return strings.Trim(string(raw), "\"")
}

func moneyText(cents int64, currency string) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%s %d.%02d", sign, currency, cents/100, cents%100)
}

func statusText(status string) string {
	switch status {
	case asset.StatusExpiring:
		return "即将到期"
	case asset.StatusExpired:
		return "已到期"
	case asset.StatusCancelled:
		return "已停用"
	default:
		return "在用"
	}
}
