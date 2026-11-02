package devmodule

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"onefrog/internal/secret"
	"onefrog/internal/store"
)

func TestBookLifecyclePersistAndSync(t *testing.T) {
	box, err := secret.New(bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "devmodules.enc")
	book, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	created, err := book.Create(Input{
		Name:        "打卡",
		Description: "点击记录时间与备注",
		HTML:        StarterHTML("打卡"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.ID, CustomPrefix) {
		t.Fatalf("unexpected id %q", created.ID)
	}
	if _, err = book.Create(Input{Name: "打卡", HTML: "<div>dup</div>"}); err == nil {
		t.Fatal("duplicate custom module should fail")
	}
	if _, err = book.SaveState("打卡", map[string]any{"streak": 3}, true); err != nil {
		t.Fatal(err)
	}
	payload, err := book.AddRecord(created.ID, map[string]any{"note": "早班打卡"})
	if err != nil || len(payload.Records) != 1 || payload.Records[0].Data["note"] != "早班打卡" {
		t.Fatalf("add record: %+v %v", payload, err)
	}

	// Update HTML without losing saved state or records.
	newHTML := "<div>v2</div>"
	updated, err := book.Update("打卡", Patch{HTML: &newHTML})
	if err != nil || len(updated.Records) != 1 || updated.State["streak"] == nil {
		t.Fatalf("update lost data: %+v %v", updated, err)
	}

	// Reload from encrypted disk.
	reloaded, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.Get(created.ID)
	if err != nil || got.HTML != "<div>v2</div>" || len(got.Records) != 1 {
		t.Fatalf("reloaded: %+v %v", got, err)
	}

	// Sync with Store and verify custom module registration & cleanup.
	mem := store.NewMemory()
	ctx := context.Background()
	if err = SyncStore(ctx, mem, reloaded); err != nil {
		t.Fatal(err)
	}
	mod, err := mem.GetModule(ctx, created.ID)
	if err != nil || mod.Type != "custom" || !mod.Enabled || mod.Name != "打卡" {
		t.Fatalf("synced mod: %+v %v", mod, err)
	}
	if _, err = reloaded.Delete(created.ID); err != nil {
		t.Fatal(err)
	}
	if err = SyncStore(ctx, mem, reloaded); err != nil {
		t.Fatal(err)
	}
	if _, err = mem.GetModule(ctx, created.ID); err == nil {
		t.Fatal("deleted custom module remained in store")
	}

	frame := RenderFrameHTML(got)
	if !strings.Contains(frame, "window.onefrog") || !strings.Contains(frame, "<div>v2</div>") {
		t.Fatal("frame missing bridge or body")
	}
}
