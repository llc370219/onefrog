package api

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"onefrog/internal/asset"
	"onefrog/internal/automation"
	"onefrog/internal/devmodule"
	"onefrog/internal/migration"
	"onefrog/internal/monitor"
	"onefrog/internal/onboarding"
	"onefrog/internal/preferences"
	"onefrog/internal/providerlogin"
	"onefrog/internal/secret"
	"onefrog/internal/services"
	"onefrog/internal/store"
	"onefrog/internal/usage"
)

func (s *Server) validateRestore(ctx context.Context, dir string, files map[string][]byte) error {
	box, err := secret.New(files["master.key"])
	if err != nil {
		return err
	}
	var db store.Store
	if s.Migration.Driver == "memory" {
		mem, err := store.NewMemoryAccounts(filepath.Join(dir, "accounts.json"))
		if err != nil {
			return errors.New("备份账号无法读取")
		}
		if err = mem.EnableServerPersistence(filepath.Join(dir, "servers.enc"), box); err != nil {
			return err
		}
		if err = mem.EnableBotPersistence(filepath.Join(dir, "bots.enc"), box); err != nil {
			return err
		}
		if err = mem.EnablePersistence(dir, box); err != nil {
			return err
		}
		n, err := mem.CountUsers(ctx)
		if err != nil || n == 0 {
			return errors.New("备份中没有可登录账号")
		}
		db = mem
	} else {
		var dump store.DatabaseBackup
		if err = json.Unmarshal(files[migration.DatabaseName], &dump); err != nil {
			return err
		}
		pg, ok := s.Store.(*store.Postgres)
		if !ok {
			return errors.New("数据库类型不匹配")
		}
		if err = pg.ValidateDatabase(ctx, dump); err != nil {
			return errors.New("数据库备份结构或记录校验失败")
		}
		db = pg
	}
	checks := []func() error{
		func() error { _, e := preferences.New(filepath.Join(dir, "settings.enc"), box); return e },
		func() error { _, e := providerlogin.New(filepath.Join(dir, "oauth.enc"), box); return e },
		func() error { _, e := usage.New(filepath.Join(dir, "usage.enc"), box); return e },
		func() error { _, e := asset.New(filepath.Join(dir, "assets.enc"), box); return e },
		func() error { _, e := devmodule.New(filepath.Join(dir, "devmodules.enc"), box); return e },
		func() error { _, e := services.NewWatch(filepath.Join(dir, "servicewatch.enc"), box, db); return e },
		func() error { _, e := automation.New(filepath.Join(dir, "automations.enc"), box, db); return e },
		func() error { _, e := monitor.New(filepath.Join(dir, "monitor.json")); return e },
		func() error {
			_, e := onboarding.New(filepath.Join(dir, "onboarding.json"), db, box, nil, nil)
			return e
		},
	}
	for _, check := range checks {
		if err = check(); err != nil {
			return errors.New("备份文件结构校验失败，原实例未修改")
		}
	}
	return nil
}
