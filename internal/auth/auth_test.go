package auth

import (
	"strings"
	"testing"
	"time"
)

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Fatalf("unexpected format %q", h)
	}
	ok, err := VerifyPassword(h, "correct horse")
	if err != nil || !ok {
		t.Fatalf("expected match, got %v %v", ok, err)
	}
	ok, err = VerifyPassword(h, "wrong")
	if err != nil || ok {
		t.Fatalf("expected mismatch, got %v %v", ok, err)
	}
	h2, _ := HashPassword("correct horse")
	if h2 == h {
		t.Fatal("salt should differ between hashes")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$YQ$YQ", "$argon2id$v=19$m=1$YQ$YQ", "$argon2id$v=19$m=1,t=1,p=1$!!$YQ"} {
		if _, err := VerifyPassword(bad, "x"); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestRandomToken(t *testing.T) {
	a, _ := RandomToken(32)
	b, _ := RandomToken(32)
	if a == b || len(a) < 40 {
		t.Fatalf("tokens look wrong: %q %q", a, b)
	}
}

func TestLimiterLocksAfterFailures(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewLimiter(3, time.Minute, 5*time.Minute)
	l.now = func() time.Time { return now }
	for range 2 {
		l.Fail("k")
	}
	if blocked, _ := l.Blocked("k"); blocked {
		t.Fatal("should not be blocked yet")
	}
	l.Fail("k")
	blocked, wait := l.Blocked("k")
	if !blocked || wait != 5*time.Minute {
		t.Fatalf("expected lockout, got %v %v", blocked, wait)
	}
	now = now.Add(5*time.Minute + time.Second)
	if blocked, _ := l.Blocked("k"); blocked {
		t.Fatal("lockout should have expired")
	}
	l.Fail("k")
	l.Fail("k")
	l.Reset("k")
	l.Fail("k")
	if blocked, _ := l.Blocked("k"); blocked {
		t.Fatal("reset should clear the counter")
	}
}

func TestLimiterWindowResetsCounter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewLimiter(3, time.Minute, time.Hour)
	l.now = func() time.Time { return now }
	l.Fail("k")
	l.Fail("k")
	now = now.Add(2 * time.Minute)
	l.Fail("k")
	if blocked, _ := l.Blocked("k"); blocked {
		t.Fatal("old failures outside the window should not count")
	}
}
