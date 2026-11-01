package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onefrog/internal/model"
)

func TestBotMemoriesIsolateCapAndRestart(t *testing.T) {
	dir, box, ctx := t.TempDir(), persistBox(t), context.Background()
	m := reopen(t, dir, box)
	if _, err := m.CreateBot(ctx, model.Bot{ID: "a", Name: "甲"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateBot(ctx, model.Bot{ID: "b", Name: "乙"}); err != nil {
		t.Fatal(err)
	}

	first, err := m.UpsertBotMemory(ctx, model.BotMemory{BotID: "a", Title: "磁盘", Body: "还剩 40%", Source: "user"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.UpsertBotMemory(ctx, model.BotMemory{BotID: "a", Title: "磁盘", Body: "还剩 20%", Source: "handoff"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Body != "还剩 20%" || second.Source != "handoff" || !second.UpdatedAt.After(first.UpdatedAt) && !second.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("same title did not replace: %+v %+v", first, second)
	}
	if _, err = m.UpsertBotMemory(ctx, model.BotMemory{BotID: "b", Title: "磁盘", Body: "乙的独有笔记", Source: "nope"}); err != nil {
		t.Fatal(err)
	}
	own, err := m.ListBotMemories(ctx, "a")
	if err != nil || len(own) != 1 || own[0].Body != "还剩 20%" {
		t.Fatalf("bot a %+v %v", own, err)
	}
	peer, err := m.ListBotMemories(ctx, "b")
	if err != nil || len(peer) != 1 || peer[0].Body != "乙的独有笔记" || peer[0].Source != "bot" {
		t.Fatalf("bot b %+v %v", peer, err)
	}
	if _, err = m.UpsertBotMemory(ctx, model.BotMemory{BotID: "a", Title: "   ", Body: "x"}); err == nil || !strings.Contains(err.Error(), "标题") {
		t.Fatal(err)
	}
	if _, err = m.UpsertBotMemory(ctx, model.BotMemory{BotID: "missing", Title: "t", Body: "b"}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	clipped, err := m.UpsertBotMemory(ctx, model.BotMemory{BotID: "b", Title: strings.Repeat("记", 100), Body: strings.Repeat("内", 900), Source: "user"})
	if err != nil || len([]rune(clipped.Title)) != maxMemoryTitle || len([]rune(clipped.Body)) != maxMemoryBody {
		t.Fatalf("clip %+v %v", clipped, err)
	}
	if err = m.DeleteBotMemory(ctx, "a", peer[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-bot delete", err)
	}

	for i := 0; i < MaxBotMemories+5; i++ {
		title := fmt.Sprintf("n%02d", i)
		if _, err = m.UpsertBotMemory(ctx, model.BotMemory{BotID: "a", Title: title, Body: "cap-body-" + title, Source: "bot"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	kept, err := m.ListBotMemories(ctx, "a")
	if err != nil || len(kept) != MaxBotMemories {
		t.Fatalf("cap %d %v", len(kept), err)
	}
	if kept[0].Title != "n44" || kept[len(kept)-1].Title != "n05" {
		t.Fatalf("order %s .. %s", kept[0].Title, kept[len(kept)-1].Title)
	}
	for _, item := range kept {
		if item.Title == "磁盘" || item.Title == "n00" || item.BotID != "a" {
			t.Fatalf("kept %+v", item)
		}
	}
	refreshed, err := m.UpsertBotMemory(ctx, model.BotMemory{BotID: "a", Title: "n05", Body: "refreshed-n05", Source: "user"})
	if err != nil || refreshed.Body != "refreshed-n05" {
		t.Fatal(refreshed, err)
	}
	time.Sleep(time.Millisecond)
	if _, err = m.UpsertBotMemory(ctx, model.BotMemory{BotID: "a", Title: "n45", Body: "cap-body-n45", Source: "bot"}); err != nil {
		t.Fatal(err)
	}
	kept, _ = m.ListBotMemories(ctx, "a")
	if len(kept) != MaxBotMemories || kept[0].Title != "n45" {
		t.Fatalf("after refresh %+v", titlesOf(kept))
	}
	for _, item := range kept {
		if item.Title == "n06" {
			t.Fatal("oldest after refresh was not dropped")
		}
		if item.Title == "n05" && item.Body != "refreshed-n05" {
			t.Fatal(item.Body)
		}
	}

	m = reopen(t, dir, box)
	kept, err = m.ListBotMemories(ctx, "a")
	if err != nil || len(kept) != MaxBotMemories || kept[0].Body != "cap-body-n45" {
		t.Fatalf("restart a %d %v", len(kept), err)
	}
	peer, err = m.ListBotMemories(ctx, "b")
	if err != nil || len(peer) != 2 {
		t.Fatalf("restart b %+v %v", peer, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, memoriesFile))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{"乙的独有笔记", "cap-body-n45", "refreshed-n05", "还剩 20%"} {
		if strings.Contains(text, secret) {
			t.Fatal("memories file is not encrypted")
		}
	}
	if err = m.DeleteBotMemory(ctx, "b", peer[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = m.DeleteBot(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if left, _ := m.ListBotMemories(ctx, "a"); len(left) != 0 {
		t.Fatal(left)
	}
	m = reopen(t, dir, box)
	if left, _ := m.ListBotMemories(ctx, "a"); len(left) != 0 {
		t.Fatal("deleted bot memories came back", left)
	}
	if left, _ := m.ListBotMemories(ctx, "b"); len(left) != 1 {
		t.Fatal(left)
	}
}

func titlesOf(items []model.BotMemory) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.Title
	}
	return out
}
