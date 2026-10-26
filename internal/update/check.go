// Package update checks GitHub for a newer OneFrog release. It only reads the
// public "latest release" endpoint and never downloads or installs anything.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultRepo is the upstream repository whose releases are checked.
	DefaultRepo = "llc370219/onefrog"
	// DefaultInterval is how often the background loop checks.
	DefaultInterval = 12 * time.Hour
	// ManualCooldown limits how often an operator can force a check.
	ManualCooldown = 30 * time.Second
	requestTimeout = 10 * time.Second
	maxNotes       = 8000
)

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// ErrTooSoon is returned by CheckNow when the previous attempt was too recent.
var ErrTooSoon = errors.New("刚刚检查过，请稍后再试")

// ValidRepo reports whether repo looks like "owner/name".
func ValidRepo(repo string) bool {
	if !repoPattern.MatchString(repo) {
		return false
	}
	owner, name, _ := strings.Cut(repo, "/")
	return owner != "." && owner != ".." && name != "." && name != ".."
}

// Status is what the API returns to the web UI.
type Status struct {
	Current     string     `json:"current"`
	Latest      string     `json:"latest,omitempty"`
	URL         string     `json:"url,omitempty"`
	Notes       string     `json:"notes,omitempty"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	Available   bool       `json:"available"`
	// Enabled is false when checking is off; DisabledBy then says why:
	// "env" (OF_UPDATE_CHECK=0), "settings", "dev" (unversioned build) or "repo".
	Enabled    bool       `json:"enabled"`
	DisabledBy string     `json:"disabled_by,omitempty"`
	Repo       string     `json:"repo"`
	CheckedAt  *time.Time `json:"checked_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}

type release struct {
	Tag         string    `json:"tag_name"`
	URL         string    `json:"html_url"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
}

// Checker caches the latest release and refreshes it periodically.
type Checker struct {
	Repo    string // owner/name, default DefaultRepo
	Current string // running version, e.g. v0.1.1 or dev
	API     string // GitHub API base URL, default https://api.github.com
	HTTP    *http.Client
	// EnvDisabled is set from OF_UPDATE_CHECK=0 and cannot be overridden in the UI.
	EnvDisabled bool
	// SettingsDisabled reports the Settings toggle; nil means enabled.
	SettingsDisabled func() bool
	Interval         time.Duration
	Now              func() time.Time

	mu          sync.Mutex
	latest      *release
	checkedAt   time.Time
	lastAttempt time.Time
	lastErr     string
}

func (c *Checker) repo() string {
	if c.Repo == "" {
		return DefaultRepo
	}
	return c.Repo
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// disabledBy returns why checking is off, or "" when it is on.
func (c *Checker) disabledBy() string {
	switch {
	case c.EnvDisabled:
		return "env"
	case c.SettingsDisabled != nil && c.SettingsDisabled():
		return "settings"
	case !ValidRepo(c.repo()):
		return "repo"
	}
	if _, ok := parseSemver(c.Current); !ok {
		return "dev"
	}
	return ""
}

// Status returns the cached result without any network access.
func (c *Checker) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Status{Current: c.Current, Repo: c.repo(), Error: c.lastErr}
	if s.Current == "" {
		s.Current = "dev"
	}
	s.DisabledBy = c.disabledBy()
	s.Enabled = s.DisabledBy == ""
	if !c.checkedAt.IsZero() {
		t := c.checkedAt
		s.CheckedAt = &t
	}
	if c.latest != nil {
		s.Latest = c.latest.Tag
		s.URL = c.latest.URL
		s.Notes = c.latest.Body
		if !c.latest.PublishedAt.IsZero() {
			t := c.latest.PublishedAt
			s.PublishedAt = &t
		}
		s.Available = s.Enabled && Newer(c.latest.Tag, c.Current)
	}
	if !s.Enabled {
		s.Error = ""
	}
	return s
}

// CheckNow forces a check unless the last attempt was within ManualCooldown.
func (c *Checker) CheckNow(ctx context.Context) (Status, error) {
	if c.disabledBy() != "" {
		return c.Status(), nil
	}
	c.mu.Lock()
	tooSoon := !c.lastAttempt.IsZero() && c.now().Sub(c.lastAttempt) < ManualCooldown
	c.mu.Unlock()
	if tooSoon {
		return c.Status(), ErrTooSoon
	}
	err := c.check(ctx)
	return c.Status(), err
}

// check fetches the latest release once and updates the cache.
func (c *Checker) check(ctx context.Context) error {
	c.mu.Lock()
	c.lastAttempt = c.now()
	c.mu.Unlock()
	rel, err := c.fetch(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.lastErr = err.Error()
		return err
	}
	c.latest, c.lastErr, c.checkedAt = rel, "", c.now()
	return nil
}

func (c *Checker) fetch(ctx context.Context) (*release, error) {
	api := strings.TrimRight(c.API, "/")
	if api == "" {
		api = "https://api.github.com"
	}
	repo := c.repo()
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "OneFrog/"+c.Current+" (update check)")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("检查更新失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("检查更新失败：仓库还没有发布正式版本")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("检查更新失败：GitHub 返回 %d", resp.StatusCode)
	}
	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("检查更新失败：无法解析响应：%w", err)
	}
	if rel.Draft || rel.Prerelease {
		return nil, errors.New("检查更新失败：最新发布不是正式版本")
	}
	if _, ok := parseSemver(rel.Tag); !ok {
		return nil, fmt.Errorf("检查更新失败：版本号 %q 无效", rel.Tag)
	}
	// Only link to the release page of the checked repository.
	if !strings.HasPrefix(rel.URL, "https://github.com/"+repo+"/releases/") {
		rel.URL = "https://github.com/" + repo + "/releases/tag/" + rel.Tag
	}
	if len(rel.Body) > maxNotes {
		cut := maxNotes
		for cut > 0 && (rel.Body[cut]&0xC0) == 0x80 {
			cut--
		}
		rel.Body = rel.Body[:cut] + "\n…"
	}
	return &rel, nil
}

// Run checks shortly after startup and then every Interval until ctx ends.
// Checks are skipped while checking is disabled.
func (c *Checker) Run(ctx context.Context) {
	interval := c.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if c.disabledBy() == "" {
			if err := c.check(ctx); err != nil {
				log.Printf("update check: %v", err)
			} else if s := c.Status(); s.Available {
				log.Printf("update available: %s -> %s (%s)", s.Current, s.Latest, s.URL)
			}
		}
		timer.Reset(interval)
	}
}
