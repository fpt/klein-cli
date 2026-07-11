package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fpt/klein-cli/internal/tool/memorydb"
)

const memoryUsage = `Usage:
  /memory [list]        List active memories with reference stats
  /memory all           Include superseded/forgotten versions
  /memory show <id>     Show full content, entities, stats, and version history
  /memory search <q>    Recall memories matching a query (records access)
  /memory forget <id>   Soft-delete a memory (excluded from future recall)`

// handleMemoryCommand implements the /memory REPL command: inspect and curate
// the sqlite long-term memory (memorydb). It reads/writes the store directly —
// this is a human curation surface, distinct from the model-facing tools.
func handleMemoryCommand(a *Agent, args string) {
	mm := a.MemoryManager()
	if mm == nil {
		fmt.Println("🧠 Long-term memory (memorydb) is not enabled in this session.")
		return
	}
	store := mm.Store()
	ctx := context.Background()

	sub, rest := "list", ""
	if fields := strings.Fields(args); len(fields) > 0 {
		sub = strings.ToLower(fields[0])
		rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(args), fields[0]))
	}

	switch sub {
	case "list", "ls":
		memoryList(ctx, store, false)
	case "all":
		memoryList(ctx, store, true)
	case "show", "get", "view":
		memoryShow(ctx, store, rest)
	case "search", "recall", "find":
		memorySearch(ctx, store, rest)
	case "forget", "delete", "del", "rm":
		memoryForget(ctx, store, rest)
	default:
		// Allow "/memory 3" as shorthand for "/memory show 3".
		if _, err := strconv.ParseInt(sub, 10, 64); err == nil {
			memoryShow(ctx, store, sub)
			return
		}
		fmt.Println(memoryUsage)
	}
}

func memoryList(ctx context.Context, store *memorydb.Store, includeInactive bool) {
	items, err := store.List(ctx, includeInactive, 100)
	if err != nil {
		fmt.Printf("Failed to list memories: %v\n", err)
		return
	}
	active, total, _ := store.Count(ctx)
	if len(items) == 0 {
		fmt.Println("🧠 Memory is empty.")
		return
	}
	fmt.Printf("🧠 %d active / %d total memories\n", active, total)
	for _, m := range items {
		flag := ""
		if !m.Active {
			flag = " ⚪️inactive"
		}
		fmt.Printf("  #%-4d [%s] imp %.2f  %s%s\n        %s\n",
			m.ID, m.Kind, m.Importance, refStats(m), flag, oneLine(m.Content, 88))
	}
	fmt.Println("\n💡 /memory show <id> · /memory search <q> · /memory forget <id> · /memory all")
}

func memoryShow(ctx context.Context, store *memorydb.Store, arg string) {
	id, ok := parseID(arg)
	if !ok {
		fmt.Println("Usage: /memory show <id>")
		return
	}
	st, err := store.Stat(ctx, id)
	if errors.Is(err, memorydb.ErrNotFound) {
		fmt.Printf("No memory #%d.\n", id)
		return
	}
	if err != nil {
		fmt.Printf("Failed to read memory: %v\n", err)
		return
	}
	mem, _ := store.Get(ctx, id) // for entities

	state := "active"
	if !st.Active {
		state = "inactive (superseded or forgotten)"
	}
	fmt.Printf("🧠 Memory #%d  [%s]  v%d  %s\n", st.ID, st.Kind, st.Version, state)
	fmt.Printf("   %s\n\n", st.Content)
	if len(mem.Entities) > 0 {
		fmt.Printf("   entities: %s\n", strings.Join(mem.Entities, ", "))
	}
	fmt.Printf("   importance %.2f · %s\n", st.Importance, refStats(st))
	fmt.Printf("   created %s\n", st.CreatedAt.Local().Format("2006-01-02 15:04"))

	hist, _ := store.History(ctx, id)
	if len(hist) > 1 {
		fmt.Printf("\n   history (%d versions):\n", len(hist))
		for _, h := range hist {
			marker := " "
			if h.ID == id {
				marker = "→"
			}
			fmt.Printf("   %s v%d (#%d): %s\n", marker, h.Version, h.ID, oneLine(h.Content, 80))
		}
	}
}

func memorySearch(ctx context.Context, store *memorydb.Store, query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		fmt.Println("Usage: /memory search <query>")
		return
	}
	hits, err := store.Recall(ctx, query, nil, 10)
	if err != nil {
		fmt.Printf("Search failed: %v\n", err)
		return
	}
	if len(hits) == 0 {
		fmt.Printf("No memories match %q.\n", query)
		return
	}
	fmt.Printf("🔎 %d match(es) for %q:\n", len(hits), query)
	for _, h := range hits {
		fmt.Printf("  #%-4d [%s] score %.2f  %s\n", h.ID, h.Kind, h.Score, oneLine(h.Content, 84))
	}
}

func memoryForget(ctx context.Context, store *memorydb.Store, arg string) {
	id, ok := parseID(arg)
	if !ok {
		fmt.Println("Usage: /memory forget <id>")
		return
	}
	if err := store.Forget(ctx, id); err != nil {
		if errors.Is(err, memorydb.ErrNotFound) {
			fmt.Printf("No active memory #%d to forget.\n", id)
			return
		}
		fmt.Printf("Failed to forget: %v\n", err)
		return
	}
	fmt.Printf("🗑️  Forgot memory #%d (excluded from future recall; history retained).\n", id)
}

// refStats renders how a memory has been referenced/reinforced.
func refStats(m memorydb.MemoryStat) string {
	parts := []string{fmt.Sprintf("used %d×", m.AccessCount), fmt.Sprintf("util %+.2f", m.UtilityEMA)}
	if m.UsefulCount > 0 {
		parts = append(parts, fmt.Sprintf("👍%.1f", m.UsefulCount))
	}
	if m.HarmfulCount > 0 {
		parts = append(parts, fmt.Sprintf("👎%.1f", m.HarmfulCount))
	}
	parts = append(parts, "last "+humanAgo(m.LastUsedAt))
	return strings.Join(parts, " · ")
}

// humanAgo renders a coarse "time since" for t, or "never" when zero.
func humanAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func parseID(arg string) (int64, bool) {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	return n, err == nil && n > 0
}

// oneLine collapses whitespace and truncates to n runes with an ellipsis.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
