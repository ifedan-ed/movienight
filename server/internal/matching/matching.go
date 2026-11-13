// Package matching scores how well a search names a title, and counts a
// search's words in any script (app/services/catalog.py title_match and
// popularity_bonus, app/services/identify.py words). Pure functions, shared
// by search and the AI's best guess.
package matching

import (
	"math"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Norm is a title or query reduced for comparing: accents dropped (NFKD,
// combining marks removed), lowercase, every run of anything but a-z and
// 0-9 one space, and a leading "the", "a" or "an" dropped. "The Amélie!" and
// "amelie" both become "amelie". Text in other scripts reduces to nothing,
// as in the Python: such titles are matched by Meilisearch's own scores.
func Norm(text string) string {
	var b strings.Builder
	space := false
	for _, r := range norm.NFKD.String(text) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		r = unicode.ToLower(r)
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
			continue
		}
		space = true
	}
	t := b.String()
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(t, article) {
			return t[len(article):]
		}
	}
	return t
}

// TitleMatch is how well query names one of titles: 1.0 exactly, 0.9 the
// start of it (or it the start of the query, for titles of 4+ letters),
// 0.85 every word of the query in it, else 0.
func TitleMatch(query string, titles ...string) float64 {
	q := Norm(query)
	if q == "" {
		return 0
	}
	words := strings.Fields(q)
	best := 0.0
	for _, title := range titles {
		t := Norm(title)
		if t == "" {
			continue
		}
		if t == q {
			return 1
		}
		if strings.HasPrefix(t, q+" ") || (len(t) >= 4 && strings.HasPrefix(q, t+" ")) {
			best = max(best, 0.9)
			continue
		}
		have := make(map[string]bool)
		for _, w := range strings.Fields(t) {
			have[w] = true
		}
		all := true
		for _, w := range words {
			if !have[w] {
				all = false
				break
			}
		}
		if all {
			best = max(best, 0.85)
		}
	}
	return best
}

// PopularityBonus nudges (up to 0.15, full at 10,000 ratings) toward
// titles many people have rated: someone describing a film from memory
// usually means a known one. Tuned on described-plot searches (2026-10-02):
// Jaws went from 7th to 2nd, Toy Story from 20th to 4th; an exact title
// (1.0) still wins.
func PopularityBonus(votes float64) float64 {
	if votes < 0 {
		votes = 0
	}
	return 0.15 * math.Min(1, math.Log10(votes+1)/4)
}

// noSpaces is the scripts written without spaces between words (Chinese,
// Japanese kana, Thai, Lao, Khmer, Myanmar): there, every two characters
// count as a word.
func noSpaces(r rune) bool {
	switch {
	case r >= 0x3400 && r <= 0x4dbf, r >= 0x4e00 && r <= 0x9fff, r >= 0xf900 && r <= 0xfaff,
		r >= 0x3040 && r <= 0x30ff, r >= 0x31f0 && r <= 0x31ff, r >= 0xff66 && r <= 0xff9f,
		r >= 0x0e00 && r <= 0x0eff, r >= 0x1780 && r <= 0x17ff, r >= 0x1000 && r <= 0x109f:
		return true
	}
	return false
}

func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r)
}

// Words is how many words a search has, in any script. A word is a run of
// letters or digits, an apostrophe inside it kept ("don't" is one word);
// in a script without spaces every two characters count as one.
func Words(query string) int {
	runes := []rune(query)
	n := 0
	for i := 0; i < len(runes); {
		if !wordRune(runes[i]) {
			i++
			continue
		}
		joined, length := 0, 0
		for i < len(runes) {
			r := runes[i]
			if wordRune(r) {
				if noSpaces(r) {
					joined++
				}
				length++
				i++
				continue
			}
			// An apostrophe between two word characters keeps the word going.
			if (r == '\'' || r == '’') && i+1 < len(runes) && wordRune(runes[i+1]) && length > 0 {
				i++
				continue
			}
			break
		}
		n += (joined + 1) / 2
		if joined < length {
			n++
		}
	}
	return n
}
