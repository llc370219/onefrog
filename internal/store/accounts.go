package store

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Persist account profiles and password hashes even when the demo uses the memory driver.
func NewMemoryAccounts(path string) (*Memory, error) {
	m := NewMemory()
	m.accountPath = path
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []userRow
	if err = json.Unmarshal(b, &rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		m.users[row.ID] = row
		m.byName[row.Username] = row.ID
	}
	return m, nil
}
func (m *Memory) saveUsersLocked() error {
	if m.accountPath == "" {
		return nil
	}
	rows := make([]userRow, 0, len(m.users))
	for _, row := range m.users {
		rows = append(rows, row)
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(m.accountPath), 0700); err != nil {
		return err
	}
	tmp := m.accountPath + ".tmp"
	if err = os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, m.accountPath)
}
