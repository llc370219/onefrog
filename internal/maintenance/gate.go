// Package maintenance coordinates snapshots with requests and background writers.
package maintenance

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

var ErrBusy = errors.New("仍有对话、任务或服务器配置正在执行，请等待完成后重试")
var ErrPaused = errors.New("工作台已暂停，请在设置的备份与迁移中恢复运行")

type Gate struct {
	mu     sync.Mutex
	active int
	paused bool
	path   string
}

func New(dir string) (*Gate, error) {
	g := &Gate{path: filepath.Join(dir, ".migration-paused")}
	_, err := os.Stat(g.path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	g.paused = err == nil
	return g, nil
}

// Enter is nonblocking. A successful caller must call the returned release.
func (g *Gate) Enter() (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused {
		return nil, ErrPaused
	}
	g.active++
	return func() { g.mu.Lock(); g.active--; g.mu.Unlock() }, nil
}

// Pause refuses to snapshot live writes, rather than interrupting a running task.
func (g *Gate) Pause() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active != 0 {
		return ErrBusy
	}
	if err := os.WriteFile(g.path, []byte("paused\n"), 0600); err != nil {
		return err
	}
	g.paused = true
	return nil
}

func (g *Gate) Resume() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := os.Remove(g.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	g.paused = false
	return nil
}

func (g *Gate) Paused() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused
}
