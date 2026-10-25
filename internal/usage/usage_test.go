package usage

import (
	"bytes"
	"onefrog/internal/secret"
	"path/filepath"
	"sync"
	"testing"
)

func TestUsagePersistsWithoutInventedEstimates(t *testing.T) {
	box, _ := secret.New(bytes.Repeat([]byte{9}, 32))
	path := filepath.Join(t.TempDir(), "usage.enc")
	m, _ := New(path, box)
	_ = m.Record("bot", "conversation", "p", "model", 10, 5, 15, true, false)
	_ = m.Record("bot", "conversation", "p", "model", 0, 0, 0, false, false)
	_ = m.Record("bot", "approval", "p", "review", 2, 3, 5, true, false)
	_ = m.Record("bot", "conversation", "p", "model", 0, 0, 0, false, true)
	next, e := New(path, box)
	if e != nil {
		t.Fatal(e)
	}
	v := next.ForBot("bot")
	if v.Requests != 4 || v.ReportedCalls != 2 || v.FailedCalls != 1 || v.TotalTokens != 20 || v.InputTokens != 12 || v.OutputTokens != 8 {
		t.Fatal(v)
	}
}
func TestUsageConcurrent(t *testing.T) {
	m, _ := New("", nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() { _ = m.Record("bot", "conversation", "p", "m", 1, 2, 3, true, false) })
	}
	wg.Wait()
	if m.ForBot("bot").TotalTokens != 60 {
		t.Fatal("lost usage")
	}
}
