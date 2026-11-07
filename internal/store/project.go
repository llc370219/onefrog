package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"onefrog/internal/id"
	"onefrog/internal/model"
)

// ProjectSession also recognizes group chats made before project memory existed.
func ProjectSession(sess model.Session) bool {
	if strings.HasPrefix(sess.ID, "inbox-") || strings.HasPrefix(sess.ID, "auto-") {
		return false
	}
	var opt struct {
		Mode string `json:"mode"`
	}
	return (len(sess.Options) == 0 || json.Unmarshal(sess.Options, &opt) == nil) && opt.Mode != "direct"
}

func cleanProjectNote(item model.ProjectNote) (model.ProjectNote, error) {
	item.Title = clipRunes(oneLine(item.Title), maxMemoryTitle)
	item.Body = clipRunes(strings.TrimSpace(item.Body), maxMemoryBody)
	if item.Title == "" {
		return model.ProjectNote{}, errMemoryTitle
	}
	if item.Body == "" {
		return model.ProjectNote{}, errMemoryBody
	}
	return item, nil
}

func upsertProjectNote(rows []model.ProjectNote, item *model.ProjectNote) []model.ProjectNote {
	item.ID = id.New()
	item.UpdatedAt = time.Now().UTC()
	if i := slices.IndexFunc(rows, func(n model.ProjectNote) bool { return n.Title == item.Title }); i >= 0 {
		item.ID = rows[i].ID
		rows = slices.Delete(rows, i, i+1)
	}
	rows = slices.Insert(rows, 0, *item)
	if len(rows) > MaxBotMemories {
		rows = rows[:MaxBotMemories]
	}
	return rows
}

func removeProjectNote(rows []model.ProjectNote, noteID string) ([]model.ProjectNote, error) {
	i := slices.IndexFunc(rows, func(n model.ProjectNote) bool { return n.ID == noteID })
	if i < 0 {
		return nil, ErrNotFound
	}
	return slices.Delete(rows, i, i+1), nil
}

func (m *Memory) ListProjectNotes(_ context.Context, sessionID string) ([]model.ProjectNote, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess, ok := m.chats[sessionID]; !ok || !ProjectSession(sess) {
		return nil, ErrNotFound
	}
	return slices.Clone(m.projectNotes[sessionID]), nil
}

func (m *Memory) editProjectNotes(sessionID string, edit func([]model.ProjectNote) ([]model.ProjectNote, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess, ok := m.chats[sessionID]; !ok || !ProjectSession(sess) {
		return ErrNotFound
	}
	before := m.projectNotes[sessionID]
	rows, err := edit(slices.Clone(before))
	if err != nil {
		return err
	}
	m.projectNotes[sessionID] = rows
	if err := m.saveChatLocked(sessionID); err != nil {
		m.projectNotes[sessionID] = before
		return err
	}
	return nil
}

func (m *Memory) UpsertProjectNote(_ context.Context, sessionID string, item model.ProjectNote) (model.ProjectNote, error) {
	item, err := cleanProjectNote(item)
	if err != nil {
		return item, err
	}
	err = m.editProjectNotes(sessionID, func(rows []model.ProjectNote) ([]model.ProjectNote, error) {
		return upsertProjectNote(rows, &item), nil
	})
	return item, err
}

func (m *Memory) DeleteProjectNote(_ context.Context, sessionID, noteID string) error {
	return m.editProjectNotes(sessionID, func(rows []model.ProjectNote) ([]model.ProjectNote, error) {
		return removeProjectNote(rows, noteID)
	})
}

func scanProjectNotes(row interface{ Scan(...any) error }, sessionID string) ([]model.ProjectNote, error) {
	sess := model.Session{ID: sessionID}
	var rows []model.ProjectNote
	if err := row.Scan(&sess.Options, &rows); err != nil {
		return nil, mapErr(err)
	}
	if !ProjectSession(sess) {
		return nil, ErrNotFound
	}
	return rows, nil
}

func (p *Postgres) ListProjectNotes(ctx context.Context, sessionID string) ([]model.ProjectNote, error) {
	return scanProjectNotes(p.pool.QueryRow(ctx, "SELECT options, project_notes FROM chat_sessions WHERE id=$1", sessionID), sessionID)
}

func (p *Postgres) editProjectNotes(ctx context.Context, sessionID string, edit func([]model.ProjectNote) ([]model.ProjectNote, error)) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := scanProjectNotes(tx.QueryRow(ctx, "SELECT options, project_notes FROM chat_sessions WHERE id=$1 FOR UPDATE", sessionID), sessionID)
	if err != nil {
		return err
	}
	rows, err = edit(rows)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE chat_sessions SET project_notes=$2::jsonb WHERE id=$1", sessionID, rows); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) UpsertProjectNote(ctx context.Context, sessionID string, item model.ProjectNote) (model.ProjectNote, error) {
	item, err := cleanProjectNote(item)
	if err != nil {
		return item, err
	}
	err = p.editProjectNotes(ctx, sessionID, func(rows []model.ProjectNote) ([]model.ProjectNote, error) {
		return upsertProjectNote(rows, &item), nil
	})
	return item, err
}

func (p *Postgres) DeleteProjectNote(ctx context.Context, sessionID, noteID string) error {
	return p.editProjectNotes(ctx, sessionID, func(rows []model.ProjectNote) ([]model.ProjectNote, error) {
		return removeProjectNote(rows, noteID)
	})
}
