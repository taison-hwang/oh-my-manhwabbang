package oidc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, CON-001
)

// expiredRetention is how long an expired row is kept so that a returning
// browser can be re-authorized with prompt=none (ruling E-74 §2.7).
const expiredRetention = 30 * 24 * time.Hour

const authSchema = `
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS oidc_sessions (
    id_hash       TEXT PRIMARY KEY,
    user_sub      TEXT NOT NULL,
    uni_sid       TEXT NOT NULL,
    id_token      TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    last_seen_at  INTEGER NOT NULL,
    reauth_at     INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    logged_out_at INTEGER NOT NULL
);
INSERT OR IGNORE INTO meta (key, value) VALUES ('schema_version', '1');
`

type sessionRow struct {
	idHash      string
	sub         string
	uniSID      string
	idToken     string
	createdAt   time.Time
	lastSeenAt  time.Time
	reauthAt    time.Time
	expiresAt   time.Time
	loggedOutAt time.Time
}

type store struct{ db *sql.DB }

// openStore opens <data_dir>/auth.db. The file holds ID tokens, so it is
// created 0600, and it uses a rollback journal rather than WAL so that no
// long-lived side file holds them with the umask's mode.
func openStore(ctx context.Context, path string) (*store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving auth.db path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}
	f, err := os.OpenFile(abs, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // path from the config
	if err != nil {
		return nil, fmt.Errorf("creating auth.db: %w", err)
	}
	_ = f.Close()
	if err := os.Chmod(abs, 0o600); err != nil {
		return nil, fmt.Errorf("restricting auth.db: %w", err)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(DELETE)")
	q.Set("_txlock", "immediate")
	u := url.URL{Path: filepath.ToSlash(abs)}
	p := u.EscapedPath()
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	db, err := sql.Open("sqlite", "file://"+p+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("opening auth.db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, authSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialising auth.db: %w", err)
	}
	return &store{db: db}, nil
}

func (s *store) close() error { return s.db.Close() }

func (s *store) insert(ctx context.Context, r sessionRow) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO oidc_sessions
		(id_hash, user_sub, uni_sid, id_token, created_at, last_seen_at, reauth_at, expires_at, logged_out_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		r.idHash, r.sub, r.uniSID, r.idToken, r.createdAt.Unix(), r.lastSeenAt.Unix(), unixOrZero(r.reauthAt), r.expiresAt.Unix())
	return err
}

func (s *store) get(ctx context.Context, idHash string) (sessionRow, bool, error) {
	var r sessionRow
	var created, seen, reauth, expires, out int64
	err := s.db.QueryRowContext(ctx, `SELECT id_hash, user_sub, uni_sid, id_token, created_at, last_seen_at,
		reauth_at, expires_at, logged_out_at FROM oidc_sessions WHERE id_hash = ?`, idHash).
		Scan(&r.idHash, &r.sub, &r.uniSID, &r.idToken, &created, &seen, &reauth, &expires, &out)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionRow{}, false, nil
	}
	if err != nil {
		return sessionRow{}, false, err
	}
	r.createdAt, r.lastSeenAt, r.expiresAt = time.Unix(created, 0), time.Unix(seen, 0), time.Unix(expires, 0)
	r.reauthAt, r.loggedOutAt = timeOrZero(reauth), timeOrZero(out)
	return r, true, nil
}

func (s *store) delete(ctx context.Context, idHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM oidc_sessions WHERE id_hash = ?`, idHash)
	return err
}

func (s *store) touch(ctx context.Context, idHash string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE oidc_sessions SET last_seen_at = ? WHERE id_hash = ?`, now.Unix(), idHash)
	return err
}

func (s *store) markLoggedOut(ctx context.Context, idHash string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE oidc_sessions SET logged_out_at = ? WHERE id_hash = ?`, now.Unix(), idHash)
	return err
}

// rotate replaces the row oldHash with r in one transaction.
func (s *store) rotate(ctx context.Context, oldHash string, r sessionRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM oidc_sessions WHERE id_hash = ?`, oldHash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO oidc_sessions
		(id_hash, user_sub, uni_sid, id_token, created_at, last_seen_at, reauth_at, expires_at, logged_out_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		r.idHash, r.sub, r.uniSID, r.idToken, r.createdAt.Unix(), r.lastSeenAt.Unix(), unixOrZero(r.reauthAt), r.expiresAt.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// sweep drops rows expired for longer than the retention and logged-out
// rows older than their grace (ruling E-74 §2.7, §2.12).
func (s *store) sweep(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM oidc_sessions
		WHERE expires_at < ? OR (logged_out_at > 0 AND logged_out_at < ?)`,
		now.Add(-expiredRetention).Unix(), now.Add(-loggedOutGrace).Unix())
	return err
}

// dropLoggedOut deletes logged-out rows past their grace (E-74 §2.12).
func (s *store) dropLoggedOut(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM oidc_sessions WHERE logged_out_at > 0 AND logged_out_at <= ?`,
		now.Add(-loggedOutGrace).Unix())
	return err
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func timeOrZero(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0)
}
