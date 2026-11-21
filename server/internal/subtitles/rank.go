package subtitles

import (
	"maps"
	"math"
	"path"
	"sort"
	"strings"
	"unicode"
)

// Rank orders results best first:
//
//  1. a moviehash match: timed against this very file;
//  2. the release name most like the file's name (similarity, below);
//  3. more downloads, then a better rating: what other people kept.
//
// Similarity is compared in steps of 0.05, so two near-equal names are
// told apart by their downloads rather than by one stray word.
func Rank(results []Result, fileName string) {
	file := tokens(stripExt(fileName))
	for i := range results {
		results[i].score = similarity(file, results[i].Release)
	}
	sort.SliceStable(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if a.HashMatch != b.HashMatch {
			return a.HashMatch
		}
		if sa, sb := math.Round(a.score*20), math.Round(b.score*20); sa != sb {
			return sa > sb
		}
		if a.Downloads != b.Downloads {
			return a.Downloads > b.Downloads
		}
		return a.Rating > b.Rating
	})
}

// similarity of a release name to the file's name, 0 to 1: the share of
// words they have in common (Jaccard), with the release group (the word
// after the last "-", which names who made the rip) counting double. A
// result listing several releases scores by its best one.
func similarity(file []string, releases string) float64 {
	if len(file) == 0 {
		return 0
	}
	best := 0.0
	for _, rel := range strings.FieldsFunc(releases, func(r rune) bool { return r == '\n' || r == '|' }) {
		best = math.Max(best, jaccard(file, tokens(stripExt(rel))))
	}
	return best
}

func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	in := map[string]bool{}
	for _, t := range a {
		in[t] = true
	}
	union := maps.Clone(in)
	common := 0
	seen := map[string]bool{}
	for _, t := range b {
		union[t] = true
		if in[t] && !seen[t] {
			common++
			seen[t] = true
		}
	}
	score := float64(common) / float64(len(union))
	// The release group: both names ending in the same group is the
	// strongest sign of the same rip after the hash.
	if a[len(a)-1] == b[len(b)-1] && len(a[len(a)-1]) > 1 {
		score += (1 - score) * 0.25
	}
	return score
}

// tokens splits a name into lowercase words and numbers: dots, dashes,
// spaces, brackets and underscores all separate. "WEB-DL" becomes "web",
// "dl"; "x264" stays one word.
func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// stripExt drops a file's folders and its extension when it looks like
// one (".mkv", ".srt"), not a release's last word (".x264").
func stripExt(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".mkv", ".mp4", ".avi", ".m4v", ".mov", ".ts", ".webm", ".wmv", ".srt", ".ass", ".ssa", ".sub", ".vtt", ".zip":
		return strings.TrimSuffix(name, path.Ext(name))
	}
	return name
}
