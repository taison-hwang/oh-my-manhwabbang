package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// metaLegacyMigratedTo records which user the pre-E-74 data was copied to
// (ruling E-74 §2.14). Its presence, whoever it names, means the copy is done.
const metaLegacyMigratedTo = "legacy_migrated_to"

// RecordLogin notes that sub signed in: first_seen_at is written once and
// last_login_at on every call. Nothing but the sub is stored (§2.13).
func (db *DB) RecordLogin(ctx context.Context, sub string) error {
	if sub == "" {
		return fmt.Errorf("userdata: empty user sub: %w", ErrInvalidArgument)
	}
	now := db.now().Unix()
	return db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO users (user_sub, first_seen_at, last_login_at) VALUES (?,?,?)
			ON CONFLICT(user_sub) DO UPDATE SET last_login_at = excluded.last_login_at`,
			sub, now, now)
		if err != nil {
			return fmt.Errorf("recording login of %q: %w", sub, err)
		}
		return nil
	})
}

// UserSubs lists every user that owns a row in the per-user tables or has
// signed in. The scan's cleanup runs once for each of them (and once more for
// the old tables).
func (db *DB) UserSubs(ctx context.Context) ([]string, error) {
	rows, err := db.sqldb.QueryContext(ctx, `
		SELECT user_sub FROM users
		UNION SELECT user_sub FROM user_progress
		UNION SELECT user_sub FROM user_book_prefs
		ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sub string
		if err := rows.Scan(&sub); err != nil {
			return nil, fmt.Errorf("scanning users: %w", err)
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// MigrateLegacyOwner copies the single-user tables (progress, book_prefs,
// settings, view_state) to sub's rows, once (ruling E-74 §2.14). The caller is
// the OIDC callback, for the sub named by auth.oidc.legacy_owner_sub, on that
// user's first login.
//
// migrated is false when the copy had already been done — for this sub or any
// other: `meta.legacy_migrated_to` is the only record and it is never cleared,
// so a second owner cannot receive a second copy. The old tables are left
// alone. Rows sub already has win over the copy, so a user who somehow wrote
// data before the migration loses nothing.
func (db *DB) MigrateLegacyOwner(ctx context.Context, sub string) (migrated bool, err error) {
	if sub == "" {
		return false, fmt.Errorf("userdata: empty user sub: %w", ErrInvalidArgument)
	}
	err = db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		migrated = false
		var done string
		switch err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, metaLegacyMigratedTo).Scan(&done); {
		case err == nil:
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("reading meta %s: %w", metaLegacyMigratedTo, err)
		}
		for _, stmt := range []string{
			`INSERT INTO user_progress (user_sub, book_id, series_id, root_name, book_path, last_page,
			                           page_count, completed, started_at, updated_at)
			 SELECT ?, book_id, series_id, root_name, book_path, last_page,
			        page_count, completed, started_at, updated_at FROM progress WHERE true
			 ON CONFLICT(user_sub, book_id) DO NOTHING`,
			`INSERT INTO user_book_prefs (user_sub, book_id, reading_dir, display_mode, fit_mode, updated_at)
			 SELECT ?, book_id, reading_dir, display_mode, fit_mode, updated_at FROM book_prefs WHERE true
			 ON CONFLICT(user_sub, book_id) DO NOTHING`,
			`INSERT INTO user_settings (user_sub, key, value, updated_at)
			 SELECT ?, key, value, updated_at FROM settings WHERE true
			 ON CONFLICT(user_sub, key) DO NOTHING`,
			`INSERT INTO user_view_state (user_sub, key, value, updated_at)
			 SELECT ?, key, value, updated_at FROM view_state WHERE true
			 ON CONFLICT(user_sub, key) DO NOTHING`,
		} {
			if _, err := tx.ExecContext(ctx, stmt, sub); err != nil {
				return fmt.Errorf("copying single-user data to %q: %w", sub, err)
			}
		}
		if err := setMeta(ctx, tx, metaLegacyMigratedTo, sub); err != nil {
			return err
		}
		migrated = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return migrated, nil
}

// RestoreResult counts what RestoreOwner wrote into the single-user tables.
type RestoreResult struct {
	Progress, Prefs, Settings, ViewState int
}

// RestoreOwner is the way back from oidc to password mode (ruling E-74 §2.14,
// `shelf auth restore-owner --sub <sub>`): it copies sub's rows over the
// single-user tables. For a key both sides have, the row with the newer
// updated_at wins; on a tie the single-user row stays, and a single-user row
// with no updated_at loses. Run it with the server stopped.
func (db *DB) RestoreOwner(ctx context.Context, sub string) (RestoreResult, error) {
	if sub == "" {
		return RestoreResult{}, fmt.Errorf("userdata: empty user sub: %w", ErrInvalidArgument)
	}
	var res RestoreResult
	err := db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res = RestoreResult{}
		run := func(into *int, stmt string) error {
			r, err := tx.ExecContext(ctx, stmt, sub)
			if err != nil {
				return fmt.Errorf("restoring single-user data from %q: %w", sub, err)
			}
			n, _ := r.RowsAffected()
			*into = int(n)
			return nil
		}
		if err := run(&res.Progress, `
			INSERT INTO progress (book_id, series_id, root_name, book_path, last_page,
			                      page_count, completed, started_at, updated_at)
			SELECT book_id, series_id, root_name, book_path, last_page,
			       page_count, completed, started_at, updated_at FROM user_progress WHERE user_sub = ?
			ON CONFLICT(book_id) DO UPDATE SET
				series_id  = excluded.series_id,
				root_name  = excluded.root_name,
				book_path  = excluded.book_path,
				last_page  = excluded.last_page,
				page_count = excluded.page_count,
				completed  = excluded.completed,
				started_at = excluded.started_at,
				updated_at = excluded.updated_at
			WHERE progress.updated_at IS NULL OR excluded.updated_at > progress.updated_at`); err != nil {
			return err
		}
		if err := run(&res.Prefs, `
			INSERT INTO book_prefs (book_id, reading_dir, display_mode, fit_mode, updated_at)
			SELECT book_id, reading_dir, display_mode, fit_mode, updated_at FROM user_book_prefs WHERE user_sub = ?
			ON CONFLICT(book_id) DO UPDATE SET
				reading_dir  = excluded.reading_dir,
				display_mode = excluded.display_mode,
				fit_mode     = excluded.fit_mode,
				updated_at   = excluded.updated_at
			WHERE book_prefs.updated_at IS NULL OR excluded.updated_at > book_prefs.updated_at`); err != nil {
			return err
		}
		if err := run(&res.Settings, `
			INSERT INTO settings (key, value, updated_at)
			SELECT key, value, updated_at FROM user_settings WHERE user_sub = ?
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
			WHERE settings.updated_at IS NULL OR excluded.updated_at > settings.updated_at`); err != nil {
			return err
		}
		return run(&res.ViewState, `
			INSERT INTO view_state (key, value, updated_at)
			SELECT key, value, updated_at FROM user_view_state WHERE user_sub = ?
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
			WHERE view_state.updated_at IS NULL OR excluded.updated_at > view_state.updated_at`)
	})
	if err != nil {
		return RestoreResult{}, err
	}
	return res, nil
}
