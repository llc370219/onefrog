package scheduler

import (
	"testing"
	"time"
)

func loadLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	return loc
}

func TestValidate(t *testing.T) {
	if err := (Spec{Kind: "monthly"}).Validate(); err == nil {
		t.Fatal("unknown kind should fail")
	}
	if err := (Spec{Kind: Once, Timezone: "Not/AZone", AtLocal: time.Now()}).Validate(); err == nil {
		t.Fatal("bad timezone should fail")
	}
	if err := (Spec{Kind: Once}).Validate(); err == nil {
		t.Fatal("once without at_local should fail")
	}
	if err := (Spec{Kind: Daily, Hour: 24}).Validate(); err == nil {
		t.Fatal("hour 24 should fail")
	}
	if err := (Spec{Kind: Weekly, Hour: 9, Minute: 0}).Validate(); err == nil {
		t.Fatal("weekly without weekdays should fail")
	}
	if err := (Spec{Kind: Weekly, Hour: 9, Minute: 0, Weekdays: []time.Weekday{time.Monday, time.Monday}}).Validate(); err == nil {
		t.Fatal("duplicate weekday should fail")
	}
	if err := (Spec{Kind: Daily, Hour: 9, Minute: 30}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultTimezoneIsShanghai(t *testing.T) {
	loc, err := LoadLocation("")
	if err != nil || loc.String() != DefaultTimezone {
		t.Fatalf("got %v %v", loc, err)
	}
	sh := loadLoc(t, "Asia/Shanghai")
	now := time.Date(2026, 11, 9, 8, 0, 0, 0, sh)
	s := Spec{Kind: Daily, Hour: 9, Minute: 0} // empty tz → Shanghai
	next, ok, err := s.Next(now)
	if err != nil || !ok || next.Location().String() != DefaultTimezone {
		t.Fatalf("next=%v ok=%v err=%v", next, ok, err)
	}
	if !next.Equal(time.Date(2026, 11, 9, 9, 0, 0, 0, sh)) {
		t.Fatalf("got %v", next)
	}
}

func TestOnceNext(t *testing.T) {
	loc := loadLoc(t, "Asia/Shanghai")
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, loc)
	at := time.Date(2026, 11, 9, 18, 0, 0, 0, time.UTC)
	s := Spec{Kind: Once, Timezone: "Asia/Shanghai", AtLocal: at}

	next, ok, err := s.Next(now)
	if err != nil || !ok {
		t.Fatalf("next: ok=%v err=%v", ok, err)
	}
	want := time.Date(2026, 11, 9, 18, 0, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("got %v want %v", next, want)
	}
	past := Spec{Kind: Once, Timezone: "Asia/Shanghai", AtLocal: time.Date(2026, 11, 9, 9, 0, 0, 0, loc)}
	if _, ok, err := past.Next(now); err != nil || ok {
		t.Fatalf("expired once should be !ok, got ok=%v err=%v", ok, err)
	}
}

func TestDailyNext(t *testing.T) {
	loc := loadLoc(t, "Asia/Shanghai")
	s := Spec{Kind: Daily, Timezone: "Asia/Shanghai", Hour: 9, Minute: 0}

	now := time.Date(2026, 11, 9, 8, 0, 0, 0, loc)
	next, ok, err := s.Next(now)
	if err != nil || !ok || !next.Equal(time.Date(2026, 11, 9, 9, 0, 0, 0, loc)) {
		t.Fatalf("got %v ok=%v err=%v", next, ok, err)
	}
	now = time.Date(2026, 11, 9, 9, 0, 0, 0, loc)
	next, ok, err = s.Next(now)
	if err != nil || !ok || !next.Equal(time.Date(2026, 11, 10, 9, 0, 0, 0, loc)) {
		t.Fatalf("got %v ok=%v err=%v", next, ok, err)
	}
	sUTC := Spec{Kind: Daily, Timezone: "UTC", Hour: 0, Minute: 0}
	nowUTC := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	next, ok, err = sUTC.Next(nowUTC)
	if err != nil || !ok || !next.Equal(time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("utc next=%v ok=%v err=%v", next, ok, err)
	}
}

func TestWeeklyNext(t *testing.T) {
	loc := loadLoc(t, "Asia/Shanghai")
	s := Spec{
		Kind: Weekly, Timezone: "Asia/Shanghai", Hour: 9, Minute: 30,
		Weekdays: []time.Weekday{time.Monday, time.Wednesday, time.Friday},
	}
	now := time.Date(2026, 11, 9, 8, 0, 0, 0, loc) // Monday
	next, ok, err := s.Next(now)
	if err != nil || !ok || !next.Equal(time.Date(2026, 11, 9, 9, 30, 0, 0, loc)) {
		t.Fatalf("mon morning got %v ok=%v err=%v", next, ok, err)
	}
	now = time.Date(2026, 11, 9, 10, 0, 0, 0, loc)
	next, ok, err = s.Next(now)
	if err != nil || !ok || !next.Equal(time.Date(2026, 11, 11, 9, 30, 0, 0, loc)) {
		t.Fatalf("mon late got %v ok=%v err=%v", next, ok, err)
	}
	now = time.Date(2026, 11, 13, 10, 0, 0, 0, loc) // Friday
	next, ok, err = s.Next(now)
	if err != nil || !ok || !next.Equal(time.Date(2026, 11, 16, 9, 30, 0, 0, loc)) {
		t.Fatalf("fri late got %v ok=%v err=%v", next, ok, err)
	}
}

func TestDSTSpringForwardAmericaNewYork(t *testing.T) {
	loc := loadLoc(t, "America/New_York")
	s := Spec{Kind: Daily, Timezone: "America/New_York", Hour: 2, Minute: 30}
	now := time.Date(2026, 3, 8, 1, 0, 0, 0, loc)
	next, ok, err := s.Next(now)
	if err != nil || !ok || !next.After(now) || next.Location().String() != "America/New_York" {
		t.Fatalf("unexpected DST result %v ok=%v err=%v", next, ok, err)
	}
}
