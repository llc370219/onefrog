package remote

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// startServer runs a minimal SSH server that answers every exec with "ok".
func startServer(t *testing.T, keys ...ssh.Signer) (string, int) {
	t.Helper()
	cfg := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	for _, k := range keys {
		cfg.AddHostKey(k)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, in, _ := nc.Accept()
					go func() {
						for r := range in {
							if r.Type == "exec" {
								_ = r.Reply(true, nil)
								_, _ = ch.Write([]byte("ok\n"))
								_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ S uint32 }{0}))
								ch.Close()
								return
							}
							_ = r.Reply(false, nil)
						}
					}()
				}
			}()
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

func signers(t *testing.T) (ed, ec ssh.Signer) {
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	var err error
	if ed, err = ssh.NewSignerFromKey(edKey); err != nil {
		t.Fatal(err)
	}
	if ec, err = ssh.NewSignerFromKey(ecKey); err != nil {
		t.Fatal(err)
	}
	return
}

func TestFirstContactPrefersEd25519AndShowsType(t *testing.T) {
	ed, ec := signers(t)
	host, port := startServer(t, ec, ed)
	ctx := context.Background()
	_, pin, err := Run(ctx, host, port, "u", "password", "p", "", "true")
	if !errors.Is(err, ErrHostKeyUnknown) {
		t.Fatal(err)
	}
	if pin != Fingerprint(ed.PublicKey()) || !strings.HasPrefix(pin, "ssh-ed25519 SHA256:") {
		t.Fatalf("want ed25519 pin, got %q", pin)
	}
	out, _, err := Run(ctx, host, port, "u", "password", "p", pin, "true")
	if err != nil || strings.TrimSpace(out) != "ok" {
		t.Fatal(out, err)
	}
}

func TestLegacyPinStillWorks(t *testing.T) {
	ed, ec := signers(t)
	host, port := startServer(t, ec, ed)
	// v0.1.1 stored the bare SHA256 of the key chosen with Go's default order (ECDSA).
	legacy := ssh.FingerprintSHA256(ec.PublicKey())
	if _, _, err := Run(context.Background(), host, port, "u", "password", "p", legacy, "true"); err != nil {
		t.Fatal(err)
	}
}

func TestMismatchAndRemovedKeyType(t *testing.T) {
	ed, ec := signers(t)
	ed2, _ := signers(t)
	ctx := context.Background()
	host, port := startServer(t, ec, ed2)
	if _, _, err := Run(ctx, host, port, "u", "password", "p", Fingerprint(ed.PublicKey()), "true"); !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatal("changed ed25519 key not detected:", err)
	}
	host, port = startServer(t, ec)
	if _, _, err := Run(ctx, host, port, "u", "password", "p", Fingerprint(ed.PublicKey()), "true"); !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatal("removed key type not reported as mismatch:", err)
	}
}

func TestSplitPin(t *testing.T) {
	if k, s := SplitPin("ssh-ed25519 SHA256:abc"); k != "ssh-ed25519" || s != "SHA256:abc" {
		t.Fatal(k, s)
	}
	if k, s := SplitPin("SHA256:abc"); k != "" || s != "SHA256:abc" {
		t.Fatal(k, s)
	}
}
