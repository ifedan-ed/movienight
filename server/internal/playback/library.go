package playback

// Play here from Movie Night's own libraries: the file is on the media
// service, which plays it directly, remuxed or transcoded as the plan
// (package plan) says. Everything else is the same play as from Jellyfin:
// the same ticket, the same answer, the same reports, resume points and
// "watched at the end". There is just no Jellyfin to tell.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"movienight/server/internal/apierr"
	"movienight/server/internal/cache"
	"movienight/server/internal/clients"
	"movienight/server/internal/jsonx"
	"movienight/server/internal/libraries"
	"movienight/server/internal/media/plan"
	"movienight/server/internal/media/probe"
	"movienight/server/internal/store"
	"movienight/server/internal/stream"
	"movienight/server/internal/titles"
)

// libraryFile is the library file to play for the request: the film, or
// the episode asked for (the next one to watch when none is named).
func (s *Service) libraryFile(ctx context.Context, u store.User, media string, tmdbID int, season, episode *int) (*libraries.File, error) {
	if s.library == nil || !s.library.Enabled() {
		return nil, nil
	}
	if media == "movie" {
		return s.library.FileFor(ctx, "movie", tmdbID, nil, nil)
	}
	if season == nil || episode == nil {
		sn, en, ok, err := s.NextEpisode(ctx, u.ID, tmdbID)
		if err != nil || !ok {
			return nil, err
		}
		season, episode = &sn, &en
	}
	return s.library.FileFor(ctx, "tv", tmdbID, season, episode)
}

// itemOf is a library file's item id in a ticket.
func itemOf(f *libraries.File) string { return "lib-" + strconv.FormatInt(f.ID, 10) }

// Too many transcodes: said before the player starts, rather than as a
// failing stream.
var errBusy = apierr.New(http.StatusServiceUnavailable, "Too many films are being converted right now. Try again in a moment.")

// watchLibrary is Watch for a library file.
func (s *Service) watchLibrary(ctx context.Context, u store.User, media string, tmdbID int, f *libraries.File,
	r WatchRequest, prev *Claims) (Watch, error) {
	if prev != nil && prev.Item != itemOf(f) {
		return Watch{}, errNoTicket
	}
	pl, err := plan.Decide(f.Probe, r.Caps.plan(), r.Audio, r.Burn)
	switch {
	case errors.Is(err, plan.ErrNoAudio):
		return Watch{}, apierr.New(http.StatusUnprocessableEntity, "The file has no such audio track")
	case errors.Is(err, plan.ErrNoSubtitle):
		return Watch{}, apierr.New(http.StatusUnprocessableEntity, "Only picture subtitles are burnt in")
	case err != nil:
		return Watch{}, errNotPlayable
	}
	media0 := s.library.Media()
	if prev != nil {
		// The play goes on with other tracks: its old ffmpeg ends now (the
		// new one starts when the player asks for the new playlist).
		if err := media0.Stop(ctx, prev.Play); err != nil {
			s.log.InfoContext(ctx, "stopping the old play's ffmpeg failed", "err", err)
		}
	}
	if pl.Mode == plan.Transcode {
		if st, err := media0.Status(ctx); err == nil && st.Transcodes >= st.MaxTranscodes {
			return Watch{}, errBusy
		}
	}

	p := f.Probe
	out := Watch{Media: media, TMDBID: tmdbID, Key: titles.Key(media, tmdbID), Duration: p.Duration, Source: "library",
		Audio: []Track{}, Subs: []Track{}, Chapters: []Mark{}, Segments: []Stretch{}, Fonts: []string{}}
	meta, err := s.titles.One(ctx, media, tmdbID)
	if err != nil {
		return Watch{}, err
	}
	out.Title = fmt.Sprintf("%s %d", media, tmdbID)
	if meta != nil && meta.Title != "" {
		out.Title = meta.Title
		if meta.BackdropPath != nil && *meta.BackdropPath != "" {
			out.Backdrop = "https://image.tmdb.org/t/p/w1280" + *meta.BackdropPath
		}
	}
	if media == "tv" && f.Season != nil && f.Episode != nil {
		out.Season, out.Episode = f.Season, f.Episode
		out.Title = fmt.Sprintf("%s · S%02dE%02d · %s", out.Title, *f.Season, *f.Episode, s.episodeName(ctx, tmdbID, *f.Season, *f.Episode))
		if next, err := s.libraryNext(ctx, tmdbID, *f.Season, *f.Episode); err != nil {
			s.log.InfoContext(ctx, "reading the next library episode failed", "err", err)
		} else {
			out.Next = next
		}
	}
	for _, c := range p.Chapters {
		out.Chapters = append(out.Chapters, Mark{Start: c.Start, Name: c.Title})
	}
	out.Segments = ChapterSegments(p.Chapters)
	credits := 0
	for _, sg := range out.Segments {
		if sg.Kind == "credits" && (credits == 0 || int(sg.Start) < credits) {
			credits = int(sg.Start)
		}
	}

	if prev != nil {
		out.Play = prev.Play
	} else {
		if out.Play, err = s.startPlay(ctx, u, media, tmdbID, out, s.jellyfinTwin(ctx, u, media, tmdbID, out)); err != nil {
			return Watch{}, err
		}
		if out.Resume, err = s.resumeAt(ctx, u, media, tmdbID, out, 0); err != nil {
			return Watch{}, err
		}
	}
	out.Ticket = s.tickets.Sign(stream.Claims{Member: u.ID, Item: itemOf(f), Source: itemOf(f), Play: out.Play, File: f.Path,
		Plan: pl.String(), Runtime: int(p.Duration), Credits: credits})
	if pl.Mode == plan.Direct {
		out.Mode, out.URL = "direct", stream.FileURL(out.Ticket, p.Container)
	} else {
		out.Mode, out.URL = "hls", stream.LibraryHLSURL(out.Ticket)
	}

	out.Audio, out.Subs = libraryTracks(p, out.Ticket)
	if pl.Audio >= 0 {
		a := pl.Audio
		out.AudioIdx = &a
	}
	if pl.Burn >= 0 {
		b := pl.Burn
		out.Burnt, out.Subtitle = &b, &b
	} else if prefs, err := s.players.Get(ctx, u.ID); err != nil {
		return Watch{}, err
	} else {
		out.Subtitle = pickShown(out.Subs, deref(prefs.SubtitleLanguage))
	}
	for _, a := range p.Of("attachment") {
		if isFontFile(a.MimeType, a.FileName) {
			out.Fonts = append(out.Fonts, stream.AttachmentURL(out.Ticket, a.Index))
		}
	}
	return out, nil
}

// Claims is a ticket's claims (stream.Claims), named here for brevity.
type Claims = stream.Claims

// plan turns the browser's caps into the decision's.
func (c BrowserCaps) plan() plan.Caps {
	return plan.Caps{DirectContainers: c.DirectContainers, DirectVideo: c.DirectVideo, DirectAudio: c.DirectAudio,
		HLSVideo: c.HLSVideo, HLSAudio: c.HLSAudio, AudioChannels: c.AudioChannels}
}

// ChapterSegments makes Skip buttons from chapters named for an intro, a
// recap, a preview or the credits ("Intro", "Opening", "OP", "End
// Credits", ...), which many releases have. Finding an intro without such
// a chapter (by matching the audio of a show's episodes, as Jellyfin's
// Intro Skipper does) is a later step.
func ChapterSegments(chapters []probe.Chapter) []Stretch {
	out := []Stretch{}
	for _, c := range chapters {
		kind := chapterKind(c.Title)
		if kind == "" || c.End <= c.Start {
			continue
		}
		out = append(out, Stretch{Kind: kind, Start: c.Start, End: c.End})
	}
	return out
}

func chapterKind(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	switch {
	case t == "":
		return ""
	case t == "intro" || t == "opening" || t == "op" || t == "opening credits" || t == "opening titles" ||
		strings.HasPrefix(t, "intro ") || t == "title sequence":
		return "intro"
	case t == "credits" || t == "end credits" || t == "ending" || t == "ed" || t == "closing credits" ||
		t == "outro" || strings.HasSuffix(t, " credits"):
		return "credits"
	case t == "recap" || t == "previously" || strings.HasPrefix(t, "previously on"):
		return "recap"
	case t == "preview" || t == "next episode" || t == "next time":
		return "preview"
	}
	return ""
}

// libraryTracks lists a library file's audio and subtitle tracks, each
// text or ASS subtitle with the proxy address the player reads it from.
// External subtitle files come after the file's own streams.
func libraryTracks(p *probe.Probe, ticket string) (audio, subs []Track) {
	audio, subs = []Track{}, []Track{}
	def, _ := p.DefaultAudio()
	for _, st := range p.Of("audio") {
		audio = append(audio, Track{Index: st.Index, Label: streamLabel(st.Title, st.Language, st.Channels, len(audio)+1),
			Language: langPtr(st.Language), Codec: st.Codec, Default: st.Index == def.Index, Channels: st.Channels})
	}
	for _, st := range p.Of("subtitle") {
		t := Track{Index: st.Index, Label: streamLabel(st.Title, st.Language, 0, len(subs)+1), Language: langPtr(st.Language),
			Codec: st.Codec, Kind: subKind(st.Codec), Default: st.Default, Forced: st.Forced}
		switch t.Kind {
		case "text":
			u := stream.SubtitleURL(ticket, st.Index, "vtt")
			t.URL = &u
		case "ass":
			u := stream.SubtitleURL(ticket, st.Index, "ass")
			t.URL = &u
		}
		subs = append(subs, t)
	}
	for n, ext := range p.External {
		index := p.ExternalIndex(n)
		title := ext.Title
		if ext.Forced {
			title = strings.TrimSpace(title + " Forced")
		}
		t := Track{Index: index, Label: streamLabel(title, ext.Language, 0, len(subs)+1), Language: langPtr(ext.Language),
			Codec: ext.Codec, Kind: subKind(ext.Codec), Default: ext.Default, Forced: ext.Forced, External: true}
		format := "vtt"
		if t.Kind == "ass" {
			format = "ass"
		}
		u := stream.ExternalSubtitleURL(ticket, index, format, ext.Name)
		t.URL = &u
		subs = append(subs, t)
	}
	return audio, subs
}

func subKind(codec string) string {
	switch {
	case codec == "ass" || codec == "ssa":
		return "ass"
	case plan.IsPictureSub(codec):
		return "image"
	}
	return "text"
}

// languageNames name the commonest languages in labels.
var languageNames = map[string]string{"eng": "English", "spa": "Spanish", "fre": "French", "ger": "German",
	"por": "Portuguese", "ita": "Italian", "dut": "Dutch", "swe": "Swedish", "dan": "Danish", "nor": "Norwegian",
	"fin": "Finnish", "pol": "Polish", "tur": "Turkish", "gre": "Greek", "ara": "Arabic", "heb": "Hebrew",
	"hin": "Hindi", "jpn": "Japanese", "kor": "Korean", "chi": "Chinese", "rus": "Russian", "ukr": "Ukrainian",
	"cze": "Czech", "hun": "Hungarian", "rum": "Romanian", "tha": "Thai", "vie": "Vietnamese", "ind": "Indonesian"}

// streamLabel is a track's name in the menu: its language and its own title
// ("English · Commentary"), the sound ("5.1"), else "Track n".
func streamLabel(title, lang string, channels, n int) string {
	var parts []string
	if name := languageNames[lang]; name != "" {
		parts = append(parts, name)
	} else if lang != "" {
		parts = append(parts, lang)
	}
	if title != "" && !strings.EqualFold(title, languageNames[lang]) {
		parts = append(parts, title)
	}
	if channels > 0 {
		switch {
		case channels >= 8:
			parts = append(parts, "7.1")
		case channels >= 6:
			parts = append(parts, "5.1")
		case channels == 1:
			parts = append(parts, "Mono")
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Track %d", n)
	}
	return strings.Join(parts, " · ")
}

func langPtr(l string) *string {
	if l == "" {
		return nil
	}
	return &l
}

func isFontFile(mime, name string) bool {
	m, n := strings.ToLower(mime), strings.ToLower(name)
	return strings.Contains(m, "font") || strings.HasSuffix(n, ".ttf") || strings.HasSuffix(n, ".otf") ||
		strings.HasSuffix(n, ".woff") || strings.HasSuffix(n, ".woff2")
}

// seasonNamesFor is how long a season's episode names are kept.
const seasonNamesFor = 24 * time.Hour

// episodeName is an episode's name from TMDB ("Episode 3" when TMDB has
// none or fails: the play goes on either way).
func (s *Service) episodeName(ctx context.Context, showID, season, episode int) string {
	names, err := cache.Cached(ctx, s.cache, fmt.Sprintf("mn:season-names:%d:%d", showID, season), cache.Options{TTL: seasonNamesFor},
		func(ctx context.Context) (map[string]string, error) {
			d, err := s.clients.TMDB.Season(ctx, showID, season)
			if err != nil {
				return nil, err
			}
			out := map[string]string{}
			for _, e := range jsonx.List(clients.JSON(d), "episodes") {
				if n, ok := jsonx.Int(e, "episode_number"); ok {
					out[strconv.Itoa(n)] = jsonx.Str(e, "name")
				}
			}
			return out, nil
		})
	if err == nil && names[strconv.Itoa(episode)] != "" {
		return names[strconv.Itoa(episode)]
	}
	return fmt.Sprintf("Episode %d", episode)
}

// libraryNext is the library's next episode after (season, episode).
func (s *Service) libraryNext(ctx context.Context, showID, season, episode int) (*Next, error) {
	eps, err := s.library.Episodes(ctx, showID)
	if err != nil {
		return nil, err
	}
	for _, e := range eps {
		// Specials (season 0) sort first, so they are never "next" after a
		// regular episode.
		if e[0] > season || (e[0] == season && e[1] > episode) {
			return &Next{Season: e[0], Episode: e[1], Title: s.episodeName(ctx, showID, e[0], e[1])}, nil
		}
	}
	return nil, nil
}

// jellyfinTwin is the Jellyfin item of the same film or episode when
// Jellyfin is set up and has it, so the watch recorded at the end also
// marks it played there (its Next Up and resume list stay right), as a
// play from Jellyfin does. "" when there is none; Jellyfin failing costs
// only that mark.
func (s *Service) jellyfinTwin(ctx context.Context, u store.User, media string, tmdbID int, w Watch) string {
	if !s.cfg.JellyfinEnabled() {
		return ""
	}
	id, err := s.findItem(ctx, u, media, tmdbID, w.Season, w.Episode)
	if err != nil {
		s.log.InfoContext(ctx, "Jellyfin's copy of a library play couldn't be looked up", "err", err)
		return ""
	}
	return id
}
