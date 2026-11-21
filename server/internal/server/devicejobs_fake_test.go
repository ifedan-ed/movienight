package server_test

// A stand-in for the outside services the devices and jobs side calls
// (Jellyfin, TMDB, Meilisearch), as one httptest server with a path prefix
// per service, and the helpers its tests share.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"movienight/server/internal/clients"
	"movienight/server/internal/store"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

// up is the fake upstream. Every field is read under mu at request time, so
// a test may change what Jellyfin answers between calls.
type up struct {
	*httptest.Server
	mu sync.Mutex
	// Jellyfin
	library    []map[string]any            // the owner's library (JELLYFIN_USER_ID)
	libraryErr int                         // answer the library read with this status
	played     map[string][]map[string]any // Jellyfin user id -> played items
	playedErr  map[string]int              // Jellyfin user id -> status to fail with
	playedRead int                         // how many played reads came in
	items      map[string]map[string]any   // item id -> item
	episodes   map[string][]map[string]any // series id -> episodes
	marked     []string                    // "user/item" marked played
	serverID   string
	// Jellyfin playback (Play here): PlaybackInfo's media source, the
	// file it streams, segments, and what came in.
	source    map[string]any   // PlaybackInfo's media source; a remux when direct play is refused
	file      []byte           // /Videos/{id}/stream.*
	segments  []map[string]any // /MediaSegments/{id}
	infoAsked []map[string]any // PlaybackInfo request bodies
	reports   []jfReport       // /Sessions/Playing...
	encodings []string         // DELETE /Videos/ActiveEncodings queries
	positions []string         // "user/item/ticks" set with the install's key
	mediaAuth []string         // the Authorization header of media requests
	// TMDB
	titles map[string]map[string]any // "movie/603" -> payload
	find   map[string]map[string]any // external id -> find answer
	// Meilisearch
	docs      map[string]string // id -> searchText
	docsSent  int               // documents sent for indexing
	deletions int
}

func newUp(t *testing.T) *up {
	u := &up{played: map[string][]map[string]any{}, playedErr: map[string]int{}, items: map[string]map[string]any{},
		episodes: map[string][]map[string]any{}, titles: map[string]map[string]any{}, find: map[string]map[string]any{},
		docs: map[string]string{}, serverID: "5e1f" + strings.Repeat("0", 28)}
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	page := func(w http.ResponseWriter, r *http.Request, all []map[string]any) {
		start, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("Limit"))
		end := min(len(all), start+limit)
		if start > end {
			start = end
		}
		items := append([]map[string]any{}, all[start:end]...) // [] for none, never null
		write(w, map[string]any{"Items": items, "TotalRecordCount": len(all)})
	}
	mux.HandleFunc("GET /jf/Users/{uid}/Items", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		uid := r.PathValue("uid")
		if r.URL.Query().Get("Filters") == "IsPlayed" {
			u.playedRead++
			if st := u.playedErr[uid]; st != 0 {
				w.WriteHeader(st)
				return
			}
			page(w, r, u.played[uid])
			return
		}
		if u.libraryErr != 0 {
			w.WriteHeader(u.libraryErr)
			return
		}
		page(w, r, u.library)
	})
	mux.HandleFunc("GET /jf/Users/{uid}/Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		it, ok := u.items[r.PathValue("id")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		write(w, it)
	})
	mux.HandleFunc("GET /jf/Shows/{id}/Episodes", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		write(w, map[string]any{"Items": u.episodes[r.PathValue("id")]})
	})
	mux.HandleFunc("POST /jf/Users/{uid}/PlayedItems/{id}", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.marked = append(u.marked, r.PathValue("uid")+"/"+r.PathValue("id"))
		write(w, map[string]any{})
	})
	mux.HandleFunc("POST /jf/Items/{id}/PlaybackInfo", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		u.infoAsked = append(u.infoAsked, body)
		src := map[string]any{}
		for k, v := range u.source {
			src[k] = v
		}
		if body["EnableDirectPlay"] == false {
			// Jellyfin's HLS, with the caller's token in the address as
			// Jellyfin writes it.
			src["SupportsDirectPlay"] = false
			src["TranscodingUrl"] = fmt.Sprintf("/videos/%s/master.m3u8?DeviceId=movienight-x&MediaSourceId=%v&PlaySessionId=ps-%d&ApiKey=user-token&AudioStreamIndex=%v",
				r.PathValue("id"), src["Id"], len(u.infoAsked), body["AudioStreamIndex"])
		}
		write(w, map[string]any{"MediaSources": []any{src}, "PlaySessionId": fmt.Sprintf("ps-%d", len(u.infoAsked))})
	})
	mux.HandleFunc("GET /jf/Videos/{id}/{file}", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.mediaAuth = append(u.mediaAuth, r.Header.Get("Authorization"))
		file := u.file
		u.mu.Unlock()
		http.ServeContent(w, r, "film.mp4", time.Unix(0, 0), bytes.NewReader(file))
	})
	mux.HandleFunc("GET /jf/videos/{id}/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.mediaAuth = append(u.mediaAuth, r.Header.Get("Authorization"))
		u.mu.Unlock()
		_, _ = fmt.Fprintf(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nmain.m3u8?%s&ApiKey=user-token\n", r.URL.RawQuery)
	})
	for _, p := range []string{"/Sessions/Playing", "/Sessions/Playing/Progress", "/Sessions/Playing/Stopped"} {
		mux.HandleFunc("POST /jf"+p, func(w http.ResponseWriter, r *http.Request) {
			u.mu.Lock()
			defer u.mu.Unlock()
			rep := jfReport{Path: p, Auth: r.Header.Get("Authorization")}
			_ = json.NewDecoder(r.Body).Decode(&rep.Body)
			u.reports = append(u.reports, rep)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	mux.HandleFunc("DELETE /jf/Videos/ActiveEncodings", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.encodings = append(u.encodings, r.URL.RawQuery)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /jf/MediaSegments/{id}", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		write(w, map[string]any{"Items": u.segments})
	})
	mux.HandleFunc("POST /jf/UserItems/{id}/UserData", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		u.positions = append(u.positions, fmt.Sprintf("%s/%s/%v", r.URL.Query().Get("userId"), r.PathValue("id"), body["PlaybackPositionTicks"]))
		write(w, map[string]any{})
	})
	mux.HandleFunc("GET /jf/System/Info/Public", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"Id": u.serverID})
	})
	mux.HandleFunc("GET /tmdb/{media}/{id}", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		media, id := r.PathValue("media"), r.PathValue("id")
		if p, ok := u.titles[media+"/"+id]; ok {
			write(w, p)
			return
		}
		n, _ := strconv.Atoi(id)
		if media == "movie" {
			write(w, map[string]any{"id": n, "title": "Movie " + id, "runtime": 100, "release_date": "1999-03-31",
				"poster_path": "/p" + id + ".jpg"})
			return
		}
		write(w, map[string]any{"id": n, "name": "Show " + id, "episode_run_time": []int{30},
			"seasons":             []map[string]any{{"season_number": 1, "episode_count": 10}},
			"last_episode_to_air": map[string]any{"season_number": 1, "episode_number": 10}})
	})
	mux.HandleFunc("GET /tmdb/find/{id}", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		if f, ok := u.find[r.PathValue("id")]; ok {
			write(w, f)
			return
		}
		write(w, map[string]any{"movie_results": []any{}, "tv_results": []any{}})
	})
	mux.HandleFunc("POST /meili/indexes", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		write(w, map[string]any{"taskUid": 1})
	})
	mux.HandleFunc("PATCH /meili/indexes/{index}/settings", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		write(w, map[string]any{"taskUid": 2})
	})
	mux.HandleFunc("POST /meili/indexes/{index}/documents/fetch", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		var out []map[string]any
		for id, text := range u.docs {
			out = append(out, map[string]any{"id": id, "searchText": text})
		}
		write(w, map[string]any{"results": out, "total": len(out)})
	})
	mux.HandleFunc("PUT /meili/indexes/{index}/documents", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		var docs []map[string]any
		_ = json.NewDecoder(r.Body).Decode(&docs)
		for _, d := range docs {
			u.docs[fmt.Sprint(d["id"])] = fmt.Sprint(d["searchText"])
		}
		u.docsSent += len(docs)
		w.WriteHeader(http.StatusAccepted)
		write(w, map[string]any{"taskUid": 3})
	})
	mux.HandleFunc("POST /meili/indexes/{index}/documents/delete-batch", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		var ids []string
		_ = json.NewDecoder(r.Body).Decode(&ids)
		for _, id := range ids {
			delete(u.docs, id)
		}
		u.deletions += len(ids)
		w.WriteHeader(http.StatusAccepted)
		write(w, map[string]any{"taskUid": 4})
	})
	u.Server = httptest.NewServer(mux)
	t.Cleanup(u.Close)
	return u
}

// jfReport is one playback report Jellyfin got.
type jfReport struct {
	Path string
	Auth string
	Body map[string]any
}

func (u *up) set(f func(u *up)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	f(u)
}

// owner is JELLYFIN_USER_ID in testbase.BaseEnv: whose library is read.
var owner = strings.Repeat("c", 32)

// startUp starts the app against the fake upstream.
func startUp(t *testing.T, env testbase.Env) (*testenv.Server, *up) {
	t.Helper()
	u := newUp(t)
	env["JELLYFIN_BASE_URL"] = u.URL + "/jf"
	env["MEILI_BASE_URL"] = u.URL + "/meili"
	s := testenv.Start(t, env)
	// TMDB's address is a constant in the client; every service holds the
	// shared set, so swapping the client points them all at the fake.
	s.App.Clients.TMDB = clients.NewTMDB(http.DefaultClient, u.URL+"/tmdb", "k", "en-US", "US", false)
	return s, u
}

// userID makes the person (their first request) and returns their id.
func userID(t *testing.T, s *testenv.Server, name string) int32 {
	t.Helper()
	if r := s.Client(name, false).Do(t, "GET", "/api/v1/me/players", nil); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	var id int32
	if err := s.Pool.QueryRow(context.Background(), "SELECT id FROM users WHERE username = $1", name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// link connects the person's Jellyfin account (the Quick Connect routes are
// the read/write side's; here the row is written as they leave it) and
// returns their Jellyfin user id.
func link(t *testing.T, s *testenv.Server, name string) (int32, string) {
	t.Helper()
	id := userID(t, s, name)
	jf := fmt.Sprintf("%032x", id)
	enc := s.App.Keys.Fernet().Encrypt("user-token")
	exec(t, s, "UPDATE users SET jellyfin_user_id = $2, jellyfin_token_enc = $3, jellyfin_linked_at = now() WHERE id = $1", id, jf, enc)
	return id, jf
}

func exec(t *testing.T, s *testenv.Server, sql string, args ...any) {
	t.Helper()
	if _, err := s.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, s *testenv.Server, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func decode[T any](t *testing.T, r *testenv.Response) T {
	t.Helper()
	var out T
	if err := json.Unmarshal([]byte(r.Body), &out); err != nil {
		t.Fatalf("%d %s: %v", r.StatusCode, r.Body, err)
	}
	return out
}

func want(t *testing.T, r *testenv.Response, status int) *testenv.Response {
	t.Helper()
	if r.StatusCode != status {
		t.Fatalf("status %d, want %d: %s", r.StatusCode, status, r.Body)
	}
	return r
}

// movie is a library film as Jellyfin lists it.
func movie(id string, tmdb int) map[string]any {
	return map[string]any{"Id": id, "Type": "Movie", "Name": fmt.Sprintf("Movie %d", tmdb), "ProductionYear": 1999,
		"ProviderIds": map[string]any{"Tmdb": strconv.Itoa(tmdb)}, "DateCreated": "2026-10-01T10:00:00.0000000Z"}
}

func storeQ(s *testenv.Server) *store.Queries { return store.New(s.Pool) }
