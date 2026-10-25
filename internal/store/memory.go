package store

import (
	"context"
	"encoding/json"
	"onefrog/internal/id"
	"onefrog/internal/model"
	"onefrog/internal/secret"
	"sort"
	"sync"
	"time"
)

type Memory struct {
	serverPath  string
	serverBox   *secret.Box
	accountPath string
	botPath     string
	botBox      *secret.Box
	mu          sync.Mutex
	users       map[string]userRow
	byName      map[string]string
	sessions    map[string]sessionRow
	servers     map[string]ServerRec
	bots        map[string]model.Bot
	secrets     map[string]SecretRec
	secretName  map[string]string
	modules     map[string]model.Module
	history     map[string]HistoryRec
	chats       map[string]model.Session
	messages    map[string][]model.Message
}

type userRow struct {
	model.User
	Hash string
}

type sessionRow struct {
	UserID  string
	Expires time.Time
}

func NewMemory() *Memory {
	return &Memory{
		users: map[string]userRow{}, byName: map[string]string{}, sessions: map[string]sessionRow{},
		servers: map[string]ServerRec{}, bots: map[string]model.Bot{},
		secrets: map[string]SecretRec{}, secretName: map[string]string{},
		modules: map[string]model.Module{}, history: map[string]HistoryRec{},
		chats: map[string]model.Session{}, messages: map[string][]model.Message{},
	}
}

func (m *Memory) CreateUser(_ context.Context, username, passwordHash string) (model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byName[username]; ok {
		return model.User{}, ErrConflict
	}
	user := model.User{ID: id.New(), Username: username, CreatedAt: time.Now().UTC()}
	m.users[user.ID] = userRow{User: user, Hash: passwordHash}
	m.byName[username] = user.ID
	if err := m.saveUsersLocked(); err != nil {
		delete(m.users, user.ID)
		delete(m.byName, username)
		return model.User{}, err
	}
	return user, nil
}

func (m *Memory) UserByName(_ context.Context, username string) (model.User, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idv, ok := m.byName[username]
	if !ok {
		return model.User{}, "", ErrNotFound
	}
	row := m.users[idv]
	return row.User, row.Hash, nil
}

func (m *Memory) CountUsers(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.users), nil
}

func (m *Memory) CreateSession(_ context.Context, userID, tokenHash string, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[tokenHash] = sessionRow{UserID: userID, Expires: expires}
	return nil
}

func (m *Memory) UserBySession(_ context.Context, tokenHash string) (model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.sessions[tokenHash]
	if !ok || time.Now().After(row.Expires) {
		return model.User{}, ErrNotFound
	}
	user, ok := m.users[row.UserID]
	if !ok {
		return model.User{}, ErrNotFound
	}
	return user.User, nil
}

func (m *Memory) DeleteSession(_ context.Context, tokenHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, tokenHash)
	return nil
}

func (m *Memory) ListServers(_ context.Context) ([]model.Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Server, 0, len(m.servers))
	for _, rec := range m.servers {
		out = append(out, rec.Server)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Memory) CountServers(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.servers), nil
}

func (m *Memory) GetServer(_ context.Context, idv string) (ServerRec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.servers[idv]
	if !ok {
		return ServerRec{}, ErrNotFound
	}
	return rec, nil
}

func (m *Memory) CreateServer(_ context.Context, rec ServerRec) (model.Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	old, existed := m.servers[rec.ID]
	m.servers[rec.ID] = rec
	if e := m.saveServersLocked(); e != nil {
		if existed {
			m.servers[rec.ID] = old
		} else {
			delete(m.servers, rec.ID)
		}
		return model.Server{}, e
	}
	return rec.Server, nil
}

func (m *Memory) UpdateServer(_ context.Context, rec ServerRec) (model.Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.servers[rec.ID]
	if !ok {
		return model.Server{}, ErrNotFound
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
	m.servers[rec.ID] = rec
	if e := m.saveServersLocked(); e != nil {
		m.servers[rec.ID] = old
		return model.Server{}, e
	}
	return rec.Server, nil
}

func (m *Memory) DeleteServer(_ context.Context, idv string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.servers[idv]; !ok {
		return ErrNotFound
	}
	old := m.servers[idv]
	delete(m.servers, idv)
	if e := m.saveServersLocked(); e != nil {
		m.servers[idv] = old
		return e
	}
	return nil
}

func (m *Memory) ListBots(_ context.Context) ([]model.Bot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Bot, 0, len(m.bots))
	for _, bot := range m.bots {
		out = append(out, bot)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Memory) GetBot(_ context.Context, idv string) (model.Bot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bot, ok := m.bots[idv]
	if !ok {
		return model.Bot{}, ErrNotFound
	}
	return bot, nil
}

func (m *Memory) CreateBot(_ context.Context, bot model.Bot) (model.Bot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if bot.ID == "" {
		bot.ID = id.New()
	}
	if bot.CreatedAt.IsZero() {
		bot.CreatedAt = time.Now().UTC()
	}
	previous, existed := m.bots[bot.ID]
	m.bots[bot.ID] = bot
	if err := m.saveBotsLocked(); err != nil {
		if existed {
			m.bots[bot.ID] = previous
		} else {
			delete(m.bots, bot.ID)
		}
		return model.Bot{}, err
	}
	return bot, nil
}

func (m *Memory) UpdateBot(_ context.Context, bot model.Bot) (model.Bot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.bots[bot.ID]
	if !ok {
		return model.Bot{}, ErrNotFound
	}
	if bot.CreatedAt.IsZero() {
		bot.CreatedAt = old.CreatedAt
	}
	m.bots[bot.ID] = bot
	if err := m.saveBotsLocked(); err != nil {
		m.bots[bot.ID] = old
		return model.Bot{}, err
	}
	return bot, nil
}

func (m *Memory) DeleteBot(_ context.Context, idv string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.bots[idv]; !ok {
		return ErrNotFound
	}
	old := m.bots[idv]
	delete(m.bots, idv)
	if err := m.saveBotsLocked(); err != nil {
		m.bots[idv] = old
		return err
	}
	return nil
}

func (m *Memory) ListSecrets(_ context.Context) ([]model.Secret, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Secret, 0, len(m.secrets))
	for _, rec := range m.secrets {
		out = append(out, rec.Secret)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Memory) GetSecret(_ context.Context, idv string) (SecretRec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.secrets[idv]
	if !ok {
		return SecretRec{}, ErrNotFound
	}
	return rec, nil
}

func (m *Memory) SecretByName(_ context.Context, name string) (SecretRec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idv, ok := m.secretName[name]
	if !ok {
		return SecretRec{}, ErrNotFound
	}
	return m.secrets[idv], nil
}

func (m *Memory) CreateSecret(_ context.Context, rec SecretRec) (model.Secret, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.secretName[rec.Name]; ok {
		return model.Secret{}, ErrConflict
	}
	if rec.ID == "" {
		rec.ID = id.New()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	m.secrets[rec.ID] = rec
	m.secretName[rec.Name] = rec.ID
	return rec.Secret, nil
}

func (m *Memory) DeleteSecret(_ context.Context, idv string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.secrets[idv]
	if !ok {
		return ErrNotFound
	}
	delete(m.secretName, rec.Name)
	delete(m.secrets, idv)
	return nil
}

func (m *Memory) AllSecrets(ctx context.Context) ([]SecretRec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SecretRec, 0, len(m.secrets))
	for _, rec := range m.secrets {
		out = append(out, rec)
	}
	return out, nil
}

func (m *Memory) ListModules(_ context.Context) ([]model.Module, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Module, 0, len(m.modules))
	for _, item := range m.modules {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *Memory) GetModule(_ context.Context, idv string) (model.Module, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.modules[idv]
	if !ok {
		return model.Module{}, ErrNotFound
	}
	return item, nil
}

func (m *Memory) SaveModule(_ context.Context, item model.Module) (model.Module, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if item.CreatedAt.IsZero() {
		if old, ok := m.modules[item.ID]; ok {
			item.CreatedAt = old.CreatedAt
		} else {
			item.CreatedAt = time.Now().UTC()
		}
	}
	m.modules[item.ID] = item
	return item, nil
}

func (m *Memory) SetModuleEnabled(ctx context.Context, idv string, enabled bool) (model.Module, error) {
	item, err := m.GetModule(ctx, idv)
	if err != nil {
		return model.Module{}, err
	}
	item.Enabled = enabled
	return m.SaveModule(ctx, item)
}

func (m *Memory) ListHistory(_ context.Context, limit int) ([]model.History, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	out := make([]model.History, 0, len(m.history))
	for _, rec := range m.history {
		item := rec.History
		if bot, ok := m.bots[item.BotID]; ok {
			item.BotName = bot.Name
		}
		if srv, ok := m.servers[item.ServerID]; ok {
			item.ServerName = srv.Name
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Memory) GetHistory(_ context.Context, idv string) (HistoryRec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.history[idv]
	if !ok {
		return HistoryRec{}, ErrNotFound
	}
	return rec, nil
}

func (m *Memory) AddHistory(_ context.Context, rec HistoryRec) (model.History, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec.ID == "" {
		rec.ID = id.New()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	if rec.Snapshot == "" {
		rec.Snapshot = "{}"
	}
	m.history[rec.ID] = rec
	return rec.History, nil
}

func (m *Memory) FinishRollback(_ context.Context, idv string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.history[idv]
	if !ok {
		return ErrNotFound
	}
	rec.CanRollback = false
	rec.Status = "rolled_back"
	m.history[idv] = rec
	return nil
}

func (m *Memory) EnsureSession(_ context.Context, idv, botID, title string) (model.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if idv != "" {
		if sess, ok := m.chats[idv]; ok {
			return sess, nil
		}
	} else {
		idv = id.New()
	}
	if title == "" {
		title = "对话"
	}
	sess := model.Session{ID: idv, BotID: botID, Title: title, CreatedAt: time.Now().UTC()}
	m.chats[idv] = sess
	return sess, nil
}

func (m *Memory) AddMessage(_ context.Context, msg model.Message) (model.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.chats[msg.SessionID]; !ok {
		return model.Message{}, ErrNotFound
	}
	if msg.ID == "" {
		msg.ID = id.New()
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	m.messages[msg.SessionID] = append(m.messages[msg.SessionID], msg)
	return msg, nil
}

func (m *Memory) ListMessages(_ context.Context, sessionID string) ([]model.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.chats[sessionID]; !ok {
		return nil, ErrNotFound
	}
	out := append([]model.Message(nil), m.messages[sessionID]...)
	return out, nil
}

func (m *Memory) UpdateUser(_ context.Context, user model.User, passwordHash, keepSession string) (model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.users[user.ID]
	if !ok {
		return model.User{}, ErrNotFound
	}
	if other, ok := m.byName[user.Username]; ok && other != user.ID {
		return model.User{}, ErrConflict
	}
	row := old
	row.User = user
	if passwordHash != "" {
		row.Hash = passwordHash
	}
	m.users[user.ID] = row
	if err := m.saveUsersLocked(); err != nil {
		m.users[user.ID] = old
		return model.User{}, err
	}
	delete(m.byName, old.Username)
	m.byName[user.Username] = user.ID
	if passwordHash != "" || old.Username != user.Username {
		for key, s := range m.sessions {
			if s.UserID == user.ID && key != keepSession {
				delete(m.sessions, key)
			}
		}
	}
	return user, nil
}
func (m *Memory) ListChatSessions(_ context.Context) ([]model.SessionSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.SessionSummary, 0, len(m.chats))
	for _, s := range m.chats {
		item := model.SessionSummary{Session: s, UpdatedAt: s.CreatedAt}
		for _, msg := range m.messages[s.ID] {
			if !msg.CreatedAt.Before(item.UpdatedAt) {
				item.UpdatedAt = msg.CreatedAt
				item.Preview = msg.Content
			}
		}
		r := []rune(item.Preview)
		if len(r) > 120 {
			item.Preview = string(r[:120])
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if len(out) > 100 {
		out = out[:100]
	}
	return out, nil
}

func (m *Memory) SaveChatOptions(_ context.Context, id string, options json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.chats[id]
	if !ok {
		return ErrNotFound
	}
	s.Options = append(json.RawMessage(nil), options...)
	m.chats[id] = s
	return nil
}
