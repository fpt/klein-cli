package app

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// WriteSplashScreen writes an ASCII art splash screen of Klein to w.
// Generated using https://codeshack.io/image-to-ascii-art-generator/
// When colored is true, uses ANSI color codes; otherwise plain text.
func WriteSplashScreen(w io.Writer, colored bool) {
	if w == nil {
		return
	}

	// Minimum left margin for readability when centering
	minLeftIndent := 2

	splash := `          ░▒▒▒░  ░░░               
         ▒▓▒▒▒▒░ ░░ ░              
        ░ ▓▓▓▒ ▒▒▒░ ▓░             
        ░▒▒ ░░ ▒▒▒▒▒               
       ░ ░░▒░ ▒░▒░▒▒▒░▓            
      ░░░░░░░▒▒ ▒▒▒▒ ▒▓            
      ░ ░░░░ ▒▒▒▒░▒░ ▒░▒▓▓         
     ░ ░░░░░░▒▒▒▒ ▒░▒░░▒░▒▓░       
    ░░░░ ░░░▒▒▒▒░▒     ░▒▒ ░▒      
    ░ ░░░░░▒▒ ▒▒▒▒░     ▓░░▒ ░▓    
     ░░░░░▒▒▒▒▒ ▒░          ░▒▒▒   
     ░░░░░░░▒▒░▒            ░▒▒▒▒  
    ░░░░░░░░▒ ░ ░            ░░▒▒  
    ░░░░ ░░▒▒░░▒▓▓▓▓ ▓▓▒ ▒     ░░  
      ░░░░░▒▒▒░▒▓ ▓▓▒▒▒▒░▒▒ ▒▒▒░   
       ░░ ░▒▒ ▒░ ░▒▒ ▒▒ ▒▒▒▒▒▒░    
        ░░░  ▒░▒▒░▒▒░ ▒▒▒▒▒▒░      
             ░░▒▒▒▒▒▒▒▒ ░          
                                   `

	// Prepare columns: left logo, right art
	logoLines := []string{
		"KLEIN CLI",
		"AI Coding Agent",
	}
	artLines := strings.Split(splash, "\n")

	// Compute widths using rune count for better alignment with wide chars
	maxLogoWidth := 0
	for _, l := range logoLines {
		if n := runeLen(l); n > maxLogoWidth {
			maxLogoWidth = n
		}
	}
	maxArtWidth := 0
	for _, l := range artLines {
		if n := runeLen(l); n > maxArtWidth {
			maxArtWidth = n
		}
	}

	// Determine terminal width (fallback to 80)
	termWidth := 80
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 {
		termWidth = width
	}

	gap := 2 // spaces between columns
	contentWidth := maxLogoWidth + gap + maxArtWidth
	// Decide layout purely on content width vs terminal width
	sideBySide := contentWidth <= termWidth

	// Color prefix/suffix
	prefix, suffix := "", ""
	if colored {
		prefix = "\x1b[90m"
		suffix = "\x1b[0m"
	}

	if sideBySide {
		// Vertically center logo relative to art height
		rows := len(artLines)
		topPad := 0
		if rows > len(logoLines) {
			topPad = (rows - len(logoLines)) / 2
		}
		// Compute centered indent for the whole two-column block
		leftIndent := minLeftIndent
		if termWidth > contentWidth {
			pad := (termWidth - contentWidth) / 2
			if pad > leftIndent {
				leftIndent = pad
			}
		}

		for i := 0; i < rows; i++ {
			// Left column (logo)
			var left string
			logoIdx := i - topPad
			if logoIdx >= 0 && logoIdx < len(logoLines) {
				left = padRight(logoLines[logoIdx], maxLogoWidth)
			} else {
				left = strings.Repeat(" ", maxLogoWidth)
			}
			// Right column (art)
			right := ""
			if i < len(artLines) {
				right = artLines[i]
			}
			// Apply left indent before color prefix
			fmt.Fprintf(w, "%s%s%s%s%s%s\n", strings.Repeat(" ", leftIndent), prefix, left, strings.Repeat(" ", gap), right, suffix)
		}
		fmt.Fprintln(w)
		return
	}

	// Stacked fallback: logo above art
	// Compute block width and centered base indent
	blockWidth := maxLogoWidth
	if maxArtWidth > blockWidth {
		blockWidth = maxArtWidth
	}
	baseIndent := minLeftIndent
	if termWidth > blockWidth {
		pad := (termWidth - blockWidth) / 2
		if pad > baseIndent {
			baseIndent = pad
		}
	}

	for _, l := range logoLines {
		// Center each line within the block width
		innerPad := (blockWidth - runeLen(l)) / 2
		// Apply indent before color prefix
		fmt.Fprintf(
			w,
			"%s%s%s%s%s\n",
			strings.Repeat(" ", baseIndent),
			strings.Repeat(" ", innerPad),
			prefix,
			l,
			suffix,
		)
	}
	fmt.Fprintln(w)
	for _, l := range artLines {
		// Center each line within the block width
		innerPad := (blockWidth - runeLen(l)) / 2
		// Apply indent before color prefix
		fmt.Fprintf(
			w,
			"%s%s%s%s%s\n",
			strings.Repeat(" ", baseIndent),
			strings.Repeat(" ", innerPad),
			prefix,
			l,
			suffix,
		)
	}
	fmt.Fprintln(w)
}

// WriteResponseHeader writes a standardized response header to w.
// When colored is true, prints in bright cyan; otherwise plain text.
func WriteResponseHeader(w io.Writer, model string, colored bool) {
	if w == nil {
		return
	}
	if colored {
		// Bright cyan (sky-blue like)
		fmt.Fprintf(w, "\x1b[36m%s (%s)\x1b[0m\n", "klein", model)
	} else {
		fmt.Fprintf(w, "%s (%s)\n", "klein", model)
	}
}

// runeLen returns the number of runes in s.
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// padRight pads s with spaces on the right to width runes.
func padRight(s string, width int) string {
	n := runeLen(s)
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}
