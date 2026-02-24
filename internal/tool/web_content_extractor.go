package tool

import (
	"fmt"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

// Tuning constants for dense text block extraction.
const (
	minBlockTextLength = 50
	maxLinkDensity     = 0.5
	minTextDensity     = 10.0
	blockPreviewLength = 150
	maxBlocks          = 50
	cacheMaxEntries    = 10
	cacheTTL           = 5 * time.Minute
)

// textBlock represents a dense text block extracted from a webpage.
type textBlock struct {
	Index   int
	DOMPath string
	Text    string // full text content
}

// cachedPage stores extraction results for a URL.
type cachedPage struct {
	blocks    []textBlock
	title     string
	fetchedAt time.Time
}

// nodeMetrics holds computed metrics for a DOM node.
type nodeMetrics struct {
	textLength     int
	linkTextLength int
	tagCount       int
}

// nonContentTags are HTML tags that never contain useful content.
var nonContentTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "svg": true,
	"iframe": true, "object": true, "embed": true,
	"nav": true, "header": true, "footer": true, "aside": true,
}

// nonContentClasses are CSS class substrings indicating non-content areas.
var nonContentClasses = []string{
	"sidebar", "menu", "nav", "ad", "advertisement",
	"cookie", "popup", "modal", "banner", "widget",
}

// isNonContentElement returns true if the selection should be skipped.
func isNonContentElement(s *goquery.Selection) bool {
	tag := goquery.NodeName(s)
	if nonContentTags[tag] {
		return true
	}
	cls, _ := s.Attr("class")
	if cls != "" {
		lower := strings.ToLower(cls)
		for _, nc := range nonContentClasses {
			if strings.Contains(lower, nc) {
				return true
			}
		}
	}
	return false
}

// computeNodeMetrics computes text density metrics for a selection.
func computeNodeMetrics(s *goquery.Selection) nodeMetrics {
	text := strings.TrimSpace(s.Text())
	textLen := len(text)

	var linkTextLen int
	s.Find("a").Each(func(_ int, a *goquery.Selection) {
		linkTextLen += len(strings.TrimSpace(a.Text()))
	})

	// Count direct child element nodes.
	tagCount := 0
	s.Children().Each(func(_ int, _ *goquery.Selection) {
		tagCount++
	})

	return nodeMetrics{
		textLength:     textLen,
		linkTextLength: linkTextLen,
		tagCount:       tagCount,
	}
}

// isDenseBlock returns true if the node metrics qualify as a dense text block.
func isDenseBlock(m nodeMetrics) bool {
	if m.textLength < minBlockTextLength {
		return false
	}
	if m.textLength > 0 {
		linkDensity := float64(m.linkTextLength) / float64(m.textLength)
		if linkDensity >= maxLinkDensity {
			return false
		}
	}
	tc := m.tagCount
	if tc < 1 {
		tc = 1
	}
	textDensity := float64(m.textLength) / float64(tc)
	return textDensity >= minTextDensity
}

// extractDenseBlocks walks the DOM and returns qualifying dense text blocks.
func extractDenseBlocks(doc *goquery.Document) []textBlock {
	type candidate struct {
		node    *html.Node
		text    string
		textLen int
	}

	var candidates []candidate

	// Recursive walk to find leaf-level dense blocks.
	var walk func(s *goquery.Selection)
	walk = func(s *goquery.Selection) {
		s.Children().Each(func(_ int, child *goquery.Selection) {
			if isNonContentElement(child) {
				return
			}
			// Recurse first so children are processed before parent.
			walk(child)
		})

		// Skip non-element or non-content nodes.
		if len(s.Nodes) == 0 {
			return
		}
		node := s.Nodes[0]
		if node.Type != html.ElementNode {
			return
		}
		if isNonContentElement(s) {
			return
		}

		m := computeNodeMetrics(s)
		if !isDenseBlock(m) {
			return
		}

		text := strings.TrimSpace(s.Text())

		// Leaf promotion: if this node has a single child that is already
		// a candidate covering >80% of the parent's text, skip the parent.
		denseChildCount := 0
		var denseChildTextLen int
		for i := len(candidates) - 1; i >= 0; i-- {
			c := candidates[i]
			// Check if c is a direct child of node.
			if c.node.Parent == node {
				denseChildCount++
				denseChildTextLen += c.textLen
			}
		}
		if denseChildCount == 1 && m.textLength > 0 {
			coverage := float64(denseChildTextLen) / float64(m.textLength)
			if coverage > 0.8 {
				return // child already covers this content
			}
		}

		candidates = append(candidates, candidate{
			node:    node,
			text:    text,
			textLen: m.textLength,
		})
	}

	body := doc.Find("body")
	if body.Length() == 0 {
		// Fallback: use the whole document.
		body = doc.Selection
	}
	walk(body)

	// Cap at maxBlocks.
	if len(candidates) > maxBlocks {
		candidates = candidates[:maxBlocks]
	}

	blocks := make([]textBlock, len(candidates))
	for i, c := range candidates {
		blocks[i] = textBlock{
			Index:   i + 1,
			DOMPath: buildDOMPath(c.node),
			Text:    c.text,
		}
	}
	return blocks
}

// buildDOMPath walks from node to <body> and builds a CSS-like path.
func buildDOMPath(node *html.Node) string {
	var parts []string
	for n := node; n != nil && n.Type == html.ElementNode; n = n.Parent {
		tag := n.Data
		if tag == "html" {
			break
		}
		segment := tag
		if id := getNodeAttr(n, "id"); id != "" {
			segment += "#" + id
		} else if cls := getNodeAttr(n, "class"); cls != "" {
			first := strings.Fields(cls)[0]
			segment += "." + first
		}
		parts = append([]string{segment}, parts...)
	}
	return strings.Join(parts, " > ")
}

// getNodeAttr returns the value of an attribute on a raw html.Node.
func getNodeAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// formatBlockSummary produces the numbered block summary output.
func formatBlockSummary(title, urlStr string, blocks []textBlock, previewLen int) string {
	var b strings.Builder
	if title != "" {
		b.WriteString(fmt.Sprintf("# Page: %s\n", title))
	}
	b.WriteString(fmt.Sprintf("URL: %s\n\n", urlStr))

	if len(blocks) == 0 {
		b.WriteString("No substantial content blocks found.\n")
		return b.String()
	}

	b.WriteString(fmt.Sprintf("Found %d content blocks:\n\n", len(blocks)))
	for _, block := range blocks {
		preview := block.Text
		if len(preview) > previewLen {
			preview = preview[:previewLen] + "..."
		}
		// Collapse whitespace in preview for readability.
		preview = collapseWhitespace(preview)
		b.WriteString(fmt.Sprintf("[%d] %s\n    \"%s\"\n\n", block.Index, block.DOMPath, preview))
	}

	b.WriteString("Use web_fetch_block with url and block_indices to retrieve full content of specific blocks.\n")
	return b.String()
}

// formatBlockContent returns the full text of the requested block indices.
func formatBlockContent(blocks []textBlock, indices []int) string {
	var b strings.Builder
	for _, idx := range indices {
		// Indices are 1-based.
		if idx < 1 || idx > len(blocks) {
			b.WriteString(fmt.Sprintf("[%d] Block not found (valid range: 1-%d)\n\n", idx, len(blocks)))
			continue
		}
		block := blocks[idx-1]
		b.WriteString(fmt.Sprintf("[%d] %s\n\n%s\n\n---\n\n", block.Index, block.DOMPath, block.Text))
	}
	return b.String()
}

// collapseWhitespace replaces runs of whitespace (including newlines) with a single space.
func collapseWhitespace(s string) string {
	var b strings.Builder
	inSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !inSpace {
				b.WriteRune(' ')
				inSpace = true
			}
		} else {
			b.WriteRune(r)
			inSpace = false
		}
	}
	return b.String()
}
