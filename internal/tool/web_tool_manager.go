package tool

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// WebToolManager provides web-related tools for search, navigation, and content fetching
type WebToolManager struct {
	tools      map[message.ToolName]message.Tool
	blockCache map[string]*cachedPage
	cacheMu    sync.Mutex
}

// NewWebToolManager creates a new web tool manager with all web-related tools
func NewWebToolManager() domain.ToolManager {
	m := &WebToolManager{
		tools:      make(map[message.ToolName]message.Tool),
		blockCache: make(map[string]*cachedPage),
	}

	// Register all web-related tools
	m.registerWebTools()
	return m
}

func (m *WebToolManager) registerWebTools() {
	// WebFetch — default mode returns dense text block summaries.
	m.RegisterTool("WebFetch",
		"Fetch a webpage and extract content. Default mode returns a summary of dense text blocks with DOM paths; use WebFetchBlock to retrieve full content of specific blocks. Set mode='full' for complete markdown.",
		[]message.ToolArgument{
			{Name: "url", Description: "URL of the webpage to fetch", Required: true, Type: "string"},
			{Name: "mode", Description: "Extraction mode: 'blocks' (default) for block summaries, 'full' for complete markdown", Required: false, Type: "string"},
		},
		m.handleFetchWeb)

	// WebFetchBlock — retrieve full content of specific blocks from cache.
	m.RegisterTool("WebFetchBlock",
		"Retrieve full content of specific text blocks from a previously fetched webpage. Use after WebFetch to get detailed content of interesting blocks.",
		[]message.ToolArgument{
			{Name: "url", Description: "URL of the webpage (should match a previous WebFetch call)", Required: true, Type: "string"},
			{Name: "block_indices", Description: "Comma-separated block indices to retrieve (e.g., '1,3,5')", Required: true, Type: "string"},
		},
		m.handleFetchWebBlock)

	// WebSearch (stub): declare interface compatibility; return informative message
	m.RegisterTool("WebSearch", "Search the web (stub). Not implemented in this build. Provide URLs or use WebFetch with a concrete link.",
		[]message.ToolArgument{
			{Name: "query", Description: "Search query", Required: true, Type: "string"},
			{Name: "allowed_domains", Description: "Only include results from these domains", Required: false, Type: "array"},
			{Name: "blocked_domains", Description: "Exclude results from these domains", Required: false, Type: "array"},
		},
		m.handleWebSearchStub)
}

// Implement domain.ToolManager interface
func (m *WebToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	tool, exists := m.tools[name]
	return tool, exists
}

func (m *WebToolManager) GetTools() map[message.ToolName]message.Tool {
	return m.tools
}

func (m *WebToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	tool, exists := m.tools[name]
	if !exists {
		return message.NewToolResultError(fmt.Sprintf("tool '%s' not found", name)), nil
	}

	return tool.Handler()(ctx, args)
}

func (m *WebToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	m.tools[name] = &webTool{
		name:        name,
		description: description,
		arguments:   arguments,
		handler:     handler,
	}
}

// fetchAndParse fetches a URL and returns the parsed goquery document and parsed URL.
func (m *WebToolManager) fetchAndParse(ctx context.Context, urlStr string) (*goquery.Document, *url.URL, error) {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid URL format: %v", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, nil, fmt.Errorf("invalid URL scheme: must be http or https")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Compatible Web Fetcher Bot)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch webpage: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("HTTP error %d: %s", resp.StatusCode, resp.Status)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse HTML: %v", err)
	}
	return doc, parsedURL, nil
}

// handleFetchWeb fetches a webpage. Default mode returns dense block summaries.
func (m *WebToolManager) handleFetchWeb(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	urlStr, ok := args["url"].(string)
	if !ok {
		return message.NewToolResultError("url parameter is required and must be a string"), nil
	}

	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "blocks"
	}

	doc, parsedURL, err := m.fetchAndParse(ctx, urlStr)
	if err != nil {
		return message.NewToolResultError(err.Error()), nil
	}

	if mode == "full" {
		markdown := m.convertToMarkdown(doc, parsedURL)
		return message.NewToolResultText(markdown), nil
	}

	// Default: block extraction mode.
	title := strings.TrimSpace(doc.Find("title").First().Text())
	blocks := extractDenseBlocks(doc)

	// Cache for WebFetchBlock.
	m.cacheBlocks(urlStr, blocks, title)

	// If no blocks found, fallback to basic info.
	if len(blocks) == 0 {
		metaDesc := doc.Find("meta[name='description']").AttrOr("content", "")
		bodyText := strings.TrimSpace(doc.Find("body").Text())
		if len(bodyText) > 500 {
			bodyText = bodyText[:500] + "..."
		}
		var fallback strings.Builder
		if title != "" {
			fallback.WriteString(fmt.Sprintf("# Page: %s\n", title))
		}
		fallback.WriteString(fmt.Sprintf("URL: %s\n\n", urlStr))
		fallback.WriteString("No dense content blocks found.\n\n")
		if metaDesc != "" {
			fallback.WriteString(fmt.Sprintf("Meta description: %s\n\n", metaDesc))
		}
		if bodyText != "" {
			fallback.WriteString(fmt.Sprintf("Body preview:\n%s\n", bodyText))
		}
		return message.NewToolResultText(fallback.String()), nil
	}

	summary := formatBlockSummary(title, urlStr, blocks, blockPreviewLength)
	return message.NewToolResultText(summary), nil
}

// handleFetchWebBlock retrieves full content of specific blocks from cache.
func (m *WebToolManager) handleFetchWebBlock(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	urlStr, ok := args["url"].(string)
	if !ok {
		return message.NewToolResultError("url parameter is required and must be a string"), nil
	}
	indicesStr, ok := args["block_indices"].(string)
	if !ok {
		return message.NewToolResultError("block_indices parameter is required (e.g., '1,3,5')"), nil
	}

	// Parse comma-separated indices.
	indices, err := parseBlockIndices(indicesStr)
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("invalid block_indices: %v", err)), nil
	}

	// Try cache first.
	cached := m.getCachedBlocks(urlStr)
	if cached == nil {
		// Cache miss — re-fetch and extract.
		doc, _, fetchErr := m.fetchAndParse(ctx, urlStr)
		if fetchErr != nil {
			return message.NewToolResultError(fetchErr.Error()), nil
		}
		title := strings.TrimSpace(doc.Find("title").First().Text())
		blocks := extractDenseBlocks(doc)
		m.cacheBlocks(urlStr, blocks, title)
		cached = m.getCachedBlocks(urlStr)
	}
	if cached == nil || len(cached.blocks) == 0 {
		return message.NewToolResultError("no blocks available for this URL"), nil
	}

	content := formatBlockContent(cached.blocks, indices)
	return message.NewToolResultText(content), nil
}

// cacheBlocks stores extraction results with TTL and size limits.
func (m *WebToolManager) cacheBlocks(urlStr string, blocks []textBlock, title string) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	// Evict oldest if at capacity.
	if len(m.blockCache) >= cacheMaxEntries {
		var oldestURL string
		var oldestTime time.Time
		for u, p := range m.blockCache {
			if oldestURL == "" || p.fetchedAt.Before(oldestTime) {
				oldestURL = u
				oldestTime = p.fetchedAt
			}
		}
		if oldestURL != "" {
			delete(m.blockCache, oldestURL)
		}
	}

	m.blockCache[urlStr] = &cachedPage{
		blocks:    blocks,
		title:     title,
		fetchedAt: time.Now(),
	}
}

// getCachedBlocks returns cached blocks if available and not expired.
func (m *WebToolManager) getCachedBlocks(urlStr string) *cachedPage {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	cached, ok := m.blockCache[urlStr]
	if !ok {
		return nil
	}
	if time.Since(cached.fetchedAt) > cacheTTL {
		delete(m.blockCache, urlStr)
		return nil
	}
	return cached
}

// GetToolState implements domain.ToolStateProvider.
// Returns cached URL entries so the model knows what pages are already in cache.
func (m *WebToolManager) GetToolState() string {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	var parts []string
	for u, page := range m.blockCache {
		if time.Since(page.fetchedAt) > cacheTTL {
			continue
		}
		ago := time.Since(page.fetchedAt).Truncate(time.Second)
		parts = append(parts, fmt.Sprintf("%s (%s ago)", u, ago))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Web cache: " + strings.Join(parts, ", ")
}

// Compile-time check that WebToolManager implements ToolStateProvider.
var _ domain.ToolStateProvider = (*WebToolManager)(nil)

// parseBlockIndices parses a comma-separated string of 1-based indices.
func parseBlockIndices(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	indices := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("'%s' is not a valid integer", p)
		}
		indices = append(indices, n)
	}
	if len(indices) == 0 {
		return nil, fmt.Errorf("no valid indices provided")
	}
	return indices, nil
}

// handleWebSearchStub returns a compatibility message explaining unavailability
func (m *WebToolManager) handleWebSearchStub(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	query, _ := args["query"].(string)
	msg := "WebSearch is not supported in this build. Provide relevant URLs or documents, or use WebFetch with a specific URL."
	if query != "" {
		msg = fmt.Sprintf("WebSearch not available. Query: %q. Please supply URLs, or use WebFetch.", query)
	}
	return message.NewToolResultText(msg), nil
}

// convertToMarkdown converts HTML document to clean markdown
func (m *WebToolManager) convertToMarkdown(doc *goquery.Document, baseURL *url.URL) string {
	var result strings.Builder

	// Get page title
	title := doc.Find("title").First().Text()
	if title != "" {
		result.WriteString(fmt.Sprintf("# %s\n\n", strings.TrimSpace(title)))
	}

	// Get meta description
	metaDesc := doc.Find("meta[name='description']").AttrOr("content", "")
	if metaDesc != "" {
		result.WriteString(fmt.Sprintf("*%s*\n\n", strings.TrimSpace(metaDesc)))
	}

	// Process main content
	// Try to find main content areas first
	var contentSelectors = []string{
		"main", "article", "[role='main']", ".main-content",
		".content", ".post-content", ".article-content", "#content",
	}

	var contentFound bool
	for _, selector := range contentSelectors {
		if contentElem := doc.Find(selector).First(); contentElem.Length() > 0 {
			m.processElement(contentElem, &result, baseURL, 0)
			contentFound = true
			break
		}
	}

	// If no main content found, process body but skip navigation/footer
	if !contentFound {
		doc.Find("nav, header, footer, .navigation, .nav, .sidebar, .menu").Remove()
		m.processElement(doc.Find("body"), &result, baseURL, 0)
	}

	// Extract important links
	links := m.extractLinks(doc, baseURL)
	if len(links) > 0 {
		result.WriteString("\n## Important Links\n\n")
		for _, link := range links {
			result.WriteString(fmt.Sprintf("- [%s](%s)\n", link.Text, link.URL))
		}
	}

	return result.String()
}

// processElement recursively processes HTML elements and converts to markdown
func (m *WebToolManager) processElement(selection *goquery.Selection, result *strings.Builder, baseURL *url.URL, depth int) {
	selection.Contents().Each(func(i int, s *goquery.Selection) {
		// Handle text nodes
		if goquery.NodeName(s) == "#text" {
			text := strings.TrimSpace(s.Text())
			if text != "" {
				result.WriteString(text)
			}
			return
		}

		// Handle HTML elements
		tagName := goquery.NodeName(s)
		switch tagName {
		case "h1", "h2", "h3", "h4", "h5", "h6":
			level := int(tagName[1] - '0')
			result.WriteString(fmt.Sprintf("\n%s %s\n\n", strings.Repeat("#", level), strings.TrimSpace(s.Text())))

		case "p":
			text := strings.TrimSpace(s.Text())
			if text != "" {
				result.WriteString(text + "\n\n")
			}

		case "br":
			result.WriteString("\n")

		case "strong", "b":
			result.WriteString(fmt.Sprintf("**%s**", strings.TrimSpace(s.Text())))

		case "em", "i":
			result.WriteString(fmt.Sprintf("*%s*", strings.TrimSpace(s.Text())))

		case "code":
			result.WriteString(fmt.Sprintf("`%s`", strings.TrimSpace(s.Text())))

		case "pre":
			result.WriteString(fmt.Sprintf("\n```\n%s\n```\n\n", strings.TrimSpace(s.Text())))

		case "ul", "ol":
			result.WriteString("\n")
			s.Find("li").Each(func(j int, li *goquery.Selection) {
				marker := "-"
				if tagName == "ol" {
					marker = fmt.Sprintf("%d.", j+1)
				}
				result.WriteString(fmt.Sprintf("%s %s\n", marker, strings.TrimSpace(li.Text())))
			})
			result.WriteString("\n")

		case "a":
			href, exists := s.Attr("href")
			text := strings.TrimSpace(s.Text())
			if exists && text != "" {
				// Convert relative URLs to absolute
				if absoluteURL := m.resolveURL(href, baseURL); absoluteURL != "" {
					result.WriteString(fmt.Sprintf("[%s](%s)", text, absoluteURL))
				} else {
					result.WriteString(text)
				}
			} else {
				result.WriteString(text)
			}

		case "img":
			alt := s.AttrOr("alt", "Image")
			src := s.AttrOr("src", "")
			if src != "" {
				absoluteSrc := m.resolveURL(src, baseURL)
				result.WriteString(fmt.Sprintf("![%s](%s)", alt, absoluteSrc))
			}

		case "blockquote":
			lines := strings.Split(strings.TrimSpace(s.Text()), "\n")
			for _, line := range lines {
				if strings.TrimSpace(line) != "" {
					result.WriteString(fmt.Sprintf("> %s\n", strings.TrimSpace(line)))
				}
			}
			result.WriteString("\n")

		case "div", "span", "section", "article":
			// Process children recursively for container elements
			m.processElement(s, result, baseURL, depth+1)

		case "script", "style", "noscript":
			// Skip these elements entirely

		default:
			// For other elements, just process their text content
			text := strings.TrimSpace(s.Text())
			if text != "" {
				result.WriteString(text + " ")
			}
		}
	})
}

// resolveURL converts relative URLs to absolute URLs
func (m *WebToolManager) resolveURL(href string, baseURL *url.URL) string {
	if href == "" {
		return ""
	}

	// If already absolute, return as-is
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}

	// Resolve relative URL
	if resolvedURL, err := baseURL.Parse(href); err == nil {
		return resolvedURL.String()
	}

	return href
}

// Link represents a extracted link
type Link struct {
	Text string
	URL  string
}

// extractLinks extracts important links from the page
func (m *WebToolManager) extractLinks(doc *goquery.Document, baseURL *url.URL) []Link {
	var links []Link
	seen := make(map[string]bool)

	// Find important links (excluding navigation)
	doc.Find("a[href]").Each(func(i int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		text := strings.TrimSpace(s.Text())

		// Skip empty links or navigation links
		if text == "" || len(text) > 100 {
			return
		}

		// Skip common navigation patterns
		lowerText := strings.ToLower(text)
		if strings.Contains(lowerText, "home") || strings.Contains(lowerText, "about") ||
			strings.Contains(lowerText, "contact") || strings.Contains(lowerText, "menu") {
			return
		}

		// Resolve to absolute URL
		absoluteURL := m.resolveURL(href, baseURL)
		if absoluteURL == "" || seen[absoluteURL] {
			return
		}

		seen[absoluteURL] = true
		links = append(links, Link{Text: text, URL: absoluteURL})

		// Limit to prevent overwhelming output
		if len(links) >= 10 {
			return
		}
	})

	return links
}

// webTool implements the domain.Tool interface for web tools
type webTool struct {
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (t *webTool) RawName() message.ToolName {
	return t.name
}

func (t *webTool) Name() message.ToolName {
	return t.name
}

func (t *webTool) Description() message.ToolDescription {
	return t.description
}

func (t *webTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}

func (t *webTool) Arguments() []message.ToolArgument {
	return t.arguments
}
