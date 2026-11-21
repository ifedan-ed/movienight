package testenv

import (
	"io"
	"log/slog"
	"testing"

	"movienight/server/internal/clients"
	"movienight/server/internal/subtitles"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv/fakes"
)

// Fakes are the outside services a faked server talks to.
type Fakes struct {
	TMDB     *fakes.TMDB
	Seerr    *fakes.Seerr
	Meili    *fakes.Meili
	MDBList  *fakes.MDBList
	Jellyfin *fakes.Jellyfin
	// Subtitles is the subtitle sites of the player's Search online.
	Subtitles *fakes.Subtitles
}

// StartFaked is Start with every outside service faked (package fakes):
// Seerr on in DRY_RUN, MDBList
// with a key. Those settings already in env win (a test may turn MDBList
// off).
// TMDB's and MDBList's addresses are fixed in the clients, so those two
// clients are swapped for ones pointing at the fakes after the app is
// built (every route reads its client per call).
func StartFaked(t testing.TB, env testbase.Env) (*Server, *Fakes) {
	t.Helper()
	f := &Fakes{TMDB: fakes.NewTMDB(t), Seerr: fakes.NewSeerr(t), Meili: fakes.NewMeili(t),
		MDBList: fakes.NewMDBList(t), Jellyfin: fakes.NewJellyfin(t), Subtitles: fakes.NewSubtitles(t)}
	set := func(k, v string) {
		if _, ok := env[k]; !ok {
			env[k] = v
		}
	}
	// BaseEnv's Jellyfin and Meilisearch addresses are placeholders: always
	// the fakes here.
	// An install without Jellyfin (NoJellyfin) keeps none.
	if _, ok := env["JELLYFIN_API_KEY"]; ok {
		env["JELLYFIN_BASE_URL"] = f.Jellyfin.URL
	}
	env["MEILI_BASE_URL"] = f.Meili.URL
	set("SEERR_BASE_URL", f.Seerr.URL)
	set("SEERR_API_KEY", "test")
	set("DRY_RUN", "true")
	set("MDBLIST_API_KEY", "test")
	s := Start(t, env)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	s.App.Clients.TMDB = clients.NewTMDB(clients.Upstream(), f.TMDB.URL, "test", "en-US", "US", false)
	s.App.Clients.MDBList = clients.NewMDBList(clients.Upstream(), f.MDBList.URL, env["MDBLIST_API_KEY"], s.KV, quiet)
	// The subtitle sites' addresses are fixed too: OpenSubtitles (with a
	// key, no login), Podnapisi and Gestdown, all on the one fake.
	w := subtitles.NewWeb(clients.Upstream(), "Movie Night test")
	s.App.RW.Subtitles.SetSources([]subtitles.Source{
		subtitles.NewOpenSubtitles(w, f.Subtitles.URL+"/opensubtitles/api/v1", "test", "", "", s.KV),
		subtitles.NewPodnapisi(w, f.Subtitles.URL+"/podnapisi/subtitles"),
		subtitles.NewGestdown(w, f.Subtitles.URL+"/gestdown"),
	})
	return s, f
}
