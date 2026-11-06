package migration

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Proof binds the one-use capability to the exact transfer bytes. The code is
// never sent over the network; it also encrypts the archive and authenticates
// the receiver's acknowledgement, including on an IP-only HTTP installation.
func Proof(code, purpose string, body []byte) string {
	h := hmac.New(sha256.New, []byte(code))
	h.Write([]byte(purpose))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}
func Verify(code, purpose string, body []byte, proof string) bool {
	return hmac.Equal([]byte(Proof(code, purpose, body)), []byte(proof))
}

func blockedIP(ip net.IP) bool {
	return ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

// TransferClient pins a validated address, refuses metadata/loopback endpoints,
// proxies and redirects, and allows private networks for user-owned machines.
func TransferClient(ctx context.Context, address string) (*http.Client, string, error) {
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, "", errors.New("请输入新机的完整面板地址，如 https://panel.example.com 或 http://IP:8080")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil || len(ips) == 0 {
		return nil, "", errors.New("无法解析新服务器地址")
	}
	for _, a := range ips {
		if blockedIP(a.IP) {
			return nil, "", errors.New("不能迁移到回环、链路本地或元数据地址")
		}
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	target := net.JoinHostPort(ips[0].IP.String(), port)
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	transport := &http.Transport{TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 10 * time.Minute, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, target)
	}}
	client := &http.Client{Timeout: 15 * time.Minute, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("迁移地址不能重定向") }}
	return client, strings.TrimRight(u.String(), "/"), nil
}
