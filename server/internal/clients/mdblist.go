package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/valkey-io/valkey-go"
)

// MDBListAPI is MDBList's address.
const MDBListAPI = "https://api.mdblist.com"

// MDBListDownFor: MDBList is left alone this long after a failure or a
// refusal (10 minutes, as in the Python).
const MDBListDownFor = 10 * time.Minute

// MDBListRefusedText is what callers say when MDBList refuses, in plain words.
const MDBListRefusedText = "MDBList refused Movie Night's requests just now (its own limit for this install's key)"

// MDBList is curated lists, outside scores and a search fallback
// (app/clients/mdblist.py). Movie Night sets no allowance of its own; when
// MDBList refuses (its own limit for the key) or fails, it is left alone for
// MDBListDownFor, shared across replicas (Breaker), and admins see the
// refusal (Refusal). Nothing depends on MDBList answering: callers keep
// what they have.
type MDBList struct {
	http    *http.Client
	base    string
	apiKey  string
	breaker *Breaker
	kv      valkey.Client
	log     *slog.Logger
}

// MDBListError is MDBList failing.
type MDBListError struct {
	Status int
	Msg    string
}

func (e *MDBListError) Error() string { return "MDBList: " + e.Msg }

// MDBListRefused is MDBList saying no (HTTP 429, or a 4xx naming a limit).
type MDBListRefused struct{ MDBListError }

// refusedKey holds the last refusal for admins: {"at", "why"}. Kept a week
// at most; cleared by the next good answer (the Python kept it in memory).
const refusedKey = "mdblist:refused"

// NewMDBList makes the client. base is MDBListAPI outside tests.
func NewMDBList(c *http.Client, base, apiKey string, kv valkey.Client, log *slog.Logger) *MDBList {
	return &MDBList{http: c, base: base, apiKey: apiKey, breaker: NewBreaker(kv, "mdblist", log), kv: kv, log: log}
}

// Enabled reports whether an MDBList key is set.
func (m *MDBList) Enabled() bool { return m.apiKey != "" }

// Refusal is MDBList's last refusal for admins ({"at", "why"}), or nil.
func (m *MDBList) Refusal(ctx context.Context) (map[string]string, error) {
	raw, err := m.kv.Do(ctx, m.kv.B().Get().Key(refusedKey).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out map[string]string
	return out, json.Unmarshal([]byte(raw), &out)
}

func (m *MDBList) markRefused(ctx context.Context, why string) {
	m.breaker.Open(ctx, "refused: "+why, MDBListDownFor)
	raw, _ := json.Marshal(map[string]string{"at": time.Now().UTC().Format(time.RFC3339), "why": why})
	if err := m.kv.Do(ctx, m.kv.B().Set().Key(refusedKey).Value(string(raw)).Ex(7*24*time.Hour).Build()).Error(); err != nil {
		m.log.WarnContext(ctx, "could not record MDBList's refusal", "err", err)
	}
}

func (m *MDBList) answered(ctx context.Context) {
	if err := m.kv.Do(ctx, m.kv.B().Del().Key(refusedKey).Build()).Error(); err != nil {
		m.log.WarnContext(ctx, "could not clear MDBList's refusal", "err", err)
	}
}

func (m *MDBList) request(ctx context.Context, path string, params url.Values, wait time.Duration) (any, http.Header, error) {
	if !m.Enabled() {
		return nil, nil, &MDBListError{Status: http.StatusServiceUnavailable, Msg: "MDBList is not configured"}
	}
	if open, why := m.breaker.IsOpen(ctx); open {
		if strings.HasPrefix(why, "refused") {
			return nil, nil, &MDBListRefused{MDBListError{Status: http.StatusTooManyRequests, Msg: MDBListRefusedText + "; it is asked again in a few minutes"}}
		}
		return nil, nil, &MDBListError{Status: http.StatusServiceUnavailable, Msg: "MDBList isn't answering; asked again later"}
	}
	q := url.Values{"apikey": {m.apiKey}}
	for k, v := range params {
		q[k] = v
	}
	req, _ := http.NewRequest(http.MethodGet, m.base+path+"?"+q.Encode(), nil)
	req.Header.Set("User-Agent", "MovieNight/2.0")
	resp, err := do(ctx, m.http, req, wait, maxBody)
	if err != nil {
		m.breaker.Open(ctx, "no answer", MDBListDownFor)
		return nil, nil, &MDBListError{Status: http.StatusServiceUnavailable, Msg: "MDBList didn't answer"}
	}
	if resp.Status == http.StatusNotFound {
		m.answered(ctx)
		return nil, resp.Header, nil
	}
	// MDBList's own limit for this key: a 429, or a 4xx whose answer says
	// a limit was reached.
	if resp.Status == http.StatusTooManyRequests || (resp.Status >= 400 && resp.Status < 500 && strings.Contains(strings.ToLower(string(resp.Body)), "limit")) {
		m.markRefused(ctx, fmt.Sprintf("HTTP %d", resp.Status))
		return nil, nil, &MDBListRefused{MDBListError{Status: http.StatusTooManyRequests, Msg: MDBListRefusedText}}
	}
	if resp.Status >= 500 {
		m.breaker.Open(ctx, fmt.Sprintf("HTTP %d", resp.Status), MDBListDownFor)
	}
	if resp.Status >= 400 {
		return nil, nil, &MDBListError{Status: resp.Status, Msg: clip(resp.Body, 200)}
	}
	var out any
	if err := decode(resp.Body, &out); err != nil {
		m.breaker.Open(ctx, "an answer that isn't JSON", MDBListDownFor)
		return nil, nil, &MDBListError{Status: http.StatusBadGateway, Msg: "MDBList's answer wasn't JSON"}
	}
	m.answered(ctx)
	return out, resp.Header, nil
}

// ListRef names an MDBList list: by id, or by user and slug.
type ListRef struct{ ID, User, Slug string }

var mdblistHostRe = regexp.MustCompile(`(?i)^(www\.)?mdblist\.com/`)

// ParseListURL reads mdblist.com/lists/<user>/<slug> or a bare id.
func ParseListURL(text string) (ListRef, bool) {
	raw := strings.TrimSpace(text)
	if raw != "" && strings.Trim(raw, "0123456789") == "" {
		return ListRef{ID: raw}, true
	}
	if !strings.Contains(raw, "://") {
		if !mdblistHostRe.MatchString(raw) {
			return ListRef{}, false
		}
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ListRef{}, false
	}
	host := strings.ToLower(u.Hostname())
	if host != "mdblist.com" && host != "www.mdblist.com" {
		return ListRef{}, false
	}
	var parts []string
	for _, p := range strings.Split(u.Path, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) >= 3 && parts[0] == "lists" {
		return ListRef{User: parts[1], Slug: parts[2]}, true
	}
	return ListRef{}, false
}

// ListSummary is a list as the list explorer shows it.
func ListSummary(o JSON) JSON {
	name := str(o, "name")
	if name == "" {
		name = "Untitled list"
	}
	var media any
	switch str(o, "mediatype") {
	case "movie":
		media = "movie"
	case "show":
		media = "tv"
	}
	dynamic, _ := o["dynamic"].(bool)
	return JSON{"mdblist_id": o["id"], "name": name, "slug": o["slug"], "user": str(o, "user_name"),
		"description": str(o, "description"), "media": media, "item_count": o["items"], "likes": o["likes"], "dynamic": dynamic}
}

// TopLists and SearchLists are the list explorer's.
func (m *MDBList) TopLists(ctx context.Context) ([]JSON, error) {
	data, _, err := m.request(ctx, "/lists/top", nil, 10*time.Second)
	return summaries(data), err
}

func (m *MDBList) SearchLists(ctx context.Context, query string) ([]JSON, error) {
	data, _, err := m.request(ctx, "/lists/search", url.Values{"query": {query}}, 10*time.Second)
	return summaries(data), err
}

func summaries(data any) []JSON {
	rows := list(data)
	out := make([]JSON, len(rows))
	for i, r := range rows {
		out[i] = ListSummary(r)
	}
	return out
}

// GetList is one list's summary (nil when unknown).
func (m *MDBList) GetList(ctx context.Context, ref ListRef) (JSON, error) {
	path := "/lists/" + url.PathEscape(ref.User) + "/" + url.PathEscape(ref.Slug)
	if ref.ID != "" {
		path = "/lists/" + url.PathEscape(ref.ID)
	}
	data, _, err := m.request(ctx, path, nil, 10*time.Second)
	if err != nil {
		return nil, err
	}
	if arr := list(data); len(arr) > 0 {
		return ListSummary(arr[0]), nil
	}
	if obj, ok := data.(JSON); ok {
		return ListSummary(obj), nil
	}
	return nil, nil
}

// ListItem is one title of a list.
type ListItem struct {
	Media  string
	TMDBID int
	Title  string
	Year   any
}

// ListItems is every title of a list, in order, and how many MDBList sent
// without a TMDB id (Movie Night cannot show those; the list page says how
// many). No cap: pages of 1,000 until MDBList says there are no more.
func (m *MDBList) ListItems(ctx context.Context, listID string) ([]ListItem, int, error) {
	var out []ListItem
	noTMDB, offset := 0, 0
	cursor := ""
	seen := map[string]bool{}
	for {
		if cursor != "" {
			if seen[cursor] {
				return nil, 0, &MDBListError{Status: http.StatusBadGateway, Msg: "MDBList's pages of this list went round in a circle"}
			}
			seen[cursor] = true
		}
		q := url.Values{"limit": {"1000"}, "unified": {"true"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		} else {
			q.Set("offset", strconv.Itoa(offset))
		}
		data, hdr, err := m.request(ctx, "/lists/"+url.PathEscape(listID)+"/items", q, 30*time.Second)
		if err != nil {
			return nil, 0, err
		}
		rows := list(data)
		if obj, ok := data.(JSON); ok {
			rows = append(list(obj["movies"]), list(obj["shows"])...)
		}
		for _, it := range rows {
			ids, _ := it["ids"].(JSON)
			tmdb, ok := num(ids, "tmdb")
			if !ok || tmdb == 0 {
				tmdb, ok = num(it, "id")
			}
			if !ok || tmdb == 0 || tmdb != math.Trunc(tmdb) {
				noTMDB++
				continue
			}
			media := "movie"
			if str(it, "mediatype") == "show" {
				media = "tv"
			}
			out = append(out, ListItem{Media: media, TMDBID: int(tmdb), Title: str(it, "title"), Year: it["release_year"]})
		}
		if hdr == nil || hdr.Get("x-has-more") != "true" || len(rows) == 0 {
			return out, noTMDB, nil
		}
		offset += len(rows)
		cursor = hdr.Get("x-next-cursor")
	}
}

// MediaInfo is MDBList's scores for one title (nil when unknown).
func (m *MDBList) MediaInfo(ctx context.Context, media string, tmdbID int) (JSON, error) {
	kind := "movie"
	if media == "tv" {
		kind = "show"
	}
	data, _, err := m.request(ctx, fmt.Sprintf("/tmdb/%s/%d", kind, tmdbID), nil, 10*time.Second)
	obj, _ := data.(JSON)
	return obj, err
}

// SearchHit is one title MDBList's search found.
type SearchHit struct {
	Media  string
	TMDBID int
	Title  string
	Year   any
}

// Search is the search fallback for titles TMDB's search misses (6 s).
func (m *MDBList) Search(ctx context.Context, query string, limit int) ([]SearchHit, error) {
	data, _, err := m.request(ctx, "/search/any", url.Values{"query": {query}, "limit": {strconv.Itoa(limit)}, "sort_by_score": {"true"}}, 6*time.Second)
	if err != nil {
		return nil, err
	}
	obj, _ := data.(JSON)
	var out []SearchHit
	for _, it := range list(obj["search"]) {
		ids, _ := it["ids"].(JSON)
		if tmdb, ok := num(ids, "tmdbid"); ok && tmdb > 0 {
			media := "movie"
			if str(it, "type") == "show" {
				media = "tv"
			}
			out = append(out, SearchHit{Media: media, TMDBID: int(tmdb), Title: str(it, "title"), Year: it["year"]})
		}
	}
	return out, nil
}

// RatingsOf turns MDBList's media info into the scores the title page shows.
func RatingsOf(info JSON) JSON {
	out := JSON{"mdblist_score": info["score"]}
	for _, r := range list(info["ratings"]) {
		src := str(r, "source")
		value, has := num(r, "value")
		if !has && (src == "tomatoes" || src == "popcorn" || src == "metacritic") {
			value, has = num(r, "score") // MDBList sometimes fills only the 0-100 score
		}
		if !has {
			continue
		}
		votes := r["votes"]
		switch src {
		case "imdb":
			out["imdb_rating"], out["imdb_votes"] = value, votes
		case "tomatoes":
			out["rt_critics"] = int(value)
			if u := str(r, "url"); u != "" {
				out["rt_url"] = "https://www.rottentomatoes.com" + u
			}
		case "popcorn":
			out["rt_audience"] = int(value)
		case "metacritic":
			out["metacritic"] = int(value)
		case "letterboxd":
			out["letterboxd"] = value
		case "trakt":
			out["trakt_rating"], out["trakt_votes"] = math.Round(value)/10, votes
		}
	}
	return out
}
