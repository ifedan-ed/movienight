// Package spelling fixes typos in a search before it is embedded
// (app/services/spelling.py).
//
// Meilisearch's keyword search forgives a typo or two per word, but the
// meaning model reads "trasure" as noise, and a described plot full of typos
// ("kids seraching for trasure with a amp") finds nothing. Each word is
// checked against the catalog's own vocabulary (every title and plot, with
// how often each word appears) and replaced by the commonest word one or two
// letters away. A real but rare word is kept unless a far commoner one is a
// single letter away ("amp" -> "map"). Search then runs on both the original
// and the fixed wording, so a fix never loses anything.
//
// Speed: the Python built every candidate string into a set (about 100,000
// for a 7-letter word's two-letter edits, ~50 ms on the event loop). Here
// the candidates are generated into one reused buffer and looked up in the
// vocabulary map as they are made, nothing kept; a query is at most
// SEARCH_MAX_CHARS long, so the work is bounded.
//
// The vocabulary is built by the catalog job (AddWords and Keep, saved
// under VocabKey in app_settings; one definition of a word for both sides) and read here at most once an hour per process
// (Vocab), as the Python did.
package spelling

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"movienight/server/internal/store"
)

// VocabKey is where the catalog job keeps the vocabulary (app_settings).
const VocabKey = "catalog_vocab"

const (
	minCount   = 3  // words seen fewer times are left out of the vocabulary
	maxWordLen = 30 // longer "words" are left out too
	common     = 30 // a word seen this often is never "corrected"
	rareRatio  = 20 // a rare real word gives way only to one this much commoner
	letters    = "abcdefghijklmnopqrstuvwxyz"
)

// words splits lowercased text into runs of a-z (the vocabulary's words).
func words(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return r < 'a' || r > 'z' })
}

// AddWords counts the a-z words of one text into counts (lowercased).
// TMDB's plots carry HTML entities ("&amp;"): read as text, not as words.
// The catalog build calls it for every catalog text, then Keep.
func AddWords(counts map[string]int, text string) {
	for _, w := range words(strings.ToLower(html.UnescapeString(text))) {
		counts[w]++
	}
}

// Keep is the vocabulary from counts: each word seen at least minCount
// times and at most maxWordLen letters (longer "words" are noise: URLs, run
// together text). counts is changed in place and returned.
func Keep(counts map[string]int) map[string]int {
	for w, n := range counts {
		if n < minCount || len(w) > maxWordLen {
			delete(counts, w)
		}
	}
	return counts
}

// edits1 calls fn with every string one edit (delete, swap of neighbours,
// replace, insert of a-z) away from word, built in buf (reused: the bytes
// are only valid during the call; vocab[string(b)] looks them up without a
// copy). fn returns false to stop.
func edits1(word string, buf []byte, fn func([]byte) bool) []byte {
	n := len(word)
	for i := 0; i <= n; i++ {
		if i < n { // delete
			buf = append(append(buf[:0], word[:i]...), word[i+1:]...)
			if !fn(buf) {
				return buf
			}
		}
		if i+1 < n { // swap
			buf = append(append(append(buf[:0], word[:i]...), word[i+1], word[i]), word[i+2:]...)
			if !fn(buf) {
				return buf
			}
		}
		for c := 0; c < len(letters); c++ {
			if i < n { // replace
				buf = append(append(append(buf[:0], word[:i]...), letters[c]), word[i+1:]...)
				if !fn(buf) {
					return buf
				}
			}
			// insert
			buf = append(append(append(buf[:0], word[:i]...), letters[c]), word[i:]...)
			if !fn(buf) {
				return buf
			}
		}
	}
	return buf
}

// better reports whether candidate c (seen n times) beats the best so far:
// more common, ties broken alphabetically so the answer never depends on
// the order edits are made in.
func better(c string, n int, best string, bestN int) bool {
	return n > bestN || (n == bestN && n > 0 && c < best)
}

func correctWord(w string, vocab map[string]int) string {
	if len(w) < 3 || vocab[w] >= common {
		return w
	}
	best, bestN := "", 0
	buf := make([]byte, 0, len(w)+2)
	buf = edits1(w, buf, func(c []byte) bool {
		if n := vocab[string(c)]; n > 0 && better(string(c), n, best, bestN) {
			best, bestN = string(c), n
		}
		return true
	})
	if own, known := vocab[w]; known {
		if best != "" && bestN >= rareRatio*own {
			return best
		}
		return w
	}
	if len(w) > 12 {
		if best != "" {
			return best
		}
		return w
	}
	// Two letters away wins only by a wide margin ("thive": thieves over
	// thrive), and only a word of about the same length with the same
	// first letter: otherwise "the" would answer every short typo.
	far, farN := "", 0
	inner := make([]byte, 0, len(w)+4)
	edits1(w, buf, func(e []byte) bool {
		inner = edits1(string(e), inner, func(c []byte) bool {
			if len(c) >= len(w)-1 && len(c) > 0 && c[0] == w[0] {
				if n := vocab[string(c)]; n > 0 && better(string(c), n, far, farN) {
					far, farN = string(c), n
				}
			}
			return true
		})
		return true
	})
	if best != "" && (far == "" || farN < 8*bestN) {
		return best
	}
	if far != "" {
		return far
	}
	return w
}

// Dict is a vocabulary with a memory of the words it has corrected: the
// two-letter search for one unknown word is the costly part (~8 ms for a
// 7-letter word), and the same typos come back again and again. The memory
// is bounded (memoMax words, then started afresh) and lives as long as the
// vocabulary it was made from.
type Dict struct {
	words map[string]int
	mu    sync.Mutex
	memo  map[string]string
}

const memoMax = 50_000

// NewDict wraps a vocabulary.
func NewDict(words map[string]int) *Dict { return &Dict{words: words, memo: map[string]string{}} }

// Len is how many words the vocabulary has.
func (d *Dict) Len() int { return len(d.words) }

func (d *Dict) word(w string) string {
	d.mu.Lock()
	got, ok := d.memo[w]
	d.mu.Unlock()
	if ok {
		return got
	}
	got = correctWord(w, d.words)
	d.mu.Lock()
	if len(d.memo) >= memoMax {
		d.memo = map[string]string{}
	}
	d.memo[w] = got
	d.mu.Unlock()
	return got
}

// Correct is Dict.Correct with a one-off dictionary.
func Correct(query string, vocab map[string]int) string { return NewDict(vocab).Correct(query) }

// Correct is the query with its typos fixed (lowercase), or as it was when
// nothing needed fixing or there is no vocabulary yet.
func (d *Dict) Correct(query string) string {
	vocab := d.words
	ws := words(strings.ToLower(query))
	if len(vocab) == 0 || len(ws) == 0 {
		return query
	}
	out := make([]string, 0, len(ws))
	for i := 0; i < len(ws); i++ {
		w := ws[i]
		// A word split by a stray space: "fo r" -> "for".
		if i+1 < len(ws) && vocab[w] < common && len(ws[i+1]) <= 2 && vocab[w+ws[i+1]] >= common {
			out = append(out, w+ws[i+1])
			i++
			continue
		}
		out = append(out, d.word(w))
	}
	fixed := strings.Join(out, " ")
	if fixed == strings.Join(ws, " ") {
		return query
	}
	return fixed
}

// Vocab reads the vocabulary from app_settings at most once per TTL in this
// process (it changes once a week, with the catalog).
type Vocab struct {
	q   *store.Queries
	ttl time.Duration

	mu   sync.Mutex
	at   time.Time
	dict *Dict
}

// NewVocab makes the reader.
func NewVocab(q *store.Queries) *Vocab { return &Vocab{q: q, ttl: time.Hour} }

// Get is the vocabulary (empty when the catalog job has not built one). A
// database failure is returned; a stored value that is not JSON is too
// (the Python silently searched without corrections forever).
func (v *Vocab) Get(ctx context.Context) (*Dict, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.dict != nil && time.Since(v.at) < v.ttl {
		return v.dict, nil
	}
	raw, err := v.q.GetAppSetting(ctx, VocabKey)
	words := map[string]int{}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal([]byte(raw), &words); err != nil {
			return nil, errors.New("the spelling vocabulary (app_settings catalog_vocab) is not valid JSON")
		}
	}
	v.dict, v.at = NewDict(words), time.Now()
	return v.dict, nil
}

// Forget drops the copy in memory (after the catalog job saved a new one,
// and in tests).
func (v *Vocab) Forget() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.dict = nil
}
