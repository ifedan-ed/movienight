package testenv

import (
	"io"
	"log/slog"
	"testing"

	"movienight/server/internal/clients"
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
		MDBList: fakes.NewMDBList(t), Jellyfin: fakes.NewJellyfin(t)}
	set := func(k, v string) {
		if _, ok := env[k]; !ok {
			env[k] = v
		}
	}
	// BaseEnv's Jellyfin and Meilisearch addresses are placeholders: always
	// the fakes here.
	env["JELLYFIN_BASE_URL"] = f.Jellyfin.URL
	env["MEILI_BASE_URL"] = f.Meili.URL
	set("SEERR_BASE_URL", f.Seerr.URL)
	set("SEERR_API_KEY", "test")
	set("DRY_RUN", "true")
	set("MDBLIST_API_KEY", "test")
	s := Start(t, env)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	s.App.Clients.TMDB = clients.NewTMDB(clients.Upstream(), f.TMDB.URL, "test", "en-US", "US", false)
	s.App.Clients.MDBList = clients.NewMDBList(clients.Upstream(), f.MDBList.URL, env["MDBLIST_API_KEY"], s.KV, quiet)
	return s, f
}
