package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{3,64}$`)

type UserSummary struct {
	ID        int64
	Username  string
	KeyCount  int
	CreatedAt string
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) UserCount(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("store is not initialized")
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_users`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) ListUsers(ctx context.Context) ([]UserSummary, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store is not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT u.id, u.username, u.created_at, COUNT(k.id)
FROM auth_users u
LEFT JOIN auth_user_keys k ON k.user_id = u.id
GROUP BY u.id, u.username, u.created_at
ORDER BY u.username ASC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]UserSummary, 0, 16)
	for rows.Next() {
		var u UserSummary
		if err := rows.Scan(&u.ID, &u.Username, &u.CreatedAt, &u.KeyCount); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) PublicKeysByUsername(ctx context.Context, username string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store is not initialized")
	}
	username, err := normalizeUsername(username)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT k.public_key
FROM auth_user_keys k
JOIN auth_users u ON u.id = k.user_id
WHERE u.username = ?
ORDER BY k.id ASC
`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keys := make([]string, 0, 4)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, sql.ErrNoRows
	}
	return keys, nil
}

func (s *Store) CreateUser(ctx context.Context, username, publicKey string) error {
	if s == nil || s.db == nil {
		return errors.New("store is not initialized")
	}
	username, err := normalizeUsername(username)
	if err != nil {
		return err
	}
	publicKey = strings.TrimSpace(publicKey)
	if publicKey == "" {
		return errors.New("public key is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `INSERT INTO auth_users (username) VALUES (?)`, username)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return fmt.Errorf("user %q already exists", username)
		}
		return err
	}
	userID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO auth_user_keys (user_id, public_key) VALUES (?, ?)`, userID, publicKey); err != nil {
		return err
	}
	return tx.Commit()
}

func normalizeUsername(username string) (string, error) {
	username = strings.TrimSpace(username)
	if !usernameRe.MatchString(username) {
		return "", errors.New("username must match [a-zA-Z0-9._-]{3,64}")
	}
	return username, nil
}
