package update

import "testing"

func TestStableTag(t *testing.T) {
	for tag, want := range map[string]bool{
		"v0.1.32": true, "v1.0.0": true,
		"v0.1.32-beta.12": false, "v0.2.0-rc.1": false, "": false, "latest": false,
	} {
		if got := StableTag(tag); got != want {
			t.Errorf("StableTag(%q) = %v, want %v", tag, got, want)
		}
	}
}

// Auto-upgrade must never install a pre-release, even on the beta channel.
func TestAutoApplySkipsPrerelease(t *testing.T) {
	c := &Checker{}
	for _, tag := range []string{"v0.1.33-beta.1", "v0.2.0-rc.1"} {
		c.maybeAutoApply(Status{AutoUpdate: true, CanApply: true, Available: true, Latest: tag})
		if c.lastAutoApplied != "" {
			t.Fatalf("auto-applied pre-release %s", tag)
		}
	}
}
