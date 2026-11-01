package bot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"onefrog/internal/model"
	"onefrog/internal/remote"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

type captureSSH struct {
	mu     sync.Mutex
	calls  []string
	host   string
	port   int
	user   string
	auth   string
	secret string
	known  []string
	// unknownOnce makes the first dial return the real "fingerprint not confirmed" error.
	unknownOnce bool
}

func (s *captureSSH) Run(ctx context.Context, host string, port int, user, authType, secret, knownKey, command string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, command)
	s.known = append(s.known, knownKey)
	s.host, s.port, s.user, s.auth, s.secret = host, port, user, authType, secret
	if s.unknownOnce && len(s.calls) == 1 {
		return "", "SHA256:test", remote.ErrHostKeyUnknown
	}
	return "ok:" + command, "SHA256:test", nil
}

func TestConnectServerFromChatThenRun(t *testing.T) {
	const password = "s3cret-pass"
	mem := store.NewMemory()
	box, err := secret.New(bytesRepeat())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = mem.CreateBot(ctx, model.Bot{ID: "main", Name: "主控", BaseURL: "http://model.test/v1", ModelName: "demo"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		call := n
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			_, _ = io.WriteString(w, toolJSON("c1", "connect_server", "{\"name\":\"edge\",\"host\":\"203.0.113.10\",\"port\":22,\"username\":\"root\",\"secret\":\""+password+"\",\"command\":\"uname -a\"}"))
			return
		}
		_, _ = io.WriteString(w, "{\"choices\":[{\"message\":{\"content\":\"已接入 "+password+"\"}}]}")
	}))
	defer srv.Close()
	main, _ := mem.GetBot(ctx, "main")
	main.BaseURL = srv.URL + "/v1"
	if _, err = mem.UpdateBot(ctx, main); err != nil {
		t.Fatal(err)
	}
	ssh := &captureSSH{}
	agent := &Agent{Store: mem, Box: box, SSH: ssh, HTTP: srv.Client()}
	result, err := agent.ReplyWithOptions(ctx, "main", "", "", "接入这台机器\n地址 203.0.113.10\n端口 22\n用户 root\n密码 "+password, Options{RestrictServers: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ssh.host != "203.0.113.10" || ssh.port != 22 || ssh.user != "root" || ssh.secret != password || ssh.auth != "password" {
		t.Fatalf("ssh target host=%s port=%d user=%s auth=%s", ssh.host, ssh.port, ssh.user, ssh.auth)
	}
	if len(ssh.calls) != 2 || ssh.calls[0] != "printf onefrog-ok" || ssh.calls[1] != "uname -a" {
		t.Fatalf("calls %#v", ssh.calls)
	}
	if strings.Contains(result.Text, password) {
		t.Fatal("reply echoed the password")
	}
	found := false
	for _, ev := range result.Tools {
		if strings.Contains(ev.Command, password) || strings.Contains(ev.Output, password) {
			t.Fatal("tool event leaked the password")
		}
		if ev.Name == "connect_server" && ev.OK && strings.Contains(ev.Output, "203.0.113.10") {
			found = true
		}
	}
	if !found {
		t.Fatal("connect event missing")
	}
	servers, err := mem.ListServers(ctx)
	if err != nil || len(servers) != 1 || servers[0].Host != "203.0.113.10" || servers[0].Name != "edge" || servers[0].Status != "online" {
		t.Fatalf("servers %+v %v", servers, err)
	}
}

func TestConnectServerRejectsUnmentionedHost(t *testing.T) {
	const password = "s3cret-pass"
	mem := store.NewMemory()
	box, _ := secret.New(bytesRepeat())
	ctx := context.Background()
	_, _ = mem.CreateBot(ctx, model.Bot{ID: "main", Name: "主控", BaseURL: "http://model.test/v1", ModelName: "demo"})
	var n int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		call := n
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			_, _ = io.WriteString(w, toolJSON("c1", "connect_server", "{\"host\":\"198.51.100.9\",\"secret\":\""+password+"\"}"))
			return
		}
		_, _ = io.WriteString(w, "{\"choices\":[{\"message\":{\"content\":\"没有接入\"}}]}")
	}))
	defer srv.Close()
	main, _ := mem.GetBot(ctx, "main")
	main.BaseURL = srv.URL + "/v1"
	_, _ = mem.UpdateBot(ctx, main)
	ssh := &captureSSH{}
	agent := &Agent{Store: mem, Box: box, SSH: ssh, HTTP: srv.Client()}
	_, err := agent.ReplyWithOptions(ctx, "main", "", "", "密码是 "+password+"，先别连接别的机器", Options{RestrictServers: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ssh.calls) != 0 {
		t.Fatal("ssh ran for a host the user did not name")
	}
	servers, _ := mem.ListServers(ctx)
	if len(servers) != 0 {
		t.Fatalf("server saved anyway %+v", servers)
	}
}

func TestConnectServerPinsFirstFingerprint(t *testing.T) {
	const password = "s3cret-pass"
	mem := store.NewMemory()
	box, err := secret.New(bytesRepeat())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = mem.CreateBot(ctx, model.Bot{ID: "main", Name: "主控", BaseURL: "http://model.test/v1", ModelName: "demo"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		call := n
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			_, _ = io.WriteString(w, toolJSON("c1", "connect_server", "{\"host\":\"203.0.113.10\",\"secret\":\""+password+"\"}"))
			return
		}
		_, _ = io.WriteString(w, "{\"choices\":[{\"message\":{\"content\":\"已接入\"}}]}")
	}))
	defer srv.Close()
	main, _ := mem.GetBot(ctx, "main")
	main.BaseURL = srv.URL + "/v1"
	if _, err = mem.UpdateBot(ctx, main); err != nil {
		t.Fatal(err)
	}
	ssh := &captureSSH{unknownOnce: true}
	agent := &Agent{Store: mem, Box: box, SSH: ssh, HTTP: srv.Client()}
	_, err = agent.ReplyWithOptions(ctx, "main", "", "", "接入 203.0.113.10 密码 "+password, Options{RestrictServers: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ssh.calls) != 2 || ssh.known[0] != "" || ssh.known[1] != "SHA256:test" {
		t.Fatalf("calls %#v known %#v", ssh.calls, ssh.known)
	}
	servers, err := mem.ListServers(ctx)
	if err != nil || len(servers) != 1 || servers[0].Status != "online" {
		t.Fatalf("servers %+v %v", servers, err)
	}
	full, err := mem.GetServer(ctx, servers[0].ID)
	if err != nil || full.HostKey != "SHA256:test" {
		t.Fatalf("host key %q %v", full.HostKey, err)
	}
}
