package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"onefrog/internal/model"
)

func TestProjectNotesPersistAndFollowSessionLifecycle(t *testing.T) {
	ctx, dir, box := context.Background(), t.TempDir(), persistBox(t)
	mem := reopen(t, dir, box)
	for _, sid := range []string{"group-a", "group-b", "direct"} {
		if _, err := mem.EnsureSession(ctx, sid, "main", sid); err != nil {
			t.Fatal(err)
		}
	}
	if err := mem.SaveChatOptions(ctx, "direct", json.RawMessage(`{"mode":"direct"}`)); err != nil {
		t.Fatal(err)
	}
	note, err := mem.UpsertProjectNote(ctx, "group-a", model.ProjectNote{Title: "目标", Body: "生产环境"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.UpsertProjectNote(ctx, "direct", note); !errors.Is(err, ErrNotFound) {
		t.Fatalf("direct session accepted project note: %v", err)
	}
	mem = reopen(t, dir, box)
	rows, err := mem.ListProjectNotes(ctx, "group-a")
	if err != nil || len(rows) != 1 || rows[0].ID != note.ID || rows[0].Body != note.Body {
		t.Fatalf("note lost after restart: %+v %v", rows, err)
	}
	if err := mem.DeleteProjectNote(ctx, "group-b", note.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other project could delete note: %v", err)
	}
	if err := mem.DeleteChatSession(ctx, "group-a"); err != nil {
		t.Fatal(err)
	}
	mem = reopen(t, dir, box)
	if _, err := mem.ListProjectNotes(ctx, "group-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("project survived deletion: %v", err)
	}
}
