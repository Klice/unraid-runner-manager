package names

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
)

func TestGenerateShape(t *testing.T) {
	g := New(rand.NewPCG(1, 2))
	for range 500 {
		name := g.Generate()
		parts := strings.Split(name, "-")
		if len(parts) < 2 || len(parts) > 3 {
			t.Fatalf("expected 2 or 3 words, got %q", name)
		}
		if !Valid(name) {
			t.Fatalf("generated invalid name %q", name)
		}
	}
}

func TestUniqueSkipsTaken(t *testing.T) {
	g := New(rand.NewPCG(7, 7))
	first := New(rand.NewPCG(7, 7)).Generate()
	name, err := g.Unique(func(n string) bool { return n == first })
	if err != nil {
		t.Fatal(err)
	}
	if name == first {
		t.Fatalf("returned a taken name %q", name)
	}
}

func TestUniqueExhausted(t *testing.T) {
	g := New(rand.NewPCG(3, 4))
	if _, err := g.Unique(func(string) bool { return true }); !errors.Is(err, ErrExhausted) {
		t.Fatalf("expected ErrExhausted, got %v", err)
	}
}

func TestValid(t *testing.T) {
	good := []string{"fluffy-toaster", "brave-little-teapot", "a1"}
	bad := []string{"", "-lead", "trail-", "Upper-case", "with space", "under_score", strings.Repeat("a", 65)}
	for _, n := range good {
		if !Valid(n) {
			t.Errorf("%q should be valid", n)
		}
	}
	for _, n := range bad {
		if Valid(n) {
			t.Errorf("%q should be invalid", n)
		}
	}
}
