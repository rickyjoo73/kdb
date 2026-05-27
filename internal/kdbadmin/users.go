package kdbadmin

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// AdminUser is a row of kwave_kdb_admin_users (without password_hash).
type AdminUser struct {
	Email       string
	Name        string
	Role        string
	Enabled     bool
	LastLoginAt *time.Time
}

var (
	errInvalidCreds = errors.New("invalid credentials")
	errLocked       = errors.New("account is temporarily locked")
)

const (
	maxFailedAttempts = 5
	lockoutDuration   = 15 * time.Minute
	bcryptCost        = 12
)

// adminCount returns the number of enabled admins (used for first-run setup).
func adminCount(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_kdb_admin_users WHERE enabled`).Scan(&n)
	return n, err
}

// authenticate verifies email+password against kwave_kdb_admin_users.
// Increments failed_attempts on failure and clears them on success; locks for
// 15 minutes after `maxFailedAttempts` consecutive failures.
func authenticate(ctx context.Context, pool *pgxpool.Pool, email, pass string) (*AdminUser, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || pass == "" {
		return nil, errInvalidCreds
	}

	var u AdminUser
	var hash string
	var locked *time.Time
	err := pool.QueryRow(ctx, `
SELECT email, COALESCE(name,''), role, enabled, last_login_at, password_hash, locked_until
FROM kwave_kdb_admin_users WHERE email = $1`, email).Scan(
		&u.Email, &u.Name, &u.Role, &u.Enabled, &u.LastLoginAt, &hash, &locked)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// constant-time bcrypt to avoid user-enumeration timing differences
			_ = bcrypt.CompareHashAndPassword([]byte("$2a$12$invalidsaltinvalidsaltinval.HashHashHashHashHashHashHashHa"), []byte(pass))
			return nil, errInvalidCreds
		}
		return nil, err
	}
	if !u.Enabled {
		return nil, errInvalidCreds
	}
	if locked != nil && locked.After(time.Now()) {
		return nil, errLocked
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)); err != nil {
		_, _ = pool.Exec(ctx, `
UPDATE kwave_kdb_admin_users
SET failed_attempts = failed_attempts + 1,
    locked_until = CASE
        WHEN failed_attempts + 1 >= $2 THEN now() + ($3 || ' seconds')::interval
        ELSE locked_until
    END
WHERE email = $1`, email, maxFailedAttempts, int(lockoutDuration.Seconds()))
		return nil, errInvalidCreds
	}

	_, _ = pool.Exec(ctx, `
UPDATE kwave_kdb_admin_users
SET failed_attempts = 0, locked_until = NULL, last_login_at = now()
WHERE email = $1`, email)

	return &u, nil
}

// createAdmin inserts a new admin row. Returns true if a new row was created
// (i.e. the email did not already exist).
func createAdmin(ctx context.Context, pool *pgxpool.Pool, email, pass, name string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || pass == "" {
		return false, errors.New("email and password are required")
	}
	if len(pass) < 8 {
		return false, errors.New("password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcryptCost)
	if err != nil {
		return false, err
	}
	if name == "" {
		name = email
	}
	tag, err := pool.Exec(ctx, `
INSERT INTO kwave_kdb_admin_users (email, password_hash, name, role, enabled)
VALUES ($1, $2, $3, 'admin', TRUE)
ON CONFLICT (email) DO NOTHING`, email, hash, name)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
