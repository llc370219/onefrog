package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"onefrog/internal/model"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

func TestRecallIsolatesWorkersAndRedactsVault(t *testing.T) {
	const vault = "p@ss-vault-99"
	mem, box := memoryFixture(t, vault)
	ctx := context.Background()
	agent := &Agent{Store: mem, Box: box}
	leader, err := mem.GetBot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := mem.GetBot(ctx, "check")
	if err != nil {
		t.Fatal(err)
	}

	saved, err := agent.Remember(ctx, worker.ID, "库密码 "+vault, "连库用 "+vault, "bot")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(saved.Title, vault) || strings.Contains(saved.Body, vault) || !strings.Contains(saved.Body, "***") || !strings.Contains(saved.Title, "***") {
		t.Fatalf("plaintext stored: %+v", saved)
	}
	if _, err = agent.Remember(ctx, leader.ID, "主控笔记", "只有主控看", "user"); err != nil {
		t.Fatal(err)
	}

	// The leader may not read a worker's private memory; it must go through a
	// delegation or handoff. Titles and bodies stay private.
	for _, raw := range []string{"{\"bot_id\":\"巡检\"}", "{\"bot_id\":\"check\"}"} {
		seen := agent.recallCall(ctx, leader, recallArgs(raw))
		if !strings.Contains(seen, "只能读取自己的记忆") || strings.Contains(seen, "库密码") || strings.Contains(seen, vault) {
			t.Fatal(seen)
		}
	}
	own := agent.recallCall(ctx, leader, recallArgs(""))
	if !strings.Contains(own, "只有主控看") || strings.Contains(own, "库密码") {
		t.Fatal(own)
	}

	workerCtx := context.WithValue(ctx, usageKey{}, usageActor{Purpose: "worker"})
	denied := agent.recallCall(workerCtx, worker, recallArgs("{\"bot_id\":\"主控\"}"))
	if !strings.Contains(denied, "只能读取自己的记忆") || strings.Contains(denied, "只有主控看") {
		t.Fatal(denied)
	}
	own = agent.recallCall(workerCtx, worker, recallArgs(""))
	if !strings.Contains(own, "***") || strings.Contains(own, vault) || strings.Contains(own, "只有主控看") {
		t.Fatal(own)
	}

	asset, _ := json.Marshal(toolDefs(false, true))
	if strings.Contains(string(asset), "remember") || strings.Contains(string(asset), "recall") {
		t.Fatal("asset bot has memory tools")
	}
	leaderTools, _ := json.Marshal(toolDefs(true, false))
	if !strings.Contains(string(leaderTools), "remember") || !strings.Contains(string(leaderTools), "recall") {
		t.Fatal("leader missing memory tools")
	}
}

func TestProjectHandoffMemoryIsSharedOnlyWithinTheSameGroup(t *testing.T) {
	const vault = "p@ss-vault-99"
	const task = "确认 web-01 磁盘"
	mem, box := memoryFixture(t, vault)
	ctx := context.Background()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		n := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1, 4:
			_, _ = io.WriteString(w, toolJSON("c"+fmt.Sprint(n), "ask_agent", "{\"bot_id\":\"巡检\",\"task\":\""+task+"\"}"))
		case 2:
			fmt.Fprintf(w, "{\"choices\":[{\"message\":{\"content\":\"磁盘还剩 40%%，口令 %s\"}}]}", vault)
		case 5:
			_, _ = io.WriteString(w, "{\"choices\":[{\"message\":{\"content\":\"沿用上次结论\"}}]}")
		default:
			_, _ = io.WriteString(w, "{\"choices\":[{\"message\":{\"content\":\"已记录\"}}]}")
		}
	}))
	defer srv.Close()

	main, _ := mem.GetBot(ctx, "main")
	main.BaseURL = srv.URL + "/v1"
	if _, err := mem.UpdateBot(ctx, main); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Store: mem, Box: box, SSH: &scriptSSH{}, HTTP: srv.Client()}
	first, err := agent.ReplyWithOptions(ctx, "main", "", "", "让巡检看磁盘", Options{ProjectTitle: "容量检查"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sess, err := mem.GetChatSession(ctx, first.SessionID); err != nil || sess.Title != "容量检查" {
		t.Fatalf("project title: %+v %v", sess, err)
	}
	items, err := mem.ListProjectNotes(ctx, first.SessionID)
	if err != nil || len(items) != 1 || items[0].Source != "handoff" || items[0].Title != task {
		t.Fatalf("%+v %v", items, err)
	}
	if strings.Contains(items[0].Body, vault) || !strings.Contains(items[0].Body, "磁盘还剩 40%") || !strings.Contains(items[0].Body, "***") {
		t.Fatal(items[0].Body)
	}

	if notes, err := mem.ListBotMemories(ctx, "check"); err != nil || len(notes) != 0 {
		t.Fatalf("project contaminated private memory: %+v %v", notes, err)
	}
	if _, err = agent.Remember(ctx, "main", "私聊偏好", "只属于单聊", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Reply(ctx, "main", "", first.SessionID, "再确认一次"); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Reply(ctx, "main", "", "", "另一个项目"); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.ReplyWithOptions(ctx, "main", "", "", "单聊", Options{Mode: "direct"}, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 8 {
		t.Fatalf("model calls %d", len(bodies))
	}
	for _, body := range bodies {
		if strings.Contains(body, vault) {
			t.Fatal("model request contained the vault secret")
		}
	}
	if strings.Contains(bodies[0], task) {
		t.Fatal("memory was injected before the handoff finished")
	}
	for _, i := range []int{3, 4} {
		if !strings.Contains(bodies[i], "磁盘还剩 40%") || strings.Contains(bodies[i], "只属于单聊") {
			t.Fatalf("call %d used the wrong memory: %s", i+1, bodies[i])
		}
	}
	for _, i := range []int{6, 7} {
		if strings.Contains(bodies[i], task) || strings.Contains(bodies[i], "磁盘还剩 40%") {
			t.Fatalf("project memory leaked to call %d", i+1)
		}
	}
	if strings.Contains(bodies[6], "只属于单聊") || !strings.Contains(bodies[7], "只属于单聊") {
		t.Fatal("private note was not confined to the direct chat")
	}
}

func TestHandoffSkipsEmptyAndPaused(t *testing.T) {
	mem, box := memoryFixture(t, "")
	ctx := context.Background()
	conclusions := []string{"  没有结论  ", "这轮步骤用完了，先停在这里。下一步看证书"}
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n++
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1, 4:
			_, _ = io.WriteString(w, toolJSON("c", "ask_agent", "{\"bot_id\":\"巡检\",\"task\":\"看一下\"}"))
		case 2:
			fmt.Fprintf(w, "{\"choices\":[{\"message\":{\"content\":%q}}]}", conclusions[0])
		case 5:
			fmt.Fprintf(w, "{\"choices\":[{\"message\":{\"content\":%q}}]}", conclusions[1])
		default:
			_, _ = io.WriteString(w, "{\"choices\":[{\"message\":{\"content\":\"好\"}}]}")
		}
	}))
	defer srv.Close()
	main, _ := mem.GetBot(ctx, "main")
	main.BaseURL = srv.URL + "/v1"
	if _, err := mem.UpdateBot(ctx, main); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Store: mem, Box: box, HTTP: srv.Client()}
	for _, task := range []string{"第一次", "第二次"} {
		res, err := agent.Reply(ctx, "main", "", "", task)
		if err != nil {
			t.Fatal(err)
		}
		notes, err := mem.ListProjectNotes(ctx, res.SessionID)
		if err != nil || len(notes) != 0 {
			t.Fatalf("skipped conclusions were saved: %+v %v", notes, err)
		}
	}
}

func memoryFixture(t *testing.T, vault string) (*store.Memory, *secret.Box) {
	t.Helper()
	mem := store.NewMemory()
	box, err := secret.New(bytesRepeat())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if vault != "" {
		enc, err := box.Encrypt(vault)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = mem.CreateSecret(ctx, store.SecretRec{Secret: model.Secret{Name: "db-password"}, EncryptedValue: enc}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = mem.CreateBot(ctx, model.Bot{ID: "main", Name: "主控", Description: "接收指令，分派任务"}); err != nil {
		t.Fatal(err)
	}
	if _, err = mem.CreateBot(ctx, model.Bot{ID: "check", Name: "巡检", Description: "看磁盘"}); err != nil {
		t.Fatal(err)
	}
	return mem, box
}

func recallArgs(raw string) toolCall {
	var call toolCall
	call.Function.Name = "recall"
	call.Function.Arguments = raw
	return call
}
