package tool

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

func TestResolveSymbol(t *testing.T) {
	cases := map[string]string{
		"日経平均":     "^N225",
		"Nikkei":   "^N225",
		"nikkei":   "^N225",
		"topix":    "1306.T", // TOPIX ETF proxy (Yahoo lacks a clean TOPIX index)
		"ドル円":      "USDJPY=X",
		"usd/jpy":  "USDJPY=X",
		"7203":     "7203.T", // bare 4-digit code → TSE
		"7203.T":   "7203.T",
		"^n225":    "^N225", // pass-through, uppercased
		"aapl":     "AAPL",
		"usdjpy=x": "USDJPY=X",
	}
	for in, want := range cases {
		if got := resolveSymbol(in); got != want {
			t.Errorf("resolveSymbol(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitSymbols(t *testing.T) {
	got := splitSymbols(" ^N225 , USDJPY=X ,, 7203.T ")
	want := []string{"^N225", "USDJPY=X", "7203.T"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSignedFormatting(t *testing.T) {
	if signed(12.5) != "+12.50" || signed(-3.0) != "-3.00" {
		t.Errorf("signed: got %q / %q", signed(12.5), signed(-3.0))
	}
	if signedPct(2.789) != "+2.79%" || signedPct(-1.2) != "-1.20%" {
		t.Errorf("signedPct: got %q / %q", signedPct(2.789), signedPct(-1.2))
	}
}

// TestFeedParsing verifies both RSS 2.0 (items under <channel>) and RSS 1.0/RDF
// (items at the root) are parsed by the shared feedDoc struct.
func TestFeedParsing(t *testing.T) {
	rss20 := `<?xml version="1.0"?><rss version="2.0"><channel>
		<title>x</title>
		<item><title>日経平均が反発</title><link>https://ex.com/a</link><pubDate>Mon, 02 Jan 2006 15:04:05 +0900</pubDate></item>
	</channel></rss>`
	rdf := `<?xml version="1.0"?><rdf:RDF xmlns="http://purl.org/rss/1.0/" xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns:dc="http://purl.org/dc/elements/1.1/">
		<channel><title>y</title></channel>
		<item><title>半導体株が上昇</title><link>https://ex.com/b</link><dc:date>2006-01-02T15:04:05+09:00</dc:date></item>
	</rdf:RDF>`

	for name, xmlStr := range map[string]string{"rss2.0": rss20, "rdf": rdf} {
		items, err := parseFeedBytes([]byte(xmlStr))
		if err != nil {
			t.Fatalf("%s: parse error: %v", name, err)
		}
		if len(items) != 1 {
			t.Fatalf("%s: got %d items, want 1", name, len(items))
		}
		if items[0].Title == "" || items[0].Link == "" {
			t.Errorf("%s: missing title/link: %+v", name, items[0])
		}
		if items[0].when().IsZero() {
			t.Errorf("%s: date not parsed: %+v", name, items[0])
		}
	}
}

// TestMarketLive hits the real Yahoo Finance + RSS endpoints. It is gated behind
// KLEIN_LIVE_MARKET=1 so normal/CI runs stay offline and deterministic.
func TestMarketLive(t *testing.T) {
	if os.Getenv("KLEIN_LIVE_MARKET") != "1" {
		t.Skip("set KLEIN_LIVE_MARKET=1 to run live market data tests")
	}
	m := NewMarketToolManager()
	ctx := context.Background()

	q, _ := m.CallTool(ctx, "MarketQuote", message.ToolArgumentValues{"symbols": "日経平均, ドル円"})
	if q.Error != "" || !strings.Contains(q.Text, "^N225") {
		t.Errorf("MarketQuote: err=%q text=%q", q.Error, q.Text)
	}

	h, _ := m.CallTool(ctx, "MarketHistory", message.ToolArgumentValues{"symbol": "^N225", "range": "5d"})
	if h.Error != "" || !strings.Contains(h.Text, "Daily closes") {
		t.Errorf("MarketHistory: err=%q text=%q", h.Error, h.Text)
	}

	n, _ := m.CallTool(ctx, "MarketNews", message.ToolArgumentValues{"limit": float64(5)})
	if n.Error != "" || strings.TrimSpace(n.Text) == "" {
		t.Errorf("MarketNews: err=%q text=%q", n.Error, n.Text)
	}
}
