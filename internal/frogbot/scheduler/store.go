package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"onefrog/internal/frogbot/state"
)

const storeVersion = 1

type fileFormat struct {
	Version int               `json:"version"`
	Plans   map[string]*Plan  `json:"plans"`
	Claims  map[string]*Claim `json:"claims"`
}

// Store 是线程安全的计划/认领持久化（data/schedules.json）。
type Store struct {
	path  string
	codec state.Codec
	mu    sync.Mutex
	data  fileFormat
}

func OpenStore(dir string) (*Store, error) {
	return OpenStoreWithCodec(dir, nil)
}

func OpenStoreWithCodec(dir string, codec state.Codec) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "schedules.json"), codec: codec, data: fileFormat{
		Version: storeVersion, Plans: map[string]*Plan{}, Claims: map[string]*Claim{},
	}}
	raw, err := state.ReadFile(codec, s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, s.saveLocked()
		}
		return nil, err
	}
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, fmt.Errorf("读取 schedules.json：%w", err)
	}
	if s.data.Plans == nil {
		s.data.Plans = map[string]*Plan{}
	}
	if s.data.Claims == nil {
		s.data.Claims = map[string]*Claim{}
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	s.data.Version = storeVersion
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return state.WriteFile(s.codec, s.path, raw)
}

func (s *Store) Put(p *Plan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *p
	s.data.Plans[p.ID] = &cp
	return s.saveLocked()
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Plans[id]; !ok {
		return errors.New("没有这个计划")
	}
	delete(s.data.Plans, id)
	return s.saveLocked()
}

func (s *Store) Get(id string) (Plan, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.data.Plans[id]
	if !ok {
		return Plan{}, false
	}
	return *p, true
}

func (s *Store) List() []Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Plan, 0, len(s.data.Plans))
	for _, p := range s.data.Plans {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Enabled != out[j].Enabled {
			return out[i].Enabled
		}
		ni, nj := out[i].NextRun, out[j].NextRun
		switch {
		case ni == nil && nj == nil:
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		case ni == nil:
			return false
		case nj == nil:
			return true
		default:
			return ni.Before(*nj)
		}
	})
	return out
}

// TryClaim 原子认领一次触发：已有同 key 则返回已有记录和 created=false。
func (s *Store) TryClaim(c Claim) (Claim, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.data.Claims[c.Key]; ok {
		return *old, false, nil
	}
	cp := c
	s.data.Claims[c.Key] = &cp
	if p := s.data.Plans[c.PlanID]; p != nil {
		t := c.IntendedAt
		p.LastRun, p.LastStatus, p.LastTaskID, p.LastClaim = &t, c.Status, c.TaskID, c.Key
		p.UpdatedAt = time.Now().UTC()
	}
	if err := s.saveLocked(); err != nil {
		return Claim{}, false, err
	}
	return cp, true, nil
}

func (s *Store) UpdateClaim(key, status, note string) (Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.data.Claims[key]
	if !ok {
		return Claim{}, errors.New("没有这条认领")
	}
	c.Status = status
	if note != "" {
		c.Note = note
	}
	now := time.Now().UTC()
	switch status {
	case "dispatched":
		c.DispatchedAt = &now
	case "completed", "failed", "needs_verify", "skipped_misfire", "skipped_overlap", "expired":
		c.FinishedAt = &now
	}
	if p := s.data.Plans[c.PlanID]; p != nil {
		p.LastStatus = status
		p.UpdatedAt = now
	}
	if err := s.saveLocked(); err != nil {
		return Claim{}, err
	}
	return *c, nil
}

func (s *Store) Claim(key string) (Claim, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.data.Claims[key]
	if !ok {
		return Claim{}, false
	}
	return *c, true
}

func (s *Store) Claims() []Claim {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Claim, 0, len(s.data.Claims))
	for _, c := range s.data.Claims {
		out = append(out, *c)
	}
	return out
}

// SetNext 更新计划的下次触发时间。
func (s *Store) SetNext(id string, next *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.data.Plans[id]
	if !ok {
		return errors.New("没有这个计划")
	}
	p.NextRun = next
	p.UpdatedAt = time.Now().UTC()
	return s.saveLocked()
}

// OpenClaimsFor 返回某计划仍未结束的认领。
func (s *Store) OpenClaimsFor(planID string) []Claim {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Claim
	for _, c := range s.data.Claims {
		if c.PlanID != planID {
			continue
		}
		switch c.Status {
		case "claimed", "dispatched":
			out = append(out, *c)
		}
	}
	return out
}
