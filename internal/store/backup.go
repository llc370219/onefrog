package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// The order respects foreign keys. Browser login sessions deliberately do not
// travel to another machine; the original account/password still works there.
var backupTables = []string{"users", "servers", "bots", "secrets", "modules", "operation_history", "chat_sessions", "messages", "bot_memories"}

type DatabaseBackup map[string]json.RawMessage

func (p *Postgres) ExportDatabase(ctx context.Context) (DatabaseBackup, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	out := DatabaseBackup{}
	for _, name := range backupTables {
		var raw []byte
		err = tx.QueryRow(ctx, "SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY id), '[]'::jsonb) FROM "+pgx.Identifier{name}.Sanitize()+" t").Scan(&raw)
		if err != nil {
			return nil, err
		}
		out[name] = raw
	}
	return out, tx.Commit(ctx)
}

func (p *Postgres) ImportDatabase(ctx context.Context, data DatabaseBackup) error {
	return p.restoreDatabase(ctx, data, false, "")
}

func (p *Postgres) ImportDatabaseOnce(ctx context.Context, id string, data DatabaseBackup) error {
	return p.restoreDatabase(ctx, data, false, id)
}

// ValidateDatabase checks constraints in a rollback-only transaction. No data
// from the receiving instance is committed until the new generation starts.
func (p *Postgres) ValidateDatabase(ctx context.Context, data DatabaseBackup) error {
	return p.restoreDatabase(ctx, data, true, "")
}

func (p *Postgres) restoreDatabase(ctx context.Context, data DatabaseBackup, validate bool, receipt string) error {
	if len(data) != len(backupTables) {
		return errors.New("数据库备份表不完整")
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if receipt != "" {
		if _, err = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS onefrog_restore_receipts (id TEXT PRIMARY KEY)"); err != nil {
			return err
		}
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM onefrog_restore_receipts WHERE id=$1)", receipt).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		if _, err = tx.Exec(ctx, "INSERT INTO onefrog_restore_receipts (id) VALUES ($1)", receipt); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "DELETE FROM sessions"); err != nil {
		return err
	}
	for i := len(backupTables) - 1; i >= 0; i-- {
		if _, err = tx.Exec(ctx, "DELETE FROM "+pgx.Identifier{backupTables[i]}.Sanitize()); err != nil {
			return err
		}
	}
	for _, name := range backupTables {
		raw, ok := data[name]
		if !ok || !json.Valid(raw) {
			return fmt.Errorf("数据库备份缺少 %s", name)
		}
		ident := pgx.Identifier{name}.Sanitize()
		if _, err = tx.Exec(ctx, "INSERT INTO "+ident+" SELECT * FROM jsonb_populate_recordset(NULL::"+ident+", $1::jsonb)", string(raw)); err != nil {
			return err
		}
	}
	var n int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return errors.New("备份没有账号")
	}
	if validate {
		return tx.Rollback(ctx)
	}
	return tx.Commit(ctx)
}
