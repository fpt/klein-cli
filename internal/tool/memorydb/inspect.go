package memorydb

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MemoryStat is a memory plus its reference/usage stats — how often it was
// recalled and how it has been reinforced. Used by human inspection surfaces
// (the REPL /memory command), not the model-facing tools.
type MemoryStat struct {
	LastUsedAt time.Time
	Memory
	AccessCount  int
	UsefulCount  float64
	HarmfulCount float64
	Active       bool
}

// List returns memories with their usage stats, most-recently-referenced first
// (falling back to newest-created). When includeInactive is false, only active
// memories (current, non-forgotten versions) are returned.
func (s *Store) List(ctx context.Context, includeInactive bool, limit int) ([]MemoryStat, error) {
	if limit <= 0 {
		limit = 50
	}
	where := "WHERE superseded_by IS NULL AND forgotten = 0"
	if includeInactive {
		where = ""
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, kind, content, importance, version, utility_ema, created_at,
		       access_count, useful_count, harmful_count, last_used_at,
		       (superseded_by IS NULL AND forgotten = 0) AS active
		FROM memories
		`+where+`
		ORDER BY (last_used_at IS NULL), last_used_at DESC, created_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, dbErr("list", err)
	}
	defer rows.Close()

	var out []MemoryStat
	for rows.Next() {
		st, scanErr := scanStat(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, st)
	}
	return out, dbErr("iterate list", rows.Err())
}

// Stat returns a single memory with its usage stats (any state).
func (s *Store) Stat(ctx context.Context, id int64) (MemoryStat, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, kind, content, importance, version, utility_ema, created_at,
		       access_count, useful_count, harmful_count, last_used_at,
		       (superseded_by IS NULL AND forgotten = 0) AS active
		FROM memories WHERE id = ?`, id)
	if err != nil {
		return MemoryStat{}, dbErr("stat", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return MemoryStat{}, dbErr("stat", err)
		}
		return MemoryStat{}, ErrNotFound
	}
	return scanStat(rows)
}

// scanStat scans one MemoryStat row (shared by List and Stat).
func scanStat(rows *sql.Rows) (MemoryStat, error) {
	var (
		st       MemoryStat
		created  int64
		lastUsed sql.NullInt64
	)
	if err := rows.Scan(&st.ID, &st.Kind, &st.Content, &st.Importance, &st.Version, &st.UtilityEMA,
		&created, &st.AccessCount, &st.UsefulCount, &st.HarmfulCount, &lastUsed, &st.Active); err != nil {
		return MemoryStat{}, dbErr("scan stat", err)
	}
	st.CreatedAt = time.Unix(created, 0).UTC()
	if lastUsed.Valid {
		st.LastUsedAt = time.Unix(lastUsed.Int64, 0).UTC()
	}
	return st, nil
}

// Count returns the number of active and total memories.
func (s *Store) Count(ctx context.Context) (active, total int, err error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE superseded_by IS NULL AND forgotten = 0),
			COUNT(*)
		FROM memories`)
	if scanErr := row.Scan(&active, &total); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return 0, 0, nil
		}
		return 0, 0, dbErr("count", scanErr)
	}
	return active, total, nil
}
