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

func TestTrafficAllowance(t *testing.T) {
	l, err := New("", nil)
	if err != nil {
		t.Fatal(err)
	}
	row, err := l.Create(Input{Name: "tyo", ExpiresAt: "2026-12-31", TrafficGB: 1024, TrafficMode: "out"})
	if err != nil || row.TrafficGB != 1024 || row.TrafficMode != "out" {
		t.Fatal(row, err)
	}
	row, err = l.Create(Input{Name: "lax", ExpiresAt: "2026-12-31", TrafficGB: 500})
	if err != nil || row.TrafficMode != "both" {
		t.Fatal(row, err)
	}
	if row, err = l.Create(Input{Name: "none", ExpiresAt: "2026-12-31", TrafficMode: "in"}); err != nil || row.TrafficMode != "" {
		t.Fatal(row, err)
	}
	if _, err = l.Create(Input{Name: "bad", ExpiresAt: "2026-12-31", TrafficGB: 10, TrafficMode: "sum"}); err == nil {
		t.Fatal("bad mode accepted")
	}
	if _, err = l.Create(Input{Name: "neg", ExpiresAt: "2026-12-31", TrafficGB: -1}); err == nil {
		t.Fatal("negative allowance accepted")
	}
}

func TestSubscriptionSummaryHistoryAndSevenDayBoundary(t *testing.T) {
	l, _ := New("", nil)
	l.now = func() time.Time { return time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC) }
	for _, in := range []Input{
		{Name: "AI 会员", Cycle: "monthly", AmountCents: 2000, ExpiresAt: "2026-11-11"},
		{Name: "域名", Cycle: "yearly", AmountCents: 1200, ExpiresAt: "2026-11-12"},
		{Name: "过期不续费", Cycle: "monthly", AmountCents: 99900, ExpiresAt: "2026-11-03"},
		{Name: "过期继续续费", Cycle: "monthly", AmountCents: 300, AutoRenew: true, ExpiresAt: "2026-11-03"},
		{Name: "已结束", Cycle: "monthly", AmountCents: 88800, Status: StatusCancelled, ExpiresAt: "2026-12-01"},
		{Name: "一次性", Cycle: "once", AmountCents: 500},
	} {
		if _, err := l.Create(in); err != nil {
			t.Fatal(err)
		}
	}
	s := l.Summary()
	if s.Count != 6 || s.Subscriptions != 4 || s.Historical != 2 || s.Expiring != 1 || s.MonthlyCents["USD"] != 2400 || s.NextName != "AI 会员" {
		t.Fatalf("summary %+v", s)
	}
	for _, row := range l.List() {
		if row.Name == "过期不续费" {
			_, err := l.Update(row.ID, Input{Name: row.Name, Cycle: "monthly", AmountCents: 200, Currency: "USD", ExpiresAt: "2026-12-01"})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if s = l.Summary(); s.Historical != 1 || s.Subscriptions != 5 || s.MonthlyCents["USD"] != 2600 {
		t.Fatalf("renewal not reflected %+v", s)
	}
}
