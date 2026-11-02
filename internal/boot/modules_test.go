package boot

import (
	"context"
	"errors"
	"testing"

	"onefrog/internal/config"
	"onefrog/internal/model"
	"onefrog/internal/store"
)

func TestRetireActivityCardPreservesHistoryAndCustomModules(t *testing.T) {
	for _, kind := range []string{"builtin", "", "custom"} {
		t.Run("type="+kind, func(t *testing.T) {
			ctx := context.Background()
			db := store.NewMemory()
			if _, err := db.SaveModule(ctx, model.Module{ID: "activity", Name: "最近操作", Type: kind, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			entry, err := db.AddHistory(ctx, store.HistoryRec{History: model.History{Action: "历史操作记录", Status: "success"}})
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{AdminUser: "admin", AdminPassword: "test-password"}
			for range 2 {
				if err := Prepare(ctx, db, cfg); err != nil {
					t.Fatal(err)
				}
			}
			_, err = db.GetModule(ctx, "activity")
			if kind == "custom" {
				if err != nil {
					t.Fatal("custom module was removed", err)
				}
			} else if !errors.Is(err, store.ErrNotFound) {
				t.Fatal("legacy card still registered", err)
			}
			history, err := db.GetHistory(ctx, entry.ID)
			if err != nil || history.Action != "历史操作记录" {
				t.Fatal("audit history was modified", err)
			}
		})
	}
}
