package playback

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"movienight/server/internal/apierr"
	"movienight/server/internal/cache"
	"movienight/server/internal/crypto"
	"movienight/server/internal/jfsync"
	"movienight/server/internal/jsonx"
	"movienight/server/internal/players"
	"movienight/server/internal/store"
)

// Subtitle is one subtitle track of the file.
type Subtitle struct {
	Index    int     `json:"index"`
	Label    string  `json:"label"`
	Language *string `json:"language"`
	External bool    `json:"external"`
	Text     bool    `json:"text"`
	Default  bool    `json:"default"`
}

// Info is what the Play sheet shows.
type Info struct {
	Available bool
	// InJellyfin: the outside players can play it (they play Jellyfin's
	// file). InLibrary: Movie Night's own library has it (Play here plays
	// that copy).
	InJellyfin, InLibrary bool
	Linked                bool
	JellyfinItemID        *string
	WebURL                *string
	Title                 *string
	Season                *int
	Episode               *int
	Subtitles             []Subtitle
	ChosenSubtitle        *int
	ResumeSeconds         int
	players.Settings
	// source is the file's media source (for Start; not shown).
	source jsonx.Obj
}

var errJellyfinDown = apierr.New(http.StatusBadGateway, "Jellyfin didn't answer — try again")

// libraryUser is whose Jellyfin view to read: the person's own, else the
// install owner's.
func (s *Service) libraryUser(u store.User) string {
	if u.JellyfinUserID != nil && *u.JellyfinUserID != "" {
		return *u.JellyfinUserID
	}
	return s.cfg.JellyfinUserID
}

// libraryItem is the Jellyfin item of a title in the library ("" when not).
func (s *Service) libraryItem(ctx context.Context, media string, tmdbID int) (string, error) {
	id, err := s.q.LibraryItemFor(ctx, store.LibraryItemForParams{MediaType: media, TmdbID: ptr32(tmdbID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func ptr32(n int) *int32 { v := int32(n); return &v }

// NextEpisode is the episode to play next: after the last one ticked, or
// S1E1; ok false when the person is caught up with what has aired (or TMDB
// does not know the show).
func (s *Service) NextEpisode(ctx context.Context, userID int32, showID int) (season, episode int, ok bool, err error) {
	d, err := s.clients.TMDB.Basic(ctx, "tv", showID)
	if err != nil || d == nil {
		return 0, 0, false, err
	}
	counts := map[int]int{}
	first := 0
	for _, se := range jsonx.List(d, "seasons") {
		n, _ := jsonx.Int(se, "season_number")
		if n <= 0 {
			continue
		}
		c, _ := jsonx.Int(se, "episode_count")
		counts[n] = c
		if first == 0 || n < first {
			first = n
		}
	}
	if len(counts) == 0 {
		return 0, 0, false, nil
	}
	last := jsonx.Child(d, "last_episode_to_air")
	ls, _ := jsonx.Int(last, "season_number")
	le, _ := jsonx.Int(last, "episode_number")
	watched, err := s.q.PlayWatchedEpisodes(ctx, store.PlayWatchedEpisodesParams{UserID: userID, TmdbID: ptr32(showID)})
	if err != nil {
		return 0, 0, false, err
	}
	sn, en := first, 1
	if len(watched) > 0 {
		ms, me := 0, 0
		for _, w := range watched {
			if int(w.Season) > ms || (int(w.Season) == ms && int(w.Episode) > me) {
				ms, me = int(w.Season), int(w.Episode)
			}
		}
		if me+1 <= counts[ms] {
			sn, en = ms, me+1
		} else {
			sn, en = ms+1, 1
		}
		for {
			c, exists := counts[sn]
			if !exists || c != 0 {
				break
			}
			sn++
		}
	}
	if _, exists := counts[sn]; !exists || sn > ls || (sn == ls && en > le) {
		return 0, 0, false, nil
	}
	return sn, en, true, nil
}

// findItem is the Jellyfin item to play: the film, or the show's episode
// (the next one when none is named).
func (s *Service) findItem(ctx context.Context, u store.User, media string, tmdbID int, season, episode *int) (string, error) {
	if media == "movie" {
		return s.libraryItem(ctx, "movie", tmdbID)
	}
	if season == nil || episode == nil {
		sn, en, ok, err := s.NextEpisode(ctx, u.ID, tmdbID)
		if err != nil || !ok {
			return "", err
		}
		season, episode = &sn, &en
	}
	series, err := s.libraryItem(ctx, "tv", tmdbID)
	uid := s.libraryUser(u)
	if err != nil || series == "" || uid == "" {
		return "", err
	}
	eps, err := s.clients.Jellyfin.Episodes(ctx, series, uid, *season)
	if err != nil {
		return "", errJellyfinDown
	}
	for _, it := range eps {
		sn, ok1 := jsonx.Int(it, "ParentIndexNumber")
		en, ok2 := jsonx.Int(it, "IndexNumber")
		if ok1 && ok2 && sn == *season && en == *episode && jsonx.Str(it, "LocationType") != "Virtual" {
			return jsonx.Str(it, "Id"), nil
		}
	}
	return "", nil
}

func subtitles(source jsonx.Obj) []Subtitle {
	out := []Subtitle{}
	def, hasDef := jsonx.Int(source, "DefaultSubtitleStreamIndex")
	for _, st := range jsonx.List(source, "MediaStreams") {
		if jsonx.Str(st, "Type") != "Subtitle" {
			continue
		}
		idx, _ := jsonx.Int(st, "Index")
		label := jsonx.Str(st, "DisplayTitle")
		if label == "" {
			label = jsonx.Str(st, "Language")
		}
		if label == "" {
			label = fmt.Sprintf("Track %d", idx)
		}
		var lang *string
		if l := strings.ToLower(jsonx.Str(st, "Language")); l != "" {
			lang = &l
		}
		out = append(out, Subtitle{Index: idx, Label: label, Language: lang, External: jsonx.Bool(st, "IsExternal"),
			Text: jsonx.Bool(st, "IsTextSubtitleStream"), Default: hasDef && idx == def})
	}
	return out
}

// PickSubtitle is the person's language: an external file first (the
// player cannot see those on its own), then an embedded text track.
func PickSubtitle(subs []Subtitle, language string) *int {
	if language == "" {
		return nil
	}
	var mine []Subtitle
	for _, s := range subs {
		if s.Language != nil && *s.Language == language && s.Text {
			mine = append(mine, s)
		}
	}
	for _, s := range mine {
		if s.External {
			i := s.Index
			return &i
		}
	}
	if len(mine) > 0 {
		i := mine[0].Index
		return &i
	}
	return nil
}

// PlayInfo is what the Play sheet needs for a title.
func (s *Service) PlayInfo(ctx context.Context, u store.User, media string, tmdbID int, season, episode *int) (Info, error) {
	file, err := s.libraryFile(ctx, u, media, tmdbID, season, episode)
	if err != nil {
		return Info{}, err
	}
	itemID := ""
	if s.cfg.JellyfinEnabled() {
		if itemID, err = s.findItem(ctx, u, media, tmdbID, season, episode); err != nil {
			return Info{}, err
		}
	}
	prefs, err := s.players.Get(ctx, u.ID)
	if err != nil {
		return Info{}, err
	}
	var on []players.Player
	for _, p := range prefs.Players {
		if p.Enabled {
			on = append(on, p)
		}
	}
	prefs.Players = on
	out := Info{Available: itemID != "" || file != nil, InJellyfin: itemID != "", InLibrary: file != nil,
		Linked: u.JellyfinTokenEnc != nil, Subtitles: []Subtitle{}, Settings: prefs}
	if itemID == "" {
		if file != nil && file.Season != nil {
			// Only Play here: the sheet needs the episode it plays.
			out.Season, out.Episode = file.Season, file.Episode
		}
		return out, nil
	}
	web := s.clients.Jellyfin.WebURL(itemID)
	out.JellyfinItemID, out.WebURL = &itemID, &web
	uid := s.libraryUser(u)
	var item jsonx.Obj
	if uid != "" {
		if item, err = s.clients.Jellyfin.Item(ctx, uid, itemID, "MediaSources,ProviderIds"); err != nil {
			return Info{}, errJellyfinDown
		}
	}
	if sources := jsonx.List(item, "MediaSources"); len(sources) > 0 {
		out.source = sources[0]
	}
	out.Subtitles = subtitles(out.source)
	out.ChosenSubtitle = PickSubtitle(out.Subtitles, deref(prefs.SubtitleLanguage))
	if ticks, ok := jsonx.Float(jsonx.Child(item, "UserData"), "PlaybackPositionTicks"); ok {
		out.ResumeSeconds = int(math.RoundToEven(ticks / 10_000_000))
	}
	name := jsonx.Str(item, "Name")
	if jsonx.Str(item, "Type") == "Episode" {
		sn, _ := jsonx.Int(item, "ParentIndexNumber")
		en, _ := jsonx.Int(item, "IndexNumber")
		t := fmt.Sprintf("%s · S%02dE%02d · %s", jsonx.Str(item, "SeriesName"), sn, en, name)
		out.Title, out.Season, out.Episode = &t, &sn, &en
	} else if item != nil {
		out.Title = &name
	}
	return out, nil
}

// Started is a Play: its session and the link that opens the player.
type Started struct {
	SessionID string
	URL       string
	WebURL    *string
}

// serverIDFor is how long Jellyfin's server id is kept (Swiftfin links).
const serverIDFor = 24 * time.Hour

// errNoServerID keeps an empty answer out of the cache: a Jellyfin that
// names no server id now may name one on the next Play.
var errNoServerID = errors.New("jellyfin named no server id")

// serverID is Jellyfin's server id ("" when it names none), from the shared
// cache under the Jellyfin prefix, so a library sync forgets it with the
// rest and every replica asks Jellyfin once a day at most.
func (s *Service) serverID(ctx context.Context) (string, error) {
	id, err := cache.Cached(ctx, s.cache, cache.JellyfinPrefix+"server-id", cache.Options{TTL: serverIDFor},
		func(ctx context.Context) (string, error) {
			id, err := s.clients.Jellyfin.ServerID(ctx)
			if err == nil && id == "" {
				return "", errNoServerID
			}
			return id, err
		})
	if errors.Is(err, errNoServerID) {
		return "", nil
	}
	return id, err
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// q is Python's quote(s, safe=""): every byte but letters, digits and
// "_.-~" escaped, spaces as %20.
func q(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }

// emptyParamRe is an empty optional parameter and what follows it ("&"
// or the end); Go's regexp has no look-ahead, so the follower is captured
// and put back.
var emptyParamRe = regexp.MustCompile(`&(?:sub|filename)=(&|$)`)

// dropEmpty removes empty optional parameters (an empty &sub= confuses some
// players) and VLC for Android's empty subtitle extra.
func dropEmpty(link string) string {
	for {
		next := emptyParamRe.ReplaceAllString(link, "$1")
		if next == link {
			break
		}
		link = next
	}
	return strings.ReplaceAll(link, "S.subtitles_location=;", "")
}

// Start starts a Play: it closes any open one, records the new session and
// returns the player's link. returnTo "app" sends the player back to the
// phone app rather than the website.
func (s *Service) Start(ctx context.Context, u store.User, media string, tmdbID int, season, episode *int,
	player string, subtitle *int, returnTo string) (Started, error) {
	info, err := s.PlayInfo(ctx, u, media, tmdbID, season, episode)
	if err != nil {
		return Started{}, err
	}
	if !info.Available {
		return Started{}, apierr.New(http.StatusNotFound, "This isn't in the library yet")
	}
	if !info.InJellyfin {
		// Outside players are handed Jellyfin's file; a library file plays
		// in Movie Night's own player.
		return Started{}, apierr.New(http.StatusConflict, "This plays only here (Play here)")
	}
	// Only a player the Play sheet offers (ticked) can be started.
	template := ""
	for _, p := range info.Players {
		if p.ID == player {
			template = players.Template(p)
		}
	}
	if template == "" {
		return Started{}, apierr.New(http.StatusUnprocessableEntity, "Unknown player")
	}
	token := ""
	if u.JellyfinTokenEnc != nil {
		if token, err = s.keys.Fernet().Decrypt(*u.JellyfinTokenEnc); err != nil {
			// A token that no longer opens (SECRET_KEY changed) is a link to
			// make again, said plainly, never a silent "no token".
			return Started{}, apierr.New(http.StatusConflict, "Your Jellyfin connection needs to be made again (Profile → Jellyfin)")
		}
	}
	if token == "" && !players.IsJellyfinApp(player) {
		return Started{}, apierr.New(http.StatusConflict, "Connect your Jellyfin account first (Profile → Jellyfin)")
	}
	itemID := *info.JellyfinItemID
	msID := jsonx.Str(info.source, "Id")
	if msID == "" {
		msID = itemID
	}
	if err := s.q.PlaySessionCloseOpen(ctx, u.ID); err != nil {
		return Started{}, err
	}
	kind := "episode"
	if media == "movie" {
		kind = "movie"
	}
	if len(player) > 40 {
		player = player[:40]
	}
	title := ""
	if info.Title != nil {
		title = *info.Title
	}
	ps, err := s.q.PlaySessionCreate(ctx, store.PlaySessionCreateParams{ID: crypto.NewToken(24), UserID: u.ID, MediaType: kind,
		TmdbID: ptr32(tmdbID), Season: i32(info.Season), Episode: i32(info.Episode), Title: title,
		JellyfinItemID: itemID, Player: player})
	if err != nil {
		return Started{}, err
	}
	done := s.cfg.PublicURL + "/play/" + ps.ID
	if returnTo == "app" {
		done = "movienight://play/" + ps.ID
	}
	values := map[string]string{"web": *info.WebURL, "done": q(done), "title": q(title),
		"jellyfin_id": strings.ReplaceAll(itemID, "-", "")}
	if player == "swiftfin" && u.JellyfinUserID != nil {
		server, err := s.serverID(ctx)
		if err != nil {
			return Started{}, errJellyfinDown
		}
		if server != "" {
			values["swiftfin"] = fmt.Sprintf("swiftfin://%s/%s/item/%s", jfsync.NormalizeID(server),
				jfsync.NormalizeID(*u.JellyfinUserID), jfsync.NormalizeID(itemID))
		}
	}
	if token != "" {
		container := jsonx.Str(info.source, "Container")
		stream := s.clients.Jellyfin.StreamURL(itemID, msID, container, token)
		subURL := ""
		if subtitle != nil {
			for _, st := range info.Subtitles {
				if st.Index == *subtitle {
					subURL = s.clients.Jellyfin.SubtitleURL(itemID, msID, st.Index, token, "")
				}
			}
		}
		scheme, rest, _ := strings.Cut(stream, "://")
		ext := "mkv"
		if container != "" {
			ext = strings.Split(container, ",")[0]
		}
		name := title
		if name == "" {
			name = "video"
		}
		values["url"], values["url_raw"], values["url_noscheme"], values["url_scheme"] = q(stream), stream, rest, scheme
		values["sub"], values["sub_raw"], values["filename"] = q(subURL), subURL, q(name+"."+ext)
	}
	return Started{SessionID: ps.ID, URL: dropEmpty(players.Fill(template, values)), WebURL: info.WebURL}, nil
}

func i32(n *int) *int32 {
	if n == nil {
		return nil
	}
	v := int32(*n)
	return &v
}
