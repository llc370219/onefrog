package monitor

import (
	"testing"
	"time"
)

func counters(rx, tx, uptime uint64) Sample {
	return Sample{CPUCores: 1, MemoryTotal: 1, DiskTotal: 1, RXBytes: rx, TXBytes: tx, UptimeSeconds: uptime}
}

func TestTrafficAccumulatesAcrossReboots(t *testing.T) {
	m, _ := New("")
	if _, err := m.Issue("a"); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 4, 10, 0, 0, 0, time.Local)
	steps := []Sample{
		counters(1000, 500, 100), // first report: only a baseline
		counters(1600, 900, 115), // +600 / +400
		counters(300, 200, 5),    // rebooted: counters restarted, +300 / +200
		counters(1300, 1200, 20), // +1000 / +1000
	}
	for i, s := range steps {
		if err := m.Report("a", s, day.Add(time.Duration(i)*15*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	v := m.Views([]string{"a"}, day.Add(time.Minute))[0]
	if v.Traffic == nil || v.Traffic.RX != 1900 || v.Traffic.TX != 1600 || v.Traffic.Period != "2026-11" {
		t.Fatalf("traffic %+v", v.Traffic)
	}
	if !v.Traffic.Since.Equal(day.UTC()) {
		t.Fatalf("a probe added mid-month counts from its first report, got %v", v.Traffic.Since)
	}

	// Next month: totals move to the previous period and counting restarts from the 1st.
	next := time.Date(2026, 12, 1, 0, 0, 30, 0, time.Local)
	if err := m.Report("a", counters(1500, 1300, 50), next); err != nil {
		t.Fatal(err)
	}
	v = m.Views([]string{"a"}, next)[0]
	if v.Traffic.Period != "2026-12" || v.Traffic.RX != 200 || v.Traffic.TX != 100 || v.Traffic.PrevPeriod != "2026-11" || v.Traffic.PrevRX != 1900 || v.Traffic.PrevTX != 1600 {
		t.Fatalf("rollover %+v", v.Traffic)
	}
	if want := time.Date(2026, 12, 1, 0, 0, 0, 0, time.Local).UTC(); !v.Traffic.Since.Equal(want) {
		t.Fatalf("since %v want %v", v.Traffic.Since, want)
	}
}

func TestProvinceTargets(t *testing.T) {
	for _, in := range []string{"gd", "广东", "广东省"} {
		p, ok := LookupProvince(in)
		if !ok || p.Code != "gd" {
			t.Fatalf("%q -> %+v %v", in, p, ok)
		}
	}
	if p, ok := LookupProvince("新疆维吾尔自治区"); !ok || p.Code != "xj" {
		t.Fatal("autonomous region full name")
	}
	if _, ok := LookupProvince("台湾"); ok {
		t.Fatal("unknown province accepted")
	}
	if len(Provinces) != 31 {
		t.Fatalf("expected 31 provinces, got %d", len(Provinces))
	}
	got := ISPTargets(Province{Code: "sc", Name: "四川"})
	want := []PingTarget{{"电信", "sc-ct-v4.ip.zstaticcdn.com", 80}, {"联通", "sc-cu-v4.ip.zstaticcdn.com", 80}, {"移动", "sc-cm-v4.ip.zstaticcdn.com", 80}}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("targets %+v", got)
		}
	}

	m, _ := New("")
	if err := m.SetProvince("a", "四川"); err != nil {
		t.Fatal(err)
	}
	if v := m.Views([]string{"a"}, time.Now())[0]; v.Province == nil || v.Province.Name != "四川" || v.Enrolled {
		t.Fatalf("view %+v", v)
	}
	if err := m.SetProvince("a", "火星"); err == nil {
		t.Fatal("unknown province accepted")
	}
}
