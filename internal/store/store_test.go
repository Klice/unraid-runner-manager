package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return s
}

func TestUserLifecycle(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	if n, _ := s.CountUsers(ctx); n != 0 {
		t.Fatalf("expected empty store, got %d", n)
	}
	u, err := s.CreateUser(ctx, "max", "hash", RoleAdmin, false)
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsAdmin() || u.Username != "max" || u.Disabled || u.MustChangePassword || u.LastLoginAt != nil {
		t.Fatalf("unexpected user %+v", u)
	}
	if _, err := s.CreateUser(ctx, "max", "hash", RoleUser, true); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("expected ErrUsernameTaken, got %v", err)
	}
	if _, err := s.CreateUser(ctx, "Bad Name", "hash", RoleUser, true); !errors.Is(err, ErrInvalidUsername) {
		t.Fatalf("expected ErrInvalidUsername, got %v", err)
	}
	if _, err := s.CreateUser(ctx, "ola", "hash", "root", true); !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("expected ErrInvalidRole, got %v", err)
	}
	ola, err := s.CreateUser(ctx, "ola", "hash2", RoleUser, true)
	if err != nil {
		t.Fatal(err)
	}
	if !ola.MustChangePassword {
		t.Fatal("must_change_password not stored")
	}
	users, err := s.ListUsers(ctx)
	if err != nil || len(users) != 2 || users[0].Username != "max" {
		t.Fatalf("list: %v %+v", err, users)
	}
	if err := s.SetPassword(ctx, ola.ID, "hash3", false); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchLogin(ctx, ola.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.UserByName(ctx, "ola")
	if got.PasswordHash != "hash3" || got.MustChangePassword || got.LastLoginAt == nil {
		t.Fatalf("updates not applied: %+v", got)
	}
	if err := s.SetDisabled(ctx, ola.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.UserByID(ctx, ola.ID); !got.Disabled {
		t.Fatal("disable not applied")
	}
	if err := s.DeleteUser(ctx, ola.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserByID(ctx, ola.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := s.DeleteUser(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing user, got %v", err)
	}
}

func TestSessions(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	u, _ := s.CreateUser(ctx, "max", "hash", RoleAdmin, false)
	if err := s.CreateSession(ctx, u.ID, "tok1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, u.ID, "expired", -time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := s.UserBySession(ctx, "tok1")
	if err != nil || got.ID != u.ID {
		t.Fatalf("session lookup failed: %v %+v", err, got)
	}
	if _, err := s.UserBySession(ctx, "expired"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session should not resolve, got %v", err)
	}
	if _, err := s.UserBySession(ctx, "unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown session should not resolve, got %v", err)
	}
	if err := s.PurgeExpiredSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, "tok1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserBySession(ctx, "tok1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("session should be deleted")
	}
	_ = s.CreateSession(ctx, u.ID, "tok2", time.Hour)
	if err := s.SetDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserBySession(ctx, "tok2"); !errors.Is(err, ErrNotFound) {
		t.Fatal("disabling a user should revoke sessions")
	}
	_ = s.SetDisabled(ctx, u.ID, false)
	_ = s.CreateSession(ctx, u.ID, "tok3", time.Hour)
	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserBySession(ctx, "tok3"); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleting a user should cascade to sessions")
	}
}

func TestValidUsername(t *testing.T) {
	for _, ok := range []string{"max", "ola2", "ab"} {
		if !ValidUsername(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "a", "Max", "1abc", "with-dash", "toolongusernameover24charss"} {
		if ValidUsername(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
