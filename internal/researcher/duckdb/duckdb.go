// Package duckdb runs SQL queries against the Researcher event/narrative
// store via the DuckDB CLI. Mirrors the analysis pattern from
// m6o-devif-system-monitor: JSONL files on disk → views via read_json_auto
// → window/baseline/temporal-join queries on top.
//
// DuckDB is invoked as an external process so klein doesn't take on a CGO
// dependency. The user installs the CLI once
// (`brew install duckdb` or https://install.duckdb.org). If the binary is
// missing the wrapper returns a clear error pointing at install instructions
// instead of a confusing "duckdb: command not found".
package duckdb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotInstalled is returned when the duckdb binary is not on PATH. The
// caller (the tool layer) surfaces this as a friendly install hint.
var ErrNotInstalled = errors.New("duckdb CLI not found on PATH — install via 'brew install duckdb' or https://install.duckdb.org")

// Output format for query results. Markdown is human/agent-readable; box is
// fancier but uses unicode box-drawing characters that can confuse non-TTY
// consumers. Default to markdown.
const defaultOutputMode = "markdown"

// MaxResultBytes caps the response size so a runaway query doesn't blow the
// tool result back through the LLM context. Hit ~250 rows for typical queries.
const MaxResultBytes = 16 << 10

// Query runs `sql` against the standard Researcher views (`events` and
// `narratives`) defined over the JSONL store at dataDir.
//
// Implementation: writes a small prelude that CREATE VIEW's `events` and
// `narratives`, then the caller's SQL, piped through `duckdb -markdown`.
// Returns the rendered output (truncated to MaxResultBytes).
//
// Set Asia/Tokyo as the session timezone so timestamp arithmetic matches the
// rest of klein (we collect timestamps in UTC but most analysis questions
// are phrased in JST business hours). Pass an explicit `SET TimeZone` in the
// user SQL to override.
func Query(ctx context.Context, dataDir, sql string) (string, error) {
	if _, err := exec.LookPath("duckdb"); err != nil {
		return "", ErrNotInstalled
	}
	if strings.TrimSpace(sql) == "" {
		return "", fmt.Errorf("empty SQL")
	}

	prelude, err := BuildPrelude(dataDir)
	if err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, "duckdb", "-"+defaultOutputMode)
	cmd.Stdin = strings.NewReader(prelude + "\n" + sql + "\n")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// DuckDB writes the SQL error message to stderr; surface it as the
		// tool result so the agent can see the actual problem.
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return "", fmt.Errorf("duckdb: %s", errMsg)
	}

	out := stdout.String()
	if len(out) > MaxResultBytes {
		out = out[:MaxResultBytes] + fmt.Sprintf("\n\n…(truncated, %d bytes total — narrow the query with LIMIT/WHERE)", len(stdout.String()))
	}
	return out, nil
}

// BuildPrelude returns the SQL that sets up the `events` and `narratives`
// views over the JSONL store. Exported (and pure) so tests can validate
// schema-discovery behaviour without invoking the CLI.
//
// View shapes:
//
//	events     — events.jsonl, one row per event. Columns inherited from
//	             the JSONL schema (id, source, intake, role, trust_tier,
//	             weight, title, url, summary, published_at::TIMESTAMP,
//	             fetched_at::TIMESTAMP).
//	narratives — narratives.json, one row per narrative cluster. Includes
//	             themes/entities arrays and source_mix/trust_mix/intake_mix
//	             structs (DuckDB infers MAP(VARCHAR, INTEGER)).
//
// If a file doesn't exist yet, the corresponding view is created as an empty
// stub so the user's query gets a clean "no events stored" rather than a
// "could not find file" parse error.
func BuildPrelude(dataDir string) (string, error) {
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return "", fmt.Errorf("resolving data dir: %w", err)
	}

	eventsPath := filepath.Join(absDataDir, "events.jsonl")
	narrativesPath := filepath.Join(absDataDir, "narratives.json")

	var sb strings.Builder
	sb.WriteString("-- Researcher analytical prelude (auto-generated).\n")
	sb.WriteString("-- JST is the natural business timezone for klein's primary intake feeds.\n")
	sb.WriteString("SET TimeZone = 'Asia/Tokyo';\n\n")

	if fileExists(eventsPath) {
		// union_by_name=true makes the view tolerant of schema evolution
		// (e.g. if we add a `themes` field later); ignore_errors skips
		// individual malformed lines without aborting the whole load.
		//
		// read_json_auto sees ISO-8601 strings as VARCHAR, so we cast
		// published_at and fetched_at to TIMESTAMP explicitly. Otherwise
		// the agent gets a confusing "DATE_TRUNC on VARCHAR" error on the
		// most natural first query.
		fmt.Fprintf(&sb,
			"CREATE OR REPLACE VIEW events AS\n"+
				"SELECT\n"+
				"  * EXCLUDE (published_at, fetched_at),\n"+
				"  TRY_CAST(published_at AS TIMESTAMP) AS published_at,\n"+
				"  TRY_CAST(fetched_at  AS TIMESTAMP) AS fetched_at\n"+
				"FROM read_json_auto(\n"+
				"  '%s',\n"+
				"  format='nd',\n"+
				"  union_by_name=true,\n"+
				"  ignore_errors=true\n"+
				");\n\n",
			escapeSQLString(eventsPath),
		)
	} else {
		sb.WriteString("-- events.jsonl not found — `events` view is an empty stub.\n")
		sb.WriteString(emptyEventsViewSQL())
		sb.WriteString("\n")
	}

	if fileExists(narrativesPath) {
		// narratives.json is a JSON array, not newline-delimited. format='array'
		// tells DuckDB to expect a top-level array.
		//
		// We don't EXCLUDE+cast timestamps here because narratives.json may
		// be an empty `[]` (no events analyzed yet) — in that case EXCLUDE
		// fails because the column doesn't exist. Narratives are typically
		// queried by score/label/event_ids; if a user needs DATE_TRUNC on
		// first_seen/last_seen they can cast inline with `::TIMESTAMP`.
		fmt.Fprintf(&sb,
			"CREATE OR REPLACE VIEW narratives AS\n"+
				"SELECT * FROM read_json_auto(\n"+
				"  '%s',\n"+
				"  format='array',\n"+
				"  union_by_name=true,\n"+
				"  ignore_errors=true\n"+
				");\n",
			escapeSQLString(narrativesPath),
		)
	} else {
		sb.WriteString("-- narratives.json not found — `narratives` view is an empty stub.\n")
		sb.WriteString(emptyNarrativesViewSQL())
	}

	return sb.String(), nil
}

// emptyEventsViewSQL returns a CREATE VIEW that produces an empty result with
// the canonical event columns. Used when events.jsonl doesn't exist yet so
// the user's query gets a clean "no rows" rather than a load error.
func emptyEventsViewSQL() string {
	return `CREATE OR REPLACE VIEW events AS
SELECT
  CAST(NULL AS VARCHAR)   AS id,
  CAST(NULL AS VARCHAR)   AS source,
  CAST(NULL AS VARCHAR)   AS intake,
  CAST(NULL AS VARCHAR)   AS role,
  CAST(NULL AS VARCHAR)   AS trust_tier,
  CAST(NULL AS DOUBLE)    AS weight,
  CAST(NULL AS VARCHAR)   AS title,
  CAST(NULL AS VARCHAR)   AS url,
  CAST(NULL AS VARCHAR)   AS summary,
  CAST(NULL AS TIMESTAMP) AS published_at,
  CAST(NULL AS TIMESTAMP) AS fetched_at
WHERE 1 = 0;
`
}

// emptyNarrativesViewSQL returns a stub view matching the narrative shape.
func emptyNarrativesViewSQL() string {
	return `CREATE OR REPLACE VIEW narratives AS
SELECT
  CAST(NULL AS VARCHAR)        AS id,
  CAST(NULL AS VARCHAR)        AS label,
  CAST(NULL AS VARCHAR[])      AS themes,
  CAST(NULL AS VARCHAR[])      AS entities,
  CAST(NULL AS INTEGER)        AS event_count,
  CAST(NULL AS INTEGER)        AS signal_count,
  CAST(NULL AS INTEGER)        AS outcome_count,
  CAST(NULL AS INTEGER)        AS source_count,
  CAST(NULL AS DOUBLE)         AS score,
  CAST(NULL AS VARCHAR)        AS trend,
  CAST(NULL AS TIMESTAMP)      AS first_seen,
  CAST(NULL AS TIMESTAMP)      AS last_seen
WHERE 1 = 0;
`
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// escapeSQLString escapes single quotes for inline use in SQL literals.
func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
