package update

import "testing"

func TestNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v0.1.1", "v0.1.0", true},
		{"v0.2.0", "v0.1.9", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.1.10", "v0.1.9", true},
		{"0.1.1", "v0.1.0", true},
		{"v0.1.0", "v0.1.0", false},
		{"v0.1.0", "v0.1.1", false},
		{"v0.1.0", "v0.1.0-rc.1", true},
		{"v0.1.0-rc.2", "v0.1.0-rc.1", true},
		{"v0.1.0-rc.10", "v0.1.0-rc.2", true},
		{"v0.1.0-rc.1", "v0.1.0", false},
		{"v0.1.0-beta", "v0.1.0-alpha", true},
		{"v0.1.0-alpha.1", "v0.1.0-alpha", true},
		{"v0.1.0-alpha", "v0.1.0-1", true},
		{"v0.1.1+build.5", "v0.1.1", false},
		{"v0.1.1", "dev", false},
		{"v0.1.1", "", false},
		{"garbage", "v0.1.0", false},
		{"v0.1", "v0.0.1", false},
		{"v01.0.0", "v0.0.1", false},
		{"v1.0.0-", "v0.0.1", false},
		{"v1.0.0-a..b", "v0.0.1", false},
	}
	for _, c := range cases {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestValidRepo(t *testing.T) {
	for repo, want := range map[string]bool{
		"llc370219/onefrog": true, "a-b/c.d_e": true,
		"": false, "onefrog": false, "a/b/c": false, "a/b?x=1": false, "../x": false,
	} {
		if ValidRepo(repo) != want {
			t.Errorf("ValidRepo(%q) != %v", repo, want)
		}
	}
}
