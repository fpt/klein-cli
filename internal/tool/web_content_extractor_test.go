package tool

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

func docFromHTML(t *testing.T, raw string) *goquery.Document {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("failed to parse HTML: %v", err)
	}
	return doc
}

func TestExtractDenseBlocks_ArticlePage(t *testing.T) {
	page := `<html><body>
		<nav><a href="/">Home</a><a href="/about">About</a></nav>
		<article>
			<h1>Dense Text Extraction</h1>
			<p>` + strings.Repeat("This is a meaningful paragraph with real content. ", 5) + `</p>
			<p>` + strings.Repeat("Another paragraph that contains useful information. ", 5) + `</p>
		</article>
		<footer>Copyright 2025</footer>
	</body></html>`

	doc := docFromHTML(t, page)
	blocks := extractDenseBlocks(doc)

	if len(blocks) == 0 {
		t.Fatal("expected at least one block, got 0")
	}

	// Verify blocks come from article, not nav/footer.
	for _, b := range blocks {
		if strings.Contains(b.DOMPath, "nav") {
			t.Errorf("unexpected nav block: %s", b.DOMPath)
		}
		if strings.Contains(b.DOMPath, "footer") {
			t.Errorf("unexpected footer block: %s", b.DOMPath)
		}
	}
}

func TestExtractDenseBlocks_NavigationHeavy(t *testing.T) {
	// Page dominated by navigation links.
	var navLinks strings.Builder
	for i := 0; i < 50; i++ {
		navLinks.WriteString(`<li><a href="/page">Link text</a></li>`)
	}
	page := `<html><body>
		<div class="sidebar"><ul>` + navLinks.String() + `</ul></div>
		<div id="content">
			<p>` + strings.Repeat("Actual content paragraph here. ", 5) + `</p>
		</div>
	</body></html>`

	doc := docFromHTML(t, page)
	blocks := extractDenseBlocks(doc)

	// Should find the content paragraph, not the sidebar.
	for _, b := range blocks {
		if strings.Contains(b.DOMPath, "sidebar") {
			t.Errorf("unexpected sidebar block: %s", b.DOMPath)
		}
	}

	found := false
	for _, b := range blocks {
		if strings.Contains(b.Text, "Actual content paragraph") {
			found = true
		}
	}
	if !found {
		t.Error("expected to find the content paragraph block")
	}
}

func TestExtractDenseBlocks_MinimalContent(t *testing.T) {
	page := `<html><body><p>Short.</p></body></html>`
	doc := docFromHTML(t, page)
	blocks := extractDenseBlocks(doc)

	// "Short." is < minBlockTextLength, so no blocks expected.
	if len(blocks) != 0 {
		t.Errorf("expected 0 blocks for minimal content, got %d", len(blocks))
	}
}

func TestExtractDenseBlocks_LeafPromotion(t *testing.T) {
	// A div wrapping a single dense p - only the p should be returned.
	content := strings.Repeat("Leaf promotion test content here. ", 5)
	page := `<html><body>
		<div class="wrapper"><p>` + content + `</p></div>
	</body></html>`

	doc := docFromHTML(t, page)
	blocks := extractDenseBlocks(doc)

	// Should have blocks, but the wrapper div should be promoted away.
	pCount := 0
	divWrapperCount := 0
	for _, b := range blocks {
		if strings.HasSuffix(b.DOMPath, "p") {
			pCount++
		}
		if strings.HasSuffix(b.DOMPath, "div.wrapper") {
			divWrapperCount++
		}
	}
	if pCount == 0 {
		t.Error("expected at least one p block after leaf promotion")
	}
	if divWrapperCount > 0 {
		t.Error("expected wrapper div to be skipped via leaf promotion")
	}
}

func TestBuildDOMPath(t *testing.T) {
	page := `<html><body><div id="main"><section class="content post"><p>Hello</p></section></div></body></html>`
	doc := docFromHTML(t, page)

	p := doc.Find("p").Nodes[0]
	path := buildDOMPath(p)

	// Should include body, div#main, section.content, p.
	if !strings.Contains(path, "div#main") {
		t.Errorf("expected div#main in path, got: %s", path)
	}
	if !strings.Contains(path, "section.content") {
		t.Errorf("expected section.content in path, got: %s", path)
	}
	if !strings.HasSuffix(path, "p") {
		t.Errorf("expected path to end with p, got: %s", path)
	}
	if !strings.HasPrefix(path, "body") {
		t.Errorf("expected path to start with body, got: %s", path)
	}
}

func TestBuildDOMPath_PlainTags(t *testing.T) {
	page := `<html><body><div><span>Text</span></div></body></html>`
	doc := docFromHTML(t, page)

	span := doc.Find("span").Nodes[0]
	path := buildDOMPath(span)

	if path != "body > div > span" {
		t.Errorf("expected 'body > div > span', got: %s", path)
	}
}

func TestIsNonContentElement(t *testing.T) {
	tests := []struct {
		html     string
		selector string
		want     bool
	}{
		{`<script>var x=1;</script>`, "script", true},
		{`<nav><a href="/">Home</a></nav>`, "nav", true},
		{`<div class="sidebar-left">Content</div>`, "div", true},
		{`<div class="Advertisement-banner">Ad</div>`, "div", true},
		{`<p>Regular paragraph</p>`, "p", false},
		{`<div class="content">Real content</div>`, "div", false},
		{`<aside>Sidebar</aside>`, "aside", true},
	}

	for _, tt := range tests {
		doc := docFromHTML(t, `<html><body>`+tt.html+`</body></html>`)
		sel := doc.Find(tt.selector).First()
		got := isNonContentElement(sel)
		if got != tt.want {
			t.Errorf("isNonContentElement(%s) = %v, want %v", tt.html, got, tt.want)
		}
	}
}

func TestFormatBlockSummary(t *testing.T) {
	blocks := []textBlock{
		{Index: 1, DOMPath: "body > article > h1", Text: "Title Here"},
		{Index: 2, DOMPath: "body > article > p", Text: strings.Repeat("A", 200)},
	}

	result := formatBlockSummary("Test Page", "https://example.com", blocks, blockPreviewLength)

	if !strings.Contains(result, "# Page: Test Page") {
		t.Error("expected page title in output")
	}
	if !strings.Contains(result, "URL: https://example.com") {
		t.Error("expected URL in output")
	}
	if !strings.Contains(result, "Found 2 content blocks") {
		t.Error("expected block count")
	}
	if !strings.Contains(result, "[1] body > article > h1") {
		t.Error("expected first block listing")
	}
	if !strings.Contains(result, "...") {
		t.Error("expected truncation indicator for long block")
	}
	if !strings.Contains(result, "WebFetchBlock") {
		t.Error("expected WebFetchBlock usage hint")
	}
}

func TestFormatBlockSummary_Empty(t *testing.T) {
	result := formatBlockSummary("Empty", "https://example.com", nil, blockPreviewLength)
	if !strings.Contains(result, "No substantial content blocks found") {
		t.Error("expected empty message")
	}
}

func TestFormatBlockContent(t *testing.T) {
	blocks := []textBlock{
		{Index: 1, DOMPath: "body > p", Text: "First block text"},
		{Index: 2, DOMPath: "body > div > p", Text: "Second block text"},
		{Index: 3, DOMPath: "body > section > p", Text: "Third block text"},
	}

	result := formatBlockContent(blocks, []int{1, 3})

	if !strings.Contains(result, "First block text") {
		t.Error("expected first block content")
	}
	if strings.Contains(result, "Second block text") {
		t.Error("should not contain second block (not requested)")
	}
	if !strings.Contains(result, "Third block text") {
		t.Error("expected third block content")
	}
}

func TestFormatBlockContent_InvalidIndex(t *testing.T) {
	blocks := []textBlock{
		{Index: 1, DOMPath: "body > p", Text: "Only block"},
	}

	result := formatBlockContent(blocks, []int{5})
	if !strings.Contains(result, "Block not found") {
		t.Error("expected not-found message for out-of-range index")
	}
}

func TestCollapseWhitespace(t *testing.T) {
	input := "hello\n\n  world\t\tfoo"
	got := collapseWhitespace(input)
	if got != "hello world foo" {
		t.Errorf("collapseWhitespace = %q, want %q", got, "hello world foo")
	}
}

func TestGetNodeAttr(t *testing.T) {
	doc := docFromHTML(t, `<html><body><div id="test" class="foo bar">X</div></body></html>`)
	node := doc.Find("div").Nodes[0]

	if got := getNodeAttr(node, "id"); got != "test" {
		t.Errorf("getNodeAttr(id) = %q, want %q", got, "test")
	}
	if got := getNodeAttr(node, "class"); got != "foo bar" {
		t.Errorf("getNodeAttr(class) = %q, want %q", got, "foo bar")
	}
	if got := getNodeAttr(node, "missing"); got != "" {
		t.Errorf("getNodeAttr(missing) = %q, want empty", got)
	}
}

func TestIsDenseBlock(t *testing.T) {
	tests := []struct {
		name string
		m    nodeMetrics
		want bool
	}{
		{"short text", nodeMetrics{textLength: 20, tagCount: 1}, false},
		{"dense paragraph", nodeMetrics{textLength: 200, linkTextLength: 10, tagCount: 2}, true},
		{"link heavy", nodeMetrics{textLength: 200, linkTextLength: 150, tagCount: 2}, false},
		{"many tags low density", nodeMetrics{textLength: 50, linkTextLength: 0, tagCount: 10}, false},
		{"zero tags", nodeMetrics{textLength: 100, linkTextLength: 0, tagCount: 0}, true},
	}

	for _, tt := range tests {
		got := isDenseBlock(tt.m)
		if got != tt.want {
			t.Errorf("isDenseBlock(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// Verify that html import is used (for html.ElementNode reference in tests).
var _ = html.ElementNode
