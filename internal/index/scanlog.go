package index

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Log levels accepted by AppendLog, matching arch §7.10's ScanLogEntry.
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// LogRetention is the number of most recent rows kept; older ones are trimmed
// at the end of each run (arch §3.5).
const LogRetention = 5000

// LogEntry is one scan-log row. ID is monotonic (AUTOINCREMENT survives the
// retention trim), which is what makes SinceID a usable incremental cursor.
type LogEntry struct {
	ID      int64
	TS      int64
	RunID   string
	Level   string
	Root    string
	RelPath string
	Message string
}

// LogFilter is the parameter set of GET /api/scan/log.
type LogFilter struct {
	Limit   int
	Level   string
	RunID   string
	SinceID int64
}

// insertScanLogSQL is shared with Writer.AppendLog, which routes the same row
// through an open batch transaction instead of taking the write permit itself.
const insertScanLogSQL = `INSERT INTO scan_log (ts, run_id, level, root_name, rel_path, message)
	VALUES (?, ?, ?, ?, ?, ?)`

// AppendLog writes scan-log rows. FR-IDX-010 requires one warn row per isolated
// failure, so this is on the scan's hot path and takes a batch of entries.
//
// This is a DB-level write: it takes the process write permit, so the goroutine
// driving a Writer must call Writer.AppendLog instead while its batch is open.
func (db *DB) AppendLog(ctx context.Context, entries ...LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	return db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, insertScanLogSQL)
		if err != nil {
			return fmt.Errorf("preparing scan-log insert: %w", err)
		}
		defer stmt.Close()

		for _, e := range entries {
			if _, err := stmt.ExecContext(ctx, e.TS, e.RunID, e.Level,
				nullString(e.Root), nullString(e.RelPath), e.Message); err != nil {
				return fmt.Errorf("appending scan-log entry: %w", err)
			}
		}
		return nil
	})
}

// ListLog reads the scan log newest-first, or oldest-first when SinceID is set
// (an incremental poll wants the rows it has not seen, in order).
func (db *DB) ListLog(ctx context.Context, f LogFilter) ([]LogEntry, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	var conds []string
	var args []any
	if f.Level != "" {
		switch f.Level {
		case LevelInfo, LevelWarn, LevelError:
			conds = append(conds, `level = ?`)
			args = append(args, f.Level)
		default:
			return nil, fmt.Errorf("index: level %q: %w", f.Level, ErrInvalidFilter)
		}
	}
	if f.RunID != "" {
		conds = append(conds, `run_id = ?`)
		args = append(args, f.RunID)
	}
	order := "id DESC"
	if f.SinceID > 0 {
		conds = append(conds, `id > ?`)
		args = append(args, f.SinceID)
		order = "id ASC"
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit)

	rows, err := db.sqldb.QueryContext(ctx,
		`SELECT id, ts, run_id, level, COALESCE(root_name, ''), COALESCE(rel_path, ''), message
		 FROM scan_log`+where+` ORDER BY `+order+` LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("reading scan log: %w", err)
	}
	defer rows.Close()

	var out []LogEntry
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.ID, &e.TS, &e.RunID, &e.Level, &e.Root, &e.RelPath, &e.Message); err != nil {
			return nil, fmt.Errorf("scanning scan-log entry: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading scan log: %w", err)
	}
	return out, nil
}

// TrimLog keeps the most recent `keep` rows and deletes the rest. keep <= 0
// selects LogRetention.
func (db *DB) TrimLog(ctx context.Context, keep int) (int64, error) {
	if keep <= 0 {
		keep = LogRetention
	}
	var deleted int64
	err := db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx,
			`DELETE FROM scan_log WHERE id NOT IN
			 (SELECT id FROM scan_log ORDER BY id DESC LIMIT ?)`, keep)
		if err != nil {
			return fmt.Errorf("trimming scan log: %w", err)
		}
		deleted, _ = r.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}
