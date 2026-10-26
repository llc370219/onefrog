// Package monitor accepts per-node authenticated probe reports.
package monitor

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Ping struct {
	Name        string   `json:"name"`
	Target      string   `json:"target"`
	LatencyMS   *float64 `json:"latency_ms"`
	LossPercent *float64 `json:"loss_percent"`
	Samples     int      `json:"samples"`
}
type Sample struct {
	ReceivedAt     time.Time `json:"received_at"`
	Hostname       string    `json:"hostname"`
	OS             string    `json:"os"`
	Arch           string    `json:"arch"`
	CPUModel       string    `json:"cpu_model"`
	CPUCores       int       `json:"cpu_cores"`
	CPUPercent     float64   `json:"cpu_percent"`
	MemoryUsed     uint64    `json:"memory_used"`
	MemoryTotal    uint64    `json:"memory_total"`
	SwapUsed       uint64    `json:"swap_used"`
	SwapTotal      uint64    `json:"swap_total"`
	DiskUsed       uint64    `json:"disk_used"`
	DiskTotal      uint64    `json:"disk_total"`
	RXBytes        uint64    `json:"rx_bytes"`
	TXBytes        uint64    `json:"tx_bytes"`
	RXRate         float64   `json:"rx_rate"`
	TXRate         float64   `json:"tx_rate"`
	UptimeSeconds  uint64    `json:"uptime_seconds"`
	Load           []float64 `json:"load"`
	TCPConnections int       `json:"tcp_connections"`
	Processes      int       `json:"processes"`
	Pings          []Ping    `json:"pings"`
}
type Node struct {
	TokenHash string   `json:"token_hash"`
	Samples   []Sample `json:"samples"`
}
type Manager struct {
	mu    sync.RWMutex
	path  string
	nodes map[string]Node
}
type View struct {
	ServerID string   `json:"server_id"`
	Status   string   `json:"status"`
	Enrolled bool     `json:"enrolled"`
	Latest   *Sample  `json:"latest"`
	History  []Sample `json:"history"`
}

const MaxSamples = 120
const StaleAfter = 90 * time.Second

func New(path string) (*Manager, error) {
	m := &Manager{path: path, nodes: map[string]Node{}}
	if path == "" {
		return m, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &m.nodes); err != nil {
		return nil, errors.New("探针数据文件无效")
	}
	if m.nodes == nil {
		m.nodes = map[string]Node{}
	}
	return m, nil
}
func (m *Manager) persist() error {
	if m.path == "" {
		return nil
	}
	raw, err := json.Marshal(m.nodes)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(m.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(m.path), ".probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, m.path)
}
func (m *Manager) Issue(id string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	sum := sha256.Sum256([]byte(token))
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.nodes[id]
	next := old
	next.TokenHash = hex.EncodeToString(sum[:])
	m.nodes[id] = next
	if err := m.persist(); err != nil {
		m.nodes[id] = old
		return "", err
	}
	return token, nil
}
func (m *Manager) Authorized(id, token string) bool {
	if token == "" {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])
	m.mu.RLock()
	defer m.mu.RUnlock()
	n, ok := m.nodes[id]
	return ok && subtle.ConstantTimeCompare([]byte(n.TokenHash), []byte(hash)) == 1
}
func Validate(s Sample) error {
	valid := func(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }
	if !valid(s.CPUPercent) || s.CPUPercent < 0 || s.CPUPercent > 100 || !valid(s.RXRate) || !valid(s.TXRate) || s.RXRate < 0 || s.TXRate < 0 || s.CPUCores < 1 || s.CPUCores > 65536 {
		return errors.New("CPU 或网络指标无效")
	}
	if s.MemoryTotal == 0 || s.DiskTotal == 0 || s.MemoryUsed > s.MemoryTotal || s.DiskUsed > s.DiskTotal || s.SwapUsed > s.SwapTotal {
		return errors.New("内存或磁盘指标无效")
	}
	if len(s.Load) > 3 || len(s.Pings) > 8 || len(s.Hostname) > 200 || len(s.CPUModel) > 300 || len(s.OS) > 200 || len(s.Arch) > 50 || s.Processes < 0 || s.TCPConnections < 0 {
		return errors.New("指标超出范围")
	}
	for _, v := range s.Load {
		if !valid(v) || v < 0 {
			return errors.New("负载指标无效")
		}
	}
	for _, p := range s.Pings {
		if len(p.Name) > 80 || len(p.Target) > 200 || p.Samples < 0 || p.Samples > 100 {
			return errors.New("网络检测指标无效")
		}
		if p.LatencyMS != nil && (!valid(*p.LatencyMS) || *p.LatencyMS < 0) {
			return errors.New("延迟无效")
		}
		if p.LossPercent != nil && (!valid(*p.LossPercent) || *p.LossPercent < 0 || *p.LossPercent > 100) {
			return errors.New("丢包无效")
		}
	}
	return nil
}

var ErrRateLimit = errors.New("上报过于频繁")

func (m *Manager) Report(id string, s Sample, now time.Time) error {
	if err := Validate(s); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.nodes[id]
	if !ok {
		return errors.New("节点未接入")
	}
	if len(old.Samples) > 0 && now.Sub(old.Samples[len(old.Samples)-1].ReceivedAt) < 2*time.Second {
		return ErrRateLimit
	}
	if s.Pings == nil {
		s.Pings = []Ping{}
	}
	if s.Load == nil {
		s.Load = []float64{}
	}
	s.ReceivedAt = now.UTC()
	next := old
	next.Samples = append(append([]Sample{}, old.Samples...), s)
	if len(next.Samples) > MaxSamples {
		next.Samples = next.Samples[len(next.Samples)-MaxSamples:]
	}
	m.nodes[id] = next
	if err := m.persist(); err != nil {
		m.nodes[id] = old
		return err
	}
	return nil
}
func (m *Manager) Views(ids []string, now time.Time) []View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]View, 0, len(ids))
	for _, id := range ids {
		n := m.nodes[id]
		v := View{ServerID: id, Status: "pending", Enrolled: n.TokenHash != "", History: []Sample{}}
		if len(n.Samples) > 0 {
			v.History = append([]Sample{}, n.Samples...)
			last := n.Samples[len(n.Samples)-1]
			v.Latest = &last
			v.Status = "online"
			if now.Sub(last.ReceivedAt) > StaleAfter {
				v.Status = "offline"
			}
		}
		out = append(out, v)
	}
	return out
}

// Remove forgets a server's probe credential and samples, so a deleted
// server's probe can no longer report.
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.nodes[id]
	if !ok {
		return nil
	}
	delete(m.nodes, id)
	if err := m.persist(); err != nil {
		m.nodes[id] = old
		return err
	}
	return nil
}
