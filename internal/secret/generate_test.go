package secret

import (
	"strings"
	"testing"
)

func TestGenerateDefaults(t *testing.T) {
	v, err := Generate(Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != DefaultGenLength {
		t.Fatalf("len %d", len(v))
	}
	if !strings.ContainsAny(v, lower) || !strings.ContainsAny(v, upper) || !strings.ContainsAny(v, digits) {
		t.Fatal("alnum missed a class")
	}
	if err = ValidValue(v); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateCharsets(t *testing.T) {
	for _, name := range Charsets() {
		cs := charsets[name]
		for _, n := range []int{MinGenLength, 40, MaxGenLength} {
			v, err := Generate(Spec{Length: n, Charset: name})
			if err != nil {
				t.Fatalf("%s %d: %v", name, n, err)
			}
			if len(v) != n {
				t.Fatalf("%s len %d", name, len(v))
			}
			for _, r := range v {
				if !strings.ContainsRune(cs.alpha, r) {
					t.Fatalf("%s produced %q", name, r)
				}
			}
			if strings.ContainsAny(v, "{}\"'`$ \n\\/") {
				t.Fatalf("%s produced an unsafe character", name)
			}
			if !hasClasses([]byte(v), cs.classes) {
				t.Fatalf("%s missed a class", name)
			}
		}
		if CharsetLabel(name) == "" {
			t.Fatalf("%s has no label", name)
		}
	}
}

func TestGenerateRejectsBadSpec(t *testing.T) {
	for _, s := range []Spec{{Length: 7}, {Length: 257}, {Length: -1}, {Charset: "emoji"}} {
		if _, err := Generate(s); err == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
	s, err := NormalizeSpec(Spec{Charset: " HEX "})
	if err != nil || s.Charset != "hex" || s.Length != DefaultGenLength {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestGenerateIsNotRepeated(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		v, err := Generate(Spec{Length: 16})
		if err != nil {
			t.Fatal(err)
		}
		if seen[v] {
			t.Fatal("duplicate value")
		}
		seen[v] = true
	}
}
