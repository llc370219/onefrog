package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

const releasesURL = "https://github.com/llc370219/frog-bot/releases"
const latestReleaseAPI = "https://api.github.com/repos/llc370219/frog-bot/releases/latest"

// Version may be set for a release build with -ldflags "-X frogbot/internal/bot.Version=vX.Y.Z".
var Version string

func currentVersion() string {
	if Version != "" {
		return Version
	}
	if build, ok := debug.ReadBuildInfo(); ok && build.Main.Version != "" && build.Main.Version != "(devel)" {
		return build.Main.Version
	}
	return "开发版"
}

type updateStatus struct {
	Current    string `json:"current"`
	Latest     string `json:"latest,omitempty"`
	ReleaseURL string `json:"release_url"`
	State      string `json:"state"`
}

var releaseClient = &http.Client{Timeout: 8 * time.Second}

func checkUpdate(ctx context.Context, client *http.Client, endpoint, current string) (updateStatus, error) {
	status := updateStatus{Current: current, ReleaseURL: releasesURL, State: "no_release"}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return status, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return status, errors.New("无法连接 GitHub，请稍后重试")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return status, nil
	}
	if resp.StatusCode != http.StatusOK {
		return status, fmt.Errorf("GitHub 暂时无法提供更新信息（HTTP %d）", resp.StatusCode)
	}
	var release struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil || strings.TrimSpace(release.Tag) == "" {
		return status, errors.New("无法读取 GitHub 发布信息")
	}
	status.Latest = release.Tag
	status.ReleaseURL += "/tag/" + url.PathEscape(release.Tag)
	status.State = "unversioned"
	if newer, comparable := newerRelease(current, release.Tag); comparable {
		status.State = "current"
		if newer {
			status.State = "available"
		}
	}
	return status, nil
}

var releaseVersion = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// The GitHub latest endpoint returns a stable release. Compare its core version
// numerically, treating a local prerelease of that version as older.
func newerRelease(current, latest string) (newer, comparable bool) {
	a, b := releaseVersion.FindStringSubmatch(current), releaseVersion.FindStringSubmatch(latest)
	if a == nil || b == nil || b[4] != "" {
		return false, false
	}
	for i := 1; i <= 3; i++ {
		left, errA := strconv.ParseUint(a[i], 10, 64)
		right, errB := strconv.ParseUint(b[i], 10, 64)
		if errA != nil || errB != nil {
			return false, false
		}
		if left != right {
			return left < right, true
		}
	}
	return a[4] != "", true
}
