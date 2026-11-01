package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/secret"
)

func TestSecretLimitAndCardRoundTrip(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	for i := 0; i < secret.MaxCount; i++ {
		name := "s" + strings.Repeat("x", 0) + itoa(i)
		if _, err := m.CreateSecret(ctx, SecretRec{Secret: model.Secret{Name: name}, EncryptedValue: "cipher"}); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := m.CreateSecret(ctx, SecretRec{Secret: model.Secret{Name: "overflow"}, EncryptedValue: "cipher"}); err != ErrLimit {
		t.Fatalf("limit: %v", err)
	}
	items, err := m.ListSecrets(ctx)
	if err != nil || len(items) != secret.MaxCount {
		t.Fatalf("count %d %v", len(items), err)
	}
	for _, item := range items {
		if strings.Contains(item.Name, "cipher") {
			t.Fatal("name leaked ciphertext")
		}
	}

	sess, err := m.EnsureSession(ctx, "", "main", "card")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := m.AddMessage(ctx, model.Message{SessionID: sess.ID, Role: "assistant", SecretRequest: &model.SecretRequest{Name: "s0", Label: "数据库"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.MarkSecretProvided(ctx, sess.ID, msg.ID, time.Unix(10, 0).UTC())
	if err != nil || got.SecretRequest == nil || got.SecretRequest.ProvidedAt == nil {
		t.Fatal(got, err)
	}
	if _, err = m.MarkSecretProvided(ctx, sess.ID, msg.ID, time.Now()); err != ErrConflict {
		t.Fatalf("second mark: %v", err)
	}
	desc := "换过"
	updated, err := m.UpdateSecret(ctx, items[0].ID, "cipher-2", &desc)
	if err != nil || updated.ID != items[0].ID || updated.Name != items[0].Name || updated.Description != "换过" {
		t.Fatalf("update %+v %v", updated, err)
	}
	rec, err := m.GetSecret(ctx, items[0].ID)
	if err != nil || rec.EncryptedValue != "cipher-2" {
		t.Fatal("ciphertext not replaced", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
