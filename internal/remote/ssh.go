package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrHostKeyUnknown is returned (together with the presented fingerprint) when
// no host key has been confirmed for the server yet. The handshake is aborted
// before any credential is sent, so nothing is trusted silently.
var ErrHostKeyUnknown = errors.New("主机指纹尚未确认，请核对后再连接")

// ErrHostKeyMismatch means the server presented a different key than the one
// stored. The stored key is never replaced automatically.
var ErrHostKeyMismatch = errors.New("主机密钥不匹配：服务器指纹与已保存的不一致，可能是重装了系统或遭到中间人攻击。确认无误后请由管理员重置已保存的指纹")

// Run executes command over SSH. knownKey must be the confirmed SHA256
// fingerprint of the host; an empty knownKey only probes the fingerprint and
// returns ErrHostKeyUnknown.
func Run(ctx context.Context, host string, port int, user, authType, secret, knownKey, command string) (string, string, error) {
	if port == 0 {
		port = 22
	}
	auth, err := method(authType, secret)
	if err != nil {
		return "", "", err
	}
	var seen string
	cfg := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{auth},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			seen = ssh.FingerprintSHA256(key)
			if knownKey == "" {
				return ErrHostKeyUnknown
			}
			if knownKey != seen {
				return ErrHostKeyMismatch
			}
			return nil
		},
		Timeout: 8 * time.Second,
	}
	dialer := net.Dialer{Timeout: 8 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return "", seen, err
	}
	_ = conn.SetDeadline(time.Now().Add(12 * time.Second))
	stopHandshake := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopHandshake()
	clientConn, chans, reqs, err := ssh.NewClientConn(conn, host, cfg)
	if err != nil {
		conn.Close()
		switch {
		case errors.Is(err, ErrHostKeyUnknown):
			return "", seen, ErrHostKeyUnknown
		case errors.Is(err, ErrHostKeyMismatch):
			return "", seen, ErrHostKeyMismatch
		}
		return "", seen, err
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(clientConn, chans, reqs)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return "", seen, err
	}
	defer session.Close()
	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() {
		out, runErr = session.CombinedOutput(command)
		close(done)
	}()
	select {
	case <-ctx.Done():
		_ = session.Close()
		return "", seen, ctx.Err()
	case <-done:
		return string(out), seen, runErr
	}
}

func method(authType, secret string) (ssh.AuthMethod, error) {
	if secret == "" {
		return nil, fmt.Errorf("未配置登录密钥")
	}
	if authType == "private_key" {
		var signer ssh.Signer
		var err error
		if strings.HasPrefix(secret, "onefrog-ssh-v1:") {
			var c struct {
				Key        string `json:"key"`
				Passphrase string `json:"passphrase"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(secret, "onefrog-ssh-v1:")), &c) != nil {
				return nil, fmt.Errorf("私钥凭据格式无效")
			}
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(c.Key), []byte(c.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(secret))
		}
		if err != nil {
			return nil, fmt.Errorf("私钥无法解析")
		}
		return ssh.PublicKeys(signer), nil
	}
	return ssh.Password(secret), nil
}
