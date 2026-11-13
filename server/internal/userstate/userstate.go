// Package userstate is a title's state as every card shows it: in the
// Jellyfin library, requested or downloading in Seerr (shared by everyone on
// the install), and the person's own watchlist, watches, rating and
// favourite (app/services/state.py).
//
// What changed from the Python, on purpose:
//
//   - Load reads the person's state for the titles on the page only, in one
//     query (StateForTitles), not their whole history, watchlist, ratings
//     and favourites plus the whole library on every request (the Python
//     did that once per Home row, ~12 times per Home view).
//   - Seerr's status of every title lives in one Valkey hash shared by all
//     replicas (SeerrMap), refreshed by one replica at a time; a page reads
//     only its own titles' fields (HMGET). The Python pulled Seerr's whole
//     media table into each process every 90 seconds and kept it in memory.
//   - A watch counted per day uses the person's zone (users.Location), not
//     UTC.
package userstate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/valkey-io/valkey-go"

	"movienight/server/internal/clients"
	"movienight/server/internal/config"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
	"movienight/server/internal/users"
)

// Key is one title: ("movie", 603) or ("tv", 1399).
type Key struct {
	Media string
	ID    int
}

// KeyOf reads a title key ("movie:603"); ok false for anything else.
func KeyOf(key string) (Key, bool) {
	m, id, ok := titles.Split(key)
	return Key{m, id}, ok
}

// String is the title key.
func (k Key) String() string { return titles.Key(k.Media, k.ID) }

// State is one person's state for a set of titles.
type State struct {
	UserID    int32
	Library   map[Key]string // -> Jellyfin item id
	Watchlist map[Key]bool
	Watched   map[Key]int // film: plays on distinct days; show: distinct episodes seen
	Ratings   map[Key]int
	Favorites map[Key]bool
	Seerr     map[Key]SeerrMedia
	jfPublic  string
}

// SeerrMedia is what Seerr knows of one title.
type SeerrMedia struct {
	Status     int              `json:"s"`
	JellyfinID string           `json:"j,omitempty"`
	Progress   *schema.Progress `json:"p,omitempty"`
}

// Service loads states.
type Service struct {
	q     *store.Queries
	cfg   *config.Config
	seerr *SeerrMap
	log   *slog.Logger
}

// New makes the service.
func New(q *store.Queries, cfg *config.Config, seerr *SeerrMap, log *slog.Logger) *Service {
	return &Service{q: q, cfg: cfg, seerr: seerr, log: log}
}

// Seerr is the shared Seerr status map.
func (s *Service) Seerr() *SeerrMap { return s.seerr }

// Load reads the person's state for keys (title keys; anything else is
// skipped). An empty set costs nothing. A film's watch count is the days
// it was watched on, in the person's zone (users.Location).
func (s *Service) Load(ctx context.Context, u store.User, keys []string) (*State, error) {
	userID := u.ID
	st := &State{UserID: userID, Library: map[Key]string{}, Watchlist: map[Key]bool{}, Watched: map[Key]int{},
		Ratings: map[Key]int{}, Favorites: map[Key]bool{}, Seerr: map[Key]SeerrMedia{},
		jfPublic: strings.TrimRight(s.cfg.JellyfinPublicURL, "/")}
	var medias []string
	var ids []int32
	var want []Key
	seen := make(map[Key]bool, len(keys))
	for _, raw := range keys {
		k, ok := KeyOf(raw)
		if !ok || seen[k] {
			continue
		}
		seen[k] = true
		want = append(want, k)
		medias = append(medias, k.Media)
		ids = append(ids, int32(k.ID))
	}
	if len(want) == 0 {
		return st, nil
	}
	rows, err := s.q.StateForTitles(ctx, store.StateForTitlesParams{Medias: medias, Ids: ids, UserID: userID,
		Tz: users.Location(u, s.cfg.Location).String()})
	if err != nil {
		return nil, fmt.Errorf("title state: %w", err)
	}
	for _, r := range rows {
		k := Key{r.MediaType, int(r.TmdbID)}
		switch r.Kind {
		case "w":
			st.Watchlist[k] = true
		case "r":
			st.Ratings[k] = int(r.N)
		case "f":
			st.Favorites[k] = true
		case "h":
			st.Watched[k] = int(r.N)
		case "l":
			st.Library[k] = r.Jf
		}
	}
	st.Seerr = s.seerr.Get(ctx, want)
	return st, nil
}

// LoadKeys is Load for Keys.
func (s *Service) LoadKeys(ctx context.Context, u store.User, keys []Key) (*State, error) {
	raw := make([]string, len(keys))
	for i, k := range keys {
		raw[i] = k.String()
	}
	return s.Load(ctx, u, raw)
}

// JellyfinURL is the Jellyfin web page of an item.
func JellyfinURL(public, itemID string) *string {
	if itemID == "" {
		return nil
	}
	u := strings.TrimRight(public, "/") + "/web/#/details?id=" + itemID
	return &u
}

// StatusLabel is Seerr's media status as the request button names it.
func StatusLabel(status int) *string {
	var s string
	switch status {
	case clients.SeerrPending:
		s = "pending"
	case clients.SeerrProcessing:
		s = "processing"
	case clients.SeerrPartial:
		s = "partial"
	case clients.SeerrAvailable:
		s = "available"
	default:
		return nil
	}
	return &s
}

// Of is one title's state (app/services/state.py title_state).
func (st *State) Of(media string, id int) schema.TitleState {
	k := Key{media, id}
	if media != "tv" {
		k.Media = "movie"
	}
	sm := st.Seerr[k]
	jf := st.Library[k]
	status := StatusLabel(sm.Status)
	if jf == "" && status != nil && *status == "available" && sm.JellyfinID != "" {
		// Seerr says it is in Jellyfin before the next library sync does.
		jf = sm.JellyfinID
	}
	out := schema.TitleState{
		InLibrary: jf != "", JellyfinURL: JellyfinURL(st.jfPublic, jf), Progress: sm.Progress,
		Watchlist: st.Watchlist[k], Watched: st.Watched[k], Favorite: st.Favorites[k],
	}
	if jf != "" {
		out.JellyfinID = &jf
	}
	// What the request button shows when not in the library: nothing once
	// it is there and Seerr has no download running.
	if !(jf != "" && (status == nil || *status == "available")) {
		out.Request = status
	}
	if r, ok := st.Ratings[k]; ok {
		out.Rating = &r
	}
	return out
}

// Card is a title's card for this person.
func (st *State) Card(m *titles.Meta) schema.Card {
	c := schema.Card{TitleState: st.Of(m.MediaType, m.TMDBID), Key: m.Key, MediaType: schema.MediaType(m.MediaType),
		TMDBID: m.TMDBID, Title: m.Title, Year: m.Year, Poster: m.PosterPath, Backdrop: m.BackdropPath, Vote: m.Vote()}
	return c
}

// Cards makes cards for metas in order, skipping nils and repeats.
func (st *State) Cards(metas []*titles.Meta) []schema.Card {
	out := make([]schema.Card, 0, len(metas))
	seen := make(map[string]bool, len(metas))
	for _, m := range metas {
		if m == nil || seen[m.Key] {
			continue
		}
		seen[m.Key] = true
		out = append(out, st.Card(m))
	}
	return out
}

// DownloadProgress folds Seerr's downloadStatus (Radarr/Sonarr's queue)
// into one figure: percent done, the soonest finish, and the first item's
// status. nil when nothing is downloading.
func DownloadProgress(v any) *schema.Progress {
	items, _ := v.([]any)
	var size, left float64
	var eta, status *string
	n := 0
	for _, x := range items {
		d, _ := x.(map[string]any)
		sz, _ := d["size"].(float64)
		if sz == 0 {
			continue
		}
		n++
		l, _ := d["sizeLeft"].(float64)
		size, left = size+sz, left+l
		if e, _ := d["estimatedCompletionTime"].(string); e != "" && (eta == nil || e < *eta) {
			eta = &e
		}
		if n == 1 {
			if s, ok := d["status"].(string); ok {
				status = &s
			}
		}
	}
	if n == 0 {
		return nil
	}
	pct := 0
	if size > 0 {
		pct = int(math.Round(100 * (size - left) / size))
	}
	pct = max(0, min(100, pct))
	return &schema.Progress{Percent: pct, ETA: eta, Status: status}
}

// ---------------------------------------------------------------------------
// Seerr's status of every title, shared by every replica
// ---------------------------------------------------------------------------

// SeerrMap keeps Seerr's media table in a Valkey hash, field "movie:603" ->
// SeerrMedia as JSON.
type SeerrMap struct {
	kv    valkey.Client
	seerr func() *clients.Seerr
	log   *slog.Logger
	// TTL is how old the map may get before a page view refreshes it (the
	// Python's 90 s). A request made here updates its own title at once.
	TTL time.Duration
}

const (
	seerrHash    = "seerr:media"
	seerrAt      = "seerr:media:at"   // unix ms of the last complete pull
	seerrLock    = "seerr:media:lock" // one replica pulls at a time
	seerrNewHash = "seerr:media:new"  // the pull in progress, renamed over seerrHash when complete
	seerrPage    = 500
)

// NewSeerrMap makes the map. seerr is read per call (tests swap clients).
func NewSeerrMap(kv valkey.Client, seerr func() *clients.Seerr, log *slog.Logger) *SeerrMap {
	return &SeerrMap{kv: kv, seerr: seerr, log: log, TTL: 90 * time.Second}
}

// Get returns Seerr's status for keys. When the map is older than TTL, one
// replica pulls it again: in the background when a map exists (this page
// is served from the one before), or now when there is none yet. Seerr or
// Valkey trouble leaves cards without request status (logged): the page
// still loads, as in the Python.
func (m *SeerrMap) Get(ctx context.Context, keys []Key) map[Key]SeerrMedia {
	out := map[Key]SeerrMedia{}
	if !m.seerr().Enabled() || len(keys) == 0 {
		return out
	}
	at, err := m.kv.Do(ctx, m.kv.B().Get().Key(seerrAt).Build()).AsInt64()
	missing := err != nil
	if missing || time.Since(time.UnixMilli(at)) > m.TTL {
		if missing {
			m.refresh(ctx)
		} else {
			go m.refresh(context.WithoutCancel(ctx))
		}
	}
	fields := make([]string, len(keys))
	for i, k := range keys {
		fields[i] = k.String()
	}
	vals, err := m.kv.Do(ctx, m.kv.B().Hmget().Key(seerrHash).Field(fields...).Build()).ToArray()
	if err != nil {
		m.log.WarnContext(ctx, "Seerr status read failed", "err", err)
		return out
	}
	for i, v := range vals {
		s, err := v.ToString()
		if err != nil {
			continue // nil: Seerr does not know the title
		}
		var sm SeerrMedia
		if json.Unmarshal([]byte(s), &sm) == nil {
			out[keys[i]] = sm
		}
	}
	return out
}

// refresh pulls Seerr's whole media table, if no other replica is at it,
// into a new hash renamed over the old one when complete (so a page never
// reads a half-pulled map). Every page of it, however many (the Python's
// rule: no cap).
func (m *SeerrMap) refresh(ctx context.Context) {
	got, err := m.kv.Do(ctx, m.kv.B().Set().Key(seerrLock).Value("1").Nx().Ex(2*time.Minute).Build()).ToString()
	if err != nil || got != "OK" {
		return // another replica is pulling (or Valkey is down: logged by the read)
	}
	defer m.kv.Do(context.WithoutCancel(ctx), m.kv.B().Del().Key(seerrLock).Build())
	s := m.seerr()
	_ = m.kv.Do(ctx, m.kv.B().Del().Key(seerrNewHash).Build()).Error()
	count := 0
	for skip := 0; ; {
		page, err := s.MediaPage(ctx, seerrPage, skip)
		if err != nil {
			// Stamped as pulled anyway, so the next try waits a TTL: with
			// Seerr down, every page would otherwise wait on its timeout.
			m.log.WarnContext(ctx, "Seerr media pull failed; the map before is kept", "err", err)
			m.stamp(ctx)
			return
		}
		results, _ := page["results"].([]any)
		if len(results) > 0 {
			cmd := m.kv.B().Hset().Key(seerrNewHash).FieldValue()
			n := 0
			for _, x := range results {
				r, _ := x.(map[string]any)
				id, _ := r["tmdbId"].(float64)
				if id <= 0 {
					continue
				}
				media := "movie"
				if r["mediaType"] == "tv" {
					media = "tv"
				}
				status, _ := r["status"].(float64)
				jf, _ := r["jellyfinMediaId"].(string)
				b, _ := json.Marshal(SeerrMedia{Status: int(status), JellyfinID: jf, Progress: DownloadProgress(r["downloadStatus"])})
				cmd = cmd.FieldValue(titles.Key(media, int(id)), string(b))
				n++
			}
			if n > 0 {
				if err := m.kv.Do(ctx, cmd.Build()).Error(); err != nil {
					m.log.WarnContext(ctx, "Seerr map write failed", "err", err)
					return
				}
			}
			count += n
		}
		skip += len(results)
		info, _ := page["pageInfo"].(map[string]any)
		total, _ := info["results"].(float64)
		if len(results) == 0 || skip >= int(total) {
			break
		}
	}
	// The rule: a pull that brought no media never replaces the map. Seerr
	// answering "nothing" while titles were known is far likelier a Seerr
	// hiccup (a restart, an empty first page) than every request gone, and
	// the map also holds entries written by Put a moment ago (a request
	// made here), which deleting it would lose: the request buttons would
	// read "Request" again for titles just requested. So an empty pull
	// keeps what is there; a title Seerr truly dropped goes at the next pull
	// that brings anything back.
	if count > 0 {
		if err := m.kv.Do(ctx, m.kv.B().Rename().Key(seerrNewHash).Newkey(seerrHash).Build()).Error(); err != nil {
			m.log.WarnContext(ctx, "Seerr map swap failed", "err", err)
			return
		}
	}
	m.stamp(ctx)
}

func (m *SeerrMap) stamp(ctx context.Context) {
	_ = m.kv.Do(ctx, m.kv.B().Set().Key(seerrAt).Value(fmt.Sprint(time.Now().UnixMilli())).Build()).Error()
}

// putScript writes one entry into the map and, while a pull is under way
// (its lock is held), into the pull's new hash too: the pull may have read
// Seerr's page with this title before the request was made, and renaming
// its hash over the map would otherwise drop the entry just written.
// KEYS: map, new hash, lock. ARGV: field, value.
var putScript = valkey.NewLuaScript(`
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
if redis.call('EXISTS', KEYS[3]) == 1 then redis.call('HSET', KEYS[2], ARGV[1], ARGV[2]) end
return 1`)

// Put sets one title's entry (after a request made here, from Seerr's live
// record), so every card shows it at once rather than after the next pull.
func (m *SeerrMap) Put(ctx context.Context, k Key, info clients.JSON) {
	if info == nil {
		return
	}
	status, _ := info["status"].(float64)
	jf, _ := info["jellyfinMediaId"].(string)
	b, _ := json.Marshal(SeerrMedia{Status: int(status), JellyfinID: jf, Progress: DownloadProgress(info["downloadStatus"])})
	err := putScript.Exec(ctx, m.kv, []string{seerrHash, seerrNewHash, seerrLock}, []string{k.String(), string(b)}).Error()
	if err != nil {
		m.log.WarnContext(ctx, "Seerr map write failed", "err", err)
	}
}
