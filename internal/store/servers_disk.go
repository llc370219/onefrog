package store

import (
	"encoding/json"
	"onefrog/internal/secret"
	"os"
	"path/filepath"
)

func (m *Memory) EnableServerPersistence(path string, box *secret.Box) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.serverPath = path
	m.serverBox = box
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	plain, e := box.Decrypt(string(b))
	if e != nil {
		return e
	}
	var rows []ServerRec
	if e = json.Unmarshal([]byte(plain), &rows); e != nil {
		return e
	}
	for _, r := range rows {
		m.servers[r.ID] = r
	}
	return nil
}
func (m *Memory) saveServersLocked() error {
	if m.serverPath == "" {
		return nil
	}
	rows := []ServerRec{}
	for _, r := range m.servers {
		rows = append(rows, r)
	}
	raw, e := json.Marshal(rows)
	if e != nil {
		return e
	}
	enc, e := m.serverBox.Encrypt(string(raw))
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(m.serverPath), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(m.serverPath), ".servers-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.WriteString(enc); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), m.serverPath)
}
