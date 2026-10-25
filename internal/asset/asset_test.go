package asset

import (
	"bytes"
	"onefrog/internal/secret"
	"path/filepath"
	"testing"
	"time"
)

func TestResidualAndPersistence(t *testing.T) {
	box, err := secret.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "assets.enc")
	l, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	l.now = func() time.Time { return time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC) }
	row, err := l.Create(Input{Name: "web-01", Provider: "搬瓦工", Cycle: "yearly", Currency: "usd", AmountCents: 36500, StartsAt: "2026-01-01", ExpiresAt: "2026-12-31", AutoRenew: true, Specs: "2C 2G", Region: "洛杉矶", IP: "203.0.113.10", Tags: []string{"生产", "生产"}})
	if err != nil {
		t.Fatal(err)
	}
	if row.DaysTotal != 365 || row.DaysLeft != 68 || row.ResidualCents != 6800 || row.DerivedStatus != StatusActive || row.Currency != "USD" || len(row.Tags) != 1 {
		t.Fatalf("%+v", row)
	}
	again, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	again.now = l.now
	got, err := again.Get(row.ID)
	if err != nil || got.ResidualCents != 6800 {
		t.Fatal(got, err)
	}
	sum := again.Summary()
	if sum.MonthlyCents["USD"] != 3042 || sum.ResidualCents["USD"] != 6800 || sum.NextName != "web-01" {
		t.Fatalf("%+v", sum)
	}
	soon, err := l.Create(Input{Name: "即将到期", Cycle: "monthly", AmountCents: 3000, StartsAt: "2026-10-01", ExpiresAt: "2026-10-31"})
	if err != nil || soon.DerivedStatus != StatusExpiring || soon.DaysLeft != 7 {
		t.Fatal(soon, err)
	}
	old, err := l.Create(Input{Name: "已过期", Cycle: "monthly", AmountCents: 1000, StartsAt: "2026-09-01", ExpiresAt: "2026-09-30"})
	if err != nil || old.DerivedStatus != StatusExpired || old.ResidualCents != 0 {
		t.Fatal(old, err)
	}
	if _, err = l.Create(Input{Name: "坏日期", Cycle: "monthly", StartsAt: "2026-11-02", ExpiresAt: "2026-11-01"}); err == nil {
		t.Fatal("expected date error")
	}
}
