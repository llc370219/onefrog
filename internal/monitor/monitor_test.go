package monitor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sample() Sample {
	latency := 35.2
	loss := 0.0
	return Sample{CPUCores: 2, CPUPercent: 20, MemoryUsed: 512, MemoryTotal: 1024, DiskUsed: 1000, DiskTotal: 4000, RXRate: 123, TXRate: 25, Load: []float64{0.2, 0.1, 0.1}, Pings: []Ping{{Name: "test", Target: "example.test", LatencyMS: &latency, LossPercent: &loss, Samples: 3}}}
}
func TestEnrollmentAuthenticationPersistenceAndFreshness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.json")
	m, _ := New(path)
	now := time.Now()
	before := m.Views([]string{"a"}, now)[0]
	if before.Enrolled || before.Status != "pending" || before.Latest != nil {
		t.Fatal("unreported node treated as online")
	}
	token, err := m.Issue("a")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Authorized("a", token) || m.Authorized("b", token) || m.Authorized("a", "wrong") || m.Authorized("a", "") {
		t.Fatal("probe token not scoped")
	}
	if err = m.Report("a", sample(), now); err != nil {
		t.Fatal(err)
	}
	if m.Views([]string{"a"}, now)[0].Status != "online" {
		t.Fatal("fresh sample not online")
	}
	if m.Views([]string{"a"}, now.Add(91*time.Second))[0].Status != "offline" {
		t.Fatal("stale sample not offline")
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), token) {
		t.Fatal("plaintext token on disk")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("insecure file permissions")
	}
	restored, err := New(path)
	if err != nil || !restored.Authorized("a", token) || restored.Views([]string{"a"}, now)[0].Latest == nil {
		t.Fatal("probe persistence failed")
	}
	newToken, _ := restored.Issue("a")
	if restored.Authorized("a", token) || !restored.Authorized("a", newToken) {
		t.Fatal("rotation failed")
	}
	if err = m.Report("a", sample(), now.Add(time.Second)); err != ErrRateLimit {
		t.Fatal("rate limit not enforced")
	}
	for i := 1; i < 130; i++ {
		if err = m.Report("a", sample(), now.Add(time.Duration(i)*15*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if len(m.Views([]string{"a"}, now)[0].History) != MaxSamples {
		t.Fatal("history unbounded")
	}
}
func TestInvalidSamplesRejected(t *testing.T) {
	s := sample()
	s.MemoryUsed = s.MemoryTotal + 1
	if Validate(s) == nil {
		t.Fatal("invalid memory accepted")
	}
	s = sample()
	s.CPUPercent = 101
	if Validate(s) == nil {
		t.Fatal("invalid CPU accepted")
	}
	s = sample()
	loss := 101.0
	s.Pings[0].LossPercent = &loss
	if Validate(s) == nil {
		t.Fatal("invalid packet loss accepted")
	}
}
