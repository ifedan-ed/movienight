package subtitles

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// Formats a stored subtitle can be in: WebVTT, which the player's own
// renderer draws, or ASS/SSA, which JASSUB draws with its styles.
const (
	FormatVTT = "vtt"
	FormatASS = "ass"
)

// maxText caps a subtitle's text once unpacked and converted: a long film's
// is 100 to 200 KB, a styled anime episode's up to a few MB.
const maxText = 8 << 20

// ErrFormat is a download Movie Night cannot turn into a track.
var (
	ErrFormat  = errors.New("this subtitle's format can't be shown")
	ErrArchive = errors.New("the download held no subtitle Movie Night can read")
)

// Prepare turns a download into a stored track's text: out of a zip if it
// is one, into UTF-8, and into WebVTT unless it is styled ASS.
func Prepare(data []byte, l Language, season, episode int) (format, body string, err error) {
	if isZip(data) {
		if data, err = unzip(data, season, episode); err != nil {
			return "", "", err
		}
	}
	if isRar(data) {
		return "", "", fmt.Errorf("%w: it is a RAR archive", ErrFormat)
	}
	text, err := toUTF8(data, l)
	if err != nil {
		return "", "", err
	}
	return Normalize(text)
}

func isZip(b []byte) bool { return bytes.HasPrefix(b, []byte("PK\x03\x04")) }
func isRar(b []byte) bool { return bytes.HasPrefix(b, []byte("Rar!\x1a\x07")) }

// subtitleExt ranks the files an archive may hold: the text formats, then
// MicroDVD. Anything else (an .nfo, a picture) is never picked.
var subtitleExt = map[string]int{".srt": 4, ".ass": 4, ".ssa": 4, ".vtt": 3, ".sub": 1}

// unzip picks the archive's subtitle: for an episode the one whose name has
// its number (a season pack holds them all), else the best format, then
// the biggest (the full one, not a forced-only file).
func unzip(data []byte, season, episode int) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: the archive is damaged", ErrArchive)
	}
	var best *zip.File
	bestScore := -1
	for _, f := range zr.File {
		name := strings.ToLower(f.Name)
		ext := path.Ext(name)
		if f.FileInfo().IsDir() || subtitleExt[ext] == 0 || strings.HasPrefix(name, "__macosx/") {
			continue
		}
		score := subtitleExt[ext] * 10
		if episode > 0 {
			if mentions(name, season, episode) {
				score += 100
			} else if mentionsAnyEpisode(name) {
				continue // another episode of a pack
			}
		}
		if best == nil || score > bestScore || (score == bestScore && f.UncompressedSize64 > best.UncompressedSize64) {
			best, bestScore = f, score
		}
	}
	if best == nil {
		return nil, ErrArchive
	}
	rc, err := best.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: the archive is damaged", ErrArchive)
	}
	defer rc.Close()
	// Read through a limit, never trusting the archive's own size: a
	// small zip can unpack into gigabytes.
	b, err := io.ReadAll(io.LimitReader(rc, maxText+1))
	if err != nil {
		return nil, fmt.Errorf("%w: the archive is damaged", ErrArchive)
	}
	if len(b) > maxText {
		return nil, fmt.Errorf("%w: the subtitle is too large", ErrFormat)
	}
	return b, nil
}

var episodeMark = regexp.MustCompile(`(?i)(?:s(\d{1,2})[ ._-]?e(\d{1,3})|(\d{1,2})x(\d{2,3})|\be(?:p|pisode)?[ ._-]?(\d{1,3})\b)`)

// mentions reports whether a file name names this episode (S01E02, 1x02,
// E02).
func mentions(name string, season, episode int) bool {
	for _, m := range episodeMark.FindAllStringSubmatch(name, -1) {
		s, e := atoi(m[1]+m[3]), atoi(m[2]+m[4]+m[5])
		if e == episode && (s == 0 || s == season) {
			return true
		}
	}
	return false
}

func mentionsAnyEpisode(name string) bool { return episodeMark.MatchString(name) }

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// toUTF8 decodes a subtitle's bytes: UTF-8 as it is (without a BOM),
// UTF-16 by its BOM or by its zero bytes, and anything else in the
// charset old subtitles in that language most often use (Windows-1250 for
// Czech, 1251 for Russian, GB18030 for Chinese, ...). Detecting a charset
// from the bytes alone guesses between ones that decode the same bytes
// differently; the language picks the right one where a guesser would
// often not.
func toUTF8(b []byte, l Language) (string, error) {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		b = b[3:]
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}), bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return decode(b, unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder())
	case looksUTF16(b):
		order := unicode.LittleEndian
		if b[0] == 0 {
			order = unicode.BigEndian
		}
		return decode(b, unicode.UTF16(order, unicode.IgnoreBOM).NewDecoder())
	}
	if utf8.Valid(b) {
		return string(b), nil
	}
	name := l.Legacy
	if name == "" {
		name = "windows-1252"
	}
	enc, err := htmlindex.Get(name)
	if err != nil {
		return "", err
	}
	return decode(b, enc.NewDecoder())
}

// looksUTF16 spots UTF-16 without a BOM: in Latin text every other byte is
// zero.
func looksUTF16(b []byte) bool {
	n := min(len(b), 2000) &^ 1
	if n < 20 {
		return false
	}
	zeros := 0
	for _, c := range b[:n] {
		if c == 0 {
			zeros++
		}
	}
	return zeros*10 > n*3
}

func decode(b []byte, t transform.Transformer) (string, error) {
	out, _, err := transform.Bytes(t, b)
	if err != nil {
		return "", fmt.Errorf("%w: its text couldn't be read", ErrFormat)
	}
	return strings.TrimPrefix(string(out), "\uFEFF"), nil
}

// Normalize turns subtitle text into a stored format, by what the text is
// rather than what its file is called (sites rename files freely): WebVTT is
// cleaned and kept, ASS/SSA kept as they are, SRT and MicroDVD become
// WebVTT.
func Normalize(text string) (format, body string, err error) {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	trimmed := strings.TrimSpace(text)
	var out string
	switch {
	case strings.HasPrefix(trimmed, "WEBVTT"):
		out, format = cuesToVTT(trimmed), FormatVTT
	case strings.Contains(trimmed[:min(len(trimmed), 4000)], "[Script Info]") || strings.Contains(trimmed, "\nDialogue:"):
		out, format = trimmed+"\n", FormatASS
	case microDVD.MatchString(trimmed):
		out, format = microToVTT(trimmed), FormatVTT
	case strings.Contains(trimmed, "-->"):
		out, format = cuesToVTT(trimmed), FormatVTT
	default:
		return "", "", ErrFormat
	}
	if format == FormatVTT && out == "WEBVTT\n" {
		return "", "", fmt.Errorf("%w: it has no lines", ErrFormat)
	}
	if len(out) > maxText {
		return "", "", fmt.Errorf("%w: the subtitle is too large", ErrFormat)
	}
	return format, out, nil
}

// stamp matches a cue time in SRT's or WebVTT's spelling, hours optional:
// 00:01:02,345 / 01:02.345 / 0:01:02.3
var (
	timing    = regexp.MustCompile(`^\s*((?:\d+:)?\d{1,2}:\d{1,2}[,.]\d{1,3})\s*-->\s*((?:\d+:)?\d{1,2}:\d{1,2}[,.]\d{1,3})`)
	assTag    = regexp.MustCompile(`\{\\[^}]*\}`)
	microDVD  = regexp.MustCompile(`^\{\d+\}\{\d*\}`)
	microLine = regexp.MustCompile(`^\{(\d+)\}\{(\d*)\}(.*)$`)
)

// parseStamp reads a cue time into milliseconds.
func parseStamp(s string) (int64, bool) {
	s = strings.Replace(s, ",", ".", 1)
	main, frac, _ := strings.Cut(s, ".")
	parts := strings.Split(main, ":")
	var secs int64
	for _, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return 0, false
		}
		secs = secs*60 + n
	}
	for len(frac) < 3 {
		frac += "0" // ".3" is 300 ms
	}
	ms, err := strconv.ParseInt(frac[:3], 10, 64)
	if err != nil {
		return 0, false
	}
	return secs*1000 + ms, true
}

func vttStamp(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// cuesToVTT rebuilds SRT or WebVTT cues as clean WebVTT: numbered cues,
// times in one spelling, ASS override tags ({\an8}, common in SRT files)
// removed, and an arrow inside a line (which would end the cue) replaced.
// A cue without text, or one that ends before it starts, is dropped.
func cuesToVTT(text string) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n")
	n := 0
	for _, block := range strings.Split(text, "\n\n") {
		lines := strings.Split(strings.Trim(block, "\n"), "\n")
		at := -1
		for i, l := range lines {
			if timing.MatchString(l) {
				at = i
				break
			}
		}
		if at < 0 {
			continue // the header, a NOTE, a STYLE block, a stray number
		}
		m := timing.FindStringSubmatch(lines[at])
		start, ok1 := parseStamp(m[1])
		end, ok2 := parseStamp(m[2])
		var body []string
		for _, l := range lines[at+1:] {
			l = strings.TrimSpace(assTag.ReplaceAllString(l, ""))
			if l != "" {
				body = append(body, strings.ReplaceAll(l, "-->", "→"))
			}
		}
		if !ok1 || !ok2 || end <= start || len(body) == 0 {
			continue
		}
		n++
		fmt.Fprintf(&b, "\n%d\n%s --> %s\n%s\n", n, vttStamp(start), vttStamp(end), strings.Join(body, "\n"))
	}
	return b.String()
}

// microFPS is MicroDVD's frame rate when the file does not name one in its
// first line ({1}{1}25.000): film's 23.976, by far the most common rate
// these files were made for.
const microFPS = 23.976

// microToVTT converts MicroDVD ({start frame}{end frame}text, "|" between
// lines) to WebVTT.
func microToVTT(text string) string {
	fps := microFPS
	var cues []string
	n := 0
	for i, line := range strings.Split(text, "\n") {
		m := microLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		if i == 0 && m[1] == m[2] {
			if f, err := strconv.ParseFloat(strings.TrimSpace(m[3]), 64); err == nil && f > 1 && f < 200 {
				fps = f
				continue
			}
		}
		start, _ := strconv.ParseInt(m[1], 10, 64)
		end, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil || end <= start {
			end = start + int64(fps*3) // an open end: three seconds
		}
		body := strings.TrimSpace(microStyle.ReplaceAllString(m[3], ""))
		if body == "" {
			continue
		}
		body = strings.ReplaceAll(strings.ReplaceAll(body, "|", "\n"), "-->", "→")
		n++
		cues = append(cues, fmt.Sprintf("%d\n%s --> %s\n%s\n", n, vttStamp(int64(float64(start)*1000/fps)),
			vttStamp(int64(float64(end)*1000/fps)), body))
	}
	return "WEBVTT\n" + prefixEach("\n", cues)
}

// microStyle is MicroDVD's {y:i}-style formatting codes.
var microStyle = regexp.MustCompile(`\{[a-zA-Z]:[^}]*\}`)

func prefixEach(p string, s []string) string {
	var b strings.Builder
	for _, x := range s {
		b.WriteString(p + x)
	}
	return b.String()
}
