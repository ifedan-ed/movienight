// Package sidecar finds the subtitle files that sit next to a video
// ("Film.en.srt", "Film.eng.forced.ass", "Subs/English.srt") and reads
// their language and flags from the name. It also normalises a language
// code or name to the three-letter ISO 639-2/B form ("fre", "ger") that
// Movie Night's subtitle setting and Jellyfin both use.
//
// The scanner (which lists them for a library) and the media engine (which
// serves one by name) share it, so a name means the same file in both.
package sidecar

import (
	"path"
	"sort"
	"strings"
)

// Exts are the subtitle files read, by format: SubRip and WebVTT are
// turned into WebVTT; ASS and SSA are kept for the styled renderer.
var Exts = map[string]string{".srt": "subrip", ".vtt": "webvtt", ".ass": "ass", ".ssa": "ssa"}

// Folders next to a video whose subtitle files belong to it when it is the
// only video in its folder (release layouts: "Subs/English.srt").
var Folders = map[string]bool{"subs": true, "subtitles": true, "sub": true}

// File is one subtitle file of a video.
type File struct {
	Name     string // relative to the video's folder ("Film.en.srt", "Subs/2_English.srt")
	Codec    string // subrip, webvtt, ass or ssa
	Language string // ISO 639-2/B ("eng"), "" when the name names none
	Title    string // what is left of the name once language and flags are read ("SDH", "Commentary")
	Forced   bool
	Default  bool
	HI       bool // for the hard of hearing (SDH, CC, HI)
}

// Match lists the subtitle files of the video named video among names
// (the files in its folder) and subNames (the files in a Subs folder next
// to it, with that folder's name as their prefix: "Subs/English.srt").
// Only the video's own files are taken: "Film.en.srt" for "Film.mkv", not
// "Film 2.en.srt". Subs folder files are taken when onlyVideo is true.
// The order is stable (by name), so a track's position never depends on
// the order the file system lists them in.
func Match(video string, names, subNames []string, onlyVideo bool) []File {
	stem := strings.TrimSuffix(video, path.Ext(video))
	var out []File
	for _, n := range names {
		codec, ok := Exts[strings.ToLower(path.Ext(n))]
		if !ok {
			continue
		}
		base := strings.TrimSuffix(n, path.Ext(n))
		var rest string
		switch {
		case base == stem:
		case strings.HasPrefix(base, stem+"."):
			rest = base[len(stem)+1:]
		case onlyVideo && !strings.HasPrefix(strings.ToLower(base), strings.ToLower(stem)):
			// The only video in the folder: any subtitle there is its own
			// ("movie.mkv" with "English.srt").
			rest = base
		default:
			continue
		}
		out = append(out, describe(n, codec, rest))
	}
	if onlyVideo {
		for _, n := range subNames {
			codec, ok := Exts[strings.ToLower(path.Ext(n))]
			if !ok {
				continue
			}
			base := path.Base(strings.TrimSuffix(n, path.Ext(n)))
			// "2_English" (a numbered release) and "Film.en" both work.
			if i := strings.IndexByte(base, '_'); i > 0 && isDigits(base[:i]) {
				base = base[i+1:]
			}
			if strings.HasPrefix(base, stem+".") {
				base = base[len(stem)+1:]
			}
			out = append(out, describe(n, codec, base))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// describe reads language and flags from the words between the video's
// name and the extension: "en", "eng.forced", "English.SDH", "pt-BR".
func describe(name, codec, rest string) File {
	f := File{Name: name, Codec: codec}
	var other []string
	for _, w := range strings.FieldsFunc(rest, func(r rune) bool { return r == '.' || r == ' ' || r == '_' || r == '[' || r == ']' }) {
		lw := strings.ToLower(w)
		switch lw {
		case "forced", "foreign":
			f.Forced = true
			continue
		case "default":
			f.Default = true
			continue
		case "sdh", "cc", "hi":
			f.HI = true
			other = append(other, strings.ToUpper(lw))
			continue
		}
		if f.Language == "" {
			if l := Language(w); l != "" {
				f.Language = l
				continue
			}
		}
		other = append(other, w)
	}
	f.Title = strings.Join(other, " ")
	return f
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// lang is one language's names: ISO 639-1, 639-2/T and 639-2/B codes, and
// its English name.
type lang struct{ one, t, b, name string }

var langs = []lang{
	{"en", "eng", "eng", "english"}, {"es", "spa", "spa", "spanish"}, {"fr", "fra", "fre", "french"},
	{"de", "deu", "ger", "german"}, {"pt", "por", "por", "portuguese"}, {"it", "ita", "ita", "italian"},
	{"nl", "nld", "dut", "dutch"}, {"sv", "swe", "swe", "swedish"}, {"da", "dan", "dan", "danish"},
	{"no", "nor", "nor", "norwegian"}, {"nb", "nob", "nob", "bokmal"}, {"fi", "fin", "fin", "finnish"},
	{"pl", "pol", "pol", "polish"}, {"tr", "tur", "tur", "turkish"}, {"el", "ell", "gre", "greek"},
	{"ar", "ara", "ara", "arabic"}, {"he", "heb", "heb", "hebrew"}, {"hi", "hin", "hin", "hindi"},
	{"ja", "jpn", "jpn", "japanese"}, {"ko", "kor", "kor", "korean"}, {"zh", "zho", "chi", "chinese"},
	{"ru", "rus", "rus", "russian"}, {"uk", "ukr", "ukr", "ukrainian"}, {"cs", "ces", "cze", "czech"},
	{"sk", "slk", "slo", "slovak"}, {"hu", "hun", "hun", "hungarian"}, {"ro", "ron", "rum", "romanian"},
	{"bg", "bul", "bul", "bulgarian"}, {"hr", "hrv", "hrv", "croatian"}, {"sr", "srp", "srp", "serbian"},
	{"sl", "slv", "slv", "slovenian"}, {"et", "est", "est", "estonian"}, {"lv", "lav", "lav", "latvian"},
	{"lt", "lit", "lit", "lithuanian"}, {"is", "isl", "ice", "icelandic"}, {"ga", "gle", "gle", "irish"},
	{"cy", "cym", "wel", "welsh"}, {"ca", "cat", "cat", "catalan"}, {"eu", "eus", "baq", "basque"},
	{"gl", "glg", "glg", "galician"}, {"fa", "fas", "per", "persian"}, {"ur", "urd", "urd", "urdu"},
	{"bn", "ben", "ben", "bengali"}, {"ta", "tam", "tam", "tamil"}, {"te", "tel", "tel", "telugu"},
	{"ml", "mal", "mal", "malayalam"}, {"th", "tha", "tha", "thai"}, {"vi", "vie", "vie", "vietnamese"},
	{"id", "ind", "ind", "indonesian"}, {"ms", "msa", "may", "malay"}, {"tl", "tgl", "tgl", "tagalog"},
	{"fil", "fil", "fil", "filipino"}, {"sw", "swa", "swa", "swahili"}, {"af", "afr", "afr", "afrikaans"},
	{"yo", "yor", "yor", "yoruba"}, {"ig", "ibo", "ibo", "igbo"}, {"ha", "hau", "hau", "hausa"},
	{"zu", "zul", "zul", "zulu"}, {"am", "amh", "amh", "amharic"}, {"la", "lat", "lat", "latin"},
}

var byCode = func() map[string]string {
	m := map[string]string{}
	for _, l := range langs {
		for _, k := range []string{l.one, l.t, l.b, l.name} {
			m[k] = l.b
		}
	}
	// Names files and players also use.
	for k, v := range map[string]string{"pob": "por", "pt-br": "por", "pt_br": "por", "brazilian": "por",
		"zh-cn": "chi", "zh-tw": "chi", "zh-hans": "chi", "zh-hant": "chi", "chs": "chi", "cht": "chi",
		"es-419": "spa", "es-mx": "spa", "latino": "spa", "castilian": "spa", "fr-ca": "fre", "en-us": "eng",
		"en-gb": "eng", "nn": "nor", "nno": "nor", "flemish": "dut", "farsi": "per", "mandarin": "chi",
		"cantonese": "chi", "yue": "chi", "cmn": "chi"} {
		m[k] = v
	}
	return m
}()

// Language is s (a code in any of ISO 639's forms, a regional code such as
// "pt-BR", or an English name) as its 639-2/B code; "" when s is none of
// those. "und" (undetermined) is "".
func Language(s string) string {
	return byCode[strings.ToLower(strings.TrimSpace(s))]
}
