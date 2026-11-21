package config

import (
	"fmt"
	"slices"
	"strings"
)

// The online subtitle sources the player's "Search online" asks
// (internal/subtitles). Their names are what SUBTITLE_SOURCES lists.
const (
	SourceOpenSubtitles = "opensubtitles"
	SourceSubDL         = "subdl"
	SourceSubSource     = "subsource"
	SourceJimaku        = "jimaku"
	SourcePodnapisi     = "podnapisi"
	SourceGestdown      = "gestdown"
)

// SubtitleSourceNames is every source, in the order they are described.
var SubtitleSourceNames = []string{SourceOpenSubtitles, SourceSubDL, SourceSubSource, SourceJimaku, SourcePodnapisi, SourceGestdown}

// Subtitles is the subtitle sources' settings. Every one is optional: a
// source that needs a key is off without it, and the two that need none
// (Podnapisi, Gestdown) are on. SUBTITLE_SOURCES is the one switch that
// says exactly which are on instead.
type Subtitles struct {
	OpenSubtitlesAPIKey   string // OPENSUBTITLES_API_KEY: search, and downloads within the key's own allowance
	OpenSubtitlesUsername string // OPENSUBTITLES_USERNAME and _PASSWORD: downloads count against this account's quota
	OpenSubtitlesPassword string
	SubDLAPIKey           string // SUBDL_API_KEY
	SubSourceAPIKey       string // SUBSOURCE_API_KEY
	JimakuAPIKey          string // JIMAKU_API_KEY (anime, mostly Japanese)
	// Sources is the sources that are on, in SubtitleSourceNames order.
	Sources []string
}

// On reports whether a source is on.
func (s Subtitles) On(name string) bool { return slices.Contains(s.Sources, name) }

func (r *reader) subtitles() Subtitles {
	s := Subtitles{
		OpenSubtitlesAPIKey:   r.optional("OPENSUBTITLES_API_KEY"),
		OpenSubtitlesUsername: r.optional("OPENSUBTITLES_USERNAME"),
		OpenSubtitlesPassword: r.optional("OPENSUBTITLES_PASSWORD"),
		SubDLAPIKey:           r.optional("SUBDL_API_KEY"),
		SubSourceAPIKey:       r.optional("SUBSOURCE_API_KEY"),
		JimakuAPIKey:          r.optional("JIMAKU_API_KEY"),
	}
	// A username without its password (or the other way round) is a
	// half-configured login, and either without the key cannot be used at
	// all: both are stopped here rather than quietly searched without.
	if (s.OpenSubtitlesUsername == "") != (s.OpenSubtitlesPassword == "") {
		r.fail("OPENSUBTITLES_USERNAME and OPENSUBTITLES_PASSWORD go together: set both or neither")
	}
	if s.OpenSubtitlesUsername != "" && s.OpenSubtitlesAPIKey == "" {
		r.fail("OPENSUBTITLES_USERNAME is set but OPENSUBTITLES_API_KEY is not")
	}
	keys := map[string]string{
		SourceOpenSubtitles: s.OpenSubtitlesAPIKey, SourceSubDL: s.SubDLAPIKey,
		SourceSubSource: s.SubSourceAPIKey, SourceJimaku: s.JimakuAPIKey,
	}
	keyName := map[string]string{
		SourceOpenSubtitles: "OPENSUBTITLES_API_KEY", SourceSubDL: "SUBDL_API_KEY",
		SourceSubSource: "SUBSOURCE_API_KEY", SourceJimaku: "JIMAKU_API_KEY",
	}
	listed := r.list("SUBTITLE_SOURCES", true)
	if len(listed) == 0 {
		// Unset: every source that can work is on.
		for _, name := range SubtitleSourceNames {
			if k, needsKey := keys[name]; !needsKey || k != "" {
				s.Sources = append(s.Sources, name)
			}
		}
		return s
	}
	if len(listed) == 1 && listed[0] == "none" {
		s.Sources = []string{}
		return s
	}
	for _, name := range listed {
		if !slices.Contains(SubtitleSourceNames, name) {
			r.fail(fmt.Sprintf("SUBTITLE_SOURCES: %q is not a source (%s, or none)", name, strings.Join(SubtitleSourceNames, ", ")))
			continue
		}
		if k, needsKey := keys[name]; needsKey && k == "" {
			r.fail(fmt.Sprintf("SUBTITLE_SOURCES lists %s but %s is not set", name, keyName[name]))
		}
	}
	for _, name := range SubtitleSourceNames {
		if slices.Contains(listed, name) {
			s.Sources = append(s.Sources, name)
		}
	}
	return s
}
