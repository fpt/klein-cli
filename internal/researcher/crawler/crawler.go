// Package crawler provides primary-source crawling for the Researcher
// pipeline. It complements the RSS/Atom fetcher in internal/researcher/
// source by adding support for two common shapes:
//
//   - Single documents (a press release PDF, an IR PDF, a regulator filing)
//     via FetchSingle. The crawler stores only a pointer (URL + title +
//     short summary) into the event store; full PDF body extraction stays
//     out-of-band and is handled by klein's existing PDFRead tool when an
//     agent decides to dig in.
//
//   - Index/listing pages (corporate IR landing pages, regulator news
//     indexes) via FetchListing, which uses heuristic anchor + date
//     scraping to surface candidate items as Event records.
//
// Both produce model.Event values that slot into the existing JSONL store
// alongside RSS-sourced events, so downstream narrative extraction treats
// them uniformly.
package crawler

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"github.com/fpt/klein-cli/internal/researcher/model"
)

// userAgent is sent on every request. Sites that block default Go user-agents
// (e.g. those running aggressive WAFs) usually allow a real-looking string.
const userAgent = "klein-researcher/0.1 (+https://github.com/fpt/klein-cli)"

// defaultTimeout caps any single HTTP request. Listing pages can be large;
// PDFs especially. 30s is generous for normal IR pages, conservative for
// adversarial ones.
const defaultTimeout = 30 * time.Second

// Result is the structured output of FetchSingle. Body is the extracted
// plain text (for HTML) or empty (for PDFs — see package doc).
type Result struct {
	URL         string
	ContentType string // "html" | "pdf" | "unknown"
	Title       string
	Summary     string // short excerpt of the body (≤ 500 chars), suitable for an Event.Summary
	PublishedAt time.Time
}

// ListingItem is one candidate event surfaced from an index page.
type ListingItem struct {
	URL         string
	Title       string
	PublishedAt time.Time // zero when no date could be parsed
}

// FetchSingle GETs the URL, sniffs the content type, and returns a Result
// suitable for wrapping as a model.Event. For PDFs only metadata + URL are
// captured — the agent uses PDFRead to read the body.
func FetchSingle(ctx context.Context, target string) (*Result, error) {
	body, ct, finalURL, err := fetch(ctx, target)
	if err != nil {
		return nil, err
	}

	out := &Result{
		URL:         finalURL,
		ContentType: classifyContentType(ct, finalURL),
		PublishedAt: time.Now().UTC(), // best default — caller can override
	}

	switch out.ContentType {
	case "html":
		title, summary, published := parseHTMLSingle(body)
		out.Title = title
		out.Summary = summary
		if !published.IsZero() {
			out.PublishedAt = published
		}
	case "pdf":
		// We deliberately do not extract PDF text here — it bloats the
		// event store and klein's PDFRead/PDFInfo tools already cover
		// on-demand inspection. The Title is derived from the filename.
		out.Title = pdfTitleFromURL(finalURL)
		out.Summary = fmt.Sprintf("PDF document at %s — use PDFRead to extract text.", finalURL)
	default:
		out.Title = finalURL
		out.Summary = strings.TrimSpace(string(body))
		if len(out.Summary) > 500 {
			out.Summary = out.Summary[:500] + "…"
		}
	}

	if strings.TrimSpace(out.Title) == "" {
		out.Title = finalURL
	}
	return out, nil
}

// FetchListing GETs an HTML index page and returns up to maxItems candidate
// entries. Each entry is an anchor link paired with a date discovered in
// nearby text. Items without a parseable date are still returned but with a
// zero PublishedAt — the caller can decide whether to keep them.
//
// maxItems <= 0 means no cap.
func FetchListing(ctx context.Context, target string, maxItems int) ([]ListingItem, error) {
	body, ct, finalURL, err := fetch(ctx, target)
	if err != nil {
		return nil, err
	}
	if classifyContentType(ct, finalURL) != "html" {
		return nil, fmt.Errorf("listing URL must be HTML, got %s", ct)
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("parsing html: %w", err)
	}

	base, err := url.Parse(finalURL)
	if err != nil {
		return nil, fmt.Errorf("parsing base url: %w", err)
	}

	var items []ListingItem
	seen := make(map[string]bool)

	doc.Find("a").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if maxItems > 0 && len(items) >= maxItems {
			return false
		}
		href, ok := s.Attr("href")
		if !ok {
			return true
		}
		href = strings.TrimSpace(href)
		if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:") || strings.HasPrefix(href, "mailto:") {
			return true
		}
		resolved, err := base.Parse(href)
		if err != nil {
			return true
		}
		abs := resolved.String()
		if seen[abs] {
			return true
		}

		title := collapseSpaces(s.Text())
		if utf8.RuneCountInString(title) < 4 {
			// Skip tiny anchor texts ("more", "詳細", "→", "TOP"). Rune
			// count (not byte length) so we filter multibyte scripts
			// correctly. Real announcement titles run 10+ runes.
			return true
		}
		// Pull date hints from the anchor itself and its parent/grandparent
		// — IR listings typically render dates as a sibling element or
		// inside the same <li> as the link.
		context := collapseSpaces(s.Text() + " " + s.Parent().Text())
		published := extractDate(context)

		// A real announcement entry has EITHER a date in nearby text OR a
		// long descriptive title. Anchors with neither are almost always
		// navigation chrome ("Investors", "Press releases", "Contact").
		// We're generous about the title threshold (12 runes ~ "Q3 2025
		// Earnings" or 「決算短信を公表」) so we don't drop real items.
		if published.IsZero() && utf8.RuneCountInString(title) < 12 {
			return true
		}

		seen[abs] = true
		items = append(items, ListingItem{
			URL:         abs,
			Title:       title,
			PublishedAt: published,
		})
		return true
	})

	return items, nil
}

// ResultToEvent builds a model.Event from a single-document Result, applying
// the requested provenance (intake/role/trust_tier). The event ID is derived
// from the URL so re-ingesting the same primary source is idempotent.
func ResultToEvent(r *Result, intake, role, trustTier string) model.Event {
	now := time.Now().UTC()
	src := model.NormalizeSource(model.Source{
		Name:      hostFromURL(r.URL),
		Type:      r.ContentType,
		URL:       r.URL,
		Intake:    intake,
		Role:      role,
		TrustTier: trustTier,
	})
	ev := model.Event{
		Source:      src.Name,
		Intake:      src.Intake,
		Role:        src.Role,
		TrustTier:   src.TrustTier,
		Weight:      src.Weight,
		Title:       r.Title,
		URL:         r.URL,
		PublishedAt: r.PublishedAt,
		Summary:     r.Summary,
		FetchedAt:   now,
	}
	if ev.PublishedAt.IsZero() {
		ev.PublishedAt = now
	}
	ev.ID = stableID(ev.URL)
	return ev
}

// ListingItemToEvent wraps one listing entry as an Event. sourceName is the
// listing's friendly name (e.g. "kioxia-ir") so all items from one listing
// cluster under a single source.
func ListingItemToEvent(item ListingItem, sourceName, intake, role, trustTier string) model.Event {
	now := time.Now().UTC()
	src := model.NormalizeSource(model.Source{
		Name:      sourceName,
		Type:      "html",
		URL:       item.URL,
		Intake:    intake,
		Role:      role,
		TrustTier: trustTier,
	})
	ev := model.Event{
		Source:      src.Name,
		Intake:      src.Intake,
		Role:        src.Role,
		TrustTier:   src.TrustTier,
		Weight:      src.Weight,
		Title:       item.Title,
		URL:         item.URL,
		PublishedAt: item.PublishedAt,
		FetchedAt:   now,
	}
	if ev.PublishedAt.IsZero() {
		ev.PublishedAt = now
	}
	ev.ID = stableID(ev.URL)
	return ev
}

// ---------- internals ----------

func fetch(ctx context.Context, target string) (body []byte, contentType, finalURL string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/pdf,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en;q=0.9,ja;q=0.8")

	client := &http.Client{Timeout: defaultTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", "", fmt.Errorf("%s returned HTTP %d", target, resp.StatusCode)
	}

	// Cap body to 8 MiB. Larger PDFs blow our memory budget for a tool call.
	body, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", "", err
	}
	return body, resp.Header.Get("Content-Type"), resp.Request.URL.String(), nil
}

func classifyContentType(ct, target string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	switch {
	case strings.Contains(ct, "html"):
		return "html"
	case strings.Contains(ct, "pdf"), strings.HasSuffix(strings.ToLower(target), ".pdf"):
		return "pdf"
	default:
		return "unknown"
	}
}

func parseHTMLSingle(body []byte) (title, summary string, published time.Time) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return "", "", time.Time{}
	}

	title = collapseSpaces(doc.Find("title").First().Text())
	// Many sites duplicate the site name into <title>. Prefer an explicit
	// og:title meta when present.
	if og, ok := doc.Find(`meta[property="og:title"]`).First().Attr("content"); ok && strings.TrimSpace(og) != "" {
		title = collapseSpaces(og)
	}

	// Best-effort body summary: og:description first, otherwise the first
	// substantial <p> or <main> text.
	if desc, ok := doc.Find(`meta[name="description"], meta[property="og:description"]`).First().Attr("content"); ok && strings.TrimSpace(desc) != "" {
		summary = collapseSpaces(desc)
	} else {
		// Strip nav-like elements first.
		doc.Find("nav, header, footer, script, style, noscript").Remove()
		mainText := collapseSpaces(doc.Find("main, article, [role=main]").First().Text())
		if mainText == "" {
			mainText = collapseSpaces(doc.Find("body").First().Text())
		}
		summary = mainText
	}
	if len(summary) > 500 {
		summary = summary[:500] + "…"
	}

	// Published date hints: <meta property="article:published_time">,
	// <time datetime=...>, og:updated_time. Best-effort.
	if v, ok := doc.Find(`meta[property="article:published_time"]`).First().Attr("content"); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			published = t.UTC()
		}
	}
	if published.IsZero() {
		if v, ok := doc.Find("time[datetime]").First().Attr("datetime"); ok {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				published = t.UTC()
			} else if t, err := time.Parse("2006-01-02", v); err == nil {
				published = t.UTC()
			}
		}
	}
	return title, summary, published
}

func pdfTitleFromURL(target string) string {
	if u, err := url.Parse(target); err == nil {
		base := path.Base(u.Path)
		if base == "" || base == "/" {
			return target
		}
		return base
	}
	return target
}

func hostFromURL(target string) string {
	if u, err := url.Parse(target); err == nil {
		return u.Host
	}
	return "primary-source"
}

func stableID(input string) string {
	sum := sha1.Sum([]byte(input))
	return hex.EncodeToString(sum[:])
}

var multiSpaceRE = regexp.MustCompile(`\s+`)

func collapseSpaces(s string) string {
	return strings.TrimSpace(multiSpaceRE.ReplaceAllString(s, " "))
}

// extractDate looks for common Japanese and Western date patterns in free
// text near anchor links. Order matters: more specific patterns first.
var dateREs = []*regexp.Regexp{
	regexp.MustCompile(`(\d{4})年\s*(\d{1,2})月\s*(\d{1,2})日`), // 2026年6月21日
	regexp.MustCompile(`(\d{4})/(\d{1,2})/(\d{1,2})`),        // 2026/06/21
	regexp.MustCompile(`(\d{4})-(\d{1,2})-(\d{1,2})`),        // 2026-06-21
	regexp.MustCompile(`(\d{4})\.(\d{1,2})\.(\d{1,2})`),      // 2026.06.21
	regexp.MustCompile(`(\d{1,2})/(\d{1,2})/(\d{4})`),        // 6/21/2026 (US-ish)
}

func extractDate(text string) time.Time {
	for _, re := range dateREs {
		if m := re.FindStringSubmatch(text); m != nil {
			y, mo, d := atoi(m[1]), atoi(m[2]), atoi(m[3])
			// Last pattern is M/D/YYYY — swap.
			if y < 1900 {
				y, mo, d = atoi(m[3]), atoi(m[1]), atoi(m[2])
			}
			if y >= 1900 && mo >= 1 && mo <= 12 && d >= 1 && d <= 31 {
				return time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
			}
		}
	}
	return time.Time{}
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
