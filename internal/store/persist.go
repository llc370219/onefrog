package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"onefrog/internal/model"
	"onefrog/internal/secret"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Limits for data kept by the memory driver on disk. The oldest entries are
// dropped first so a long-running panel cannot fill the data volume.
const (
	MaxChats           = 500
	MaxMessagesPerChat = 2000
	MaxHistory         = 2000
	MaxSessions        = 1000
)

// File names inside the data directory used by EnablePersistence.
const (
	secretsFile  = "secrets.enc"
	sessionsFile = "sessions.enc"
	historyFile  = "history.enc"
	modulesFile  = "modules.enc"
	chatsDir     = "chats"
)

// writeEncryptedJSON atomically replaces path with the encrypted JSON form of v.
func writeEncryptedJSON(path string, box *secret.Box, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	enc, err := box.Encrypt(string(raw))
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(enc); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// readEncryptedJSON loads path into v. A missing file is not an error and
// leaves v untouched, so data directories from older versions start empty.
func readEncryptedJSON(path string, box *secret.Box, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	plain, err := box.Decrypt(string(b))
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(plain), v)
}

type persistedSession struct {
	TokenHash string
	UserID    string
	Expires   time.Time
}

type persistedChat struct {
	Session  model.Session
	Messages []model.Message
}

// EnablePersistence stores the secret vault, login sessions, chats, change
// history and custom modules encrypted under dir, next to servers.enc and
// bots.enc. Existing files are loaded; missing files mean "no data yet".
func (m *Memory) EnablePersistence(dir string, box *secret.Box) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dataDir = dir
	m.dataBox = box

	var secrets []SecretRec
	if err := readEncryptedJSON(filepath.Join(dir, secretsFile), box, &secrets); err != nil {
		return err
	}
	for _, r := range secrets {
		m.secrets[r.ID] = r
		m.secretName[r.Name] = r.ID
	}

	var sessions []persistedSession
	if err := readEncryptedJSON(filepath.Join(dir, sessionsFile), box, &sessions); err != nil {
		return err
	}
	now := time.Now()
	for _, s := range sessions {
		if now.After(s.Expires) {
			continue
		}
		m.sessions[s.TokenHash] = sessionRow{UserID: s.UserID, Expires: s.Expires}
	}

	var history []HistoryRec
	if err := readEncryptedJSON(filepath.Join(dir, historyFile), box, &history); err != nil {
		return err
	}
	for _, r := range history {
		m.history[r.ID] = r
	}

	var modules []model.Module
	if err := readEncryptedJSON(filepath.Join(dir, modulesFile), box, &modules); err != nil {
		return err
	}
	for _, r := range modules {
		m.modules[r.ID] = r
	}

	entries, err := os.ReadDir(filepath.Join(dir, chatsDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".enc") {
			continue
		}
		var c persistedChat
		if err := readEncryptedJSON(filepath.Join(dir, chatsDir, e.Name()), box, &c); err != nil {
			return err
		}
		if c.Session.ID == "" {
			continue
		}
		m.chats[c.Session.ID] = c.Session
		m.messages[c.Session.ID] = c.Messages
	}
	return nil
}

func (m *Memory) persisting() bool { return m.dataDir != "" }

func (m *Memory) saveSecretsLocked() error {
	if !m.persisting() {
		return nil
	}
	rows := make([]SecretRec, 0, len(m.secrets))
	for _, r := range m.secrets {
		rows = append(rows, r)
	}
	return writeEncryptedJSON(filepath.Join(m.dataDir, secretsFile), m.dataBox, rows)
}

func (m *Memory) saveSessionsLocked() error {
	if !m.persisting() {
		return nil
	}
	now := time.Now()
	rows := make([]persistedSession, 0, len(m.sessions))
	for k, s := range m.sessions {
		if now.After(s.Expires) {
			delete(m.sessions, k)
			continue
		}
		rows = append(rows, persistedSession{TokenHash: k, UserID: s.UserID, Expires: s.Expires})
	}
	if len(rows) > MaxSessions {
		sort.Slice(rows, func(i, j int) bool { return rows[i].Expires.After(rows[j].Expires) })
		for _, r := range rows[MaxSessions:] {
			delete(m.sessions, r.TokenHash)
		}
		rows = rows[:MaxSessions]
	}
	return writeEncryptedJSON(filepath.Join(m.dataDir, sessionsFile), m.dataBox, rows)
}

func (m *Memory) saveHistoryLocked() error {
	if !m.persisting() {
		return nil
	}
	rows := make([]HistoryRec, 0, len(m.history))
	for _, r := range m.history {
		rows = append(rows, r)
	}
	if len(rows) > MaxHistory {
		sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })
		for _, r := range rows[MaxHistory:] {
			delete(m.history, r.ID)
		}
		rows = rows[:MaxHistory]
	}
	return writeEncryptedJSON(filepath.Join(m.dataDir, historyFile), m.dataBox, rows)
}

func (m *Memory) saveModulesLocked() error {
	if !m.persisting() {
		return nil
	}
	rows := make([]model.Module, 0, len(m.modules))
	for _, r := range m.modules {
		rows = append(rows, r)
	}
	return writeEncryptedJSON(filepath.Join(m.dataDir, modulesFile), m.dataBox, rows)
}

// chatFile maps a chat ID (which may come from the client) to a safe file name.
func (m *Memory) chatFile(id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(m.dataDir, chatsDir, hex.EncodeToString(sum[:16])+".enc")
}

func (m *Memory) saveChatLocked(id string) error {
	if !m.persisting() {
		return nil
	}
	if msgs := m.messages[id]; len(msgs) > MaxMessagesPerChat {
		m.messages[id] = append([]model.Message(nil), msgs[len(msgs)-MaxMessagesPerChat:]...)
	}
	return writeEncryptedJSON(m.chatFile(id), m.dataBox, persistedChat{Session: m.chats[id], Messages: m.messages[id]})
}

func (m *Memory) chatUpdatedLocked(id string) time.Time {
	t := m.chats[id].CreatedAt
	if msgs := m.messages[id]; len(msgs) > 0 && msgs[len(msgs)-1].CreatedAt.After(t) {
		t = msgs[len(msgs)-1].CreatedAt
	}
	return t
}

// pruneChatsLocked drops the least recently used chats beyond MaxChats,
// never the one currently being written.
func (m *Memory) pruneChatsLocked(keep string) {
	if len(m.chats) <= MaxChats {
		return
	}
	ids := make([]string, 0, len(m.chats))
	for id := range m.chats {
		if id != keep {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return m.chatUpdatedLocked(ids[i]).Before(m.chatUpdatedLocked(ids[j])) })
	for _, id := range ids[:len(m.chats)-MaxChats] {
		delete(m.chats, id)
		delete(m.messages, id)
		if m.persisting() {
			_ = os.Remove(m.chatFile(id))
		}
	}
}
