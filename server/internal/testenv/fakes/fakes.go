// Package fakes stands in for TMDB, Seerr, Meilisearch, MDBList, Jellyfin
// and the AI model over real HTTP, so tests run the real clients (package
// clients) against canned answers and never reach the internet. Titles are made up from their
// ids: movie 603 is "Movie 603", show 1399 has 3 seasons of 10 episodes.
//
// Every fake records the calls it got (Calls, Count) and lets a test
// replace any answer (Override), e.g. to make TMDB fail.
package fakes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// JSON is a canned answer.
type JSON = map[string]any

// Server is one fake service.
type Server struct {
	*httptest.Server
	mu        sync.Mutex
	calls     []string
	overrides map[string]http.HandlerFunc
	route     func(w http.ResponseWriter, r *http.Request)
}

func newServer(t testing.TB, route func(s *Server, w http.ResponseWriter, r *http.Request)) *Server {
	s := &Server{overrides: map[string]http.HandlerFunc{}}
	s.route = func(w http.ResponseWriter, r *http.Request) { route(s, w, r) }
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := r.Method + " " + r.URL.Path
		s.mu.Lock()
		s.calls = append(s.calls, call+"?"+r.URL.RawQuery)
		h := s.overrides[call]
		if h == nil {
			h = s.overrides[r.URL.Path]
		}
		s.mu.Unlock()
		if h != nil {
			h(w, r)
			return
		}
		s.route(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

// Override answers "METHOD /path" (or "/path" for any method) with h
// instead of the canned answer.
func (s *Server) Override(pattern string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.overrides[pattern] = h
}

// Fail makes pattern answer status with an empty body.
func (s *Server) Fail(pattern string, status int) {
	s.Override(pattern, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
}

// Calls is every call so far ("GET /path?query").
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// Count is how many calls' "METHOD /path" start with prefix.
func (s *Server) Count(prefix string) int {
	n := 0
	for _, c := range s.Calls() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// Reset forgets the calls so far.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = nil
}

// Reply writes v as JSON.
func Reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) { w.WriteHeader(http.StatusNotFound) }

// ---------------------------------------------------------------------------
// TMDB
// ---------------------------------------------------------------------------

// Movie and Show are TMDB results made up from an id.
func Movie(id int) JSON {
	return JSON{"id": id, "media_type": "movie", "title": fmt.Sprintf("Movie %d", id), "original_title": fmt.Sprintf("Movie %d", id),
		"release_date": "2000-01-01", "poster_path": fmt.Sprintf("/p%d.jpg", id), "backdrop_path": fmt.Sprintf("/b%d.jpg", id),
		"overview": fmt.Sprintf("About movie %d.", id), "genre_ids": []int{35}, "vote_average": 7.0, "vote_count": 100}
}

func Show(id int) JSON {
	return JSON{"id": id, "media_type": "tv", "name": fmt.Sprintf("Show %d", id), "original_name": fmt.Sprintf("Show %d", id),
		"first_air_date": "2011-04-17", "poster_path": fmt.Sprintf("/p%d.jpg", id), "backdrop_path": fmt.Sprintf("/b%d.jpg", id),
		"overview": fmt.Sprintf("About show %d.", id), "genre_ids": []int{18}, "vote_average": 8.0, "vote_count": 100}
}

func make_(media string, id int) JSON {
	if media == "tv" {
		return Show(id)
	}
	return Movie(id)
}

// NoSuchTitle: ids from here up are titles TMDB does not know (404).
const NoSuchTitle = 9_000_000

// TMDB is the fake TMDB; the client's base URL is its URL.
type TMDB struct{ *Server }

// NewTMDB starts it.
func NewTMDB(t testing.TB) *TMDB {
	return &TMDB{newServer(t, tmdbRoute)}
}

func page(r *http.Request) int {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		p = 1
	}
	return p
}

// tmdbPage is 20 made-up titles per page: ids 100+20·page onwards.
func tmdbPage(media string, p int) []JSON {
	out := make([]JSON, 0, 20)
	for i := 100 + p*20; i < 120+p*20; i++ {
		out = append(out, make_(media, i))
	}
	return out
}

func basic(media string, id int) JSON {
	b := make_(media, id)
	b["genres"] = []JSON{{"id": 35, "name": "Comedy"}}
	b["runtime"] = 100
	delete(b, "genre_ids")
	return b
}

func details(media string, id int) JSON {
	d := basic(media, id)
	d["credits"] = JSON{"cast": []JSON{{"id": 1, "name": "Actor One", "character": "Lead"}},
		"crew": []JSON{{"job": "Director", "name": "Dee Rector"}}}
	d["videos"] = JSON{"results": []JSON{{"site": "YouTube", "key": "abc", "type": "Trailer", "official": true}}}
	d["watch/providers"] = JSON{"results": JSON{"US": JSON{"link": "https://x",
		"flatrate": []JSON{{"provider_id": 8, "provider_name": "Netflix", "logo_path": "/n.png"}}}}}
	d["release_dates"] = JSON{"results": []JSON{{"iso_3166_1": "US", "release_dates": []JSON{{"certification": "R"}}}}}
	if media == "tv" {
		var seasons []JSON
		for n := 1; n <= 3; n++ {
			seasons = append(seasons, JSON{"season_number": n, "name": fmt.Sprintf("Season %d", n), "episode_count": 10,
				"air_date": fmt.Sprintf("201%d-04-17", n), "poster_path": nil})
		}
		d["seasons"] = seasons
		d["number_of_seasons"], d["number_of_episodes"], d["status"] = 3, 30, "Ended"
		d["created_by"] = []JSON{{"name": "Cree Ator"}}
		d["last_episode_to_air"] = JSON{"season_number": 3, "episode_number": 10, "name": "Finale", "air_date": "2013-06-09"}
		d["content_ratings"] = JSON{"results": []JSON{{"iso_3166_1": "US", "rating": "TV-MA"}}}
	}
	return d
}

// Season is a made-up season: 10 episodes, all aired.
func Season(number int) JSON {
	var eps []JSON
	for e := 1; e <= 10; e++ {
		eps = append(eps, JSON{"episode_number": e, "name": fmt.Sprintf("Episode %d", e),
			"air_date": fmt.Sprintf("201%d-05-%02d", number, e), "still_path": nil, "runtime": 55, "vote_average": 8.0})
	}
	return JSON{"name": fmt.Sprintf("Season %d", number), "air_date": fmt.Sprintf("201%d-04-17", number), "poster_path": nil, "episodes": eps}
}

func tmdbRoute(_ *Server, w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	q := r.URL.Query()
	switch {
	case len(parts) == 2 && (parts[0] == "movie" || parts[0] == "tv") && isNum(parts[1]):
		id, _ := strconv.Atoi(parts[1])
		if id >= NoSuchTitle {
			notFound(w)
			return
		}
		if strings.Contains(q.Get("append_to_response"), "credits") {
			Reply(w, details(parts[0], id))
			return
		}
		Reply(w, basic(parts[0], id))
	case len(parts) == 4 && parts[0] == "tv" && parts[2] == "season":
		n, _ := strconv.Atoi(parts[3])
		Reply(w, Season(n))
	case len(parts) == 3 && parts[2] == "recommendations":
		id, _ := strconv.Atoi(parts[1])
		var out []JSON
		if page(r) == 1 {
			for i := id + 1; i < id+6; i++ {
				out = append(out, make_(parts[0], i))
			}
		}
		Reply(w, JSON{"results": out, "total_pages": 1})
	case len(parts) == 3 && parts[2] == "similar":
		Reply(w, JSON{"results": []JSON{}, "total_pages": 0})
	case parts[0] == "discover" || parts[0] == "trending":
		media := parts[1]
		if media == "all" {
			media = "movie"
		}
		Reply(w, JSON{"results": tmdbPage(media, page(r)), "total_pages": 500})
	case parts[0] == "search" && parts[1] == "multi":
		var out []JSON
		if page(r) == 1 {
			out = []JSON{Movie(777), {"id": 5, "media_type": "person", "name": "Some Person", "profile_path": nil}}
		}
		Reply(w, JSON{"results": out, "total_pages": 1})
	case parts[0] == "search" && (parts[1] == "movie" || parts[1] == "tv"):
		query := q.Get("query")
		n := 2000
		for _, ch := range query {
			n += int(ch)
		}
		n = n%1000 + 2000
		m := Movie(n)
		m["title"] = query
		year := q.Get("year")
		if year == "" {
			year = "2001"
		}
		m["release_date"] = year + "-01-01"
		Reply(w, JSON{"results": []JSON{m}})
	case parts[0] == "collection":
		Reply(w, JSON{"parts": []JSON{Movie(603), Movie(604)}})
	case parts[0] == "person":
		id, _ := strconv.Atoi(parts[1])
		Reply(w, JSON{"id": id, "name": "Some Person", "biography": "", "combined_credits": JSON{"cast": []JSON{Movie(603)}, "crew": []JSON{}}})
	default:
		notFound(w)
	}
}

func isNum(s string) bool { _, err := strconv.Atoi(s); return err == nil }

// ---------------------------------------------------------------------------
// Seerr
// ---------------------------------------------------------------------------

// Seerr is the fake Seerr: nothing requested, no users, a 201 for a request
// (DRY_RUN in the test install keeps the client from sending it anyway).
type Seerr struct {
	*Server
	mu    sync.Mutex
	Media []JSON          // its /media table
	Info  map[string]JSON // "movie:603" -> mediaInfo of /movie/603
	Reqs  []JSON          // its /request list
}

// NewSeerr starts it.
func NewSeerr(t testing.TB) *Seerr {
	s := &Seerr{Info: map[string]JSON{}}
	s.Server = newServer(t, func(_ *Server, w http.ResponseWriter, r *http.Request) { s.route(w, r) })
	return s
}

// SetMedia replaces its /media table while the server runs.
func (s *Seerr) SetMedia(media []JSON) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Media = media
}

// SetInfo sets a title's live record.
func (s *Seerr) SetInfo(key string, info JSON) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Info[key] = info
}

func (s *Seerr) route(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/api/v1")
	parts := strings.Split(strings.Trim(p, "/"), "/")
	switch {
	case p == "/media":
		skip, _ := strconv.Atoi(r.URL.Query().Get("skip"))
		take, _ := strconv.Atoi(r.URL.Query().Get("take"))
		end := min(len(s.Media), skip+take)
		var out []JSON
		if skip < end {
			out = s.Media[skip:end]
		}
		Reply(w, JSON{"results": out, "pageInfo": JSON{"results": len(s.Media)}})
	case len(parts) == 2 && (parts[0] == "movie" || parts[0] == "tv"):
		info := s.Info[parts[0]+":"+parts[1]]
		if info == nil {
			Reply(w, JSON{"id": parts[1]})
			return
		}
		Reply(w, JSON{"id": parts[1], "mediaInfo": info})
	case p == "/request" && r.Method == http.MethodGet:
		Reply(w, JSON{"results": s.Reqs, "pageInfo": JSON{"results": len(s.Reqs)}})
	case p == "/request" && r.Method == http.MethodPost:
		w.WriteHeader(http.StatusCreated)
		Reply(w, JSON{"id": 99})
	case p == "/user":
		Reply(w, JSON{"results": []JSON{}, "pageInfo": JSON{"results": 0}})
	case p == "/user/import-from-jellyfin":
		Reply(w, []JSON{{"id": 7, "jellyfinUserId": "f"}})
	default:
		notFound(w)
	}
}

// ---------------------------------------------------------------------------
// Meilisearch
// ---------------------------------------------------------------------------

// Meili is the fake Meilisearch. The library index answers movie 603 with
// a weak score; the catalog index answers whatever Catalog holds.
type Meili struct {
	*Server
	mu      sync.Mutex
	Catalog []JSON
	Queries []string // catalog searches, in order
}

// NewMeili starts it.
func NewMeili(t testing.TB) *Meili {
	m := &Meili{}
	m.Server = newServer(t, func(_ *Server, w http.ResponseWriter, r *http.Request) { m.route(w, r) })
	return m
}

func (m *Meili) route(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 3 && parts[0] == "indexes" && parts[2] == "search" {
		var body struct {
			Q      string `json:"q"`
			Limit  int    `json:"limit"`
			Filter string `json:"filter"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		media := strings.TrimPrefix(body.Filter, "media_type = ")
		if parts[1] == "catalog" {
			m.mu.Lock()
			m.Queries = append(m.Queries, body.Q)
			var hits []JSON
			for _, d := range m.Catalog {
				if media == "" || d["media_type"] == media {
					hits = append(hits, d)
				}
			}
			m.mu.Unlock()
			if body.Limit < len(hits) {
				hits = hits[:body.Limit]
			}
			Reply(w, JSON{"hits": hits})
			return
		}
		Reply(w, JSON{"hits": []JSON{{"id": strings.Repeat("a", 32), "tmdb_id": 603, "media_type": "movie", "title": "Movie 603", "_rankingScore": 0.62}}})
		return
	}
	Reply(w, JSON{"taskUid": 1})
}

// ---------------------------------------------------------------------------
// MDBList
// ---------------------------------------------------------------------------

// MDBList is the fake MDBList: one top list (2194, "Latest TV Shows") of
// show 1399 and movie 603, and scores for any title.
type MDBList struct{ *Server }

// NewMDBList starts it.
func NewMDBList(t testing.TB) *MDBList {
	return &MDBList{newServer(t, mdblistRoute)}
}

// TopList is the raw list MDBList sends.
var TopList = JSON{"id": 2194, "name": "Latest TV Shows", "slug": "latest-tv-shows", "user_name": "gary", "description": "",
	"mediatype": "show", "items": 2, "likes": 10, "dynamic": true}

func mdblistRoute(_ *Server, w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/lists/top", r.URL.Path == "/lists/search":
		Reply(w, []JSON{TopList})
	case len(parts) == 3 && parts[0] == "lists" && parts[2] == "items":
		Reply(w, []JSON{
			{"ids": JSON{"tmdb": 1399}, "mediatype": "show", "title": "Show 1399", "release_year": 2011},
			{"ids": JSON{"tmdb": 603}, "mediatype": "movie", "title": "Movie 603", "release_year": 2000},
		})
	case parts[0] == "lists":
		Reply(w, []JSON{TopList})
	case parts[0] == "tmdb":
		id, _ := strconv.Atoi(parts[2])
		Reply(w, JSON{"score": 82, "ids": JSON{"tmdb": id, "trakt": 481}, "ratings": []JSON{
			{"source": "imdb", "value": 8.7, "votes": 100}, {"source": "tomatoes", "value": 83, "url": "/m/x"},
			{"source": "trakt", "value": 87, "votes": 60}}})
	case r.URL.Path == "/search/any":
		Reply(w, JSON{"search": []JSON{{"ids": JSON{"tmdbid": 22705}, "type": "movie", "title": "Cheeky", "year": 2000}}})
	default:
		notFound(w)
	}
}

// ---------------------------------------------------------------------------
// Jellyfin
// ---------------------------------------------------------------------------

// Jellyfin is the fake Jellyfin: a library of movie 603 ("a"×32) and show
// 1399 ("b"×32) whose seasons have 10 episodes each; nothing to resume.
//
// Quick Connect hands out one Jellyfin account per code asked for (as a
// real server does when different people approve their codes): the first is
// "alice-jf" ("f"×32), the next ones jf-user-2, -3 ... Two people
// connecting in one run (the browser tests' WebKit, then Chromium) so get
// two accounts instead of colliding on one, which Movie Night rightly
// refuses ("already connected to someone else"). Every code reads 123456.
type Jellyfin struct {
	*Server
	mu      sync.Mutex
	Resume  []JSON
	NextUp  []JSON
	QCReady bool
	qcUsers map[string]JSON // Quick Connect secret -> the account it signs in
	// MediaDir is the browser tests' clip (jellyfin_play.go); "" serves
	// no media.
	MediaDir     string
	playSessions int
}

// NewJellyfin starts it.
func NewJellyfin(t testing.TB) *Jellyfin {
	j := &Jellyfin{}
	j.Server = newServer(t, func(_ *Server, w http.ResponseWriter, r *http.Request) { j.route(w, r) })
	return j
}

func (j *Jellyfin) route(w http.ResponseWriter, r *http.Request) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.MediaDir != "" && j.play(w, r) {
		return
	}
	p := r.URL.Path
	parts := strings.Split(strings.Trim(p, "/"), "/")
	switch {
	case strings.HasSuffix(p, "/Items/Resume"):
		Reply(w, JSON{"Items": j.Resume, "TotalRecordCount": len(j.Resume)})
	case p == "/Shows/NextUp":
		Reply(w, JSON{"Items": j.NextUp, "TotalRecordCount": len(j.NextUp)})
	case len(parts) == 3 && parts[0] == "Shows" && parts[2] == "Episodes":
		season, _ := strconv.Atoi(r.URL.Query().Get("Season")) // as the client sends it
		var items []JSON
		for e := 1; e <= 10; e++ {
			id := fmt.Sprintf("ep%d%02d", season, e)
			items = append(items, JSON{"Id": id + strings.Repeat("0", 32-len(id)), "ParentIndexNumber": season, "IndexNumber": e, "LocationType": "FileSystem"})
		}
		Reply(w, JSON{"Items": items, "TotalRecordCount": len(items)})
	case strings.Contains(p, "/PlayedItems/"):
		Reply(w, JSON{})
	case p == "/QuickConnect/Enabled":
		Reply(w, true)
	case p == "/QuickConnect/Initiate":
		if j.qcUsers == nil {
			j.qcUsers = map[string]JSON{}
		}
		n := len(j.qcUsers) + 1
		secret, user := "secret-123456", JSON{"Id": strings.Repeat("f", 32), "Name": "alice-jf"}
		if n > 1 {
			secret = fmt.Sprintf("secret-123456-%d", n)
			user = JSON{"Id": fmt.Sprintf("%s%04x", strings.Repeat("f", 28), n), "Name": fmt.Sprintf("jf-user-%d", n)}
		}
		j.qcUsers[secret] = user
		Reply(w, JSON{"Code": "123456", "Secret": secret})
	case p == "/QuickConnect/Connect":
		Reply(w, JSON{"Authenticated": j.QCReady})
	case p == "/Users/AuthenticateWithQuickConnect":
		var body struct{ Secret string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		user, ok := j.qcUsers[body.Secret]
		if !ok {
			// A secret this server never handed out: Jellyfin refuses it.
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		Reply(w, JSON{"AccessToken": "user-token", "User": user})
	case p == "/Users/AuthenticateByName":
		var body struct{ Username, Pw string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Pw != "right" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		Reply(w, JSON{"AccessToken": "user-token", "User": JSON{"Id": strings.Repeat("e", 32), "Name": body.Username}})
	case p == "/Sessions/Logout":
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 4 && parts[0] == "Users" && parts[2] == "Items":
		// The file's media source with an embedded and an external English
		// subtitle (the Play sheet offers both and puts the file's in the
		// player's link), as the Python's browser-test fake had it.
		Reply(w, JSON{"Id": parts[3], "Type": "Movie", "Name": "Movie 603", "CriticRating": nil,
			"UserData": JSON{"PlaybackPositionTicks": 0}, "RunTimeTicks": ticks(ClipSeconds), "Chapters": ClipChapters,
			"MediaSources": []JSON{{"Id": parts[3], "Container": "mkv", "DefaultSubtitleStreamIndex": 2, "MediaStreams": []JSON{
				{"Type": "Subtitle", "Index": 2, "Language": "eng", "DisplayTitle": "English", "IsExternal": false, "IsTextSubtitleStream": true},
				{"Type": "Subtitle", "Index": 3, "Language": "eng", "DisplayTitle": "English (file)", "IsExternal": true, "IsTextSubtitleStream": true},
			}}}})
	default:
		notFound(w)
	}
}

// ---------------------------------------------------------------------------
// The AI model (LiteLLM's OpenAI-style chat API)
// ---------------------------------------------------------------------------

// AI answers every chat with Answer (JSON, as the model's message content).
type AI struct {
	*Server
	mu     sync.Mutex
	Answer any
}

// NewAI starts it.
func NewAI(t testing.TB, answer any) *AI {
	a := &AI{Answer: answer}
	a.Server = newServer(t, func(_ *Server, w http.ResponseWriter, _ *http.Request) {
		a.mu.Lock()
		content, _ := json.Marshal(a.Answer)
		a.mu.Unlock()
		Reply(w, JSON{"choices": []JSON{{"message": JSON{"content": string(content)}, "finish_reason": "stop"}}})
	})
	return a
}
