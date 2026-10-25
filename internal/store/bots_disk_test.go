package store

import (
	"bytes"
	"context"
	"onefrog/internal/model"
	"onefrog/internal/secret"
	"path/filepath"
	"testing"
)

func TestBotEditsPersist(t *testing.T) {
	box, _ := secret.New(bytes.Repeat([]byte{7}, 32))
	path := filepath.Join(t.TempDir(), "bots.enc")
	m := NewMemory()
	if e := m.EnableBotPersistence(path, box); e != nil {
		t.Fatal(e)
	}
	b, e := m.CreateBot(context.Background(), model.Bot{ID: "main", Name: "first", Description: "desc", SystemPrompt: "prompt"})
	if e != nil {
		t.Fatal(e)
	}
	b.Name = "renamed"
	_, e = m.UpdateBot(context.Background(), b)
	if e != nil {
		t.Fatal(e)
	}
	again := NewMemory()
	if e = again.EnableBotPersistence(path, box); e != nil {
		t.Fatal(e)
	}
	got, e := again.GetBot(context.Background(), "main")
	if e != nil || got.Name != "renamed" || got.SystemPrompt != "prompt" {
		t.Fatal(got, e)
	}
}
