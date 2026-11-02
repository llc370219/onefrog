package modulebuilder

import (
	"errors"
	"onefrog/internal/asset"
	"onefrog/internal/model"
	"strings"
)

const BotID = "module-builder"

func Bot() model.Bot {
	return model.Bot{
		ID:          BotID,
		Name:        "模块管理",
		Description: "开关面板模块，维护资产与订阅及各模块数据",
		SystemPrompt: "你是模块管理 Bot。可以调用 manage_modules 查看或开关面板上的内置模块与自定义模块、读写自定义模块的记录；" +
			"也可以调用 manage_module_record 维护资产与订阅（先 list 再改，金额用 amount_yuan，日期用 YYYY-MM-DD）。新建自定义交互模块请引导到自定义模块或自定义模块 Bot。",
	}
}

// LegacyName is the name used before v0.1.18; existing installs are renamed.
const LegacyName = "模块构建"

func Ensure(list func() ([]model.Bot, error), get func(string) (model.Bot, error), create func(model.Bot) (model.Bot, error), update func(model.Bot) (model.Bot, error)) error {
	if item, err := get(BotID); err == nil {
		if (item.Name == LegacyName || strings.Contains(item.Description, "当前负责资产与订阅")) && update != nil {
			stock := Bot()
			item.Name = stock.Name
			if item.Description == "" || strings.Contains(item.Description, "当前负责资产与订阅") {
				item.Description = stock.Description
			}
			if strings.Contains(item.SystemPrompt, LegacyName) || strings.Contains(item.SystemPrompt, "金额用分") {
				item.SystemPrompt = stock.SystemPrompt
			}
			_, err = update(item)
			return err
		}
		return nil
	}
	bots, err := list()
	if err != nil {
		return err
	}
	for _, item := range bots {
		if item.Name == LegacyName && update != nil {
			stock := Bot()
			item.Name = stock.Name
			if strings.Contains(item.Description, "当前负责资产与订阅") {
				item.Description = stock.Description
			}
			if strings.Contains(item.SystemPrompt, LegacyName) {
				item.SystemPrompt = strings.ReplaceAll(item.SystemPrompt, LegacyName, stock.Name)
			}
			_, err = update(item)
			return err
		}
		if item.ID == BotID || item.Name == "模块管理" {
			return nil
		}
	}
	_, err = create(Bot())
	return err
}

type Result struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	ID      string `json:"id,omitempty"`
}

func Act(ledger *asset.Ledger, action, recordID string, in asset.Input) (Result, error) {
	if ledger == nil {
		return Result{}, errors.New("资产模块未就绪")
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "list":
		return Result{OK: true, Message: Format(ledger.List())}, nil
	case "create":
		row, err := ledger.Create(in)
		if err != nil {
			return Result{Message: err.Error()}, nil
		}
		return Result{OK: true, ID: row.ID, Message: "已登记 " + row.Name}, nil
	case "update":
		row, err := ledger.Update(recordID, in)
		if err != nil {
			return Result{Message: err.Error()}, nil
		}
		return Result{OK: true, ID: row.ID, Message: "已更新 " + row.Name}, nil
	case "delete":
		row, err := ledger.Get(recordID)
		if err != nil {
			return Result{Message: err.Error()}, nil
		}
		if err = ledger.Delete(recordID); err != nil {
			return Result{Message: err.Error()}, nil
		}
		return Result{OK: true, ID: recordID, Message: "已删除 " + row.Name}, nil
	default:
		return Result{Message: "动作只能是 list、create、update、delete"}, nil
	}
}

func Format(rows []asset.View) string {
	var b strings.Builder
	b.WriteString("资产与订阅模块 assets。")
	if len(rows) == 0 {
		b.WriteString("目前没有记录。")
		return b.String()
	}
	for _, row := range rows {
		b.WriteString("\n- id=" + row.ID + " name=" + row.Name)
		if row.ServerID != "" {
			b.WriteString(" server_id=" + row.ServerID)
		}
		b.WriteString(" provider=" + blank(row.Provider) + " plan=" + blank(row.Plan) + " region=" + blank(row.Region))
		b.WriteString(" cycle=" + row.Cycle + " amount_cents=" + itoa(row.AmountCents) + " " + row.Currency)
		b.WriteString(" starts=" + blank(row.StartsAt) + " expires=" + blank(row.ExpiresAt))
		b.WriteString(" days_left=" + itoa(int64(row.DaysLeft)) + " residual_cents=" + itoa(row.ResidualCents))
		b.WriteString(" status=" + row.DerivedStatus)
		if row.AutoRenew {
			b.WriteString(" auto_renew=true")
		}
		if row.IP != "" {
			b.WriteString(" ip=" + row.IP)
		}
		if row.Specs != "" {
			b.WriteString(" specs=" + row.Specs)
		}
		if row.OrderNo != "" {
			b.WriteString(" order=" + row.OrderNo)
		}
		if row.Notes != "" {
			b.WriteString(" notes=" + clip(row.Notes, 80))
		}
	}
	return b.String()
}

func blank(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

func clip(v string, n int) string {
	r := []rune(v)
	if len(r) <= n {
		return v
	}
	return string(r[:n])
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [24]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
