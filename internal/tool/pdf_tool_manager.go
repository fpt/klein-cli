package tool

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

const (
	pdfMaxPagesDefault    = 20      // default page cap for pdf_read
	pdfMaxImagesPerCall   = 10      // max images returned per pdf_extract_images call
	pdfMaxTextOutputChars = 100_000 // cap text output at ~100K chars
)

// PDFToolManager provides pdf_info, pdf_read, and pdf_extract_images tools.
type PDFToolManager struct {
	tools      map[message.ToolName]message.Tool
	workingDir string
}

// NewPDFToolManager creates a PDF tool manager.
func NewPDFToolManager(workingDir string) domain.ToolManager {
	m := &PDFToolManager{
		tools:      make(map[message.ToolName]message.Tool),
		workingDir: workingDir,
	}
	m.register()
	return m
}

func (m *PDFToolManager) register() {
	m.RegisterTool("pdf_info",
		"Get PDF metadata: page count, title, author, subject, page dimensions, bookmarks. Use this first to understand a PDF before reading content.",
		[]message.ToolArgument{
			{Name: "path", Description: "Local file path or URL (http/https) to the PDF", Required: true, Type: "string"},
		},
		m.handlePDFInfo)

	m.RegisterTool("pdf_read",
		"Extract text content from PDF pages. Accepts local paths or URLs. Quality varies by PDF; some pages may be image-based. Capped at 20 pages per call.",
		[]message.ToolArgument{
			{Name: "path", Description: "Local file path or URL (http/https) to the PDF", Required: true, Type: "string"},
			{Name: "pages", Description: "Page selection (e.g. '1-5', '3', '1,3-5'). Default: first 20 pages.", Required: false, Type: "string"},
		},
		m.handlePDFRead)

	m.RegisterTool("pdf_extract_images",
		"Extract embedded images from PDF pages and return as base64-encoded JPEG for vision analysis. Accepts local paths or URLs. Max 10 images per call.",
		[]message.ToolArgument{
			{Name: "path", Description: "Local file path or URL (http/https) to the PDF", Required: true, Type: "string"},
			{Name: "pages", Description: "Page selection (e.g. '1-5', '3'). Default: all pages.", Required: false, Type: "string"},
		},
		m.handlePDFExtractImages)
}

// resolvePath resolves a path relative to the working directory.
// If the path is a URL (http/https), it downloads the PDF to a temp file first.
func (m *PDFToolManager) resolvePath(ctx context.Context, pathParam string) (string, error) {
	if strings.HasPrefix(pathParam, "http://") || strings.HasPrefix(pathParam, "https://") {
		return m.downloadPDF(ctx, pathParam)
	}
	if filepath.IsAbs(pathParam) {
		return pathParam, nil
	}
	return filepath.Abs(filepath.Join(m.workingDir, pathParam))
}

// downloadPDF fetches a PDF from a URL and saves it to a temp file.
func (m *PDFToolManager) downloadPDF(ctx context.Context, urlStr string) (string, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Compatible Web Fetcher Bot)")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch PDF: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP error %d: %s", resp.StatusCode, resp.Status)
	}

	const maxPDFBytes = 50 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPDFBytes+1))
	if err != nil {
		return "", fmt.Errorf("failed to read PDF: %v", err)
	}
	if len(data) > maxPDFBytes {
		return "", fmt.Errorf("PDF exceeds %dMB size limit", maxPDFBytes/1024/1024)
	}

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
		return "", fmt.Errorf("failed to create temp file: %v", err)
	}
	defer tmpFile.Close()

	if _, err := tmpFile.Write(data); err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to write temp file: %v", err)
	}

	return tmpFile.Name(), nil
}

// parsePDFPages parses a comma-separated page selection string into pdfcpu format.
// Returns nil for "all pages".
func parsePDFPages(pagesArg string) []string {
	if pagesArg == "" {
		return nil
	}
	parts := strings.Split(pagesArg, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// handlePDFInfo returns PDF metadata.
func (m *PDFToolManager) handlePDFInfo(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	pathParam, ok := args["path"].(string)
	if !ok || pathParam == "" {
		return message.NewToolResultError("path parameter is required"), nil
	}

	absPath, err := m.resolvePath(ctx, pathParam)
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to resolve path: %v", err)), nil
	}

	f, err := os.Open(absPath)
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to open PDF: %v", err)), nil
	}
	defer f.Close()

	conf := model.NewDefaultConfiguration()
	info, err := api.PDFInfo(f, filepath.Base(absPath), nil, false, conf)
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to read PDF info: %v", err)), nil
	}

	// Get bookmarks
	if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to seek: %v", seekErr)), nil
	}
	bookmarks, _ := api.Bookmarks(f, conf)

	// Format output
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# PDF Info: %s\n\n", filepath.Base(absPath)))
	b.WriteString(fmt.Sprintf("Pages: %d\n", info.PageCount))
	b.WriteString(fmt.Sprintf("PDF Version: %s\n", info.Version))

	if info.Title != "" {
		b.WriteString(fmt.Sprintf("Title: %s\n", info.Title))
	}
	if info.Author != "" {
		b.WriteString(fmt.Sprintf("Author: %s\n", info.Author))
	}
	if info.Subject != "" {
		b.WriteString(fmt.Sprintf("Subject: %s\n", info.Subject))
	}
	if info.Creator != "" {
		b.WriteString(fmt.Sprintf("Creator: %s\n", info.Creator))
	}
	if info.Producer != "" {
		b.WriteString(fmt.Sprintf("Producer: %s\n", info.Producer))
	}
	if info.CreationDate != "" {
		b.WriteString(fmt.Sprintf("Created: %s\n", info.CreationDate))
	}
	if info.ModificationDate != "" {
		b.WriteString(fmt.Sprintf("Modified: %s\n", info.ModificationDate))
	}
	if len(info.Keywords) > 0 {
		b.WriteString(fmt.Sprintf("Keywords: %s\n", strings.Join(info.Keywords, ", ")))
	}

	// Page dimensions
	if len(info.Dimensions) > 0 {
		b.WriteString("\nPage Dimensions:\n")
		seen := make(map[string]bool)
		for _, dim := range info.Dimensions {
			key := fmt.Sprintf("%.0fx%.0f", dim.Width, dim.Height)
			if seen[key] {
				continue
			}
			seen[key] = true
			b.WriteString(fmt.Sprintf("  %.0f x %.0f pts (%.1f x %.1f in)\n",
				dim.Width, dim.Height, dim.Width/72.0, dim.Height/72.0))
		}
	}

	// Flags
	var flags []string
	if info.Tagged {
		flags = append(flags, "Tagged")
	}
	if info.Encrypted {
		flags = append(flags, "Encrypted")
	}
	if info.Watermarked {
		flags = append(flags, "Watermarked")
	}
	if info.Form {
		flags = append(flags, "Has Forms")
	}
	if info.Signatures {
		flags = append(flags, "Has Signatures")
	}
	if info.Outlines {
		flags = append(flags, "Has Bookmarks")
	}
	if len(flags) > 0 {
		b.WriteString(fmt.Sprintf("\nFlags: %s\n", strings.Join(flags, ", ")))
	}

	// Bookmarks
	if len(bookmarks) > 0 {
		b.WriteString("\nBookmarks:\n")
		formatBookmarks(&b, bookmarks, 0)
	}

	return message.NewToolResultText(b.String()), nil
}

func formatBookmarks(b *strings.Builder, bms []pdfcpu.Bookmark, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, bm := range bms {
		b.WriteString(fmt.Sprintf("%s- [p%d] %s\n", indent, bm.PageFrom, bm.Title))
		if len(bm.Kids) > 0 {
			formatBookmarks(b, bm.Kids, depth+1)
		}
	}
}

// handlePDFRead extracts text from PDF pages.
func (m *PDFToolManager) handlePDFRead(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	pathParam, ok := args["path"].(string)
	if !ok || pathParam == "" {
		return message.NewToolResultError("path parameter is required"), nil
	}

	absPath, err := m.resolvePath(ctx, pathParam)
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to resolve path: %v", err)), nil
	}

	pagesArg, _ := args["pages"].(string)
	selectedPages := parsePDFPages(pagesArg)

	// Read and validate the PDF context
	pdfCtx, err := api.ReadContextFile(absPath)
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to read PDF: %v", err)), nil
	}
	if err := api.ValidateContext(pdfCtx); err != nil {
		return message.NewToolResultError(fmt.Sprintf("invalid PDF: %v", err)), nil
	}

	pageCount := pdfCtx.PageCount

	// Determine which pages to extract
	var pageNrs []int
	if selectedPages == nil {
		limit := pageCount
		if limit > pdfMaxPagesDefault {
			limit = pdfMaxPagesDefault
		}
		for i := 1; i <= limit; i++ {
			pageNrs = append(pageNrs, i)
		}
	} else {
		pageSet, err := api.PagesForPageSelection(pageCount, selectedPages, false, false)
		if err != nil {
			return message.NewToolResultError(fmt.Sprintf("invalid page selection: %v", err)), nil
		}
		for pg := range pageSet {
			pageNrs = append(pageNrs, pg)
		}
		sort.Ints(pageNrs)
		if len(pageNrs) > pdfMaxPagesDefault {
			pageNrs = pageNrs[:pdfMaxPagesDefault]
		}
	}

	// Extract content from each page
	var content strings.Builder
	totalChars := 0
	poorQualityPages := 0
	emptyPages := 0

	for _, pageNr := range pageNrs {
		if totalChars >= pdfMaxTextOutputChars {
			content.WriteString(fmt.Sprintf("\n[Output truncated at %d characters. Use 'pages' parameter to read specific sections.]\n", pdfMaxTextOutputChars))
			break
		}

		reader, err := pdfcpu.ExtractPageContent(pdfCtx, pageNr)
		if err != nil {
			content.WriteString(fmt.Sprintf("\n--- Page %d ---\n[Error extracting content: %v]\n", pageNr, err))
			continue
		}

		raw, err := io.ReadAll(reader)
		if err != nil {
			content.WriteString(fmt.Sprintf("\n--- Page %d ---\n[Error reading content: %v]\n", pageNr, err))
			continue
		}

		text, quality := extractTextFromContentStream(raw)

		content.WriteString(fmt.Sprintf("\n--- Page %d ---\n", pageNr))
		if text == "" {
			content.WriteString("[No extractable text - page may be image-based. Use pdf_extract_images to analyze.]\n")
			emptyPages++
		} else {
			remaining := pdfMaxTextOutputChars - totalChars
			if len(text) > remaining {
				text = text[:remaining] + "\n[Page truncated...]"
			}
			content.WriteString(text)
			content.WriteString("\n")
			totalChars += len(text)
			if !quality {
				poorQualityPages++
			}
		}
	}

	// Build header
	var header strings.Builder
	header.WriteString(fmt.Sprintf("# PDF Text: %s (%d pages total)\n", filepath.Base(absPath), pageCount))

	if emptyPages > 0 {
		header.WriteString(fmt.Sprintf("NOTE: %d page(s) had no extractable text (likely image-based). Use pdf_extract_images for vision analysis.\n", emptyPages))
	}
	if poorQualityPages > 0 {
		header.WriteString(fmt.Sprintf("NOTE: %d page(s) had poor text extraction quality (custom fonts or complex encoding).\n", poorQualityPages))
	}
	if selectedPages == nil && pageCount > pdfMaxPagesDefault {
		header.WriteString(fmt.Sprintf("NOTE: Showing first %d of %d pages. Use 'pages' parameter for specific pages.\n", pdfMaxPagesDefault, pageCount))
	}

	return message.NewToolResultText(header.String() + content.String()), nil
}

// extractTextFromContentStream parses a raw PDF content stream and extracts
// readable text from BT/ET blocks. Returns the extracted text and a quality flag.
func extractTextFromContentStream(raw []byte) (string, bool) {
	content := string(raw)
	var texts []string

	// Find all BT...ET blocks (text objects)
	btPattern := regexp.MustCompile(`(?s)BT\s(.*?)ET`)
	blocks := btPattern.FindAllStringSubmatch(content, -1)

	for _, block := range blocks {
		blockContent := block[1]
		var blockTexts []string

		// Tj operator: (text) Tj — show a text string
		tjPattern := regexp.MustCompile(`\(([^)]*)\)\s*Tj`)
		for _, match := range tjPattern.FindAllStringSubmatch(blockContent, -1) {
			blockTexts = append(blockTexts, decodePDFString(match[1]))
		}

		// TJ operator: [(text) kern (text)] TJ — show text with kerning
		tjArrayPattern := regexp.MustCompile(`\[(.*?)\]\s*TJ`)
		for _, match := range tjArrayPattern.FindAllStringSubmatch(blockContent, -1) {
			var arrayText strings.Builder
			strPattern := regexp.MustCompile(`\(([^)]*)\)`)
			for _, strMatch := range strPattern.FindAllStringSubmatch(match[1], -1) {
				arrayText.WriteString(decodePDFString(strMatch[1]))
			}
			blockTexts = append(blockTexts, arrayText.String())
		}

		// ' operator: (text) ' — move to next line and show text
		sqPattern := regexp.MustCompile(`\(([^)]*)\)\s*'`)
		for _, match := range sqPattern.FindAllStringSubmatch(blockContent, -1) {
			blockTexts = append(blockTexts, "\n"+decodePDFString(match[1]))
		}

		// " operator: aw ac (text) " — set spacing, move to next line, show text
		dqPattern := regexp.MustCompile(`\(([^)]*)\)\s*"`)
		for _, match := range dqPattern.FindAllStringSubmatch(blockContent, -1) {
			blockTexts = append(blockTexts, "\n"+decodePDFString(match[1]))
		}

		if len(blockTexts) > 0 {
			texts = append(texts, strings.Join(blockTexts, ""))
			texts = append(texts, "\n")
		}
	}

	result := strings.Join(texts, "")
	// Clean up excessive whitespace
	result = regexp.MustCompile(`\n{3,}`).ReplaceAllString(result, "\n\n")
	result = strings.TrimSpace(result)

	// Quality assessment
	quality := true
	if len(content) > 100 && len(result) < len(content)/20 {
		quality = false
	}
	if len(result) == 0 && len(content) > 50 {
		quality = false
	}

	return result, quality
}

// decodePDFString handles basic PDF string escape sequences.
func decodePDFString(s string) string {
	s = strings.ReplaceAll(s, "\\n", "\n")
	s = strings.ReplaceAll(s, "\\r", "\r")
	s = strings.ReplaceAll(s, "\\t", "\t")
	s = strings.ReplaceAll(s, "\\(", "(")
	s = strings.ReplaceAll(s, "\\)", ")")
	s = strings.ReplaceAll(s, "\\\\", "\\")
	// Handle octal escapes \ddd
	octalPattern := regexp.MustCompile(`\\([0-7]{1,3})`)
	s = octalPattern.ReplaceAllStringFunc(s, func(match string) string {
		val, err := strconv.ParseUint(match[1:], 8, 8)
		if err != nil {
			return match
		}
		return string(rune(val))
	})
	return s
}

// handlePDFExtractImages extracts embedded images from PDF pages.
func (m *PDFToolManager) handlePDFExtractImages(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	pathParam, ok := args["path"].(string)
	if !ok || pathParam == "" {
		return message.NewToolResultError("path parameter is required"), nil
	}

	absPath, err := m.resolvePath(ctx, pathParam)
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to resolve path: %v", err)), nil
	}

	pagesArg, _ := args["pages"].(string)
	selectedPages := parsePDFPages(pagesArg)

	f, err := os.Open(absPath)
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to open PDF: %v", err)), nil
	}
	defer f.Close()

	conf := model.NewDefaultConfiguration()
	imageMaps, err := api.ExtractImagesRaw(f, selectedPages, conf)
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to extract images: %v", err)), nil
	}

	var images []string
	var descriptions []string
	totalImages := 0

	for _, imgMap := range imageMaps {
		if totalImages >= pdfMaxImagesPerCall {
			break
		}
		for objNr, img := range imgMap {
			if totalImages >= pdfMaxImagesPerCall {
				break
			}

			imgData, err := io.ReadAll(img)
			if err != nil {
				descriptions = append(descriptions, fmt.Sprintf(
					"Image obj#%d (p%d): failed to read: %v", objNr, img.PageNr, err))
				continue
			}
			if len(imgData) == 0 {
				continue
			}

			// Try to resize to JPEG
			resized, resizeErr := ResizeImageToJPEG(imgData, MaxImageDim, MaxJPEGQuality)
			var b64 string
			if resizeErr != nil {
				// Fallback: raw JPEG images embedded in PDFs are already decodable
				if img.FileType == "jpg" || img.FileType == "jpeg" {
					b64 = base64.StdEncoding.EncodeToString(imgData)
				} else {
					descriptions = append(descriptions, fmt.Sprintf(
						"Image obj#%d (p%d, %s, %dx%d): could not decode/resize",
						objNr, img.PageNr, img.FileType, img.Width, img.Height))
					continue
				}
			} else {
				b64 = base64.StdEncoding.EncodeToString(resized)
			}

			images = append(images, b64)
			descriptions = append(descriptions, fmt.Sprintf(
				"Image %d: page %d, %dx%d, format=%s",
				totalImages+1, img.PageNr, img.Width, img.Height, img.FileType))
			totalImages++
		}
	}

	if len(images) == 0 {
		msg := fmt.Sprintf("No extractable images found in %s", filepath.Base(absPath))
		if selectedPages != nil {
			msg += fmt.Sprintf(" for pages %s", pagesArg)
		}
		return message.NewToolResultText(msg), nil
	}

	description := fmt.Sprintf("Extracted %d image(s) from %s:\n%s",
		len(images), filepath.Base(absPath), strings.Join(descriptions, "\n"))

	if totalImages >= pdfMaxImagesPerCall {
		description += fmt.Sprintf("\n[Limited to %d images. Use 'pages' parameter to target specific pages.]", pdfMaxImagesPerCall)
	}

	return message.NewToolResultWithImages(description, images), nil
}

// ToolManager interface implementation

func (m *PDFToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	t, ok := m.tools[name]
	return t, ok
}

func (m *PDFToolManager) GetTools() map[message.ToolName]message.Tool {
	return m.tools
}

func (m *PDFToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	t, ok := m.tools[name]
	if !ok {
		return message.NewToolResultError(fmt.Sprintf("tool '%s' not found", name)), nil
	}
	return t.Handler()(ctx, args)
}

func (m *PDFToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	m.tools[name] = &pdfTool{
		name:        name,
		description: description,
		arguments:   arguments,
		handler:     handler,
	}
}

// pdfTool implements message.Tool
type pdfTool struct {
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (t *pdfTool) RawName() message.ToolName            { return t.name }
func (t *pdfTool) Name() message.ToolName               { return t.name }
func (t *pdfTool) Description() message.ToolDescription { return t.description }
func (t *pdfTool) Arguments() []message.ToolArgument    { return t.arguments }
func (t *pdfTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}
