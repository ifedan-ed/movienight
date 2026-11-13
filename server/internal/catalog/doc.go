package catalog

import (
	"crypto/sha1"
	"encoding/hex"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"movienight/server/internal/jsonx"
	"movienight/server/internal/titles"
)

// tmdbFields are what a search card needs, kept with each document so a hit
// becomes a card without another TMDB call.
var tmdbFields = []string{"id", "title", "name", "original_title", "original_name", "release_date", "first_air_date",
	"poster_path", "backdrop_path", "overview", "genre_ids", "vote_average", "vote_count"}

// pyFloat is a number Python would write as a float (vote_average). The
// documents' hashes must be the Python's to the byte: the index already
// holds ~200,000 documents hashed by the Python app, and a hash that
// differed would send every one of them again, re-embedding the whole
// catalog for nothing. TMDB sends vote_average as a float (7.0, not 7).
type pyFloat float64

// Doc turns one TMDB discover result into one catalog document, with its
// change hash "h"; nil without an id or a title (catalog_doc).
func Doc(r jsonx.Obj, media string) map[string]any {
	id, ok := jsonx.Int(r, "id")
	titleKey, originalKey, dateKey, prefix, table := "title", "original_title", "release_date", "m", titles.MovieGenres
	if media == "tv" {
		titleKey, originalKey, dateKey, prefix, table = "name", "original_name", "first_air_date", "t", titles.TVGenres
	}
	title := strings.TrimSpace(jsonx.Str(r, titleKey))
	if !ok || id <= 0 || title == "" {
		return nil
	}
	original := strings.TrimSpace(jsonx.Str(r, originalKey))
	if original == "" {
		original = title
	}
	date := jsonx.Str(r, dateKey)
	var year any
	if len(date) >= 4 {
		if y, err := strconv.Atoi(date[:4]); err == nil && y >= 0 {
			year = y
		}
	}
	genres := []any{}
	var genreNames []string
	for _, g := range jsonx.Ints(r, "genre_ids") {
		if name, ok := table[g]; ok {
			genres = append(genres, name)
			genreNames = append(genreNames, name)
		}
	}
	overview := strings.Join(strings.Fields(jsonx.Str(r, "overview")), " ")
	parts := []string{title}
	if original != title {
		parts = append(parts, original)
	}
	if year != nil {
		parts = append(parts, "("+strconv.Itoa(year.(int))+")")
	}
	if len(genreNames) > 0 {
		parts = append(parts, strings.Join(genreNames, ", "))
	}
	if overview != "" {
		parts = append(parts, overview)
	}
	votes, _ := jsonx.Int(r, "vote_count")

	kept := map[string]any{"media_type": media}
	for _, k := range tmdbFields {
		v, present := r[k]
		if !present || v == nil {
			continue
		}
		kept[k] = pyValue(k, v)
	}
	doc := map[string]any{
		"id": prefix + strconv.Itoa(id), "tmdb_id": id, "media_type": media, "title": title,
		"original_title": original, "year": year, "genres": genres, "overview": overview, "votes": votes,
		"searchText": strings.Join(parts, " | "), "tmdb": kept,
	}
	sum := sha1.Sum([]byte(pyJSON(doc)))
	doc["h"] = hex.EncodeToString(sum[:])[:16]
	return doc
}

// pyValue gives a TMDB value the type Python had for it: vote_average a
// float, every other number an int (JSON decodes all numbers as float64).
func pyValue(key string, v any) any {
	switch x := v.(type) {
	case float64:
		if key == "vote_average" {
			return pyFloat(x)
		}
		if x == float64(int64(x)) {
			return int64(x)
		}
		return pyFloat(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = pyValue(key, e)
		}
		return out
	}
	return v
}

// pyJSON writes v exactly as Python's json.dumps(v, sort_keys=True) does:
// ", " and ": " separators, keys sorted, every non-ASCII character as a
// \uXXXX escape (ensure_ascii), floats as repr writes them.
func pyJSON(v any) string {
	var b strings.Builder
	writePy(&b, v)
	return b.String()
}

func writePy(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writePyString(b, x)
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case pyFloat:
		b.WriteString(pyRepr(float64(x)))
	case float64:
		writePy(b, pyFloat(x))
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writePy(b, e)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys) // Python sorts by code point; Go's string order is byte order, the same for UTF-8
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			writePyString(b, k)
			b.WriteString(": ")
			writePy(b, x[k])
		}
		b.WriteByte('}')
	default:
		panic("pyJSON: unsupported type") // only Doc's own values reach here
	}
}

// pyRepr is Python's repr of a float: plain digits with at least one
// decimal (7.0) from 1e-4 up to 1e16, an exponent (1e+16, 1.5e-05) outside
// that range. Both are the shortest spelling that reads back the same.
func pyRepr(f float64) string {
	if a := math.Abs(f); a != 0 && (a < 1e-4 || a >= 1e16) {
		s := strconv.FormatFloat(f, 'e', -1, 64) // Go writes 1e+16 and 1.5e-05, as Python
		return s
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

const hexDigits = "0123456789abcdef"

func writePyString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r >= 0x20 && r < 0x7f {
				b.WriteRune(r)
				continue
			}
			if r == utf8.RuneError {
				r = 0xfffd
			}
			if r > 0xffff { // a surrogate pair, as Python writes it
				r -= 0x10000
				writeU(b, 0xd800+(r>>10))
				writeU(b, 0xdc00+(r&0x3ff))
				continue
			}
			writeU(b, r)
		}
	}
	b.WriteByte('"')
}

func writeU(b *strings.Builder, r rune) {
	b.WriteString(`\u`)
	for shift := 12; shift >= 0; shift -= 4 {
		b.WriteByte(hexDigits[(r>>uint(shift))&0xf])
	}
}
