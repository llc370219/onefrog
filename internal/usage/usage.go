package usage

import (
	"encoding/json"
	"errors"
	"onefrog/internal/secret"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Totals struct {
	Requests      int64 `json:"requests"`
	ReportedCalls int64 `json:"reported_calls"`
	FailedCalls   int64 `json:"failed_calls"`
	InputTokens   int64 `json:"input_tokens"`
	OutputTokens  int64 `json:"output_tokens"`
	TotalTokens   int64 `json:"total_tokens"`
}
type Row struct {
	Totals
	BotID      string    `json:"bot_id"`
	Purpose    string    `json:"purpose"`
	ProviderID string    `json:"provider_id"`
	Model      string    `json:"model"`
	UpdatedAt  time.Time `json:"updated_at"`
}
type Manager struct {
	mu   sync.Mutex
	path string
	box  *secret.Box
	rows []Row
}

func New(path string, box *secret.Box) (*Manager, error) {
	m := &Manager{path: path, box: box, rows: []Row{}}
	if path == "" {
		return m, nil
	}
	b, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		return m, nil
	}
	if e != nil {
		return nil, e
	}
	plain, e := box.Decrypt(string(b))
	if e != nil {
		return nil, e
	}
	e = json.Unmarshal([]byte(plain), &m.rows)
	return m, e
}
func (m *Manager) Record(botID, purpose, provider, model string, input, output, total int64, reported, failed bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := len(m.rows)
	for n, r := range m.rows {
		if r.BotID == botID && r.Purpose == purpose && r.ProviderID == provider && r.Model == model {
			i = n
			break
		}
	}
	if i == len(m.rows) {
		m.rows = append(m.rows, Row{BotID: botID, Purpose: purpose, ProviderID: provider, Model: model})
	}
	r := &m.rows[i]
	r.Requests++
	if failed {
		r.FailedCalls++
	}
	if reported {
		r.ReportedCalls++
		if input >= 0 {
			r.InputTokens += input
		}
		if output >= 0 {
			r.OutputTokens += output
		}
		if total >= 0 {
			r.TotalTokens += total
		}
	}
	r.UpdatedAt = time.Now().UTC()
	return m.persist()
}
func (m *Manager) persist() error {
	if m.path == "" {
		return nil
	}
	b, e := json.Marshal(m.rows)
	if e != nil {
		return e
	}
	enc, e := m.box.Encrypt(string(b))
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(m.path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(m.path), ".usage-*")
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
	return os.Rename(f.Name(), m.path)
}
func (m *Manager) Rows() []Row { m.mu.Lock(); defer m.mu.Unlock(); return append([]Row{}, m.rows...) }
func (m *Manager) ForBot(id string) Totals {
	var t Totals
	for _, r := range m.Rows() {
		if r.BotID == id {
			t.Requests += r.Requests
			t.ReportedCalls += r.ReportedCalls
			t.FailedCalls += r.FailedCalls
			t.InputTokens += r.InputTokens
			t.OutputTokens += r.OutputTokens
			t.TotalTokens += r.TotalTokens
		}
	}
	return t
}
