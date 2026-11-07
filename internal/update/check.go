// Package update checks GitHub for a newer OneFrog release and can ask the
// host to upgrade. The stable channel only reads the public "latest release"
// endpoint (which never returns pre-releases); the beta channel reads the
// release list and also considers vX.Y.Z-beta.N pre-releases.
// Apply does not download an image and does not talk to Docker: it writes the
// cached release tag into a directory the installer mounted. A root helper on
// the host reads that tag and runs docker compose. The container never sees
// the Docker socket.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	// DefaultUpgradeDir is where Apply writes the trigger when OF_UPGRADE_DIR
	// is unset. It is only usable when the installer bind-mounted it.
	DefaultUpgradeDir = "/run/onefrog-upgrade"
	maxReleaseTag     = 40
)

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var releaseTagPattern = regexp.MustCompile("^v[0-9]{1,8}(\\.[0-9]{1,8}){2}(-beta\\.[0-9]{1,8})?$")

// Release channels. Stable installs follow vX.Y.Z releases (image tag
// "latest"); beta installs also follow vX.Y.Z-beta.N pre-releases (image tag
// "beta"), which are cut from the dev branch for testing.
const (
	ChannelStable = "stable"
	ChannelBeta   = "beta"
)

// NormalizeChannel maps OF_CHANNEL values to a channel; ok is false for
// anything it does not recognise.
func NormalizeChannel(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "stable", "latest", "release":
		return ChannelStable, true
	case "beta", "test", "testing":
		return ChannelBeta, true
	}
	return "", false
}

// ErrTooSoon is returned by CheckNow when the previous attempt was too recent.
var ErrTooSoon = errors.New("刚刚检查过，请稍后再试")

// ErrNoUpdate is returned by Apply when no newer official release is cached.
var ErrNoUpdate = errors.New("当前没有可安装的新版本")

// ErrUpgradeUnavailable is returned by Apply when this install cannot be
// upgraded from the panel yet. Running install.sh once creates the trigger
// directory and the host helper.
var ErrUpgradeUnavailable = errors.New("这次安装还不能在面板里升级。请先在服务器上重新运行一次安装命令，之后就可以点按钮升级。")

// ErrHelperStale is returned when a beta release was requested but the host
// helper still only re-pulls the pinned image. Stable upgrades are unaffected.
var ErrHelperStale = errors.New("这台机器的升级程序还不会切换到测试版，点升级只会重新拉取正式版。请在服务器上重新运行一次安装命令，并带上 OF_CHANNEL=beta。装好之后，再在面板里升级就会换到测试版。")

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
	Enabled    bool   `json:"enabled"`
	DisabledBy string `json:"disabled_by,omitempty"`
	Repo       string `json:"repo"`
	// Channel is "stable" or "beta" (OF_CHANNEL).
	Channel   string     `json:"channel"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	Error     string     `json:"error,omitempty"`
	// InstallDir and Project describe the install.sh deployment (set by the
	// installer in docker-compose.yml) so the panel can show exact upgrade commands.
	InstallDir string `json:"install_dir,omitempty"`
	Project    string `json:"project,omitempty"`
	// CanApply is true when the upgrade directory is writable, so the panel
	// can ask the host helper to upgrade. It does not mean Docker is reachable.
	CanApply bool `json:"can_apply"`
	// HelperStale is true when this install's host helper is older than the
	// one that can switch the image between latest and beta. One-click upgrade
	// to a beta tag is refused in that case.
	HelperStale bool `json:"helper_stale,omitempty"`
}

var (
	installDirPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)
	projectPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
)

// deployment returns the installer's install dir and compose project, or empty
// strings when they are not set or not safe to show inside a shell command.
func deployment() (dir, project string) {
	dir, project = os.Getenv("OF_HOST_INSTALL_DIR"), os.Getenv("OF_PROJECT")
	if !installDirPattern.MatchString(dir) {
		dir = ""
	}
	if !projectPattern.MatchString(project) {
		project = ""
	}
	return dir, project
}

// upgradeDir is the host-mounted directory Apply writes into. An empty result
// means the configured path is not safe to use.
func upgradeDir() string {
	dir := strings.TrimSpace(os.Getenv("OF_UPGRADE_DIR"))
	if dir == "" {
		dir = DefaultUpgradeDir
	}
	if !filepath.IsAbs(dir) || strings.Contains(dir, "..") || strings.ContainsAny(dir, "\n\r") || strings.IndexByte(dir, 0) >= 0 {
		return ""
	}
	return filepath.Clean(dir)
}

// safeReleaseTag is the only text Apply will put in the trigger file.
// Only vX.Y.Z and vX.Y.Z-beta.N are accepted; the host script checks the same shape.
func safeReleaseTag(tag string) bool {
	return len(tag) <= maxReleaseTag && releaseTagPattern.MatchString(tag)
}

const helperVersionCurrent = 2

func helperVersion(dir string) int {
	if dir == "" {
		return 0
	}
	b, err := os.ReadFile(filepath.Join(dir, "helper-version"))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// upgradeDirWritable reports whether dir can be written and the probe file removed.
func upgradeDirWritable(dir string) bool {
	if dir == "" {
		return false
	}
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	closeErr := f.Close()
	removeErr := os.Remove(name)
	return closeErr == nil && removeErr == nil
}

// writeUpgradeTrigger atomically publishes tag for the host helper.
func writeUpgradeTrigger(dir, tag string) error {
	f, err := os.CreateTemp(dir, ".upgrade-*")
	if err != nil {
		return err
	}
	name := f.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(name)
		}
	}()
	if _, err := f.Write([]byte(tag + "\n")); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(dir, "upgrade")); err != nil {
		return err
	}
	committed = true
	return nil
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
	Channel string // ChannelStable (default) or ChannelBeta
	API     string // GitHub API base URL, default https://api.github.com
	HTTP    *http.Client
	// EnvDisabled is set from OF_UPDATE_CHECK=0 and cannot be overridden in the UI.
	EnvDisabled bool
	// SettingsDisabled reports the Settings toggle; nil means enabled.
	SettingsDisabled func() bool
	// SettingsChannel reports the channel chosen in Settings. Empty means
	// follow Channel, which comes from OF_CHANNEL.
	SettingsChannel func() string
	Interval        time.Duration
	Now             func() time.Time

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

func (c *Checker) channel() string {
	if c.SettingsChannel != nil {
		switch c.SettingsChannel() {
		case ChannelBeta:
			return ChannelBeta
		case ChannelStable:
			return ChannelStable
		}
	}
	if c.Channel == ChannelBeta {
		return ChannelBeta
	}
	return ChannelStable
}

// Forget drops the cached release so the next check is not blocked by the
// manual cooldown and cannot show a result from the other channel.
func (c *Checker) Forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.latest = nil
	c.lastErr = ""
	c.checkedAt = time.Time{}
	c.lastAttempt = time.Time{}
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
	canApply := upgradeDirWritable(upgradeDir())
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Status{Current: c.Current, Repo: c.repo(), Channel: c.channel(), Error: c.lastErr}
	s.InstallDir, s.Project = deployment()
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
	s.CanApply = canApply
	dir := upgradeDir()
	// The install-time channel, not the Settings override. A stable install
	// whose helper never rewrites the image tag cannot move onto beta.
	s.HelperStale = canApply && helperVersion(dir) < helperVersionCurrent && c.Channel != ChannelBeta
	if !s.Enabled {
		s.Error = ""
	}
	return s
}

// Apply asks the host to upgrade to the cached newer release. The version
// comes only from that cache, never from the caller. It writes the tag and
// returns; the host helper pulls the image and recreates the container.
func (c *Checker) Apply() (string, error) {
	s := c.Status()
	if !s.Available || !safeReleaseTag(s.Latest) {
		return "", ErrNoUpdate
	}
	dir := upgradeDir()
	if !s.CanApply || dir == "" {
		return "", ErrUpgradeUnavailable
	}
	if strings.Contains(s.Latest, "-beta.") && s.HelperStale {
		return "", ErrHelperStale
	}
	if err := writeUpgradeTrigger(dir, s.Latest); err != nil {
		log.Printf("update apply: %v", err)
		return "", fmt.Errorf("升级请求没有写出去：%w", err)
	}
	return s.Latest, nil
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
	path := "/releases/latest"
	if c.channel() == ChannelBeta {
		path = "/releases?per_page=30"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/repos/"+repo+path, nil)
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
	if c.channel() == ChannelBeta {
		var list []release
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&list); err != nil {
			return nil, fmt.Errorf("检查更新失败：无法解析响应：%w", err)
		}
		best, ok := newestRelease(list)
		if !ok {
			return nil, errors.New("检查更新失败：仓库还没有发布版本")
		}
		rel = best
	} else {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
			return nil, fmt.Errorf("检查更新失败：无法解析响应：%w", err)
		}
		if rel.Draft || rel.Prerelease {
			return nil, errors.New("检查更新失败：最新发布不是正式版本")
		}
		if _, ok := parseSemver(rel.Tag); !ok {
			return nil, fmt.Errorf("检查更新失败：版本号 %q 无效", rel.Tag)
		}
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

// newestRelease picks the highest version among published releases whose tag
// is a stable or beta release tag. Drafts and other tags are ignored.
func newestRelease(list []release) (release, bool) {
	var best release
	var bestV semver
	found := false
	for _, r := range list {
		if r.Draft || !safeReleaseTag(r.Tag) {
			continue
		}
		v, ok := parseSemver(r.Tag)
		if !ok {
			continue
		}
		if !found || compareSemver(v, bestV) > 0 {
			best, bestV, found = r, v, true
		}
	}
	return best, found
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
