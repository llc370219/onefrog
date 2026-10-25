package store

import (
	"encoding/json"
	"onefrog/internal/model"
	"onefrog/internal/secret"
	"os"
	"path/filepath"
)

// EnableBotPersistence keeps the demo's editable bot profiles across restarts.
func (m *Memory) EnableBotPersistence(path string, box *secret.Box) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.botPath = path
	m.botBox = box
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	plain, err := box.Decrypt(string(b))
	if err != nil {
		return err
	}
	var rows []model.Bot
	if err = json.Unmarshal([]byte(plain), &rows); err != nil {
		return err
	}
	for _, b := range rows {
		m.bots[b.ID] = b
	}
	return nil
}
func (m *Memory) saveBotsLocked() error {
	if m.botPath == "" {
		return nil
	}
	rows := []model.Bot{}
	for _, b := range m.bots {
		rows = append(rows, b)
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	enc, err := m.botBox.Encrypt(string(raw))
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(m.botPath), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(m.botPath), ".bots-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(enc); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), m.botPath)
}
