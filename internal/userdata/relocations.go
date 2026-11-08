package userdata

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// PendingRelocation preserves scan evidence until authored data has followed it.
// It deliberately has no dependency on the derived index schema.
type PendingRelocation struct {
	OldBookID    string
	NewBookID    string
	OldRelPath   string
	NewRelPath   string
	NewRootName  string
	NewSeriesID  string
	OldSeriesID  string
	NewPageCount int
}

func (db *DB) PendingRelocations(ctx context.Context) ([]PendingRelocation, error) {
	rows, err := db.sqldb.QueryContext(ctx, `SELECT evidence FROM pending_relocations ORDER BY old_book_id`)
	if err != nil {
		return nil, fmt.Errorf("reading pending relocations: %w", err)
	}
	defer rows.Close()
	var out []PendingRelocation
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var pair PendingRelocation
		if err := json.Unmarshal([]byte(raw), &pair); err != nil {
			return nil, fmt.Errorf("decoding pending relocation: %w", err)
		}
		out = append(out, pair)
	}
	return out, rows.Err()
}
func (db *DB) SavePendingRelocations(ctx context.Context, pairs []PendingRelocation) error {
	return db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, p := range pairs {
			raw, err := json.Marshal(p)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO pending_relocations(old_book_id,evidence) VALUES (?,?) ON CONFLICT(old_book_id) DO UPDATE SET evidence=excluded.evidence`, p.OldBookID, string(raw)); err != nil {
				return err
			}
		}
		return nil
	})
}
func (db *DB) ClearPendingRelocations(ctx context.Context, oldIDs []string) error {
	return db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, id := range oldIDs {
			if _, err := tx.ExecContext(ctx, `DELETE FROM pending_relocations WHERE old_book_id=?`, id); err != nil {
				return err
			}
		}
		return nil
	})
}
