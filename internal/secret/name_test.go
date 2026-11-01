package secret

import (
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	got, err := ValidName("  数据库密码  ")
	if err != nil || got != "数据库密码" {
		t.Fatalf("chinese name: %q %v", got, err)
	}
	got, err = ValidName("db-password")
	if err != nil || got != "db-password" {
		t.Fatal(got, err)
	}
	for _, name := range []string{"", "   ", "has space", "bad}name", "bad{name", "line\nbreak", strings.Repeat("名", MaxNameRunes+1)} {
		if _, err := ValidName(name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	if _, err := ValidName(strings.Repeat("名", MaxNameRunes)); err != nil {
		t.Fatal(err)
	}
}

func TestValidValue(t *testing.T) {
	if err := ValidValue(""); err == nil {
		t.Fatal("empty value")
	}
	if err := ValidValue(strings.Repeat("a", MaxValueBytes)); err != nil {
		t.Fatal(err)
	}
	err := ValidValue(strings.Repeat("a", MaxValueBytes+1))
	if err == nil || strings.Contains(err.Error(), "a") {
		t.Fatalf("oversize error leaked or missing: %v", err)
	}
}

func TestCleanLabelAndDescription(t *testing.T) {
	label, err := CleanLabel("  数据库\n密码\t ")
	if err != nil || label != "数据库 密码" {
		t.Fatalf("%q %v", label, err)
	}
	if _, err := CleanLabel(" \n "); err == nil {
		t.Fatal("blank label")
	}
	long := CleanLabelMust(strings.Repeat("标", MaxLabelRunes+10))
	if len([]rune(long)) != MaxLabelRunes {
		t.Fatalf("label len %d", len([]rune(long)))
	}
	desc := CleanDescription("说明\n第二行" + strings.Repeat("字", MaxDescriptionRunes))
	if !strings.Contains(desc, "\n") || len([]rune(desc)) != MaxDescriptionRunes {
		t.Fatalf("description %d", len([]rune(desc)))
	}
}

func CleanLabelMust(s string) string {
	label, err := CleanLabel(s)
	if err != nil {
		panic(err)
	}
	return label
}
