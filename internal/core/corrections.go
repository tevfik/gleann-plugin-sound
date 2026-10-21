package core

import (
	"regexp"
	"strings"
	"unicode"
)

// Corrector rewrites words a recogniser systematically gets wrong: product
// names, jargon, accents ("gılan" → "gleann"). Keys are matched
// case-insensitively; the capitalisation of the first letter and surrounding
// punctuation are kept. A key containing spaces matches that phrase.
//
// It replaces the earlier hard-coded Turkish fix-ups: the table now lives in
// the user's config ("corrections" in ~/.gleann/sound.json) and is empty by
// default.
type Corrector struct {
	words   map[string]string
	phrases []phraseRule
}

type phraseRule struct {
	re  *regexp.Regexp
	dst string
}

// NewCorrector builds a Corrector. A nil or empty map yields a no-op.
func NewCorrector(rules map[string]string) *Corrector {
	c := &Corrector{words: map[string]string{}}
	for src, dst := range rules {
		src = strings.TrimSpace(src)
		if src == "" {
			continue
		}
		if strings.ContainsAny(src, " \t") {
			c.phrases = append(c.phrases, phraseRule{
				re:  regexp.MustCompile(`(?i)\b` + strings.Join(strings.Fields(regexp.QuoteMeta(src)), `\s+`) + `\b`),
				dst: dst,
			})
			continue
		}
		c.words[strings.ToLower(src)] = dst
	}
	return c
}

// Apply returns text with all corrections applied.
func (c *Corrector) Apply(text string) string {
	if c == nil || (len(c.words) == 0 && len(c.phrases) == 0) || text == "" {
		return text
	}
	for _, p := range c.phrases {
		text = p.re.ReplaceAllStringFunc(text, func(m string) string { return matchCase(m, p.dst) })
	}
	if len(c.words) == 0 {
		return text
	}
	// Rewrite word by word, keeping the original spacing.
	var b strings.Builder
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		b.WriteString(c.word(text[start:end]))
		start = -1
	}
	for i, r := range text {
		if unicode.IsSpace(r) {
			flush(i)
			b.WriteRune(r)
		} else if start < 0 {
			start = i
		}
	}
	flush(len(text))
	return b.String()
}

func (c *Corrector) word(w string) string {
	core := strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if core == "" {
		return w
	}
	dst, ok := c.words[strings.ToLower(core)]
	if !ok {
		return w
	}
	i := strings.Index(w, core)
	return w[:i] + matchCase(core, dst) + w[i+len(core):]
}

// matchCase upper-cases dst's first letter when src's first letter is upper case.
func matchCase(src, dst string) string {
	r := []rune(src)
	if len(r) > 0 && unicode.IsUpper(r[0]) && dst != "" {
		d := []rune(dst)
		d[0] = unicode.ToUpper(d[0])
		return string(d)
	}
	return dst
}
