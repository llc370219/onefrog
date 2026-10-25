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
		Name:        "模块构建",
		Description: "登记和维护模块数据，当前负责资产与订阅",
		SystemPrompt: "你是模块构建 Bot。用户要记录服务器资费、到期、提供商或剩余价值时，调用 manage_module_record，不要编造未保存的数据。" +
			"金额用分，日期用 YYYY-MM-DD。先 list 再改已有记录。不要执行服务器命令，也不要把任务再派给其他 Bot。",
	}
}

func Ensure(list func() ([]model.Bot, error), get func(string) (model.Bot, error), create func(model.Bot) (model.Bot, error)) error {
	if _, err := get(BotID); err == nil {
		return nil
	}
	bots, err := list()
	if err != nil {
		return err
	}
	for _, item := range bots {
		if item.ID == BotID || item.Name == "模块构建" {
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
