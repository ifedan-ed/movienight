// Package naming reads what a video file is from its path: a film's title
// and year, or a show's title with the season and episode, and any TMDB,
// IMDb or TVDB id written into a folder or file name.
//
// It understands the layouts people actually have on disk: Radarr's and
// Sonarr's ("Film (2010) [tmdbid-27205]/Film (2010) - 1080p.mkv",
// "Show (2008) {tvdb-81189}/Season 01/Show - S01E01 - Pilot.mkv"), Plex's
// and Jellyfin's ("{tmdb-123}", "[imdbid-tt0137523]"), and scene release
// names ("Film.Name.2010.1080p.BluRay.x264-GROUP.mkv"). It is pure string
// work with no file system access, so the scanner and the tests share it.
package naming

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Kind is what a library holds; it decides how a path is read.
type Kind string

const (
	Movies Kind = "movies"
	Shows  Kind = "shows"
)

// Parsed is what a path says about its video.
type Parsed struct {
	Title string
	Year  int // 0 when the name has none
	// The ids written in the names, the nearest folder first. Any of them
	// settles the match without a search.
	TMDB int
	IMDb string // "tt0137523"
	TVDB int
	// Shows only. EpisodeEnd is the last episode of a multi-episode file
	// ("S01E01-E02"); 0 for a single episode.
	Season, Episode, EpisodeEnd int
	// Skip is set for files that are not the film or episode itself:
	// trailers, samples, extras. The scanner leaves them out.
	Skip bool
}

var (
	// Ids in folder or file names: [tmdbid-123], {tmdb-123}, [tmdb=123],
	// (imdbid-tt123), {imdb-tt123}, [tvdbid-123], {tvdb-123}.
	tmdbRe = regexp.MustCompile(`(?i)[\[{(]tmdb(?:id)?[-=: ]?(\d{1,9})[\]})]`)
	imdbRe = regexp.MustCompile(`(?i)[\[{(]imdb(?:id)?[-=: ]?(tt\d{5,10})[\]})]`)
	tvdbRe = regexp.MustCompile(`(?i)[\[{(]tvdb(?:id)?[-=: ]?(\d{1,9})[\]})]`)
	// A bare IMDb id anywhere in a scene name ("Film.2010.tt0137523.mkv").
	bareIMDbRe = regexp.MustCompile(`(?i)\b(tt\d{7,10})\b`)
	anyIDRe    = regexp.MustCompile(`(?i)[\[{(](?:tmdb|imdb|tvdb)(?:id)?[-=: ]?[a-z]*\d+[\]})]`)

	// Season and episode: S01E02, s1e2, S01E02E03, S01E02-E03, S01E02-03,
	// S01.E02, S01 E02, 1x02, 1x02-1x03.
	sxeRe    = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,3})[ ._-]?e(\d{1,4})(?:(?:[-_ ]?e|-)(\d{1,4}))?`)
	xRe      = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(\d{1,2})x(\d{2,3})(?:[-_]\d{1,2}x(\d{2,3}))?(?:[^a-z0-9]|$)`)
	epOnlyRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:e|ep|episode)[ ._-]?(\d{1,4})(?:[^a-z0-9]|$)`)
	// "Season 1", "Season 01", "S01", "Specials" as a folder.
	seasonDirRe = regexp.MustCompile(`(?i)^(?:season|series|saison|staffel|temporada)[ ._-]*(\d{1,3})$|^s(\d{1,3})$`)
	specialsRe  = regexp.MustCompile(`(?i)^specials?$`)

	// A year in a name: 1900 to 2099, standing on its own.
	yearRe = regexp.MustCompile(`(?:^|[^0-9])((?:19|20)\d\d)(?:[^0-9]|$)`)
	// Brackets around a year: "(2010)" or "[2010]".
	bracketYearRe = regexp.MustCompile(`[(\[]((?:19|20)\d\d)[)\]]`)
	spacesRe      = regexp.MustCompile(`\s+`)
	bracketsRe    = regexp.MustCompile(`\[[^\]]*\]|\{[^}]*\}`)
	// Trailing punctuation left over once tags are cut.
	trimSet = " -_.,([{"
)

// tags are words that start the technical part of a scene name: everything
// from the first one on is not the title. Matched as whole words, any case.
var tags = map[string]bool{
	"2160p": true, "1080p": true, "1080i": true, "720p": true, "576p": true, "480p": true, "4k": true, "uhd": true,
	"bluray": true, "blu-ray": true, "bdrip": true, "brrip": true, "bdremux": true, "remux": true, "web": true,
	"web-dl": true, "webdl": true, "webrip": true, "hdtv": true, "pdtv": true, "dvdrip": true, "dvd": true,
	"dvdscr": true, "hdrip": true, "hdcam": true, "cam": true, "x264": true, "x265": true, "h264": true,
	"h265": true, "h.264": true, "h.265": true, "hevc": true, "avc": true, "xvid": true, "divx": true, "av1": true,
	"10bit": true, "8bit": true, "hdr": true, "hdr10": true, "dv": true, "dovi": true, "sdr": true, "aac": true,
	"ac3": true, "eac3": true, "dts": true, "dts-hd": true, "truehd": true, "atmos": true, "ddp5": true,
	"dd5": true, "flac": true, "proper": true, "repack": true, "extended": true, "unrated": true, "uncut": true,
	"remastered": true, "imax": true, "multi": true, "dubbed": true, "subbed": true, "internal": true,
	"limited": true, "complete": true, "nf": true, "amzn": true, "dsnp": true, "hmax": true, "atvp": true,
}

// extraWords mark a file or folder of extras: a trailer, a sample, a
// featurette. Matched against a whole folder name, or a file name's last
// "-word" (Plex's and Jellyfin's "Film-trailer.mkv").
var extraWords = map[string]bool{
	"trailer": true, "trailers": true, "sample": true, "samples": true, "extra": true, "extras": true,
	"featurette": true, "featurettes": true, "behindthescenes": true, "behind the scenes": true,
	"deleted scenes": true, "deleted": true, "deletedscenes": true, "interview": true, "interviews": true,
	"scene": true, "scenes": true, "short": true, "shorts": true, "other": true, "others": true, "clip": true,
	"clips": true, "bonus": true,
}

// Parse reads rel, a video's path relative to its library folder (forward
// slashes), as a film or an episode by the library's kind.
func Parse(rel string, kind Kind) Parsed {
	rel = strings.Trim(path.Clean(strings.ReplaceAll(rel, "\\", "/")), "/")
	dirs := strings.Split(rel, "/")
	file := dirs[len(dirs)-1]
	dirs = dirs[:len(dirs)-1]
	stem := strings.TrimSuffix(file, path.Ext(file))

	var p Parsed
	if isExtra(stem, dirs) {
		p.Skip = true
	}
	// Ids: the file first, then folders from the nearest up, so a file's own
	// id wins over its folder's.
	for i := len(dirs); i >= 0; i-- {
		name := stem
		if i < len(dirs) {
			name = dirs[i]
		}
		ids(name, &p)
	}
	if kind == Shows {
		parseEpisode(stem, dirs, &p)
		return p
	}
	parseFilm(stem, dirs, &p)
	return p
}

func ids(name string, p *Parsed) {
	if p.TMDB == 0 {
		if m := tmdbRe.FindStringSubmatch(name); m != nil {
			p.TMDB, _ = strconv.Atoi(m[1])
		}
	}
	if p.IMDb == "" {
		if m := imdbRe.FindStringSubmatch(name); m != nil {
			p.IMDb = strings.ToLower(m[1])
		} else if m := bareIMDbRe.FindStringSubmatch(name); m != nil {
			p.IMDb = strings.ToLower(m[1])
		}
	}
	if p.TVDB == 0 {
		if m := tvdbRe.FindStringSubmatch(name); m != nil {
			p.TVDB, _ = strconv.Atoi(m[1])
		}
	}
}

func isExtra(stem string, dirs []string) bool {
	for _, d := range dirs {
		if extraWords[strings.ToLower(strings.TrimSpace(d))] {
			return true
		}
	}
	low := strings.ToLower(stem)
	if i := strings.LastIndexAny(low, "-._"); i >= 0 {
		if extraWords[strings.TrimSpace(low[i+1:])] {
			return true
		}
	}
	return low == "sample" || strings.HasPrefix(low, "sample-") || strings.HasPrefix(low, "sample.")
}

// parseFilm takes the title from the film's own folder when that folder
// names it (it carries a year or an id: Radarr's layout), else from the
// file name (a scene name in a flat folder).
func parseFilm(stem string, dirs []string, p *Parsed) {
	if len(dirs) > 0 {
		folder := dirs[len(dirs)-1]
		if t, y := titleYear(folder); t != "" && (y > 0 || anyIDRe.MatchString(folder)) {
			p.Title, p.Year = t, y
			return
		}
	}
	p.Title, p.Year = titleYear(stem)
	if p.Title == "" && len(dirs) > 0 {
		// A file named only "1080p.mkv" or "movie.mkv" in its film's folder.
		p.Title, p.Year = titleYear(dirs[len(dirs)-1])
	}
}

// parseEpisode finds the season and episode in the file name (or the
// season in its folder), and the show from the show's folder: the first
// folder under the library, or, for files straight in the library folder,
// the part of the file name before the episode number.
func parseEpisode(stem string, dirs []string, p *Parsed) {
	cut := -1
	if m := sxeRe.FindStringSubmatchIndex(stem); m != nil {
		p.Season = atoi(stem[m[2]:m[3]])
		p.Episode = atoi(stem[m[4]:m[5]])
		if m[6] >= 0 {
			p.EpisodeEnd = atoi(stem[m[6]:m[7]])
		}
		cut = m[0]
	} else if m := xRe.FindStringSubmatchIndex(stem); m != nil {
		p.Season = atoi(stem[m[2]:m[3]])
		p.Episode = atoi(stem[m[4]:m[5]])
		if m[6] >= 0 {
			p.EpisodeEnd = atoi(stem[m[6]:m[7]])
		}
		cut = m[0]
	} else if m := epOnlyRe.FindStringSubmatchIndex(stem); m != nil {
		// "Episode 3" inside a "Season 2" folder.
		p.Episode = atoi(stem[m[2]:m[3]])
		p.Season = -1
		cut = m[0]
	}
	if p.EpisodeEnd <= p.Episode {
		p.EpisodeEnd = 0
	}
	// The season folder, when the name left the season out (or to check it).
	seasonDir := -1
	for i := len(dirs) - 1; i >= 0; i-- {
		d := strings.TrimSpace(dirs[i])
		if m := seasonDirRe.FindStringSubmatch(d); m != nil {
			if p.Season < 0 {
				p.Season = atoi(m[1] + m[2])
			}
			seasonDir = i
			break
		}
		if specialsRe.MatchString(d) {
			if p.Season < 0 {
				p.Season = 0
			}
			seasonDir = i
			break
		}
	}
	if p.Season < 0 {
		p.Season, p.Episode = 0, 0 // an episode number with no season anywhere
	}
	// The show's folder is the first one under the library, unless that is
	// already a season folder (a library pointed at one show).
	if len(dirs) > 0 && seasonDir != 0 {
		p.Title, p.Year = titleYear(dirs[0])
	}
	if p.Title == "" && cut > 0 {
		p.Title, p.Year = titleYear(stem[:cut])
	}
}

// titleYear cleans a folder or file name into a title and its year.
// The year is the last free-standing year in the name, unless that would
// leave no title ("1917 (2019)" is 1917 from 2019; "2012.mkv" is "2012").
// Everything after the year, or from the first technical tag, is dropped.
func titleYear(name string) (string, int) {
	name = anyIDRe.ReplaceAllString(name, " ")
	name = bareIMDbRe.ReplaceAllString(name, " ")
	// Square and curly brackets hold tags and release groups ("[1080p]",
	// "[Group]"), except a bracketed year, which is kept.
	name = bracketsRe.ReplaceAllStringFunc(name, func(m string) string {
		if bracketYearRe.MatchString(m) && len(m) == 6 {
			return m
		}
		return " "
	})
	// A bracketed year is the surest: "Blade Runner 2049 (2017)".
	if m := bracketYearRe.FindStringSubmatchIndex(name); m != nil {
		title := clean(name[:m[0]])
		if title != "" {
			return title, atoi(name[m[2]:m[3]])
		}
	}
	words := splitWords(name)
	// Cut at the first tag word.
	for i, w := range words {
		if tags[strings.ToLower(w)] || strings.HasPrefix(strings.ToLower(w), "ddp") {
			words = words[:i]
			break
		}
	}
	// The last year that leaves a title before it.
	for i := len(words) - 1; i >= 1; i-- {
		if y, ok := asYear(words[i]); ok {
			title := clean(strings.Join(words[:i], " "))
			if title != "" {
				return title, y
			}
		}
	}
	return clean(strings.Join(words, " ")), 0
}

// splitWords splits on spaces, or on dots and underscores when the name
// uses those instead of spaces (a scene name). "Mr. Robot" keeps its dot.
func splitWords(name string) []string {
	if !strings.Contains(strings.TrimSpace(name), " ") || strings.Count(name, ".")+strings.Count(name, "_") > strings.Count(name, " ") {
		name = strings.NewReplacer(".", " ", "_", " ").Replace(name)
	}
	return strings.Fields(name)
}

func asYear(w string) (int, bool) {
	w = strings.Trim(w, "()[]-,")
	if len(w) != 4 || !yearRe.MatchString(w) {
		return 0, false
	}
	return atoi(w), true
}

func clean(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	s = spacesRe.ReplaceAllString(s, " ")
	s = strings.Trim(s, trimSet)
	s = strings.TrimSuffix(s, " -")
	return strings.TrimSpace(s)
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }
