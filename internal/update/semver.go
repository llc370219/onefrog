package update

import (
	"strconv"
	"strings"
)

// semver is a parsed MAJOR.MINOR.PATCH[-PRERELEASE] version; build metadata
// after "+" is ignored.
type semver struct {
	major, minor, patch int
	pre                 []string
}

// parseSemver accepts an optional leading "v". It reports false for anything
// that is not a full semantic version, such as "dev" or "1.2".
func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var nums [3]int
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return semver{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		nums[i] = n
	}
	v := semver{major: nums[0], minor: nums[1], patch: nums[2]}
	if hasPre {
		if pre == "" {
			return semver{}, false
		}
		v.pre = strings.Split(pre, ".")
		for _, id := range v.pre {
			if id == "" {
				return semver{}, false
			}
		}
	}
	return v, true
}

// compareSemver returns -1, 0 or 1 following semver.org precedence rules.
func compareSemver(a, b semver) int {
	for _, d := range [3]int{a.major - b.major, a.minor - b.minor, a.patch - b.patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, y := a.pre[i], b.pre[i]
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil:
			if xn != yn {
				return sign(xn - yn)
			}
		case xerr == nil:
			return -1
		case yerr == nil:
			return 1
		default:
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
		}
	}
	return sign(len(a.pre) - len(b.pre))
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// Newer reports whether latest is a strictly higher semantic version than
// current. Unparseable versions (for example "dev") never count as newer.
func Newer(latest, current string) bool {
	l, ok1 := parseSemver(latest)
	c, ok2 := parseSemver(current)
	return ok1 && ok2 && compareSemver(l, c) > 0
}
