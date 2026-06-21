package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeListing serves an HTML page mimicking a Japanese corporate IR layout —
// each announcement is an <li> with a date label and a link to a PDF/details
// page. The crawler should extract these as ListingItems with parsed dates.
const fakeListingHTML = `<!DOCTYPE html>
<html lang="ja">
<head><title>Kioxia IR News</title></head>
<body>
<main>
<ul class="news-list">
  <li>
    <span class="date">2026年6月19日</span>
    <a href="/ja-jp/about/news/2026/20260619-1.html">第3四半期決算短信〔IFRS〕(連結)を公表</a>
  </li>
  <li>
    <span class="date">2026/06/12</span>
    <a href="https://www.example.com/release.pdf">2026年定時株主総会招集ご通知</a>
  </li>
  <li>
    <span class="date">2026-06-05</span>
    <a href="/ja-jp/about/news/2026/20260605.html">取締役会の決議事項に関するお知らせ</a>
  </li>
  <li>
    <a href="/ja-jp/about/news/undated.html">日付不明のリリース</a>
  </li>
</ul>
</main>
</body>
</html>`

func TestFetchListing_ParsesDatedJapaneseEntries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fakeListingHTML))
	}))
	defer srv.Close()

	items, err := FetchListing(context.Background(), srv.URL, 10)
	if err != nil {
		t.Fatalf("FetchListing: %v", err)
	}
	// Three dated entries should survive. The fourth ("日付不明のリリース",
	// 9 runes, no date) is filtered as likely nav chrome — see the
	// no-date / short-title rule in FetchListing.
	if len(items) != 3 {
		t.Fatalf("expected exactly 3 items (dated only), got %d: %+v", len(items), items)
	}

	wantDate := func(y, m, d int) time.Time {
		return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	}

	cases := []struct {
		urlSubstr string
		title     string
		date      time.Time
	}{
		{"20260619-1.html", "第3四半期決算短信", wantDate(2026, 6, 19)},
		{"example.com/release.pdf", "2026年定時株主総会招集ご通知", wantDate(2026, 6, 12)},
		{"20260605.html", "取締役会の決議事項に関するお知らせ", wantDate(2026, 6, 5)},
	}
	for _, tc := range cases {
		var found *ListingItem
		for i := range items {
			if strings.Contains(items[i].URL, tc.urlSubstr) {
				found = &items[i]
				break
			}
		}
		if found == nil {
			t.Errorf("missing item for %s", tc.urlSubstr)
			continue
		}
		if !strings.Contains(found.Title, tc.title) {
			t.Errorf("title for %s: got %q want substring %q", tc.urlSubstr, found.Title, tc.title)
		}
		if !found.PublishedAt.Equal(tc.date) {
			t.Errorf("date for %s: got %v want %v", tc.urlSubstr, found.PublishedAt, tc.date)
		}
	}

	// Confirm the undated short-title entry was filtered out as nav chrome.
	for _, it := range items {
		if strings.Contains(it.URL, "undated.html") {
			t.Errorf("undated short-title entry should be filtered, but got: %+v", it)
		}
	}
}

// TestFetchListing_KeepsLongUndatedTitles confirms an undated item with a
// substantial title still flows through. Only short undated anchors (likely
// navigation) are dropped.
func TestFetchListing_KeepsLongUndatedTitles(t *testing.T) {
	const page = `<html><body>
<a href="/foo">Short</a>
<a href="/announcement">Acme launches third-generation memory architecture for hyperscale data centers</a>
</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	items, err := FetchListing(context.Background(), srv.URL, 10)
	if err != nil {
		t.Fatalf("FetchListing: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 item, got %d: %+v", len(items), items)
	}
	if !strings.Contains(items[0].Title, "Acme launches") {
		t.Errorf("expected long-title item, got %q", items[0].Title)
	}
}

// TestFetchListing_FiltersJapaneseNavChrome reproduces the live Kioxia bug
// where 6-rune nav links like "個人のお客様" (3 bytes/rune → 18 bytes — past
// any byte-length check) were leaking into the results.
func TestFetchListing_FiltersJapaneseNavChrome(t *testing.T) {
	const page = `<html><body>
<nav>
  <a href="/personal">個人のお客様</a>
  <a href="/business">法人のお客様</a>
  <a href="/rd">研究・技術開発</a>
</nav>
<main>
  <ul>
    <li><span>2026年6月19日</span> <a href="/news/q3.html">第3四半期決算短信を公表しました</a></li>
  </ul>
</main>
</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	items, err := FetchListing(context.Background(), srv.URL, 20)
	if err != nil {
		t.Fatalf("FetchListing: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 item (the dated announcement), got %d: %+v", len(items), items)
	}
	if !strings.Contains(items[0].URL, "q3.html") {
		t.Errorf("expected the q3 announcement, got %s", items[0].URL)
	}
}

func TestFetchSingle_HTMLExtractsTitleAndDescription(t *testing.T) {
	const page = `<!DOCTYPE html>
<html>
<head>
  <title>Press release | Acme Corp</title>
  <meta property="og:title" content="Acme Q3 results: revenue up 15%">
  <meta name="description" content="Acme reported Q3 revenue of $4.2B, up 15% YoY, driven by data-centre demand.">
  <meta property="article:published_time" content="2026-06-19T13:00:00Z">
</head>
<body>
  <nav>About | Investors | Press</nav>
  <main><p>Acme Corp announced today its third quarter results.</p></main>
</body>
</html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	r, err := FetchSingle(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchSingle: %v", err)
	}
	if r.ContentType != "html" {
		t.Errorf("content type: %q", r.ContentType)
	}
	if r.Title != "Acme Q3 results: revenue up 15%" {
		t.Errorf("title: %q", r.Title)
	}
	if !strings.Contains(r.Summary, "revenue of $4.2B") {
		t.Errorf("summary missing description: %q", r.Summary)
	}
	want := time.Date(2026, 6, 19, 13, 0, 0, 0, time.UTC)
	if !r.PublishedAt.Equal(want) {
		t.Errorf("published_at: %v want %v", r.PublishedAt, want)
	}
}

func TestFetchSingle_PDFRecordsPointerOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4\nfake-pdf-body"))
	}))
	defer srv.Close()

	r, err := FetchSingle(context.Background(), srv.URL+"/files/200A-j.pdf")
	if err != nil {
		t.Fatalf("FetchSingle: %v", err)
	}
	if r.ContentType != "pdf" {
		t.Errorf("content type: %q", r.ContentType)
	}
	if r.Title != "200A-j.pdf" {
		t.Errorf("PDF title should default to basename, got %q", r.Title)
	}
	if !strings.Contains(r.Summary, "PDFRead") {
		t.Errorf("summary should hint at PDFRead, got %q", r.Summary)
	}
}

func TestResultToEvent_StableID(t *testing.T) {
	r := &Result{URL: "https://example.com/x.pdf", ContentType: "pdf", Title: "X", PublishedAt: time.Now()}
	a := ResultToEvent(r, "corporate", "signal", "corporate")
	b := ResultToEvent(r, "corporate", "signal", "corporate")
	if a.ID != b.ID {
		t.Errorf("re-ingest should yield identical IDs: %s vs %s", a.ID, b.ID)
	}
	if a.ID == "" {
		t.Error("ID is empty")
	}
}
