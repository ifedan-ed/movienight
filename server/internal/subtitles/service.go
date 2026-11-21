package subtitles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/valkey-io/valkey-go"

	"movienight/server/internal/apierr"
	"movienight/server/internal/cache"
	"movienight/server/internal/config"
	"movienight/server/internal/ratelimit"
	"movienight/server/internal/store"
	"movienight/server/internal/stream"
)

// Limits on one person (docs/limits.md): enough for a family's evening,
// low enough that one account can't use up a site's allowance for all.
const (
	SearchesPerHour  = 30
	DownloadsPerHour = 20
)

// Timings.
const (
	// sourceTimeout is how long one source may take to search: the answer
	// waits for the slowest, so a slow site is left out rather than waited
	// for (and said so).
	sourceTimeout = 8 * time.Second
	// downloadTimeout covers a download, a zip included.
	downloadTimeout = 30 * time.Second
	// searchTTL keeps a full answer; problemTTL one where a source failed,
	// so it is asked again soon.
	searchTTL  = time.Hour
	problemTTL = 2 * time.Minute
	// pickTTL keeps each result's download handle for the Add that follows
	// a search, as long as the search itself is kept.
	pickTTL = time.Hour
	// fileTTL keeps a file's name and moviehash: a file never changes
	// under its media source id.
	fileTTL = 24 * time.Hour
	// maxDelay is the delay a track may be given, either way.
	maxDelay = 10 * time.Minute
)

// Files is what the search reads of the playing file: its name, and two
// Range reads for the moviehash. The stream proxy implements it.
type Files interface {
	FileName(ctx context.Context, c stream.Claims) (string, error)
	ReadRange(ctx context.Context, c stream.Claims, off int64, n int) ([]byte, int64, error)
}

// Service is the online subtitles: search, the stored tracks, their delay.
type Service struct {
	q       *store.Queries
	kv      valkey.Client
	cache   *cache.Cache
	log     *slog.Logger
	isAdmin func(username string) bool
	// sources and files are set after the service is built (the stream
	// proxy comes later in server.go; tests point the sources at fakes),
	// and read on every call.
	sources   atomic.Pointer[[]Source]
	files     atomic.Pointer[Files]
	searches  *ratelimit.Window
	downloads *ratelimit.Window
}

// New makes the service with the sources settings turn on.
func New(cfg *config.Config, q *store.Queries, kv valkey.Client, c *cache.Cache, sources []Source, log *slog.Logger) *Service {
	s := &Service{q: q, kv: kv, cache: c, log: log, isAdmin: cfg.IsAdmin,
		searches:  ratelimit.New(kv, "subtitle-search", SearchesPerHour, time.Hour),
		downloads: ratelimit.New(kv, "subtitle-download", DownloadsPerHour, time.Hour)}
	s.SetSources(sources)
	return s
}

// SetSources replaces the sources (the browser tests' fakes).
func (s *Service) SetSources(src []Source) { s.sources.Store(&src) }

// UseFiles sets how the playing file is read (the stream proxy).
func (s *Service) UseFiles(f Files) { s.files.Store(&f) }

// Enabled reports whether any source is on: the player offers "Search
// online" only then.
func (s *Service) Enabled() bool { return len(*s.sources.Load()) > 0 }

func (s *Service) source(name string) Source {
	for _, src := range *s.sources.Load() {
		if src.Name() == name {
			return src
		}
	}
	return nil
}

// Sources builds the sources the settings turn on, in SubtitleSourceNames
// order, on the shared HTTP client. ua is the User-Agent every site gets.
func Sources(cfg config.Subtitles, hc *http.Client, ua string, kv valkey.Client) []Source {
	w := NewWeb(hc, ua)
	var out []Source
	for _, name := range cfg.Sources {
		switch name {
		case config.SourceOpenSubtitles:
			out = append(out, NewOpenSubtitles(w, OpenSubtitlesBase, cfg.OpenSubtitlesAPIKey, cfg.OpenSubtitlesUsername, cfg.OpenSubtitlesPassword, kv))
		case config.SourceSubDL:
			out = append(out, NewSubDL(w, SubDLBase, SubDLFiles, cfg.SubDLAPIKey))
		case config.SourceSubSource:
			out = append(out, NewSubSource(w, SubSourceBase, cfg.SubSourceAPIKey))
		case config.SourceJimaku:
			out = append(out, NewJimaku(w, JimakuBase, cfg.JimakuAPIKey))
		case config.SourcePodnapisi:
			out = append(out, NewPodnapisi(w, PodnapisiBase))
		case config.SourceGestdown:
			out = append(out, NewGestdown(w, GestdownBase))
		}
	}
	return out
}

// NewWeb is the HTTP side the sources share, for building them outside
// Sources (tests).
func NewWeb(hc *http.Client, ua string) Web { return Web{hc: hc, ua: ua} }

// ---------------------------------------------------------------------------
// The file and the title
// ---------------------------------------------------------------------------

// Item is the file playing: the title and episode, and the ticket's claims
// (its Jellyfin item and media source).
type Item struct {
	Media   string
	TMDBID  int
	Season  *int
	Episode *int
	File    stream.Claims
}

// Title is what TMDB says of the film or series.
type Title struct {
	Name   string
	Year   int
	IMDbID string
}

type fileFacts struct {
	Name string `json:"n"`
	Hash string `json:"h"`
}

// facts is the playing file's name and moviehash, kept a day. Either may be
// empty: a file that can't be read is still searched, by its title.
func (s *Service) facts(ctx context.Context, c stream.Claims) fileFacts {
	f := s.files.Load()
	if f == nil {
		return fileFacts{}
	}
	key := "subs:file:" + c.Item + ":" + c.Source
	if raw, err := s.kv.Do(ctx, s.kv.B().Get().Key(key).Build()).AsBytes(); err == nil {
		var ff fileFacts
		if json.Unmarshal(raw, &ff) == nil {
			return ff
		}
	}
	var ff fileFacts
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		name, err := (*f).FileName(ctx, c)
		if err != nil {
			s.log.InfoContext(ctx, "subtitles: the file's name couldn't be read", "err", err)
		}
		ff.Name = name
	}()
	go func() {
		defer wg.Done()
		h, err := MovieHash(ctx, func(ctx context.Context, off int64, n int) ([]byte, int64, error) {
			return (*f).ReadRange(ctx, c, off, n)
		})
		if err != nil && !errors.Is(err, ErrTooSmall) {
			s.log.InfoContext(ctx, "subtitles: the file's moviehash couldn't be read", "err", err)
		}
		ff.Hash = h
	}()
	wg.Wait()
	if ff.Name != "" || ff.Hash != "" {
		v, _ := json.Marshal(ff)
		s.kv.Do(ctx, s.kv.B().Set().Key(key).Value(string(v)).Ex(fileTTL).Build())
	}
	return ff
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// Found is a search's answer.
type Found struct {
	Results []Result
	// Hidden is how many machine-translated results were left out.
	Hidden int
	// Notes say which sources failed or are out of downloads.
	Notes []string
	// UsedUp names the sources whose download allowance is used up: their
	// results are listed but can't be downloaded until it comes back.
	UsedUp map[string]bool
}

// searched is what is cached: every source's results, ranked, and what
// went wrong.
type searched struct {
	Results  []Result  `json:"r"`
	Problems []Problem `json:"p"`
}

// Search asks every source for subtitles of the playing file in one
// language. Machine-translated ones are left out unless asked for.
func (s *Service) Search(ctx context.Context, u store.User, it Item, t Title, l Language, machine bool, loc *time.Location) (Found, error) {
	if !s.Enabled() {
		return Found{}, apierr.New(http.StatusNotFound, "No subtitle sources are set up")
	}
	if ok, err := s.searches.Take(ctx, strconv.Itoa(int(u.ID))); err != nil {
		return Found{}, err
	} else if !ok {
		return Found{}, apierr.New(http.StatusTooManyRequests, "That's a lot of subtitle searches. Try again in an hour.")
	}
	ff := s.facts(ctx, it.File)
	q := Query{Media: it.Media, TMDBID: it.TMDBID, IMDbID: t.IMDbID, Title: t.Name, Year: t.Year,
		Season: deref(it.Season), Episode: deref(it.Episode), Language: l, Hash: ff.Hash, FileName: ff.Name}
	srcs := *s.sources.Load()
	names := make([]string, len(srcs))
	for i, src := range srcs {
		names[i] = src.Name()
	}
	// The key holds everything the answer depends on: the query (the file's
	// hash and name included) and which sources are on.
	raw, _ := json.Marshal(struct {
		Q Query
		S []string
	}{q, names})
	key := cache.Key(true, "subs", "search", string(raw))
	got, err := cache.Cached(ctx, s.cache, key, cache.Options{TTL: searchTTL, TTLFor: func(b []byte) time.Duration {
		var sr searched
		if json.Unmarshal(b, &sr) == nil && len(sr.Problems) > 0 {
			return problemTTL
		}
		return searchTTL
	}}, func(ctx context.Context) (searched, error) { return s.askAll(ctx, srcs, q), nil })
	if err != nil {
		return Found{}, err
	}
	s.keepPicks(ctx, got.Results)

	out := Found{Results: []Result{}, UsedUp: map[string]bool{}}
	for _, r := range got.Results {
		if r.Machine && !machine {
			out.Hidden++
			continue
		}
		out.Results = append(out.Results, r)
	}
	for _, p := range got.Problems {
		out.Notes = append(out.Notes, p.Error())
	}
	// The allowance is read fresh, not from the cached answer: it changes
	// with every download.
	for _, src := range srcs {
		if qs, ok := src.(QuotaSource); ok {
			if used, until := qs.Quota(ctx); used {
				out.UsedUp[src.Name()] = true
				out.Notes = append(out.Notes, quotaWords(src.Name(), until, loc))
			}
		}
	}
	return out, nil
}

// askAll asks every source at once, each with its own timeout, and ranks
// the merged answer.
func (s *Service) askAll(ctx context.Context, srcs []Source, q Query) searched {
	type answer struct {
		results []Result
		err     error
	}
	answers := make([]answer, len(srcs))
	var wg sync.WaitGroup
	for i, src := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, sourceTimeout)
			defer cancel()
			r, err := src.Search(sctx, q)
			answers[i] = answer{r, err}
		}()
	}
	wg.Wait()
	out := searched{Results: []Result{}, Problems: []Problem{}}
	for i, a := range answers {
		if a.err != nil {
			p := asProblem(srcs[i].Name(), a.err)
			s.log.WarnContext(ctx, "subtitles: a source failed", "source", p.Source, "problem", p.Msg, "status", p.Status, "answer", p.Detail)
			out.Problems = append(out.Problems, *p)
			continue
		}
		out.Results = append(out.Results, a.results...)
	}
	Rank(out.Results, q.FileName)
	return out
}

func asProblem(source string, err error) *Problem {
	var p *Problem
	if errors.As(err, &p) {
		return p
	}
	return problem(source, "couldn't be reached")
}

// quotaWords says a used-up allowance plainly, with the time it comes
// back in the person's own zone when the source said.
func quotaWords(source string, until time.Time, loc *time.Location) string {
	msg := Labels[source] + ": today's downloads are used up"
	if !until.IsZero() {
		msg += ". They come back at " + until.In(loc).Format("15:04")
	}
	return msg
}

func pickKey(source, id string) string {
	sum := sha256.Sum256([]byte(id))
	return "subs:pick:" + source + ":" + hex.EncodeToString(sum[:16])
}

// keepPicks keeps every result's download handle for an hour, so a pick
// names a result by source and id and the server alone holds where it is
// fetched from (a client can never point a download elsewhere).
func (s *Service) keepPicks(ctx context.Context, rs []Result) {
	if len(rs) == 0 {
		return
	}
	cmds := make(valkey.Commands, 0, len(rs))
	for _, r := range rs {
		v, _ := json.Marshal(r)
		cmds = append(cmds, s.kv.B().Set().Key(pickKey(r.Source, r.ID)).Value(string(v)).Ex(pickTTL).Build())
	}
	for _, res := range s.kv.DoMulti(ctx, cmds...) {
		if err := res.Error(); err != nil {
			s.log.WarnContext(ctx, "subtitles: keeping the results failed", "err", err)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// The stored tracks
// ---------------------------------------------------------------------------

// Track is a stored subtitle as the player lists it.
type Track struct {
	ID              int64
	Language        string
	Source          string
	Release         string
	HearingImpaired bool
	Format          string
	Delay           float64 // seconds; positive shows the lines later
	AddedBy         *int32
	AddedAt         time.Time
	// CanRemove: the person who added it, or an admin.
	CanRemove bool
	ref       string // the subtitle's id at its source
}

func (s *Service) track(u store.User, id int64, lang, source, ref, release string, hi bool, format string, delayMs int32, by *int32, at time.Time) Track {
	return Track{ID: id, Language: lang, Source: source, ref: ref, Release: release, HearingImpaired: hi, Format: format,
		Delay: float64(delayMs) / 1000, AddedBy: by, AddedAt: at,
		CanRemove: (by != nil && *by == u.ID) || s.isAdmin(u.Username)}
}

func ptr32(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p)
	return &v
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// List is the file's stored tracks, oldest first.
func (s *Service) List(ctx context.Context, u store.User, it Item) ([]Track, error) {
	rows, err := s.q.OnlineSubtitlesFor(ctx, store.OnlineSubtitlesForParams{MediaType: it.Media, TmdbID: int32(it.TMDBID),
		Season: ptr32(it.Season), Episode: ptr32(it.Episode), MediaSourceID: it.File.Source})
	if err != nil {
		return nil, err
	}
	out := make([]Track, len(rows))
	for i, r := range rows {
		out[i] = s.track(u, r.ID, r.Language, r.Source, r.SourceRef, r.Release, r.HearingImpaired, r.Format, r.DelayMs, r.AddedBy, r.AddedAt)
	}
	return out, nil
}

var errSearchAgain = apierr.New(http.StatusGone, "That result is too old now. Search again.")

// Add downloads a search result and keeps it as a track of the playing
// file. A subtitle already kept for the file is given back as it is,
// without downloading it again.
func (s *Service) Add(ctx context.Context, u store.User, it Item, source, id string, loc *time.Location) (Track, error) {
	raw, err := s.kv.Do(ctx, s.kv.B().Get().Key(pickKey(source, id)).Build()).AsBytes()
	if valkey.IsValkeyNil(err) {
		return Track{}, errSearchAgain
	}
	if err != nil {
		return Track{}, err
	}
	var r Result
	if err := json.Unmarshal(raw, &r); err != nil || r.Source != source || r.ID != id {
		return Track{}, errSearchAgain
	}
	l, ok := LanguageByCode(r.Language)
	if !ok {
		return Track{}, errSearchAgain
	}
	kept, err := s.List(ctx, u, it)
	if err != nil {
		return Track{}, err
	}
	for _, t := range kept {
		if t.Source == source && t.ref == id {
			return t, nil
		}
	}
	src := s.source(source)
	if src == nil {
		return Track{}, apierr.New(http.StatusNotFound, "That subtitle source is switched off")
	}
	if ok, err := s.downloads.Take(ctx, strconv.Itoa(int(u.ID))); err != nil {
		return Track{}, err
	} else if !ok {
		return Track{}, apierr.New(http.StatusTooManyRequests, "That's a lot of subtitle downloads. Try again in an hour.")
	}
	dctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	data, err := src.Download(dctx, r)
	if err != nil {
		p := asProblem(source, err)
		if p.Quota {
			return Track{}, apierr.New(http.StatusTooManyRequests, quotaWords(source, p.Until, loc)+".")
		}
		s.log.WarnContext(ctx, "subtitles: a download failed", "source", source, "problem", p.Msg, "status", p.Status, "answer", p.Detail)
		return Track{}, apierr.New(http.StatusBadGateway, p.Error())
	}
	format, body, err := Prepare(data, l, deref(it.Season), deref(it.Episode))
	if err != nil {
		msg := "This subtitle can't be shown. Try another one."
		if errors.Is(err, ErrArchive) {
			msg = "The download held no subtitle Movie Night can read. Try another one."
		}
		return Track{}, apierr.New(http.StatusUnprocessableEntity, msg)
	}
	release := r.Release
	if i := strings.IndexByte(release, '\n'); i >= 0 {
		release = release[:i] // the first release named is enough for a label
	}
	row, err := s.q.OnlineSubtitleAdd(ctx, store.OnlineSubtitleAddParams{MediaType: it.Media, TmdbID: int32(it.TMDBID),
		Season: ptr32(it.Season), Episode: ptr32(it.Episode), ItemID: it.File.Item, MediaSourceID: it.File.Source,
		Language: l.Code, Source: source, SourceRef: id, Release: truncate(release, 300), HearingImpaired: r.HearingImpaired,
		Format: format, Body: body, AddedBy: &u.ID})
	if err != nil {
		return Track{}, err
	}
	return s.track(u, row.ID, row.Language, row.Source, row.SourceRef, row.Release, row.HearingImpaired, row.Format, row.DelayMs, row.AddedBy, row.AddedAt), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8Start(s[n]) {
		n--
	}
	return s[:n]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

var errNoTrack = apierr.New(http.StatusNotFound, "That subtitle is gone")

// Body is a stored track's text and format.
func (s *Service) Body(ctx context.Context, id int64) (format, body string, err error) {
	r, err := s.q.OnlineSubtitleBody(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", errNoTrack
	}
	if err != nil {
		return "", "", err
	}
	return r.Format, r.Body, nil
}

// SetDelay sets a track's delay for every device. Anyone in the household
// may: it is the same film for all of them.
func (s *Service) SetDelay(ctx context.Context, id int64, seconds float64) error {
	if seconds > maxDelay.Seconds() || seconds < -maxDelay.Seconds() {
		return apierr.New(http.StatusUnprocessableEntity, "A delay can be at most 10 minutes either way")
	}
	n, err := s.q.OnlineSubtitleSetDelay(ctx, store.OnlineSubtitleSetDelayParams{ID: id, DelayMs: int32(seconds * 1000)})
	if err != nil {
		return err
	}
	if n == 0 {
		return errNoTrack
	}
	return nil
}

// Delete removes a track: the person who added it, or an admin.
func (s *Service) Delete(ctx context.Context, u store.User, id int64) error {
	r, err := s.q.OnlineSubtitleGet(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNoTrack
	}
	if err != nil {
		return err
	}
	if !((r.AddedBy != nil && *r.AddedBy == u.ID) || s.isAdmin(u.Username)) {
		return apierr.New(http.StatusForbidden, "Only the person who added it, or an admin, can remove it")
	}
	_, err = s.q.OnlineSubtitleDelete(ctx, id)
	return err
}
