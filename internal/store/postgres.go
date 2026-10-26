package store

import (
	"context"
	"encoding/json"
	"errors"
	"onefrog/internal/id"
	"onefrog/internal/model"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Close() { p.pool.Close() }

func (p *Postgres) Migrate(ctx context.Context, script string) error {
	for _, stmt := range strings.Split(script, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := p.pool.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return ErrConflict
	}
	return err
}

func (p *Postgres) CreateUser(ctx context.Context, username, passwordHash string) (model.User, error) {
	user := model.User{ID: id.New(), Username: username, CreatedAt: time.Now().UTC()}
	_, err := p.pool.Exec(ctx, "INSERT INTO users (id, username, password_hash, created_at, must_change_credentials) VALUES ($1,$2,$3,$4,$5)", user.ID, user.Username, passwordHash, user.CreatedAt, user.MustChangeCredentials)
	return user, mapErr(err)
}

func (p *Postgres) UserByName(ctx context.Context, username string) (model.User, string, error) {
	var user model.User
	var hash string
	err := p.pool.QueryRow(ctx, "SELECT id, username, password_hash, created_at, display_name, avatar, must_change_credentials FROM users WHERE username=$1", username).Scan(&user.ID, &user.Username, &hash, &user.CreatedAt, &user.DisplayName, &user.Avatar, &user.MustChangeCredentials)
	return user, hash, mapErr(err)
}

func (p *Postgres) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&n)
	return n, err
}

func (p *Postgres) CreateSession(ctx context.Context, userID, tokenHash string, expires time.Time) error {
	_, err := p.pool.Exec(ctx, "INSERT INTO sessions (token_hash, user_id, expires_at, created_at) VALUES ($1,$2,$3,$4)", tokenHash, userID, expires, time.Now().UTC())
	return err
}

func (p *Postgres) UserBySession(ctx context.Context, tokenHash string) (model.User, error) {
	var user model.User
	err := p.pool.QueryRow(ctx, "SELECT u.id, u.username, u.created_at, u.display_name, u.avatar, u.must_change_credentials FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now()", tokenHash).Scan(&user.ID, &user.Username, &user.CreatedAt, &user.DisplayName, &user.Avatar, &user.MustChangeCredentials)
	return user, mapErr(err)
}

func (p *Postgres) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := p.pool.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", tokenHash)
	return err
}

func (p *Postgres) ListServers(ctx context.Context) ([]model.Server, error) {
	rows, err := p.pool.Query(ctx, "SELECT id, name, host, port, username, auth_type, status, role, disk_percent, load_text, created_at FROM servers ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Server
	for rows.Next() {
		item, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanServer(row pgx.Row) (model.Server, error) {
	var item model.Server
	err := row.Scan(&item.ID, &item.Name, &item.Host, &item.Port, &item.Username, &item.AuthType, &item.Status, &item.Role, &item.DiskPercent, &item.Load, &item.CreatedAt)
	return item, err
}

func (p *Postgres) CountServers(ctx context.Context) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, "SELECT count(*) FROM servers").Scan(&n)
	return n, err
}

func (p *Postgres) GetServer(ctx context.Context, idv string) (ServerRec, error) {
	var rec ServerRec
	err := p.pool.QueryRow(ctx, "SELECT id, name, host, port, username, auth_type, status, role, disk_percent, load_text, created_at, encrypted_key, host_key FROM servers WHERE id=$1", idv).Scan(&rec.ID, &rec.Name, &rec.Host, &rec.Port, &rec.Username, &rec.AuthType, &rec.Status, &rec.Role, &rec.DiskPercent, &rec.Load, &rec.CreatedAt, &rec.EncryptedKey, &rec.HostKey)
	return rec, mapErr(err)
}

func (p *Postgres) CreateServer(ctx context.Context, rec ServerRec) (model.Server, error) {
	if rec.ID == "" {
		rec.ID = id.New()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	if rec.Port == 0 {
		rec.Port = 22
	}
	if rec.Status == "" {
		rec.Status = "unknown"
	}
	_, err := p.pool.Exec(ctx, "INSERT INTO servers (id, name, host, port, username, auth_type, encrypted_key, host_key, status, role, disk_percent, load_text, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)", rec.ID, rec.Name, rec.Host, rec.Port, rec.Username, rec.AuthType, rec.EncryptedKey, rec.HostKey, rec.Status, rec.Role, rec.DiskPercent, rec.Load, rec.CreatedAt)
	return rec.Server, mapErr(err)
}

func (p *Postgres) UpdateServer(ctx context.Context, rec ServerRec) (model.Server, error) {
	old, err := p.GetServer(ctx, rec.ID)
	if err != nil {
		return model.Server{}, err
	}
	if rec.EncryptedKey == "" {
		rec.EncryptedKey = old.EncryptedKey
	}
	if rec.HostKey == "" && !rec.ClearHostKey {
		rec.HostKey = old.HostKey
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = old.CreatedAt
	}
	tag, err := p.pool.Exec(ctx, "UPDATE servers SET name=$2, host=$3, port=$4, username=$5, auth_type=$6, encrypted_key=$7, host_key=$8, status=$9, role=$10, disk_percent=$11, load_text=$12 WHERE id=$1", rec.ID, rec.Name, rec.Host, rec.Port, rec.Username, rec.AuthType, rec.EncryptedKey, rec.HostKey, rec.Status, rec.Role, rec.DiskPercent, rec.Load)
	if err != nil {
		return model.Server{}, err
	}
	if tag.RowsAffected() == 0 {
		return model.Server{}, ErrNotFound
	}
	return rec.Server, nil
}

func (p *Postgres) DeleteServer(ctx context.Context, idv string) error {
	tag, err := p.pool.Exec(ctx, "DELETE FROM servers WHERE id=$1", idv)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) ListBots(ctx context.Context) ([]model.Bot, error) {
	rows, err := p.pool.Query(ctx, "SELECT id, name, description, model_provider, model_name, system_prompt, base_url, api_key_secret_id, created_at FROM bots ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Bot
	for rows.Next() {
		var bot model.Bot
		if err := rows.Scan(&bot.ID, &bot.Name, &bot.Description, &bot.ModelProvider, &bot.ModelName, &bot.SystemPrompt, &bot.BaseURL, &bot.APIKeySecretID, &bot.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, bot)
	}
	return out, rows.Err()
}

func (p *Postgres) GetBot(ctx context.Context, idv string) (model.Bot, error) {
	var bot model.Bot
	err := p.pool.QueryRow(ctx, "SELECT id, name, description, model_provider, model_name, system_prompt, base_url, api_key_secret_id, created_at FROM bots WHERE id=$1", idv).Scan(&bot.ID, &bot.Name, &bot.Description, &bot.ModelProvider, &bot.ModelName, &bot.SystemPrompt, &bot.BaseURL, &bot.APIKeySecretID, &bot.CreatedAt)
	return bot, mapErr(err)
}

func (p *Postgres) CreateBot(ctx context.Context, bot model.Bot) (model.Bot, error) {
	if bot.ID == "" {
		bot.ID = id.New()
	}
	if bot.CreatedAt.IsZero() {
		bot.CreatedAt = time.Now().UTC()
	}
	_, err := p.pool.Exec(ctx, "INSERT INTO bots (id, name, description, model_provider, model_name, system_prompt, base_url, api_key_secret_id, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)", bot.ID, bot.Name, bot.Description, bot.ModelProvider, bot.ModelName, bot.SystemPrompt, bot.BaseURL, bot.APIKeySecretID, bot.CreatedAt)
	return bot, mapErr(err)
}

func (p *Postgres) UpdateBot(ctx context.Context, bot model.Bot) (model.Bot, error) {
	tag, err := p.pool.Exec(ctx, "UPDATE bots SET name=$2, description=$3, model_provider=$4, model_name=$5, system_prompt=$6, base_url=$7, api_key_secret_id=$8 WHERE id=$1", bot.ID, bot.Name, bot.Description, bot.ModelProvider, bot.ModelName, bot.SystemPrompt, bot.BaseURL, bot.APIKeySecretID)
	if err != nil {
		return model.Bot{}, err
	}
	if tag.RowsAffected() == 0 {
		return model.Bot{}, ErrNotFound
	}
	return p.GetBot(ctx, bot.ID)
}

func (p *Postgres) DeleteBot(ctx context.Context, idv string) error {
	tag, err := p.pool.Exec(ctx, "DELETE FROM bots WHERE id=$1", idv)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) ListSecrets(ctx context.Context) ([]model.Secret, error) {
	rows, err := p.pool.Query(ctx, "SELECT id, name, description, created_at FROM secrets ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Secret
	for rows.Next() {
		var item model.Secret
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (p *Postgres) GetSecret(ctx context.Context, idv string) (SecretRec, error) {
	var rec SecretRec
	err := p.pool.QueryRow(ctx, "SELECT id, name, description, created_at, encrypted_value FROM secrets WHERE id=$1", idv).Scan(&rec.ID, &rec.Name, &rec.Description, &rec.CreatedAt, &rec.EncryptedValue)
	return rec, mapErr(err)
}

func (p *Postgres) SecretByName(ctx context.Context, name string) (SecretRec, error) {
	var rec SecretRec
	err := p.pool.QueryRow(ctx, "SELECT id, name, description, created_at, encrypted_value FROM secrets WHERE name=$1", name).Scan(&rec.ID, &rec.Name, &rec.Description, &rec.CreatedAt, &rec.EncryptedValue)
	return rec, mapErr(err)
}

func (p *Postgres) CreateSecret(ctx context.Context, rec SecretRec) (model.Secret, error) {
	if rec.ID == "" {
		rec.ID = id.New()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	_, err := p.pool.Exec(ctx, "INSERT INTO secrets (id, name, encrypted_value, description, created_at) VALUES ($1,$2,$3,$4,$5)", rec.ID, rec.Name, rec.EncryptedValue, rec.Description, rec.CreatedAt)
	return rec.Secret, mapErr(err)
}

func (p *Postgres) DeleteSecret(ctx context.Context, idv string) error {
	tag, err := p.pool.Exec(ctx, "DELETE FROM secrets WHERE id=$1", idv)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) AllSecrets(ctx context.Context) ([]SecretRec, error) {
	rows, err := p.pool.Query(ctx, "SELECT id, name, description, created_at, encrypted_value FROM secrets")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecretRec
	for rows.Next() {
		var rec SecretRec
		if err := rows.Scan(&rec.ID, &rec.Name, &rec.Description, &rec.CreatedAt, &rec.EncryptedValue); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (p *Postgres) ListModules(ctx context.Context) ([]model.Module, error) {
	rows, err := p.pool.Query(ctx, "SELECT id, name, version, type, description, enabled, created_at FROM modules ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Module
	for rows.Next() {
		var item model.Module
		if err := rows.Scan(&item.ID, &item.Name, &item.Version, &item.Type, &item.Description, &item.Enabled, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (p *Postgres) GetModule(ctx context.Context, idv string) (model.Module, error) {
	var item model.Module
	err := p.pool.QueryRow(ctx, "SELECT id, name, version, type, description, enabled, created_at FROM modules WHERE id=$1", idv).Scan(&item.ID, &item.Name, &item.Version, &item.Type, &item.Description, &item.Enabled, &item.CreatedAt)
	return item, mapErr(err)
}

func (p *Postgres) SaveModule(ctx context.Context, item model.Module) (model.Module, error) {
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	_, err := p.pool.Exec(ctx, "INSERT INTO modules (id, name, version, type, description, enabled, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (id) DO UPDATE SET name=$2, version=$3, type=$4, description=$5, enabled=$6", item.ID, item.Name, item.Version, item.Type, item.Description, item.Enabled, item.CreatedAt)
	return item, err
}

func (p *Postgres) SetModuleEnabled(ctx context.Context, idv string, enabled bool) (model.Module, error) {
	tag, err := p.pool.Exec(ctx, "UPDATE modules SET enabled=$2 WHERE id=$1", idv, enabled)
	if err != nil {
		return model.Module{}, err
	}
	if tag.RowsAffected() == 0 {
		return model.Module{}, ErrNotFound
	}
	return p.GetModule(ctx, idv)
}

func (p *Postgres) ListHistory(ctx context.Context, limit int) ([]model.History, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := p.pool.Query(ctx, "SELECT h.id, h.bot_id, h.server_id, COALESCE(b.name, ''), COALESCE(s.name, ''), h.action, h.command, h.status, h.can_rollback, h.created_at FROM operation_history h LEFT JOIN bots b ON b.id=h.bot_id LEFT JOIN servers s ON s.id=h.server_id ORDER BY h.created_at DESC LIMIT $1", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.History
	for rows.Next() {
		var item model.History
		if err := rows.Scan(&item.ID, &item.BotID, &item.ServerID, &item.BotName, &item.ServerName, &item.Action, &item.Command, &item.Status, &item.CanRollback, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (p *Postgres) GetHistory(ctx context.Context, idv string) (HistoryRec, error) {
	var rec HistoryRec
	err := p.pool.QueryRow(ctx, "SELECT id, bot_id, server_id, action, command, status, can_rollback, snapshot::text, created_at FROM operation_history WHERE id=$1", idv).Scan(&rec.ID, &rec.BotID, &rec.ServerID, &rec.Action, &rec.Command, &rec.Status, &rec.CanRollback, &rec.Snapshot, &rec.CreatedAt)
	return rec, mapErr(err)
}

func (p *Postgres) AddHistory(ctx context.Context, rec HistoryRec) (model.History, error) {
	if rec.ID == "" {
		rec.ID = id.New()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	if rec.Snapshot == "" {
		rec.Snapshot = "{}"
	}
	_, err := p.pool.Exec(ctx, "INSERT INTO operation_history (id, bot_id, server_id, action, command, status, can_rollback, snapshot, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9)", rec.ID, rec.BotID, rec.ServerID, rec.Action, rec.Command, rec.Status, rec.CanRollback, rec.Snapshot, rec.CreatedAt)
	return rec.History, err
}

func (p *Postgres) FinishRollback(ctx context.Context, idv string) error {
	tag, err := p.pool.Exec(ctx, "UPDATE operation_history SET can_rollback=false, status='rolled_back' WHERE id=$1", idv)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) EnsureSession(ctx context.Context, idv, botID, title string) (model.Session, error) {
	if idv != "" {
		var sess model.Session
		err := p.pool.QueryRow(ctx, "SELECT id, bot_id, title, created_at,options FROM chat_sessions WHERE id=$1", idv).Scan(&sess.ID, &sess.BotID, &sess.Title, &sess.CreatedAt, &sess.Options)
		if err == nil {
			return sess, nil
		}
		if !errors.Is(mapErr(err), ErrNotFound) {
			return model.Session{}, err
		}
	} else {
		idv = id.New()
	}
	if title == "" {
		title = "对话"
	}
	sess := model.Session{ID: idv, BotID: botID, Title: title, CreatedAt: time.Now().UTC()}
	_, err := p.pool.Exec(ctx, "INSERT INTO chat_sessions (id, bot_id, title, created_at) VALUES ($1,$2,$3,$4)", sess.ID, sess.BotID, sess.Title, sess.CreatedAt)
	return sess, err
}

func (p *Postgres) AddMessage(ctx context.Context, msg model.Message) (model.Message, error) {
	if msg.ID == "" {
		msg.ID = id.New()
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	attachments, _ := json.Marshal(msg.Attachments)
	var choice any
	if msg.Choice != nil {
		raw, _ := json.Marshal(msg.Choice)
		choice = string(raw)
	}
	_, err := p.pool.Exec(ctx, "INSERT INTO messages (id, session_id, role, content, created_at,attachments,choice) VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb)", msg.ID, msg.SessionID, msg.Role, msg.Content, msg.CreatedAt, string(attachments), choice)
	return msg, mapErr(err)
}

func (p *Postgres) AnswerChoice(ctx context.Context, sessionID, messageID string, answer model.ChoiceAnswer) (model.Message, error) {
	if answer.AnsweredAt.IsZero() {
		answer.AnsweredAt = time.Now().UTC()
	}
	raw, _ := json.Marshal(answer)
	tag, err := p.pool.Exec(ctx, "UPDATE messages SET choice=jsonb_set(choice,'{answer}',$3::jsonb) WHERE session_id=$1 AND id=$2 AND choice IS NOT NULL AND (choice->'answer') IS NULL", sessionID, messageID, string(raw))
	if err != nil {
		return model.Message{}, err
	}
	msgs, err := p.ListMessages(ctx, sessionID)
	if err != nil {
		return model.Message{}, err
	}
	for _, msg := range msgs {
		if msg.ID == messageID && msg.Choice != nil {
			if tag.RowsAffected() == 0 {
				return msg, ErrConflict
			}
			return msg, nil
		}
	}
	return model.Message{}, ErrNotFound
}

func (p *Postgres) ListMessages(ctx context.Context, sessionID string) ([]model.Message, error) {
	rows, err := p.pool.Query(ctx, "SELECT id, session_id, role, content, created_at, attachments, choice FROM messages WHERE session_id=$1 ORDER BY created_at", sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Message
	for rows.Next() {
		var msg model.Message
		var attachments, choice []byte
		if err := rows.Scan(&msg.ID, &msg.SessionID, &msg.Role, &msg.Content, &msg.CreatedAt, &attachments, &choice); err != nil {
			return nil, err
		}
		if len(choice) > 0 {
			msg.Choice = &model.Choice{}
			if err := json.Unmarshal(choice, msg.Choice); err != nil {
				return nil, err
			}
		}
		if err := json.Unmarshal(attachments, &msg.Attachments); err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

func (p *Postgres) UpdateUser(ctx context.Context, user model.User, passwordHash, keepSession string) (model.User, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return model.User{}, err
	}
	defer tx.Rollback(ctx)
	var oldName string
	if err = tx.QueryRow(ctx, "SELECT username FROM users WHERE id=$1 FOR UPDATE", user.ID).Scan(&oldName); err != nil {
		return model.User{}, mapErr(err)
	}
	_, err = tx.Exec(ctx, "UPDATE users SET username=$2,display_name=$3,avatar=$4,password_hash=CASE WHEN $5='' THEN password_hash ELSE $5 END,must_change_credentials=$6 WHERE id=$1", user.ID, user.Username, user.DisplayName, user.Avatar, passwordHash, user.MustChangeCredentials)
	if err != nil {
		return model.User{}, mapErr(err)
	}
	if passwordHash != "" || oldName != user.Username {
		if _, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1 AND token_hash<>$2", user.ID, keepSession); err != nil {
			return model.User{}, err
		}
	}
	return user, tx.Commit(ctx)
}
func (p *Postgres) ListChatSessions(ctx context.Context) ([]model.SessionSummary, error) {
	rows, err := p.pool.Query(ctx, `SELECT c.id,c.bot_id,c.title,c.created_at,c.options,COALESCE(left(m.content,120),''),COALESCE(m.created_at,c.created_at) FROM chat_sessions c LEFT JOIN LATERAL (SELECT content,created_at FROM messages WHERE session_id=c.id ORDER BY created_at DESC LIMIT 1) m ON true ORDER BY COALESCE(m.created_at,c.created_at) DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.SessionSummary{}
	for rows.Next() {
		var s model.SessionSummary
		if err = rows.Scan(&s.ID, &s.BotID, &s.Title, &s.CreatedAt, &s.Options, &s.Preview, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *Postgres) SaveChatOptions(ctx context.Context, id string, options json.RawMessage) error {
	tag, err := p.pool.Exec(ctx, "UPDATE chat_sessions SET options=$2::jsonb WHERE id=$1", id, string(options))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
