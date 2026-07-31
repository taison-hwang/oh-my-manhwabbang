package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// KV is a key/value store backed by one table. Two exist: `settings` (the
// UI-mutable subset of the config's reader: block, arch §7.8) and `view_state`
// (sticky UI state such as the FR-LIB-002 grid/list toggle).
//
// Values are opaque strings — arch §3.6 calls them "JSON scalar". Encoding is
// the HTTP layer's business; storage does not parse them, so a future settings
// key needs no change here.
type KV struct {
	db    *DB
	table string
}

// Settings is the user-mutable settings store.
func (db *DB) Settings() *KV { return &KV{db: db, table: "settings"} }

// ViewState is the sticky view-state store.
func (db *DB) ViewState() *KV { return &KV{db: db, table: "view_state"} }

// Get reads one value. ok is false when the key has never been set.
func (kv *KV) Get(ctx context.Context, key string) (value string, ok bool, err error) {
	err = kv.db.sqldb.QueryRowContext(ctx,
		`SELECT value FROM `+kv.table+` WHERE key = ?`, key).Scan(&value)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("reading %s %q: %w", kv.table, key, err)
	}
	return value, true, nil
}

// All reads the whole store.
func (kv *KV) All(ctx context.Context) (map[string]string, error) {
	rows, err := kv.db.sqldb.QueryContext(ctx, `SELECT key, value FROM `+kv.table)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", kv.table, err)
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scanning %s: %w", kv.table, err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", kv.table, err)
	}
	return out, nil
}

// Put writes one key.
func (kv *KV) Put(ctx context.Context, key, value string) error {
	return kv.PutAll(ctx, map[string]string{key: value})
}

// PutAll writes several keys in one transaction — PUT /api/settings is partial,
// and a half-applied settings change would be visible to a concurrent GET.
// Keys are written in sorted order so the statement sequence is deterministic
// and a failure is reproducible.
func (kv *KV) PutAll(ctx context.Context, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		if k == "" {
			return fmt.Errorf("userdata: empty %s key: %w", kv.table, ErrInvalidArgument)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	now := kv.db.now().Unix()
	return kv.db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO `+kv.table+` (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`)
		if err != nil {
			return fmt.Errorf("preparing %s write: %w", kv.table, err)
		}
		defer stmt.Close()

		for _, k := range keys {
			if _, err := stmt.ExecContext(ctx, k, values[k], now); err != nil {
				return fmt.Errorf("writing %s %q: %w", kv.table, k, err)
			}
		}
		return nil
	})
}

// Delete removes one key. Deleting an absent key is not an error.
func (kv *KV) Delete(ctx context.Context, key string) error {
	return kv.db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+kv.table+` WHERE key = ?`, key); err != nil {
			return fmt.Errorf("deleting %s %q: %w", kv.table, key, err)
		}
		return nil
	})
}
