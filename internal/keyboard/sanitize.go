package keyboard

import (
	"strings"
	"unicode"
)

// SanitizeText prepares recogniser output for keystroke injection.
//
// Models emit characters that cannot be typed reliably or that unicode.IsPrint
// would silently drop (which glued words together): non-breaking and other
// Unicode spaces become a plain space, zero-width and bidi format characters
// are removed, invalid UTF-8 is discarded instead of typed as U+FFFD, and the
// remaining control characters other than newline and tab are dropped.
// Letters such as ç ğ ı ş ö ü are kept untouched.
func SanitizeText(text string) string {
	text = strings.ToValidUTF8(text, "")
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case r == '\n' || r == '\t' || r == ' ':
			b.WriteRune(r)
		case r == '\r':
			// CRLF/CR are folded into the newline that accompanies them.
			if !strings.Contains(text, "\n") {
				b.WriteRune('\n')
			}
		case r == '\uFFFD':
			// replacement character: the recogniser had no real character here.
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		case unicode.Is(unicode.Cf, r):
			// zero-width space/joiner, BOM, soft hyphen, bidi marks.
		case unicode.IsControl(r):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// splitRuns splits text into maximal runs of ordinary characters and the
// single whitespace characters between them, so a word is typed in one call.
func splitRuns(text string) []string {
	var runs []string
	start := 0
	for i, r := range text {
		if r == ' ' || r == '\n' || r == '\t' {
			if start < i {
				runs = append(runs, text[start:i])
			}
			runs = append(runs, string(r))
			start = i + len(string(r))
		}
	}
	if start < len(text) {
		runs = append(runs, text[start:])
	}
	return runs
}

// FlattenWhitespace turns newlines and tabs into plain spaces. In a terminal
// (or any form) a typed newline presses Enter and runs or submits what the
// user was dictating, and a tab triggers completion, so dictation never types
// them unless the injector is told to. A control-derived space is not added
// after an existing space.
func FlattenWhitespace(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	prevSpace := false
	for _, r := range text {
		if r == '\n' || r == '\t' || r == '\r' {
			if !prevSpace {
				b.WriteRune(' ')
			}
			prevSpace = true
			continue
		}
		b.WriteRune(r)
		prevSpace = r == ' '
	}
	return b.String()
}
