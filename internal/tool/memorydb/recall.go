package memorydb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Recall scoring weights. Lexical relevance leads; entity match is a strong
// structured signal; importance/recency are mild priors; utility is the learned
// signal that feedback moves (kept modest so a useful-but-irrelevant memory
// doesn't dominate — kb.md's exploit/explore caution).
const (
	wLexical    = 1.00
	wEntity     = 0.35
	wImportance = 0.20
	wRecency    = 0.15
	wUtility    = 0.30

	recencyHalfLifeDays = 30.0
	recallCandidatePool = 4 // fetch limit*this FTS candidates before re-ranking
)

// querier is the subset of *sql.DB / *sql.Tx used by the row helpers.
type querier interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// memoryRow mirrors the memories table (scan/insert helper).
type memoryRow struct {
	source       string
	content      string
	searchText   string
	kind         string
	supersededBy sql.NullInt64
	supersedes   sql.NullInt64
	importance   float64
	utilityEMA   float64
	createdAt    int64
	id           int64
	version      int
	usefulCount  float64
	harmfulCount float64
	forgotten    bool
}

// active reports whether the row is the current, non-forgotten version.
func (r memoryRow) active() bool { return !r.supersededBy.Valid && !r.forgotten }

func (r memoryRow) toMemory(entities []string) Memory {
	return Memory{
		ID: r.id, Kind: r.kind, Content: r.content, Importance: r.importance,
		Entities: entities, Version: r.version, UtilityEMA: r.utilityEMA,
		CreatedAt: time.Unix(r.createdAt, 0).UTC(),
	}
}

func insertMemory(ctx context.Context, q querier, r memoryRow) (int64, error) {
	res, err := q.ExecContext(ctx, `
		INSERT INTO memories(kind, content, search_text, importance, created_at, source,
		                     version, supersedes, useful_count, harmful_count, utility_ema)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.kind, r.content, r.searchText, r.importance, r.createdAt, r.source,
		r.version, r.supersedes, r.usefulCount, r.harmfulCount, r.utilityEMA)
	if err != nil {
		return 0, fmt.Errorf("insert memory: %w", err)
	}
	id, err := res.LastInsertId()
	return id, dbErr("last insert id", err)
}

func insertEntities(ctx context.Context, q querier, memoryID int64, entities []string) error {
	for _, e := range entities {
		if _, err := q.ExecContext(ctx,
			`INSERT OR IGNORE INTO memory_entities(memory_id, entity) VALUES(?, ?)`, memoryID, e); err != nil {
			return fmt.Errorf("insert entity: %w", err)
		}
	}
	return nil
}

func loadMemoryTx(ctx context.Context, q querier, id int64) (memoryRow, error) {
	var r memoryRow
	err := q.QueryRowContext(ctx, `
		SELECT id, kind, content, search_text, importance, created_at, useful_count, harmful_count,
		       utility_ema, source, version, supersedes, superseded_by, forgotten
		FROM memories WHERE id = ?`, id).
		Scan(&r.id, &r.kind, &r.content, &r.searchText, &r.importance, &r.createdAt, &r.usefulCount,
			&r.harmfulCount, &r.utilityEMA, &r.source, &r.version, &r.supersedes, &r.supersededBy, &r.forgotten)
	if errors.Is(err, sql.ErrNoRows) {
		return memoryRow{}, ErrNotFound
	}
	if err != nil {
		return memoryRow{}, fmt.Errorf("load memory: %w", err)
	}
	return r, nil
}

func loadEntitiesTx(ctx context.Context, q querier, id int64) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT entity FROM memory_entities WHERE memory_id = ? ORDER BY entity`, id)
	if err != nil {
		return nil, dbErr("query entities", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, dbErr("scan entity", err)
		}
		out = append(out, e)
	}
	return out, dbErr("iterate entities", rows.Err())
}

// candidate is an in-flight recall candidate accumulating its component scores.
type candidate struct {
	row       memoryRow
	bm25      float64
	haveBM25  bool
	entityHit float64
}

// Recall retrieves the most relevant active memories for a query, blending
// lexical (bm25), entity, importance, recency, and learned-utility signals.
// Passing entities boosts memories tagged with them (a stable signal even when
// the lexical query is weak). Matching memories have their access recorded.
func (s *Store) Recall(ctx context.Context, query string, entities []string, limit int) ([]Hit, error) {
	if limit <= 0 {
		limit = 8
	}
	entities = normalizeEntities(entities)
	cands := map[int64]*candidate{}

	if err := s.gatherLexical(ctx, query, limit*recallCandidatePool, cands); err != nil {
		return nil, err
	}
	if err := s.gatherEntities(ctx, entities, cands); err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, nil
	}

	hits := s.scoreCandidates(cands, entities)
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	if err := s.recordAccess(ctx, hits); err != nil {
		return nil, err
	}
	return hits, nil
}

// gatherLexical adds bm25 FTS hits (active memories only) to cands.
func (s *Store) gatherLexical(ctx context.Context, query string, poolLimit int, cands map[int64]*candidate) error {
	match := ftsMatch(query)
	if match == "" {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.kind, m.content, m.importance, m.created_at, m.utility_ema, m.version,
		       bm25(memory_fts) AS rank,
		       snippet(memory_fts, 0, '[', ']', ' … ', 10) AS snip
		FROM memory_fts
		JOIN memories m ON m.id = memory_fts.rowid
		WHERE memory_fts MATCH ? AND m.superseded_by IS NULL AND m.forgotten = 0
		ORDER BY rank
		LIMIT ?`, match, poolLimit)
	if err != nil {
		return fmt.Errorf("recall lexical: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var r memoryRow
		var rank float64
		var snip string
		if err := rows.Scan(&r.id, &r.kind, &r.content, &r.importance, &r.createdAt,
			&r.utilityEMA, &r.version, &rank, &snip); err != nil {
			return dbErr("scan lexical hit", err)
		}
		c := &candidate{row: r, bm25: rank, haveBM25: true}
		_ = snip // snippet currently informational; content is returned in full
		cands[r.id] = c
	}
	return dbErr("iterate lexical hits", rows.Err())
}

// gatherEntities adds/annotates candidates whose entities match, so an entity
// hit surfaces a memory even when the lexical query missed it.
func (s *Store) gatherEntities(ctx context.Context, entities []string, cands map[int64]*candidate) error {
	if len(entities) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(entities)), ",")
	args := make([]any, 0, len(entities))
	for _, e := range entities {
		args = append(args, e)
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT m.id, m.kind, m.content, m.importance, m.created_at, m.utility_ema, m.version,
		       COUNT(DISTINCT me.entity) AS hits
		FROM memories m
		JOIN memory_entities me ON me.memory_id = m.id
		WHERE me.entity IN (%s) AND m.superseded_by IS NULL AND m.forgotten = 0
		GROUP BY m.id`, placeholders), args...)
	if err != nil {
		return fmt.Errorf("recall entities: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var r memoryRow
		var hits int
		if err := rows.Scan(&r.id, &r.kind, &r.content, &r.importance, &r.createdAt,
			&r.utilityEMA, &r.version, &hits); err != nil {
			return dbErr("scan entity hit", err)
		}
		frac := float64(hits) / float64(len(entities))
		if c, ok := cands[r.id]; ok {
			c.entityHit = frac
		} else {
			cands[r.id] = &candidate{row: r, entityHit: frac}
		}
	}
	return dbErr("iterate entity hits", rows.Err())
}

// scoreCandidates computes the final blended score for each candidate. bm25 is
// normalized absolutely (SQLite returns more-negative = better; we map the
// positive magnitude through pos/(1+pos) into [0,1)). Absolute — rather than
// min-max — normalization keeps near-equal lexical matches near-equal, so the
// entity/importance/utility signals can still tip a close lexical tie.
func (s *Store) scoreCandidates(cands map[int64]*candidate, _ []string) []Hit {
	now := s.now()

	hits := make([]Hit, 0, len(cands))
	for _, c := range cands {
		lex := 0.0
		if c.haveBM25 {
			if pos := -c.bm25; pos > 0 {
				lex = pos / (1 + pos)
			}
		}
		recency := recencyScore(now, c.row.createdAt)
		util := math.Tanh(c.row.utilityEMA) // squashed into (-1,1)
		score := wLexical*lex + wEntity*c.entityHit + wImportance*c.row.importance +
			wRecency*recency + wUtility*util
		hits = append(hits, Hit{
			Memory:    c.row.toMemory(nil),
			Score:     score,
			Lexical:   lex,
			EntityHit: c.entityHit,
			Recency:   recency,
		})
	}
	return hits
}

// recordAccess bumps access_count / last_used_at for the recalled memories.
func (s *Store) recordAccess(ctx context.Context, hits []Hit) error {
	if len(hits) == 0 {
		return nil
	}
	now := s.now().UTC().Unix()
	for _, h := range hits {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE memories SET access_count = access_count + 1, last_used_at = ? WHERE id = ?`,
			now, h.ID); err != nil {
			return fmt.Errorf("record access: %w", err)
		}
	}
	return nil
}

// Reinforce applies a feedback credit to a memory's learned utility (EMA) and
// useful/harmful tallies. Positive credit = the memory helped; negative = it
// hurt (stale, wrong scope, caused a bad action). Only memories that actually
// influenced an outcome should be reinforced — not merely retrieved ones
// (kb.md). Returns the updated utility EMA.
func (s *Store) Reinforce(ctx context.Context, id int64, credit float64) (float64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, dbErr("begin tx", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	var ema float64
	err = tx.QueryRowContext(ctx, `SELECT utility_ema FROM memories WHERE id = ?`, id).Scan(&ema)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, dbErr("load utility", err)
	}
	ema = ema*(1-utilityAlpha) + credit*utilityAlpha

	usefulDelta, harmfulDelta := 0.0, 0.0
	if credit >= 0 {
		usefulDelta = credit
	} else {
		harmfulDelta = -credit
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE memories
		SET utility_ema = ?, useful_count = useful_count + ?, harmful_count = harmful_count + ?, last_used_at = ?
		WHERE id = ?`, ema, usefulDelta, harmfulDelta, s.now().UTC().Unix(), id); err != nil {
		return 0, fmt.Errorf("reinforce: %w", err)
	}
	if err := dbErr("commit", tx.Commit()); err != nil {
		return 0, err
	}
	return ema, nil
}

// Feedback signal names accepted by CreditForSignal / the Reinforce tool.
const (
	SignalConfirmed  = "confirmed"  // user explicitly confirmed it helped
	SignalUsed       = "used"       // influenced a tool call / decision
	SignalHelpful    = "helpful"    // referenced in the answer
	SignalNeutral    = "neutral"    // no effect
	SignalIrrelevant = "irrelevant" // retrieved but off-topic
	SignalStale      = "stale"      // outdated relative to current facts
	SignalCorrected  = "corrected"  // user corrected the memory's content
	SignalHarmful    = "harmful"    // led to a wrong action
)

// CreditForSignal maps a named feedback signal to a credit value (kb.md's
// staged credit table). Unknown signals return (0, false).
func CreditForSignal(signal string) (float64, bool) {
	c, ok := map[string]float64{
		SignalConfirmed:  0.5,
		SignalUsed:       0.25,
		SignalHelpful:    0.2,
		SignalNeutral:    0.0,
		SignalIrrelevant: -0.2,
		SignalStale:      -0.3,
		SignalCorrected:  -0.8,
		SignalHarmful:    -1.0,
	}[strings.ToLower(strings.TrimSpace(signal))]
	return c, ok
}

// recencyScore maps age to (0,1] with a 30-day half-life.
func recencyScore(now time.Time, createdAtUnix int64) float64 {
	ageDays := now.Sub(time.Unix(createdAtUnix, 0)).Hours() / 24
	if ageDays < 0 {
		ageDays = 0
	}
	return math.Exp(-math.Ln2 * ageDays / recencyHalfLifeDays)
}
