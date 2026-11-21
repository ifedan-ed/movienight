package playback

// Movie Night's own player ("Play here"): the title plays in the browser,
// streamed from Jellyfin through Movie Night (package stream), with
// Jellyfin told where the person got to (package jfplay) and the watch
// recorded here when the film or episode is over, as a yes to "Finished
// it?" records it.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"movienight/server/internal/apierr"
	"movienight/server/internal/cache"
	"movienight/server/internal/crypto"
	"movienight/server/internal/jfplay"
	"movienight/server/internal/jfsync"
	"movienight/server/internal/players"
	"movienight/server/internal/scrobble"
	"movienight/server/internal/store"
	"movienight/server/internal/stream"
	"movienight/server/internal/titles"
)

// JellyfinDevice is a person's own Jellyfin "device": linking their account
// signs in under it, and their plays here run under it, so their Jellyfin
// session stands alone (the Python's users.device_id).
func JellyfinDevice(u store.User) string { return "movienight-" + strconv.Itoa(int(u.ID)) }

// WatchRequest starts (or restarts) a play in the browser.
type WatchRequest struct {
	Season, Episode *int
	Caps            jfplay.Caps
	// Audio picks an audio track; nil keeps the file's default.
	Audio *int
	// Burn is an image subtitle to burn into the picture; nil burns none.
	Burn *int
	// Replace is the ticket of the play this one continues (another audio
	// track, a burnt-in subtitle, an expired ticket): the same Movie Night
	// play goes on and Jellyfin's old transcode is stopped.
	Replace string
}

// Track is one audio or subtitle track.
type Track struct {
	Index    int
	Label    string
	Language *string
	Codec    string
	Kind     string // subtitles only: text, ass or image (jfplay.SubtitleKind)
	Default  bool
	Forced   bool
	External bool
	Channels int
	URL      *string // subtitles only: where the player reads it (text and ass)
}

// Mark is a chapter.
type Mark struct {
	Start float64
	Name  string
}

// Stretch is a media segment: intro, credits, recap, preview, commercial.
type Stretch struct {
	Kind       string
	Start, End float64
}

// Next is the episode after this one.
type Next struct {
	Season, Episode int
	Title           string
}

// Watch is what the player needs to play.
type Watch struct {
	Ticket   string
	Play     string // Movie Night's play session
	Mode     string // "direct" (the file itself) or "hls"
	URL      string
	Duration float64
	Resume   float64 // where the person left off (0: from the start)
	Title    string
	Media    string
	TMDBID   int
	Season   *int
	Episode  *int
	Key      string
	ItemID   string
	Backdrop string
	Audio    []Track
	AudioIdx *int
	Subs     []Track
	// Subtitle is the subtitle to show: the burnt-in one, else the
	// person's language (Profile → Players), else none.
	Subtitle *int
	Burnt    *int
	Chapters []Mark
	Segments []Stretch
	Next     *Next
	Fonts    []string
}

var (
	errNotPlayable = apierr.New(http.StatusUnprocessableEntity, "This file can't be played in a browser")
	errNoTicket    = apierr.New(http.StatusForbidden, "This play has ended. Start it again.")
)

// segmentKinds are Jellyfin's segment types as the player names them.
var segmentKinds = map[string]string{"Intro": "intro", "Outro": "credits", "Recap": "recap", "Preview": "preview", "Commercial": "commercial"}

// watchAuth is whose Jellyfin session the play runs in: the person's own
// token when they linked one, else the install's key.
func (s *Service) watchAuth(u store.User) (jfplay.Auth, error) {
	a := jfplay.Auth{Device: JellyfinDevice(u)}
	if u.JellyfinTokenEnc == nil || *u.JellyfinTokenEnc == "" {
		return a, nil
	}
	token, err := s.keys.Fernet().Decrypt(*u.JellyfinTokenEnc)
	if err != nil {
		// Said plainly, never a silent fall back to the install's key.
		return a, apierr.New(http.StatusConflict, "Your Jellyfin connection needs to be made again (Profile → Jellyfin)")
	}
	a.Token = token
	return a, nil
}

func jellyfinDown(err error) error {
	var je *jfplay.Error
	if errors.As(err, &je) && je.Status == http.StatusUnprocessableEntity {
		return errNotPlayable
	}
	return errJellyfinDown
}

func secs(ticks int64) float64 { return float64(ticks) / jfplay.TicksPerSecond }

// Watch starts a play in the browser: Jellyfin's PlaybackInfo as the
// person, a ticket for the stream, and everything the player shows.
func (s *Service) Watch(ctx context.Context, u store.User, media string, tmdbID int, r WatchRequest) (Watch, error) {
	var prev *stream.Claims
	if r.Replace != "" {
		c, err := s.tickets.Owned(r.Replace, u.ID)
		if err != nil {
			return Watch{}, errNoTicket
		}
		prev = &c
	}
	itemID, err := s.findItem(ctx, u, media, tmdbID, r.Season, r.Episode)
	if err != nil {
		return Watch{}, err
	}
	if itemID == "" {
		return Watch{}, apierr.New(http.StatusNotFound, "This isn't in the library yet")
	}
	if prev != nil && jfsync.NormalizeID(prev.Item) != jfsync.NormalizeID(itemID) {
		return Watch{}, errNoTicket
	}
	auth, err := s.watchAuth(u)
	if err != nil {
		return Watch{}, err
	}
	uid := s.libraryUser(u)
	item, err := s.jf.Item(ctx, auth, uid, itemID)
	if err != nil {
		return Watch{}, errJellyfinDown
	}
	if item == nil {
		return Watch{}, apierr.New(http.StatusNotFound, "This isn't in the library yet")
	}

	req := jfplay.InfoRequest{UserID: uid, Caps: r.Caps, AudioIndex: r.Audio, BurnSubtitle: r.Burn, NoDirect: r.Burn != nil}
	info, err := s.jf.PlaybackInfo(ctx, auth, itemID, req)
	if err != nil {
		return Watch{}, jellyfinDown(err)
	}
	src := info.Source
	if err := checkTracks(src, r.Audio, r.Burn); err != nil {
		return Watch{}, err
	}
	// A browser plays the file's default audio track only; another one
	// is Jellyfin's HLS with that track (a remux when the codec fits).
	if r.Audio != nil && src.SupportsDirectPlay && *r.Audio != defaultAudio(src) && !req.NoDirect {
		req.NoDirect = true
		if info, err = s.jf.PlaybackInfo(ctx, auth, itemID, req); err != nil {
			return Watch{}, jellyfinDown(err)
		}
		src = info.Source
	}
	if prev != nil && prev.PlaySession != "" {
		// The old transcode would otherwise run on until Jellyfin gives
		// up on it. Best effort: the new play does not depend on it.
		if err := s.jf.StopEncoding(ctx, auth, prev.PlaySession); err != nil {
			s.log.InfoContext(ctx, "stopping the old transcode failed", "err", err)
		}
	}

	runtime := src.RunTimeTicks
	if runtime == 0 {
		runtime = item.RunTimeTicks
	}
	out := Watch{Media: media, TMDBID: tmdbID, Key: titles.Key(media, tmdbID), ItemID: itemID,
		Duration: secs(runtime), Audio: []Track{}, Subs: []Track{}, Chapters: []Mark{}, Segments: []Stretch{}, Fonts: []string{}}
	out.Title = item.Name
	if item.Type == "Episode" && item.Season != nil && item.Episode != nil {
		out.Title = fmt.Sprintf("%s · S%02dE%02d · %s", item.SeriesName, *item.Season, *item.Episode, item.Name)
		out.Season, out.Episode = item.Season, item.Episode
	}
	backdropOf := itemID
	if item.SeriesID != "" {
		backdropOf = jfsync.NormalizeID(item.SeriesID)
	}
	out.Backdrop = "/api/v1/img/jf/" + backdropOf + "?kind=Backdrop&w=1280"
	for _, c := range item.Chapters {
		out.Chapters = append(out.Chapters, Mark{Start: secs(c.StartTicks), Name: c.Name})
	}
	credits := 0
	if segs, err := s.jf.Segments(ctx, auth, itemID); err != nil {
		// No segments is the usual case (no Intro Skipper plugin); a failure
		// costs only the Skip buttons.
		s.log.InfoContext(ctx, "reading media segments failed", "err", err)
	} else {
		for _, sg := range segs {
			kind := segmentKinds[sg.Type]
			if kind == "" || sg.EndTicks <= sg.StartTicks {
				continue
			}
			out.Segments = append(out.Segments, Stretch{Kind: kind, Start: secs(sg.StartTicks), End: secs(sg.EndTicks)})
			if kind == "credits" && (credits == 0 || jfplay.Seconds(sg.StartTicks) < credits) {
				credits = jfplay.Seconds(sg.StartTicks)
			}
		}
	}
	if item.Type == "Episode" && item.SeriesID != "" {
		next, err := s.jf.NextEpisode(ctx, auth, uid, item.SeriesID, itemID)
		if err != nil {
			s.log.InfoContext(ctx, "reading the next episode failed", "err", err)
		} else if next != nil {
			out.Next = &Next{Season: *next.Season, Episode: *next.Episode, Title: next.Name}
		}
	}

	// The Movie Night play: a new one, or the one this continues.
	if prev != nil {
		out.Play = prev.Play
	} else {
		if out.Play, err = s.startPlay(ctx, u, media, tmdbID, out, itemID); err != nil {
			return Watch{}, err
		}
		if out.Resume, err = s.resumeAt(ctx, u, media, tmdbID, out, item); err != nil {
			return Watch{}, err
		}
	}

	out.Ticket = s.tickets.Sign(stream.Claims{Member: u.ID, Item: itemID, Source: src.ID, PlaySession: info.PlaySessionID,
		Device: auth.Device, Play: out.Play, Runtime: jfplay.Seconds(runtime), Credits: credits})
	switch {
	case src.SupportsDirectPlay && !req.NoDirect:
		out.Mode, out.URL = "direct", stream.FileURL(out.Ticket, src.Container)
	case src.TranscodingURL != "":
		u, err := stream.HLSURL(out.Ticket, src.TranscodingURL, itemID)
		if err != nil {
			s.log.WarnContext(ctx, "Jellyfin's transcoding address is not one the proxy serves", "err", err)
			return Watch{}, errNotPlayable
		}
		out.Mode, out.URL = "hls", u
	default:
		return Watch{}, errNotPlayable
	}

	out.Audio, out.Subs = tracks(src, out.Ticket)
	a := defaultAudio(src)
	if r.Audio != nil {
		a = *r.Audio
	}
	if a >= 0 {
		out.AudioIdx = &a
	}
	if r.Burn != nil {
		out.Burnt, out.Subtitle = r.Burn, r.Burn
	} else if prefs, err := s.players.Get(ctx, u.ID); err != nil {
		return Watch{}, err
	} else {
		out.Subtitle = pickShown(out.Subs, deref(prefs.SubtitleLanguage))
	}
	for _, at := range src.MediaAttachments {
		if isFont(at) {
			out.Fonts = append(out.Fonts, stream.AttachmentURL(out.Ticket, at.Index))
		}
	}
	return out, nil
}

// checkTracks refuses an audio track the file does not have, and a burn-in
// of anything but an image subtitle (text is shown by the player, never
// burnt in).
func checkTracks(src jfplay.Source, audio, burn *int) error {
	if audio != nil && !hasStream(src, "Audio", *audio) {
		return apierr.New(http.StatusUnprocessableEntity, "The file has no such audio track")
	}
	if burn == nil {
		return nil
	}
	for _, st := range src.MediaStreams {
		if st.Type == "Subtitle" && st.Index == *burn {
			if jfplay.SubtitleKind(st.Codec, st.IsTextSubtitleStream) != "image" {
				return apierr.New(http.StatusUnprocessableEntity, "Only picture subtitles are burnt in")
			}
			return nil
		}
	}
	return apierr.New(http.StatusUnprocessableEntity, "The file has no such subtitle track")
}

func hasStream(src jfplay.Source, kind string, index int) bool {
	for _, st := range src.MediaStreams {
		if st.Type == kind && st.Index == index {
			return true
		}
	}
	return false
}

// defaultAudio is the track a browser plays from the file: Jellyfin's
// default, else the first flagged default, else the first (-1: none).
func defaultAudio(src jfplay.Source) int {
	if src.DefaultAudioStreamIndex != nil && hasStream(src, "Audio", *src.DefaultAudioStreamIndex) {
		return *src.DefaultAudioStreamIndex
	}
	first := -1
	for _, st := range src.MediaStreams {
		if st.Type != "Audio" {
			continue
		}
		if st.IsDefault {
			return st.Index
		}
		if first < 0 {
			first = st.Index
		}
	}
	return first
}

func label(st jfplay.MediaStream, n int) string {
	switch {
	case st.DisplayTitle != "":
		return st.DisplayTitle
	case st.Title != "":
		return st.Title
	case st.Language != "":
		return st.Language
	}
	return fmt.Sprintf("Track %d", n)
}

// tracks lists the audio and subtitle tracks, each text or ASS subtitle
// with the proxy address the player reads it from.
func tracks(src jfplay.Source, ticket string) (audio, subs []Track) {
	audio, subs = []Track{}, []Track{}
	defSub := -1
	if src.DefaultSubtitleStreamIndex != nil {
		defSub = *src.DefaultSubtitleStreamIndex
	}
	defAudio := defaultAudio(src)
	for _, st := range src.MediaStreams {
		var lang *string
		if l := strings.ToLower(st.Language); l != "" {
			lang = &l
		}
		switch st.Type {
		case "Audio":
			audio = append(audio, Track{Index: st.Index, Label: label(st, len(audio)+1), Language: lang, Codec: st.Codec,
				Default: st.Index == defAudio, Channels: st.Channels})
		case "Subtitle":
			t := Track{Index: st.Index, Label: label(st, len(subs)+1), Language: lang, Codec: st.Codec,
				Kind: jfplay.SubtitleKind(st.Codec, st.IsTextSubtitleStream), Default: st.Index == defSub,
				Forced: st.IsForced, External: st.IsExternal}
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
	}
	return audio, subs
}

// pickShown is the subtitle to show at the start: one in the person's
// language that the player draws itself (an external file first, as the
// Play sheet picks), else none.
func pickShown(subs []Track, language string) *int {
	var list []Subtitle
	for _, t := range subs {
		if t.Kind != "image" {
			list = append(list, Subtitle{Index: t.Index, Language: t.Language, External: t.External, Text: true})
		}
	}
	return PickSubtitle(list, language)
}

func isFont(a jfplay.Attachment) bool {
	m, n := strings.ToLower(a.MimeType), strings.ToLower(a.FileName)
	return strings.Contains(m, "font") || strings.HasSuffix(n, ".ttf") || strings.HasSuffix(n, ".otf") ||
		strings.HasSuffix(n, ".woff") || strings.HasSuffix(n, ".woff2")
}

// startPlay records a new Movie Night play, closing any open one, as Play
// in an outside player does.
func (s *Service) startPlay(ctx context.Context, u store.User, media string, tmdbID int, w Watch, itemID string) (string, error) {
	if err := s.q.PlaySessionCloseOpen(ctx, u.ID); err != nil {
		return "", err
	}
	kind := "episode"
	if media == "movie" {
		kind = "movie"
	}
	ps, err := s.q.PlaySessionCreate(ctx, store.PlaySessionCreateParams{ID: crypto.NewToken(24), UserID: u.ID, MediaType: kind,
		TmdbID: ptr32(tmdbID), Season: i32(w.Season), Episode: i32(w.Episode), Title: w.Title,
		JellyfinItemID: itemID, Player: players.Browser})
	if err != nil {
		return "", err
	}
	return ps.ID, nil
}

// resumeAt is where the person left off: Jellyfin's resume point when it
// has one, else Movie Night's own (watch_progress: Kodi, the Jellyfin
// plugin, an earlier play here).
func (s *Service) resumeAt(ctx context.Context, u store.User, media string, tmdbID int, w Watch, item *jfplay.Item) (float64, error) {
	if t := item.UserData.PlaybackPositionTicks; t > 0 {
		return secs(t), nil
	}
	kind := "episode"
	if media == "movie" {
		kind = "movie"
	}
	key := scrobble.ProgressKey(kind, int32(tmdbID), i32(w.Season), i32(w.Episode))
	p, err := s.q.ResumeFor(ctx, store.ResumeForParams{UserID: u.ID, Media: media, MovieKey: key, TmdbID: int32(tmdbID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	// For a show the latest started episode comes back: only this one's
	// position counts.
	if p.Key != key {
		return 0, nil
	}
	return float64(p.PositionS), nil
}

// ---------------------------------------------------------------------------
// Reports from the player
// ---------------------------------------------------------------------------

// Report is the player saying where it is.
type Report struct {
	Ticket   string
	Event    string // start, progress, pause, stop
	Position float64
	// Duration is the player's own idea of the length, used only when
	// Jellyfin named none.
	Duration   float64
	Paused     bool
	Audio      *int
	Subtitle   *int
	Transcoded bool
}

// WatchedAtEnd is the rule for recording the watch when a play stops: past
// scrobble.DoneShare (90 %) of the runtime, or into the credits when
// Jellyfin knows where they start, as "Finished it?" would be answered.
func WatchedAtEnd(position, runtime, credits float64) bool {
	if position <= 0 {
		return false
	}
	if credits > 0 && position >= credits {
		return true
	}
	return runtime > 0 && position >= scrobble.DoneShare*runtime
}

var reportEvents = map[string]jfplay.Event{"start": jfplay.Started, "progress": jfplay.Progressed,
	"pause": jfplay.Progressed, "stop": jfplay.Stopped}

// WatchReport takes one report: Jellyfin hears it (so its resume point
// and Continue watching follow), Movie Night keeps the position (Home's
// Up next, the resume prompt), and a stop at the end records the watch.
// It answers whether the watch was recorded.
func (s *Service) WatchReport(ctx context.Context, u store.User, r Report) (bool, error) {
	c, err := s.tickets.Owned(r.Ticket, u.ID)
	if err != nil {
		return false, errNoTicket
	}
	ev, ok := reportEvents[r.Event]
	if !ok {
		return false, apierr.New(http.StatusUnprocessableEntity, "Unknown event")
	}
	auth, err := s.watchAuth(u)
	if err != nil {
		return false, err
	}
	auth.Device = c.Device
	ticks := jfplay.Ticks(r.Position)
	// Jellyfin's records are a courtesy next to Movie Night's own: a
	// failure is logged, and the report still counts here.
	if err := s.jf.Report(ctx, auth, ev, jfplay.Progress{ItemID: c.Item, MediaSourceID: c.Source, PlaySessionID: c.PlaySession,
		PositionTicks: ticks, Paused: r.Paused || r.Event == "pause", AudioIndex: r.Audio, SubtitleIndex: r.Subtitle,
		Transcoded: r.Transcoded}); err != nil {
		s.log.WarnContext(ctx, "reporting playback to Jellyfin failed", "user", u.ID, "err", err)
	}
	if auth.Token == "" && u.JellyfinUserID != nil && ev != jfplay.Started {
		// The install's key plays as no Jellyfin user, so the person's
		// resume point is set by hand.
		if err := s.jf.SavePosition(ctx, *u.JellyfinUserID, c.Item, ticks); err != nil {
			s.log.WarnContext(ctx, "saving the Jellyfin resume point failed", "user", u.ID, "err", err)
		}
	}

	ps, err := s.q.PlaySessionGet(ctx, store.PlaySessionGetParams{ID: c.Play, UserID: u.ID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && ps.TmdbID == nil) {
		return false, nil // a play whose record is gone: nothing more to keep
	}
	if err != nil {
		return false, err
	}
	runtime := float64(c.Runtime)
	if runtime == 0 {
		runtime = r.Duration
	}
	key := scrobble.ProgressKey(ps.MediaType, *ps.TmdbID, ps.Season, ps.Episode)
	if ev == jfplay.Stopped && WatchedAtEnd(r.Position, runtime, float64(c.Credits)) {
		if err := s.q.ProgressClear(ctx, store.ProgressClearParams{UserID: u.ID, Key: key}); err != nil {
			return false, err
		}
		// Exactly a yes to "Finished it?": history, the watchlist, and
		// Jellyfin's played mark; once per play.
		if err := s.Finish(ctx, u, c.Play, "watched"); err != nil {
			return false, err
		}
		s.cache.Forget(ctx, cache.ContinueKey(u.ID))
		return true, nil
	}
	state := "playing"
	switch {
	case ev == jfplay.Stopped:
		state = "stopped"
	case r.Paused || r.Event == "pause":
		state = "paused"
	}
	title, epTitle := ps.Title, (*string)(nil)
	if ps.MediaType == "episode" {
		// The play's title is "Show · S01E02 · Episode name".
		parts := strings.Split(ps.Title, " · ")
		title, epTitle = parts[0], &parts[len(parts)-1]
	}
	var dur *int32
	if runtime > 0 {
		d := int32(runtime)
		dur = &d
	}
	client, device := "Movie Night", "Browser"
	item := jfsync.NormalizeID(c.Item)
	if err := s.q.ProgressSave(ctx, store.ProgressSaveParams{UserID: u.ID, Key: key, MediaType: ps.MediaType,
		TmdbID: *ps.TmdbID, Season: ps.Season, Episode: ps.Episode, Title: title, EpisodeTitle: epTitle,
		PositionS: int32(r.Position), DurationS: dur, State: state, Client: &client, Device: &device,
		JellyfinItemID: &item}); err != nil {
		return false, err
	}
	if ev != jfplay.Progressed {
		// Up next moves on a start or a stop; every 10 s would only churn
		// the cache.
		s.cache.Forget(ctx, cache.ContinueKey(u.ID))
	}
	if ev == jfplay.Stopped {
		if _, err := s.close(ctx, ps, "not_finished", s.now().UTC()); err != nil {
			return false, err
		}
	}
	return false, nil
}
