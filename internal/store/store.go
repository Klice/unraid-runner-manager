package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrUsernameTaken   = errors.New("username is already taken")
	ErrInvalidUsername = errors.New("username must be 2 to 24 lowercase letters or digits and start with a letter")
	ErrInvalidRole     = errors.New("role must be admin or user")
	usernamePattern    = regexp.MustCompile(`^[a-z][a-z0-9]{1,23}$`)
)

type User struct {
	ID                 int64
	Username           string
	PasswordHash       string
	Role               string
	Disabled           bool
	MustChangePassword bool
	CreatedAt          time.Time
	LastLoginAt        *time.Time
}

func (u User) IsAdmin() bool { return u.Role == RoleAdmin }

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	username TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	role TEXT NOT NULL,
	disabled INTEGER NOT NULL DEFAULT 0,
	must_change_password INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	last_login_at TEXT
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);
`

func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, errors.Join(fmt.Errorf("migrate: %w", err), db.Close())
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func ValidUsername(name string) bool { return usernamePattern.MatchString(name) }

func (s *Store) CreateUser(ctx context.Context, username, passwordHash, role string, mustChange bool) (User, error) {
	if !ValidUsername(username) {
		return User{}, ErrInvalidUsername
	}
	if role != RoleAdmin && role != RoleUser {
		return User{}, ErrInvalidRole
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, must_change_password, created_at) VALUES (?, ?, ?, ?, ?)`,
		username, passwordHash, role, boolInt(mustChange), now.Format(time.RFC3339))
	if err != nil {
		if isUnique(err) {
			return User{}, ErrUsernameTaken
		}
		return User{}, err
	}
	id, _ := res.LastInsertId()
	return s.UserByID(ctx, id)
}

func (s *Store) UserByName(ctx context.Context, username string) (User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, username))
}

func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	out := []User{}
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		out = append(out, u)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) SetPassword(ctx context.Context, id int64, passwordHash string, mustChange bool) error {
	return s.exec(ctx, `UPDATE users SET password_hash = ?, must_change_password = ? WHERE id = ?`, passwordHash, boolInt(mustChange), id)
}

func (s *Store) SetDisabled(ctx context.Context, id int64, disabled bool) error {
	if err := s.exec(ctx, `UPDATE users SET disabled = ? WHERE id = ?`, boolInt(disabled), id); err != nil {
		return err
	}
	if disabled {
		return s.DeleteUserSessions(ctx, id)
	}
	return nil
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return s.exec(ctx, `DELETE FROM users WHERE id = ?`, id)
}

func (s *Store) TouchLogin(ctx context.Context, id int64) error {
	return s.exec(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339), id)
}

func (s *Store) CreateSession(ctx context.Context, userID int64, token string, ttl time.Duration) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		hashToken(token), userID, now.Format(time.RFC3339), now.Add(ttl).Format(time.RFC3339))
	return err
}

func (s *Store) UserBySession(ctx context.Context, token string) (User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userColsPrefixed+` FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ? AND s.expires_at > ?`,
		hashToken(token), time.Now().UTC().Format(time.RFC3339))
	return s.scanUser(row)
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().UTC().Format(time.RFC3339))
	return err
}

const userCols = `id, username, password_hash, role, disabled, must_change_password, created_at, last_login_at`
const userColsPrefixed = `u.id, u.username, u.password_hash, u.role, u.disabled, u.must_change_password, u.created_at, u.last_login_at`

type scanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanUser(row scanner) (User, error) {
	var u User
	var disabled, mustChange int
	var created string
	var lastLogin sql.NullString
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &disabled, &mustChange, &created, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u.Disabled = disabled == 1
	u.MustChangePassword = mustChange == 1
	u.CreatedAt, _ = time.Parse(time.RFC3339, created)
	if lastLogin.Valid {
		t, err := time.Parse(time.RFC3339, lastLogin.String)
		if err == nil {
			u.LastLoginAt = &t
		}
	}
	return u, nil
}

func (s *Store) exec(ctx context.Context, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
