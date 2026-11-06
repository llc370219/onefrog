package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const releaseList = `[
 {"tag_name":"v0.1.27-beta.1","html_url":"https://github.com/llc370219/onefrog/releases/tag/v0.1.27-beta.1","prerelease":true,"draft":true},
 {"tag_name":"v0.1.26-beta.2","html_url":"https://github.com/llc370219/onefrog/releases/tag/v0.1.26-beta.2","prerelease":true,"body":"beta notes"},
 {"tag_name":"v0.1.26-rc.9","html_url":"https://github.com/llc370219/onefrog/releases/tag/v0.1.26-rc.9","prerelease":true},
 {"tag_name":"v0.1.25","html_url":"https://github.com/llc370219/onefrog/releases/tag/v0.1.25"},
 {"tag_name":"v0.1.26-beta.1","html_url":"https://github.com/llc370219/onefrog/releases/tag/v0.1.26-beta.1","prerelease":true}
]`

func TestBetaChannelFollowsPrereleases(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.RequestURI()
		_, _ = w.Write([]byte(releaseList))
	}))
	defer srv.Close()
	c := &Checker{Current: "v0.1.26-beta.1", Channel: ChannelBeta, API: srv.URL}
	s, err := c.CheckNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/repos/llc370219/onefrog/releases?per_page=30" {
		t.Fatalf("beta channel requested %s", path)
	}
	// Drafts and non-beta pre-releases (rc) are skipped.
	if !s.Available || s.Latest != "v0.1.26-beta.2" || s.Notes != "beta notes" || s.Channel != ChannelBeta {
		t.Fatalf("unexpected status %+v", s)
	}
	dir := t.TempDir()
	t.Setenv("OF_UPGRADE_DIR", dir)
	got, err := c.Apply()
	if err != nil || got != "v0.1.26-beta.2" {
		t.Fatalf("apply %q %v", got, err)
	}
	if body, _ := os.ReadFile(filepath.Join(dir, "upgrade")); string(body) != "v0.1.26-beta.2\n" {
		t.Fatalf("trigger %q", body)
	}
}

func TestBetaChannelMovesToNewerStable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v0.1.26","html_url":"x"},{"tag_name":"v0.1.26-beta.3","prerelease":true,"html_url":"x"}]`))
	}))
	defer srv.Close()
	c := &Checker{Current: "v0.1.26-beta.3", Channel: ChannelBeta, API: srv.URL}
	s, err := c.CheckNow(context.Background())
	if err != nil || !s.Available || s.Latest != "v0.1.26" {
		t.Fatalf("status %+v err %v", s, err)
	}
}

func TestStableChannelIsDefault(t *testing.T) {
	srv, _ := fakeGitHub(t, 200, releaseJSON)
	c := &Checker{Current: "v0.1.1", API: srv.URL}
	if s, err := c.CheckNow(context.Background()); err != nil || s.Channel != ChannelStable || s.Latest != "v0.2.0" {
		t.Fatalf("status %+v err %v", s, err)
	}
}

func TestSettingsChannelOverridesInstallChannel(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.RequestURI()
		_, _ = w.Write([]byte("[{\"tag_name\":\"v0.1.31-beta.1\",\"prerelease\":true,\"html_url\":\"https://github.com/llc370219/onefrog/releases/tag/v0.1.31-beta.1\"}]"))
	}))
	defer srv.Close()
	chosen := ChannelBeta
	c := &Checker{Current: "v0.1.30", Channel: ChannelStable, API: srv.URL, SettingsChannel: func() string { return chosen }}
	s, err := c.CheckNow(context.Background())
	if err != nil || s.Channel != ChannelBeta || s.Latest != "v0.1.31-beta.1" || !s.Available || path != "/repos/llc370219/onefrog/releases?per_page=30" {
		t.Fatalf("status %+v path %s err %v", s, path, err)
	}
	chosen = ""
	c.Forget()
	if s := c.Status(); s.Channel != ChannelStable || s.Latest != "" || s.CheckedAt != nil {
		t.Fatalf("forget did not restore the install channel: %+v", s)
	}
}

func TestNormalizeChannelAndTags(t *testing.T) {
	for in, want := range map[string]string{"": ChannelStable, "Stable": ChannelStable, "latest": ChannelStable, "beta": ChannelBeta, "test": ChannelBeta} {
		if got, ok := NormalizeChannel(in); !ok || got != want {
			t.Fatalf("%q -> %q %v", in, got, ok)
		}
	}
	if _, ok := NormalizeChannel("nightly"); ok {
		t.Fatal("unknown channel accepted")
	}
	for tag, want := range map[string]bool{"v0.1.26": true, "v0.1.26-beta.1": true, "v0.1.26-rc.1": false, "v0.1.26-beta": false, "v0.1.26-beta.1; rm -rf /": false} {
		if safeReleaseTag(tag) != want {
			t.Fatalf("safeReleaseTag(%q) != %v", tag, want)
		}
	}
}
