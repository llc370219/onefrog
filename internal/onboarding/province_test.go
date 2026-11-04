package onboarding

import (
	"strings"
	"testing"

	"onefrog/internal/monitor"
)

func TestProbeTargetsPutThreeNetworksFirst(t *testing.T) {
	got := ProbeTargets("广东", []string{"1.1.1.1"})
	if len(got) != 4 || got[0] != (monitor.PingTarget{Name: "电信", Host: "gd-ct-v4.ip.zstaticcdn.com", Port: 80}) || got[2].Name != "移动" || got[3] != (monitor.PingTarget{Name: "1.1.1.1", Host: "1.1.1.1"}) {
		t.Fatalf("targets %+v", got)
	}
	if got := ProbeTargets("", nil); len(got) != 0 {
		t.Fatalf("no province, no targets: %+v", got)
	}
}

func TestValidateProvince(t *testing.T) {
	base := Request{Name: "n", Host: "192.0.2.1", Secret: "pw", Configure: true, ReportBase: "https://panel.example.com"}
	r := base
	r.Province = "北京"
	if err := Validate(&r); err != nil || r.Province != "bj" {
		t.Fatalf("province %q err %v", r.Province, err)
	}
	r = base
	r.Province = "火星"
	if err := Validate(&r); err == nil || !strings.Contains(err.Error(), "省份") {
		t.Fatalf("unknown province: %v", err)
	}
	r = base
	r.Province = "gd"
	r.Targets = []string{"a.example.com", "b.example.com", "c.example.com", "d.example.com", "e.example.com", "f.example.com"}
	if err := Validate(&r); err == nil {
		t.Fatal("3 + 6 targets accepted")
	}
}
