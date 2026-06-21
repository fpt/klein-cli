package tool

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// MarketToolManager provides live financial-market tools (quotes, history, news),
// with first-class support for Japanese markets. Quotes/history come from the
// public Yahoo Finance chart endpoint (no API key); news comes from Japanese
// finance RSS feeds.
type MarketToolManager struct {
	tools     map[message.ToolName]message.Tool
	client    *http.Client
	newsFeeds []string
}

// defaultNewsFeeds are reliable Japanese business/markets RSS feeds (verified).
var defaultNewsFeeds = []string{
	"https://news.yahoo.co.jp/rss/topics/business.xml",   // Yahoo!ニュース 経済 (RSS 2.0)
	"https://assets.wor.jp/rss/rdf/reuters/business.rdf", // Reuters Japan business (RSS 1.0/RDF)
}

// symbolAliases maps friendly Japanese/English names to Yahoo Finance symbols.
// Anything not listed is passed through (4-digit codes become TSE "<code>.T").
var symbolAliases = map[string]string{
	"nikkei": "^N225", "nikkei225": "^N225", "n225": "^N225",
	"日経": "^N225", "日経平均": "^N225", "日経平均株価": "^N225",
	// Yahoo has no clean real-time TOPIX index (^TPX is stale/wrong), so map to
	// the NEXT FUNDS TOPIX ETF (1306.T) as a proxy — its % move tracks TOPIX.
	"topix": "1306.T", "トピックス": "1306.T",
	"usdjpy": "USDJPY=X", "usd/jpy": "USDJPY=X", "ドル円": "USDJPY=X", "dollar yen": "USDJPY=X",
	"eurjpy": "EURJPY=X", "ユーロ円": "EURJPY=X",
	"sp500": "^GSPC", "s&p500": "^GSPC", "s&p 500": "^GSPC",
	"dow": "^DJI", "ダウ": "^DJI", "ダウ平均": "^DJI",
	"nasdaq": "^IXIC", "ナスダック": "^IXIC",
}

// validRanges limits the history range argument to values Yahoo accepts.
var validRanges = map[string]bool{
	"1d": true, "5d": true, "1mo": true, "3mo": true, "6mo": true, "1y": true, "ytd": true, "max": true,
}

// NewMarketToolManager creates a market data tool manager.
func NewMarketToolManager() *MarketToolManager {
	m := &MarketToolManager{
		tools:     make(map[message.ToolName]message.Tool),
		client:    &http.Client{Timeout: 25 * time.Second},
		newsFeeds: defaultNewsFeeds,
	}
	m.registerTools()
	return m
}

func (m *MarketToolManager) registerTools() {
	m.RegisterTool("MarketQuote",
		"Get the latest market quote (price, day change, day high/low) for one or more symbols. "+
			"Japanese names work: 日経平均/Nikkei→^N225, TOPIX→1306.T (TOPIX ETF proxy), ドル円→USDJPY=X. "+
			"Individual Tokyo-listed stocks use the 4-digit code + .T (e.g. 7203.T for Toyota); a bare 4-digit code is treated as TSE.",
		[]message.ToolArgument{
			{Name: "symbols", Description: "Comma-separated symbols or names (e.g. '^N225, USDJPY=X, 7203.T' or '日経平均, ドル円')", Required: true, Type: "string"},
		},
		m.handleQuote)

	m.RegisterTool("MarketHistory",
		"Get daily OHLC history and the period change for one symbol — use this for 'this week' / 'this month' moves (range=5d ≈ one trading week).",
		[]message.ToolArgument{
			{Name: "symbol", Description: "A single symbol or name (e.g. '^N225', '日経平均', '7203.T')", Required: true, Type: "string"},
			{Name: "range", Description: "Time range: 1d, 5d, 1mo, 3mo, 6mo, 1y, ytd, max (default 5d)", Required: false, Type: "string"},
		},
		m.handleHistory)

	m.RegisterTool("MarketNews",
		"Get recent Japanese market/business news headlines (with links) from finance RSS feeds. "+
			"Optionally filter by a keyword. Follow up with WebFetch on a headline's link to read the article.",
		[]message.ToolArgument{
			{Name: "query", Description: "Optional keyword to filter headlines (e.g. '日経', '半導体', 'トヨタ')", Required: false, Type: "string"},
			{Name: "limit", Description: "Max headlines to return (default 12, max 25)", Required: false, Type: "number"},
		},
		m.handleNews)
}

// --- domain.ToolManager ---

func (m *MarketToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	t, ok := m.tools[name]
	return t, ok
}

func (m *MarketToolManager) GetTools() map[message.ToolName]message.Tool { return m.tools }

func (m *MarketToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	t, ok := m.tools[name]
	if !ok {
		return message.NewToolResultError(fmt.Sprintf("tool '%s' not found", name)), nil
	}
	return t.Handler()(ctx, args)
}

func (m *MarketToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	m.tools[name] = &marketTool{name: name, description: description, arguments: arguments, handler: handler}
}

// --- handlers ---

func (m *MarketToolManager) handleQuote(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	raw, _ := args["symbols"].(string)
	syms := splitSymbols(raw)
	if len(syms) == 0 {
		return message.NewToolResultError("symbols parameter is required (e.g. '^N225, USDJPY=X')"), nil
	}

	var b strings.Builder
	for _, in := range syms {
		sym := resolveSymbol(in)
		chart, err := m.fetchChart(ctx, sym, "1d", "1d")
		if err != nil {
			fmt.Fprintf(&b, "- %s: error: %v\n", sym, err)
			continue
		}
		meta := chart.Chart.Result[0].Meta
		prev := meta.ChartPreviousClose
		if prev == 0 {
			prev = meta.PreviousClose
		}
		name := meta.ShortName
		if name == "" {
			name = meta.LongName
		}
		change, pct := meta.RegularMarketPrice-prev, 0.0
		if prev != 0 {
			pct = change / prev * 100
		}
		fmt.Fprintf(&b, "%s (%s): %.2f %s  %s (%s)\n",
			name, meta.Symbol, meta.RegularMarketPrice, meta.Currency,
			signed(change), signedPct(pct))
		if meta.RegularMarketDayHigh != 0 || meta.RegularMarketDayLow != 0 {
			fmt.Fprintf(&b, "   day H/L: %.2f / %.2f  prev close: %.2f", meta.RegularMarketDayHigh, meta.RegularMarketDayLow, prev)
			if meta.RegularMarketTime != 0 {
				fmt.Fprintf(&b, "  as of %s", time.Unix(meta.RegularMarketTime, 0).UTC().Format("2006-01-02 15:04 UTC"))
			}
			b.WriteString("\n")
		}
	}
	return message.NewToolResultText(strings.TrimRight(b.String(), "\n")), nil
}

func (m *MarketToolManager) handleHistory(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	in, _ := args["symbol"].(string)
	if strings.TrimSpace(in) == "" {
		return message.NewToolResultError("symbol parameter is required"), nil
	}
	rng := strings.ToLower(strings.TrimSpace(stringArg(args, "range")))
	if rng == "" {
		rng = "5d"
	}
	if !validRanges[rng] {
		return message.NewToolResultError(fmt.Sprintf("invalid range %q (use 1d, 5d, 1mo, 3mo, 6mo, 1y, ytd, max)", rng)), nil
	}

	sym := resolveSymbol(in)
	chart, err := m.fetchChart(ctx, sym, rng, "1d")
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("market history failed for %s: %v", sym, err)), nil
	}
	res := chart.Chart.Result[0]
	if len(res.Indicators.Quote) == 0 {
		return message.NewToolResultError(fmt.Sprintf("no price series returned for %s", sym)), nil
	}
	closes := res.Indicators.Quote[0].Close
	ts := res.Timestamp

	// Collect valid (non-null) daily closes.
	type row struct {
		t time.Time
		c float64
	}
	var rows []row
	for i := range closes {
		if closes[i] == nil || i >= len(ts) {
			continue
		}
		rows = append(rows, row{t: time.Unix(ts[i], 0).UTC(), c: *closes[i]})
	}
	if len(rows) == 0 {
		return message.NewToolResultText(fmt.Sprintf("No daily closes returned for %s over %s.", sym, rng)), nil
	}

	first, last := rows[0], rows[len(rows)-1]
	change := last.c - first.c
	pct := 0.0
	if first.c != 0 {
		pct = change / first.c * 100
	}
	hi, lo := rows[0].c, rows[0].c
	for _, r := range rows {
		if r.c > hi {
			hi = r.c
		}
		if r.c < lo {
			lo = r.c
		}
	}

	meta := res.Meta
	name := meta.ShortName
	if name == "" {
		name = meta.LongName
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s) — %s\n", name, meta.Symbol, rng)
	fmt.Fprintf(&b, "Period: %s → %s  close %.2f → %.2f  %s (%s) %s\n",
		first.t.Format("2006-01-02"), last.t.Format("2006-01-02"),
		first.c, last.c, signed(change), signedPct(pct), meta.Currency)
	fmt.Fprintf(&b, "Period high/low: %.2f / %.2f\n\nDaily closes:\n", hi, lo)
	for _, r := range rows {
		fmt.Fprintf(&b, "  %s  %.2f\n", r.t.Format("2006-01-02"), r.c)
	}
	return message.NewToolResultText(strings.TrimRight(b.String(), "\n")), nil
}

func (m *MarketToolManager) handleNews(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	query := strings.TrimSpace(stringArg(args, "query"))
	limit := 12
	if v, ok := numberArg(args, "limit"); ok && v > 0 {
		limit = v
	}
	if limit > 25 {
		limit = 25
	}

	var items []feedItem
	var fetchErrs []string
	for _, feed := range m.newsFeeds {
		fi, err := m.fetchFeed(ctx, feed)
		if err != nil {
			fetchErrs = append(fetchErrs, fmt.Sprintf("%s: %v", feed, err))
			continue
		}
		items = append(items, fi...)
	}

	// Optional keyword filter on the title.
	if query != "" {
		q := strings.ToLower(query)
		filtered := items[:0]
		for _, it := range items {
			if strings.Contains(strings.ToLower(it.Title), q) {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}

	// Most-recent first (best-effort date parse; undated items sink to the end).
	sort.SliceStable(items, func(i, j int) bool { return items[i].when().After(items[j].when()) })

	if len(items) == 0 {
		msg := "No matching headlines found."
		if query != "" {
			msg = fmt.Sprintf("No headlines matched %q.", query)
		}
		if len(fetchErrs) > 0 {
			msg += "\n(feed errors: " + strings.Join(fetchErrs, "; ") + ")"
		}
		return message.NewToolResultText(msg), nil
	}

	if len(items) > limit {
		items = items[:limit]
	}
	var b strings.Builder
	b.WriteString("Recent Japanese market/business headlines:\n\n")
	for i, it := range items {
		when := ""
		if t := it.when(); !t.IsZero() {
			when = " (" + t.Format("2006-01-02 15:04") + ")"
		}
		fmt.Fprintf(&b, "%d. %s%s\n   %s\n", i+1, strings.TrimSpace(it.Title), when, strings.TrimSpace(it.Link))
	}
	b.WriteString("\nUse WebFetch on a link to read the full article.")
	return message.NewToolResultText(b.String()), nil
}

// --- Yahoo Finance ---

type yahooChart struct {
	Chart struct {
		Result []struct {
			Meta struct {
				Currency             string  `json:"currency"`
				Symbol               string  `json:"symbol"`
				ShortName            string  `json:"shortName"`
				LongName             string  `json:"longName"`
				RegularMarketPrice   float64 `json:"regularMarketPrice"`
				ChartPreviousClose   float64 `json:"chartPreviousClose"`
				PreviousClose        float64 `json:"previousClose"`
				RegularMarketDayHigh float64 `json:"regularMarketDayHigh"`
				RegularMarketDayLow  float64 `json:"regularMarketDayLow"`
				RegularMarketTime    int64   `json:"regularMarketTime"`
			} `json:"meta"`
			Timestamp  []int64 `json:"timestamp"`
			Indicators struct {
				Quote []struct {
					Close []*float64 `json:"close"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
		Error any `json:"error"`
	} `json:"chart"`
}

func (m *MarketToolManager) fetchChart(ctx context.Context, symbol, rng, interval string) (*yahooChart, error) {
	endpoint := "https://query1.finance.yahoo.com/v8/finance/chart/" + url.PathEscape(symbol)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("range", rng)
	q.Set("interval", interval)
	req.URL.RawQuery = q.Encode()
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d (unknown symbol %q?)", resp.StatusCode, symbol)
	}
	var chart yahooChart
	if err := json.Unmarshal(body, &chart); err != nil {
		return nil, fmt.Errorf("failed to parse market data: %w", err)
	}
	if len(chart.Chart.Result) == 0 {
		return nil, fmt.Errorf("no data for symbol %q", symbol)
	}
	return &chart, nil
}

// --- RSS / RDF news ---

type feedItem struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"`
	PubDate string `xml:"pubDate"`                               // RSS 2.0
	DCDate  string `xml:"http://purl.org/dc/elements/1.1/ date"` // RSS 1.0 (dc:date)
}

// when parses the item's timestamp best-effort; returns zero time if unknown.
func (it feedItem) when() time.Time {
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC3339, "2006-01-02T15:04:05-07:00"} {
		if it.PubDate != "" {
			if t, err := time.Parse(layout, it.PubDate); err == nil {
				return t
			}
		}
		if it.DCDate != "" {
			if t, err := time.Parse(layout, it.DCDate); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

type feedDoc struct {
	// RSS 2.0 nests items under <channel>; RSS 1.0/RDF lists them at the root.
	ChannelItems []feedItem `xml:"channel>item"`
	RootItems    []feedItem `xml:"item"`
}

func (m *MarketToolManager) fetchFeed(ctx context.Context, feedURL string) ([]feedItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, err
	}
	return parseFeedBytes(body)
}

// parseFeedBytes parses RSS 2.0 (items under <channel>) and RSS 1.0/RDF (items
// at the root) into a flat item list.
func parseFeedBytes(body []byte) ([]feedItem, error) {
	var doc feedDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("failed to parse feed: %w", err)
	}
	return append(doc.ChannelItems, doc.RootItems...), nil
}

// --- helpers ---

func splitSymbols(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// resolveSymbol maps a friendly name to a Yahoo Finance symbol. A bare 4-digit
// code is treated as a Tokyo Stock Exchange ticker ("7203" → "7203.T").
func resolveSymbol(s string) string {
	key := strings.ToLower(strings.TrimSpace(s))
	if v, ok := symbolAliases[key]; ok {
		return v
	}
	t := strings.TrimSpace(s)
	if len(t) == 4 && isAllDigits(t) {
		return t + ".T"
	}
	return strings.ToUpper(t)
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func signed(v float64) string {
	if v >= 0 {
		return fmt.Sprintf("+%.2f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

func signedPct(v float64) string {
	if v >= 0 {
		return fmt.Sprintf("+%.2f%%", v)
	}
	return fmt.Sprintf("%.2f%%", v)
}

func numberArg(args message.ToolArgumentValues, key string) (int, bool) {
	switch v := args[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

// marketTool implements message.Tool.
type marketTool struct {
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (t *marketTool) RawName() message.ToolName            { return t.name }
func (t *marketTool) Name() message.ToolName               { return t.name }
func (t *marketTool) Description() message.ToolDescription { return t.description }
func (t *marketTool) Arguments() []message.ToolArgument    { return t.arguments }
func (t *marketTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}

var _ domain.ToolManager = (*MarketToolManager)(nil)
