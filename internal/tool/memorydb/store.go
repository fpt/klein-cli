// Package memorydb implements a lightweight, versioned long-term memory backed
// by a modernc.org/sqlite database with an FTS5 (bm25-ranked) full-text index.
// It is exposed to agents as a domain.ToolManager (see manager.go), so the same
// tools wire into both the ReAct composite and the codex app-server's embedded
// dynamic tools.
//
// The design follows kb.md: memory is stored as small atomic facts (not raw
// conversation logs) with a kind, importance, and associated entities. Updates
// supersede prior versions rather than deleting them (versioned internally).
// Retrieval ("recall") blends bm25 lexical relevance with importance, recency,
// entity match, and a learned per-memory utility that feedback ("reinforce")
// adjusts — so memories that actually helped surface more readily over time.
package memorydb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go sqlite driver (registers "sqlite")
)

// schemaVersion is the current on-disk schema version, tracked via
// PRAGMA user_version. Bump it and add a migration step when the schema changes.
const schemaVersion = 1

// utilityAlpha is the EMA weight applied to each feedback credit. Larger =
// faster adaptation, noisier; smaller = slower, steadier.
const utilityAlpha = 0.3

// Memory is a single atomic memory (its current version).
type Memory struct {
	CreatedAt  time.Time
	Kind       string
	Content    string
	Entities   []string
	ID         int64
	Importance float64
	Version    int
	UtilityEMA float64
}

// Hit is a ranked recall result: the memory plus the component scores that
// produced its final ranking (useful for debugging/telemetry).
type Hit struct {
	Snippet string
	Memory
	Score     float64
	Lexical   float64
	EntityHit float64
	Recency   float64
}

// Store is a handle to the memory database. It is safe for concurrent use: the
// pool is limited to one connection so writes and FTS-syncing triggers never
// contend.
type Store struct {
	db *sql.DB
	// clock supplies the current time; overridable in tests for deterministic
	// recency/ordering.
	clock func() time.Time
}

// Open opens (creating if needed) the memory database at path and migrates it.
func Open(path string) (*Store, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db, clock: time.Now}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying database.
func (s *Store) Close() error { return dbErr("close", s.db.Close()) }

func (s *Store) now() time.Time { return s.clock() }

// dbErr wraps a database/sql error with operation context (nil-safe). Routing
// external errors through it keeps call sites terse and satisfies wrapcheck.
func dbErr(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("memorydb %s: %w", op, err)
}

// migrate brings the schema up to schemaVersion, keyed on PRAGMA user_version.
func (s *Store) migrate(ctx context.Context) error {
	var current int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if current >= schemaVersion {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbErr("begin tx", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	if current < 1 {
		if _, err := tx.ExecContext(ctx, migrationV1); err != nil {
			return fmt.Errorf("migrate to v1: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	return dbErr("commit", tx.Commit())
}

// migrationV1 is the initial schema: atomic memories with supersession-based
// versioning, an entity index, learned-utility columns, and an external-content
// FTS5 index over the pre-tokenized search_text, synced by triggers.
const migrationV1 = `
CREATE TABLE memories (
	id            INTEGER PRIMARY KEY,
	kind          TEXT NOT NULL DEFAULT 'fact',
	content       TEXT NOT NULL,
	search_text   TEXT NOT NULL,
	importance    REAL NOT NULL DEFAULT 0.5,
	created_at    INTEGER NOT NULL,
	last_used_at  INTEGER,
	access_count  INTEGER NOT NULL DEFAULT 0,
	useful_count  REAL NOT NULL DEFAULT 0,
	harmful_count REAL NOT NULL DEFAULT 0,
	utility_ema   REAL NOT NULL DEFAULT 0,
	source        TEXT NOT NULL DEFAULT '',
	version       INTEGER NOT NULL DEFAULT 1,
	supersedes    INTEGER REFERENCES memories(id),
	superseded_by INTEGER REFERENCES memories(id),
	forgotten     INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_memories_active ON memories(superseded_by, forgotten);

CREATE TABLE memory_entities (
	memory_id INTEGER NOT NULL REFERENCES memories(id),
	entity    TEXT NOT NULL,
	PRIMARY KEY (memory_id, entity)
);
CREATE INDEX idx_memory_entities_entity ON memory_entities(entity);

CREATE VIRTUAL TABLE memory_fts USING fts5(
	search_text,
	content='memories', content_rowid='id',
	tokenize='unicode61'
);

CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
	INSERT INTO memory_fts(rowid, search_text) VALUES (new.id, new.search_text);
END;
CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
	INSERT INTO memory_fts(memory_fts, rowid, search_text) VALUES ('delete', old.id, old.search_text);
END;
CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN
	INSERT INTO memory_fts(memory_fts, rowid, search_text) VALUES ('delete', old.id, old.search_text);
	INSERT INTO memory_fts(rowid, search_text) VALUES (new.id, new.search_text);
END;
`

// ErrNotFound is returned when a memory id does not exist.
var ErrNotFound = errors.New("memorydb: memory not found")

// Remember stores a new atomic memory and returns it. kind defaults to "fact";
// importance is clamped to [0,1] (0 or negative means "use default" 0.5).
func (s *Store) Remember(
	ctx context.Context, content, kind string, importance float64, entities []string, source string,
) (Memory, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return Memory{}, errors.New("memorydb: content is required")
	}
	if kind = strings.TrimSpace(kind); kind == "" {
		kind = "fact"
	}
	if importance <= 0 {
		importance = 0.5
	}
	importance = clamp(importance, 0, 1)
	entities = normalizeEntities(entities)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Memory{}, dbErr("begin tx", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	now := s.now().UTC()
	id, err := insertMemory(ctx, tx, memoryRow{
		kind: kind, content: content, searchText: tokenize(content + " " + strings.Join(entities, " ")),
		importance: importance, createdAt: now.Unix(), source: strings.TrimSpace(source), version: 1,
	})
	if err != nil {
		return Memory{}, err
	}
	if err := insertEntities(ctx, tx, id, entities); err != nil {
		return Memory{}, err
	}
	if err := dbErr("commit", tx.Commit()); err != nil {
		return Memory{}, err
	}
	return Memory{
		ID: id, Kind: kind, Content: content, Importance: importance,
		Entities: entities, Version: 1, CreatedAt: now,
	}, nil
}

// Revise supersedes an existing active memory with new content, creating a new
// version that links back to the old one (which is marked superseded). The new
// version inherits the prior importance/kind/entities and learned utility
// unless overrides are provided (empty kind / <=0 importance / nil entities
// mean "inherit"). Returns the new memory.
func (s *Store) Revise(
	ctx context.Context, id int64, content, kind string, importance float64, entities []string,
) (Memory, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return Memory{}, errors.New("memorydb: content is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Memory{}, dbErr("begin tx", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	old, err := loadMemoryTx(ctx, tx, id)
	if err != nil {
		return Memory{}, err
	}
	if !old.active() {
		return Memory{}, errors.New("memorydb: memory is not active (already superseded or forgotten)")
	}

	kind, importance, entities, err = resolveRevisedFields(ctx, tx, old, kind, importance, entities)
	if err != nil {
		return Memory{}, err
	}

	now := s.now().UTC()
	newID, err := insertMemory(ctx, tx, memoryRow{
		kind: kind, content: content, searchText: tokenize(content + " " + strings.Join(entities, " ")),
		importance: importance, createdAt: now.Unix(), source: old.source,
		version: old.version + 1, supersedes: sql.NullInt64{Int64: id, Valid: true},
		utilityEMA: old.utilityEMA, usefulCount: old.usefulCount, harmfulCount: old.harmfulCount,
	})
	if err != nil {
		return Memory{}, err
	}
	if err := insertEntities(ctx, tx, newID, entities); err != nil {
		return Memory{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memories SET superseded_by = ? WHERE id = ?`, newID, id); err != nil {
		return Memory{}, fmt.Errorf("mark superseded: %w", err)
	}
	if err := dbErr("commit", tx.Commit()); err != nil {
		return Memory{}, err
	}
	return Memory{
		ID: newID, Kind: kind, Content: content, Importance: importance,
		Entities: entities, Version: old.version + 1, UtilityEMA: old.utilityEMA, CreatedAt: now,
	}, nil
}

// resolveRevisedFields fills in a revision's kind/importance/entities, inheriting
// from the old version when an override is omitted (empty kind / <=0 importance /
// nil entities).
func resolveRevisedFields(
	ctx context.Context, tx querier, old memoryRow, kind string, importance float64, entities []string,
) (string, float64, []string, error) {
	if kind = strings.TrimSpace(kind); kind == "" {
		kind = old.kind
	}
	if importance <= 0 {
		importance = old.importance
	}
	importance = clamp(importance, 0, 1)
	if entities == nil {
		loaded, err := loadEntitiesTx(ctx, tx, old.id)
		if err != nil {
			return "", 0, nil, err
		}
		return kind, importance, loaded, nil
	}
	return kind, importance, normalizeEntities(entities), nil
}

// Forget soft-deletes an active memory (excluded from recall; history retained).
func (s *Store) Forget(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE memories SET forgotten = 1 WHERE id = ? AND forgotten = 0`, id)
	if err != nil {
		return fmt.Errorf("forget: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns a single memory by id (regardless of active state).
func (s *Store) Get(ctx context.Context, id int64) (Memory, error) {
	row, err := loadMemoryTx(ctx, s.db, id)
	if err != nil {
		return Memory{}, err
	}
	ents, err := loadEntitiesTx(ctx, s.db, id)
	if err != nil {
		return Memory{}, err
	}
	return row.toMemory(ents), nil
}

// History returns every version in the supersession chain that id belongs to,
// oldest first (version ascending).
func (s *Store) History(ctx context.Context, id int64) ([]Memory, error) {
	// Walk back to the chain root, then forward collecting each version.
	rootID, err := s.chainRoot(ctx, id)
	if err != nil {
		return nil, err
	}
	var out []Memory
	cur := rootID
	for {
		m, err := s.Get(ctx, cur)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
		next, err := s.supersededBy(ctx, cur)
		if err != nil {
			return nil, err
		}
		if next == 0 {
			break
		}
		cur = next
	}
	return out, nil
}

func (s *Store) chainRoot(ctx context.Context, id int64) (int64, error) {
	cur := id
	for {
		var prev sql.NullInt64
		err := s.db.QueryRowContext(ctx, `SELECT supersedes FROM memories WHERE id = ?`, cur).Scan(&prev)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		if err != nil {
			return 0, dbErr("chain root", err)
		}
		if !prev.Valid {
			return cur, nil
		}
		cur = prev.Int64
	}
}

func (s *Store) supersededBy(ctx context.Context, id int64) (int64, error) {
	var next sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT superseded_by FROM memories WHERE id = ?`, id).Scan(&next); err != nil {
		return 0, dbErr("superseded by", err)
	}
	if next.Valid {
		return next.Int64, nil
	}
	return 0, nil
}

// clamp constrains v to [lo, hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// normalizeEntities lowercases, trims, and de-duplicates entity strings,
// dropping empties while preserving order.
func normalizeEntities(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range in {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}
