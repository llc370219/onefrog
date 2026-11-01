package store

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"onefrog/internal/id"
	"onefrog/internal/model"
)

const (
	// MaxBotMemories is how many notes one bot keeps. Older notes are dropped.
	MaxBotMemories = 40
	maxMemoryTitle = 80
	maxMemoryBody  = 800
	memoriesFile   = "memories.enc"
)

func cleanMemory(item model.BotMemory) (model.BotMemory, error) {
	item.BotID = strings.TrimSpace(item.BotID)
	item.Title = clipRunes(oneLine(item.Title), maxMemoryTitle)
	item.Body = clipRunes(strings.TrimSpace(item.Body), maxMemoryBody)
	switch item.Source {
	case "bot", "handoff", "user":
	default:
		item.Source = "bot"
	}
	if item.BotID == "" {
		return model.BotMemory{}, ErrNotFound
	}
	if item.Title == "" {
		return model.BotMemory{}, errMemoryTitle
	}
	if item.Body == "" {
		return model.BotMemory{}, errMemoryBody
	}
	return item, nil
}

var (
	errMemoryTitle = errString("记忆需要标题")
	errMemoryBody  = errString("记忆需要内容")
)

type errString string

func (e errString) Error() string { return string(e) }

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func (m *Memory) ListBotMemories(_ context.Context, botID string) ([]model.BotMemory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listMemoriesLocked(botID), nil
}

func (m *Memory) listMemoriesLocked(botID string) []model.BotMemory {
	out := []model.BotMemory{}
	for _, item := range m.memories {
		if item.BotID == botID {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func (m *Memory) UpsertBotMemory(_ context.Context, item model.BotMemory) (model.BotMemory, error) {
	item, err := cleanMemory(item)
	if err != nil {
		return model.BotMemory{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.bots[item.BotID]; !ok {
		return model.BotMemory{}, ErrNotFound
	}
	before := m.snapshotMemoriesLocked()
	now := time.Now().UTC()
	existing := ""
	for _, mem := range m.memories {
		if mem.BotID == item.BotID && mem.Title == item.Title {
			existing = mem.ID
			break
		}
	}
	if existing != "" {
		cur := m.memories[existing]
		cur.Body = item.Body
		cur.Source = item.Source
		cur.UpdatedAt = now
		m.memories[existing] = cur
		item = cur
	} else {
		if item.ID == "" {
			item.ID = id.New()
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = now
		}
		item.UpdatedAt = now
		m.memories[item.ID] = item
	}
	m.trimMemoriesLocked(item.BotID)
	if err = m.saveMemoriesLocked(); err != nil {
		m.memories = before
		return model.BotMemory{}, err
	}
	return m.memories[item.ID], nil
}

func (m *Memory) DeleteBotMemory(_ context.Context, botID, idv string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.memories[idv]
	if !ok || item.BotID != botID {
		return ErrNotFound
	}
	delete(m.memories, idv)
	if err := m.saveMemoriesLocked(); err != nil {
		m.memories[idv] = item
		return err
	}
	return nil
}

func (m *Memory) snapshotMemoriesLocked() map[string]model.BotMemory {
	out := make(map[string]model.BotMemory, len(m.memories))
	for k, v := range m.memories {
		out[k] = v
	}
	return out
}

func (m *Memory) trimMemoriesLocked(botID string) {
	items := m.listMemoriesLocked(botID)
	if len(items) <= MaxBotMemories {
		return
	}
	for _, item := range items[MaxBotMemories:] {
		delete(m.memories, item.ID)
	}
}

func (m *Memory) saveMemoriesLocked() error {
	if !m.persisting() {
		return nil
	}
	rows := make([]model.BotMemory, 0, len(m.memories))
	for _, item := range m.memories {
		rows = append(rows, item)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return writeEncryptedJSON(filepath.Join(m.dataDir, memoriesFile), m.dataBox, rows)
}

func (m *Memory) dropBotMemoriesLocked(botID string) {
	for idv, item := range m.memories {
		if item.BotID == botID {
			delete(m.memories, idv)
		}
	}
}

func (p *Postgres) ListBotMemories(ctx context.Context, botID string) ([]model.BotMemory, error) {
	rows, err := p.pool.Query(ctx, "SELECT id, bot_id, title, body, source, created_at, updated_at FROM bot_memories WHERE bot_id=$1 ORDER BY updated_at DESC, id DESC", botID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.BotMemory{}
	for rows.Next() {
		var item model.BotMemory
		if err := rows.Scan(&item.ID, &item.BotID, &item.Title, &item.Body, &item.Source, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (p *Postgres) UpsertBotMemory(ctx context.Context, item model.BotMemory) (model.BotMemory, error) {
	item, err := cleanMemory(item)
	if err != nil {
		return model.BotMemory{}, err
	}
	if _, err = p.GetBot(ctx, item.BotID); err != nil {
		return model.BotMemory{}, err
	}
	now := time.Now().UTC()
	if item.ID == "" {
		item.ID = id.New()
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	err = p.pool.QueryRow(ctx, `INSERT INTO bot_memories (id, bot_id, title, body, source, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (bot_id, title) DO UPDATE SET body=EXCLUDED.body, source=EXCLUDED.source, updated_at=EXCLUDED.updated_at
RETURNING id, bot_id, title, body, source, created_at, updated_at`,
		item.ID, item.BotID, item.Title, item.Body, item.Source, item.CreatedAt, item.UpdatedAt).
		Scan(&item.ID, &item.BotID, &item.Title, &item.Body, &item.Source, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return model.BotMemory{}, mapErr(err)
	}
	_, err = p.pool.Exec(ctx, `DELETE FROM bot_memories WHERE bot_id=$1 AND id NOT IN (
SELECT id FROM bot_memories WHERE bot_id=$1 ORDER BY updated_at DESC, id DESC LIMIT $2)`, item.BotID, MaxBotMemories)
	return item, err
}

func (p *Postgres) DeleteBotMemory(ctx context.Context, botID, idv string) error {
	tag, err := p.pool.Exec(ctx, "DELETE FROM bot_memories WHERE bot_id=$1 AND id=$2", botID, idv)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
