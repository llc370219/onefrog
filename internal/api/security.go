package api

import (
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// CSRFHeader must accompany every state-changing API request. Browsers cannot
// attach custom headers to cross-site requests without a CORS preflight, which
// this server never grants, so a forged form or fetch from another site fails.
const CSRFHeader = "X-OneFrog-CSRF"

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; object-src 'none'; " +
	"base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

func securityHeaders(c *gin.Context) {
	h := c.Writer.Header()
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	c.Next()
}

func stateChanging(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// requireCSRF enforces the custom header and a JSON (or multipart for uploads)
// content type on state-changing /api requests. Probe reports authenticate with
// a per-node bearer token instead of the browser cookie and are exempt.
func requireCSRF(c *gin.Context) {
	r := c.Request
	if !stateChanging(r.Method) || r.URL.Path == "/api/probe/report" {
		c.Next()
		return
	}
	if r.Header.Get(CSRFHeader) != "1" {
		fail(c, http.StatusForbidden, "请求缺少防跨站校验头")
		return
	}
	if r.ContentLength == 0 && r.Header.Get("Content-Type") == "" {
		c.Next()
		return
	}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt == "application/json" || (mt == "multipart/form-data" && r.URL.Path == "/api/attachments") {
		c.Next()
		return
	}
	fail(c, http.StatusUnsupportedMediaType, "请求必须是 JSON")
}

// sameOrigin reports whether a browser Origin header matches the request host
// or the configured public URL. Requests without Origin (non-browser clients)
// are allowed; they cannot ride on a victim's browser cookie.
func sameOrigin(r *http.Request, publicURL string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	if publicURL != "" {
		if p, err := url.Parse(publicURL); err == nil && strings.EqualFold(p.Host, u.Host) && p.Scheme == u.Scheme {
			return true
		}
	}
	return false
}

// loginLimiter tracks failed logins per client IP. It deliberately does not lock
// usernames: a username lock lets anyone keep the admin locked out by typing
// wrong passwords from anywhere.
type loginLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	failures map[string]*failRecord
}

type failRecord struct {
	count int
	first time.Time
	until time.Time
}

const (
	loginWindow     = 15 * time.Minute
	ipFailThreshold = 5
	maxLockout      = 30 * time.Minute
)

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{now: time.Now, failures: map[string]*failRecord{}}
}

// blocked returns how long the client IP must wait, or 0.
func (l *loginLimiter) blocked(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if f := l.failures[ip]; f != nil && now.Before(f.until) {
		return f.until.Sub(now)
	}
	return 0
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	f := l.failures[ip]
	if f == nil || now.Sub(f.first) > loginWindow && now.After(f.until) {
		f = &failRecord{first: now}
		l.failures[ip] = f
	}
	f.count++
	if f.count >= ipFailThreshold {
		// Exponential backoff: 1, 2, 4 ... minutes, capped.
		lock := time.Minute << min(f.count-ipFailThreshold, 5)
		if lock > maxLockout {
			lock = maxLockout
		}
		f.until = now.Add(lock)
	}
	if len(l.failures) > 10000 {
		for k, f := range l.failures {
			if now.Sub(f.first) > loginWindow && now.After(f.until) {
				delete(l.failures, k)
			}
		}
	}
}

func (l *loginLimiter) succeed(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if f := l.failures[ip]; f != nil && !l.now().Before(f.until) {
		delete(l.failures, ip)
	}
}

func retryAfter(d time.Duration) string {
	return fmt.Sprint(int(d.Seconds()) + 1)
}

// trustedProxies parses OF_TRUSTED_PROXIES; by default only loopback and
// private networks (where a reverse proxy or the Docker bridge usually sits).
func trustedProxies(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"}
	}
	if strings.TrimSpace(raw) == "none" {
		return nil
	}
	out := []string{}
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") && net.ParseIP(p) == nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// limitBody caps request bodies for routes that do not set their own limit.
func limitBody(c *gin.Context) {
	if c.Request.Body != nil && c.Request.URL.Path != "/api/attachments" {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20)
	}
	c.Next()
}
