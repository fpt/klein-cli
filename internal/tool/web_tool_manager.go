package tool

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png" // register PNG decoder
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	// web_fetch — default mode returns dense text block summaries.
	m.RegisterTool("web_fetch",
		"Fetch a webpage and extract content. Default mode returns a summary of dense text blocks with DOM paths; use web_fetch_block to retrieve full content of specific blocks. Set mode='full' for complete markdown.",
		[]message.ToolArgument{
			{Name: "url", Description: "URL of the webpage to fetch", Required: true, Type: "string"},
			{Name: "mode", Description: "Extraction mode: 'blocks' (default) for block summaries, 'full' for complete markdown", Required: false, Type: "string"},
		},
		m.handleFetchWeb)

	// web_fetch_block — retrieve full content of specific blocks from cache.
	m.RegisterTool("web_fetch_block",
		"Retrieve full content of specific text blocks from a previously fetched webpage. Use after web_fetch to get detailed content of interesting blocks.",
		[]message.ToolArgument{
			{Name: "url", Description: "URL of the webpage (should match a previous WebFetch call)", Required: true, Type: "string"},
			{Name: "block_indices", Description: "Comma-separated block indices to retrieve (e.g., '1,3,5')", Required: true, Type: "string"},
		},
		m.handleFetchWebBlock)

	// web_search (stub): declare interface compatibility; return informative message
	m.RegisterTool("web_search", "Search the web (stub). Not implemented in this build. Provide URLs or use web_fetch with a concrete link.",
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

	// Reject non-text content types (images are handled separately in handleFetchWeb)
	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "text/") && !strings.Contains(ct, "html") && !strings.Contains(ct, "xml") && !strings.Contains(ct, "json") {
		return nil, nil, fmt.Errorf("unsupported content type %q — web_fetch only handles HTML/text pages directly; binary content (PDF, images) is handled automatically by URL or content type detection", ct)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse HTML: %v", err)
	}
	return doc, parsedURL, nil
}

const (
	MaxImageBytes   = 20 * 1024 * 1024 // 20MB download limit
	MaxImageDim     = 512              // resize to fit within 512x512
	MaxJPEGQuality  = 80
)

// fetchImage downloads an image URL, resizes it to fit within MaxImageDim, and
// returns it as a base64-encoded JPEG to keep context size small.
func (m *WebToolManager) fetchImage(ctx context.Context, urlStr string) (base64Data string, contentType string, size int, err error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Compatible Web Fetcher Bot)")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to fetch image: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", 0, fmt.Errorf("HTTP error %d: %s", resp.StatusCode, resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxImageBytes+1))
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to read image: %v", err)
	}
	if len(data) > MaxImageBytes {
		return "", "", 0, fmt.Errorf("image exceeds %dMB size limit", MaxImageBytes/1024/1024)
	}

	// Decode, resize, re-encode as JPEG to save context tokens
	resized, err := ResizeImageToJPEG(data, MaxImageDim, MaxJPEGQuality)
	if err != nil {
		// Fallback: return raw data if decode/resize fails (e.g. SVG, GIF animation)
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "image/jpeg"
		}
		return base64.StdEncoding.EncodeToString(data), ct, len(data), nil
	}

	return base64.StdEncoding.EncodeToString(resized), "image/jpeg", len(resized), nil
}

// ResizeImageToJPEG decodes an image, scales it to fit within maxDim (preserving
// aspect ratio), and re-encodes as JPEG. Uses nearest-neighbor via stdlib draw.
func ResizeImageToJPEG(data []byte, maxDim int, quality int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	// Only resize if larger than maxDim
	if w > maxDim || h > maxDim {
		scale := float64(maxDim) / float64(w)
		if h > w {
			scale = float64(maxDim) / float64(h)
		}
		newW := int(float64(w) * scale)
		newH := int(float64(h) * scale)
		if newW < 1 {
			newW = 1
		}
		if newH < 1 {
			newH = 1
		}

		dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
		// Bilinear-ish scaling using stdlib: draw source into smaller rect
		draw.Draw(dst, dst.Bounds(), src, bounds.Min, draw.Src)
		// stdlib draw.Draw doesn't scale, so we do manual nearest-neighbor
		for y := 0; y < newH; y++ {
			srcY := bounds.Min.Y + int(float64(y)/scale)
			for x := 0; x < newW; x++ {
				srcX := bounds.Min.X + int(float64(x)/scale)
				dst.Set(x, y, src.At(srcX, srcY))
			}
		}
		src = dst
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// isImageContentType checks if a URL likely points to an image based on Content-Type or file extension.
func isImageURL(urlStr string) bool {
	lower := strings.ToLower(urlStr)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg"} {
		// Check before query string
		if idx := strings.Index(lower, "?"); idx > 0 {
			if strings.HasSuffix(lower[:idx], ext) {
				return true
			}
		} else if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// isPDFURL checks if a URL likely points to a PDF based on file extension.
func isPDFURL(urlStr string) bool {
	lower := strings.ToLower(urlStr)
	if idx := strings.Index(lower, "?"); idx > 0 {
		return strings.HasSuffix(lower[:idx], ".pdf")
	}
	return strings.HasSuffix(lower, ".pdf")
}

// fetchPDF downloads a PDF from a URL and saves it to a temporary file.
// Returns the local file path where the PDF was saved.
func (m *WebToolManager) fetchPDF(ctx context.Context, urlStr string) (string, int, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return "", 0, fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Compatible Web Fetcher Bot)")

	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("failed to fetch PDF: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("HTTP error %d: %s", resp.StatusCode, resp.Status)
	}

	const maxPDFBytes = 50 * 1024 * 1024 // 50MB limit
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPDFBytes+1))
	if err != nil {
		return "", 0, fmt.Errorf("failed to read PDF: %v", err)
	}
	if len(data) > maxPDFBytes {
		return "", 0, fmt.Errorf("PDF exceeds %dMB size limit", maxPDFBytes/1024/1024)
	}

	// Extract filename from URL for a meaningful temp file name
	parsedURL, _ := url.Parse(urlStr)
	baseName := "download.pdf"
	if parsedURL != nil {
		if name := filepath.Base(parsedURL.Path); name != "" && name != "." && name != "/" {
			baseName = name
		}
		if !strings.HasSuffix(strings.ToLower(baseName), ".pdf") {
			baseName += ".pdf"
		}
	}

	tmpFile, err := os.CreateTemp("", "klein-pdf-*-"+baseName)
	if err != nil {
		return "", 0, fmt.Errorf("failed to create temp file: %v", err)
	}
	defer tmpFile.Close()

	if _, err := tmpFile.Write(data); err != nil {
		os.Remove(tmpFile.Name())
		return "", 0, fmt.Errorf("failed to write temp file: %v", err)
	}

	return tmpFile.Name(), len(data), nil
}

// handleFetchWeb fetches a webpage. Default mode returns dense block summaries.
// If the URL points to an image, downloads it and returns as base64 for vision analysis.
func (m *WebToolManager) handleFetchWeb(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	urlStr, ok := args["url"].(string)
	if !ok {
		return message.NewToolResultError("url parameter is required and must be a string"), nil
	}

	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "blocks"
	}

	// If URL looks like a PDF, download to temp file for pdf_read/pdf_info tools
	if isPDFURL(urlStr) {
		filePath, size, err := m.fetchPDF(ctx, urlStr)
		if err != nil {
			return message.NewToolResultError(fmt.Sprintf("failed to download PDF: %v", err)), nil
		}
		desc := fmt.Sprintf("PDF downloaded from %s (%dKB) and saved to: %s\nUse pdf_info and pdf_read tools with this file path to extract content.", urlStr, size/1024, filePath)
		return message.NewToolResultText(desc), nil
	}

	// If URL looks like an image, try to download it for vision analysis
	if isImageURL(urlStr) {
		b64, ct, size, err := m.fetchImage(ctx, urlStr)
		if err != nil {
			return message.NewToolResultError(fmt.Sprintf("failed to download image: %v", err)), nil
		}
		desc := fmt.Sprintf("Image downloaded from %s (type: %s, size: %dKB). Analyze the attached image.", urlStr, ct, size/1024)
		return message.NewToolResultWithImages(desc, []string{b64}), nil
	}

	doc, parsedURL, err := m.fetchAndParse(ctx, urlStr)
	if err != nil {
		errMsg := err.Error()
		// If fetchAndParse failed due to content type, check what kind
		if strings.Contains(errMsg, "unsupported content type") {
			// PDF content type — download to temp file
			if strings.Contains(errMsg, "application/pdf") {
				filePath, size, pdfErr := m.fetchPDF(ctx, urlStr)
				if pdfErr != nil {
					return message.NewToolResultError(fmt.Sprintf("failed to download PDF: %v", pdfErr)), nil
				}
				desc := fmt.Sprintf("PDF downloaded from %s (%dKB) and saved to: %s\nUse pdf_info and pdf_read tools with this file path to extract content.", urlStr, size/1024, filePath)
				return message.NewToolResultText(desc), nil
			}
			// Image content type — download for vision analysis
			if strings.Contains(errMsg, "image/") {
				b64, ct, size, imgErr := m.fetchImage(ctx, urlStr)
				if imgErr != nil {
					return message.NewToolResultError(fmt.Sprintf("failed to download image: %v", imgErr)), nil
				}
				desc := fmt.Sprintf("Image downloaded from %s (type: %s, size: %dKB). Analyze the attached image.", urlStr, ct, size/1024)
				return message.NewToolResultWithImages(desc, []string{b64}), nil
			}
		}
		return message.NewToolResultError(errMsg), nil
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
	msg := "web_search is not supported in this build. Provide relevant URLs or documents, or use web_fetch with a specific URL."
	if query != "" {
		msg = fmt.Sprintf("web_search not available. Query: %q. Please supply URLs, or use web_fetch.", query)
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
