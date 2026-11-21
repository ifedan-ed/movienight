// Package server wires the API process together: it builds every service
// from the settings and the shared connections, and returns the one
// http.Handler that serves them. cmd/api runs it; the tests build the same
// thing against throwaway Postgres and Valkey containers, so what is tested
// is what is deployed.
package server

import (
	"log/slog"

	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valkey-io/valkey-go"

	"movienight/server/internal/addons"
	"movienight/server/internal/api"
	"movienight/server/internal/auth"
	"movienight/server/internal/clients"
	"movienight/server/internal/config"
	"movienight/server/internal/crypto"
	"movienight/server/internal/devices"
	"movienight/server/internal/errreports"
	"movienight/server/internal/httpx"
	"movienight/server/internal/oidc"
	"movienight/server/internal/passwords"
	"movienight/server/internal/ratelimit"
	"movienight/server/internal/rw"
	"movienight/server/internal/rw/routes"
	"movienight/server/internal/sessions"
	"movienight/server/internal/store"
	"movienight/server/internal/stream"
	"movienight/server/internal/users"
	"movienight/server/internal/web"
)

// App is the built API process.
type App struct {
	Handler   http.Handler
	API       *api.API
	Auth      *auth.Authenticator
	Clients   *clients.Set
	Users     *users.Service
	Devices   *devices.Service
	Passwords *passwords.Service
	Keys      *crypto.Keys
	// RW is the read and write routes' services (internal/rw).
	RW *rw.Deps
	// Relays forward the clients' error reports; cmd/api waits for the
	// ones under way on shutdown.
	Relays *errreports.Relays
}

// New builds the app on open connections.
func New(cfg *config.Config, pool *pgxpool.Pool, kv valkey.Client, log *slog.Logger) (*App, error) {
	keys, err := crypto.NewKeys(cfg.SecretKey)
	if err != nil {
		return nil, err
	}
	q := store.New(pool)
	cl := clients.NewSet(cfg, kv, log)
	people := users.New(q, cfg)
	devs := devices.New(pool, cfg, keys)
	sess := sessions.New(q, cfg.SessionLifetime(), cfg.SecureCookies())
	authn := auth.New(cfg, q, people, devs, sess)
	pw := passwords.NewService(passwords.NewHasher(), people,
		ratelimit.New(kv, "signin-fail", passwords.FailLimit, passwords.FailWindow), cfg.PasswordMinLength)
	callers := httpx.NewCallers(cfg.TrustedProxies)
	relays, err := newRelays(cfg, callers, kv, log)
	if err != nil {
		return nil, err
	}

	pages, err := web.New(web.Deps{
		Config: cfg, Keys: keys, Auth: authn, Sessions: sess, Passwords: pw, Users: people, Devices: devs,
		OIDC: oidc.New(cfg, kv, cl.Upstream(), log), Callers: callers, Log: log,
	})
	if err != nil {
		return nil, err
	}

	// The Kodi add-on and Jellyfin plugin downloads (/kodi/, /jellyfin/).
	downloads, err := addons.New(cfg.PublicURL, cfg.KodiAddonDir, cfg.JellyfinPluginDir)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	// Sign-in and the shared plumbing (package api), then every other route
	// (internal/rw/...) on the same Huma API, so one description holds all.
	a := api.New(mux, api.Deps{Config: cfg, Auth: authn, Devices: devs, Passwords: pw, Web: pages, Callers: callers, KV: kv, Log: log, Relays: relays})
	pages.Register(mux)
	downloads.Register(mux)
	rwDeps := rw.NewDeps(cfg, pool, kv, cl, keys, a, log)
	if err := routes.RegisterAll(rwDeps); err != nil {
		return nil, err
	}
	// The in-browser player's media (internal/stream): plain bytes, not an
	// API operation, on a connection pool of its own.
	proxy, err := stream.NewProxy(rwDeps.Tickets, stream.NewClient(), cfg.JellyfinBaseURL, cfg.JellyfinAPIKey,
		rwDeps.Libraries.Media(), log)
	if err != nil {
		return nil, err
	}
	proxy.Register(mux)
	// Search online reads the playing file's name and moviehash through it.
	rwDeps.Subtitles.UseFiles(proxy)

	// Inside out: the mux (with the route recorder for the access line),
	// then the browser sign-in redirect for /api, then the lazy auth state
	// and the request in context, then the protections and the access log.
	inner := httpx.Route(mux)
	signIn := pages.BrowserSignIn(inner)
	routed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A browser opening an API address while signed out goes to sign
		// in. Not the sign-in routes themselves: a failed OIDC callback
		// answers 401 with the sign-in page, which must not loop back to
		// the provider.
		if strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/api/v1/auth/") {
			signIn.ServeHTTP(w, r)
			return
		}
		inner.ServeHTTP(w, r)
	})
	h := httpx.Handler(httpx.KeepRequest(authn.Middleware(routed)), log)

	return &App{Handler: h, API: a, Auth: authn, Clients: cl, Users: people, Devices: devs, Passwords: pw, Keys: keys, RW: rwDeps, Relays: relays}, nil
}

// newRelays builds the website's and the app's error-report relays; each is
// off (404) while its DSN is empty.
func newRelays(cfg *config.Config, callers *httpx.Callers, kv valkey.Client, log *slog.Logger) (*errreports.Relays, error) {
	er := cfg.ErrorReports
	web, err := errreports.NewRelay(errreports.RelayOptions{Kind: errreports.Web, DSN: er.WebDSN, Origin: cfg.PublicOrigin(),
		Environment: er.Environment, Callers: callers, KV: kv, Log: log})
	if err != nil {
		return nil, err
	}
	app, err := errreports.NewRelay(errreports.RelayOptions{Kind: errreports.App, DSN: er.AppDSN,
		Environment: er.Environment, Callers: callers, KV: kv, Log: log})
	if err != nil {
		return nil, err
	}
	return &errreports.Relays{Web: web, App: app}, nil
}

// Spec builds the whole API description without any connection: every
// route registered on a throwaway mux with empty services, which only the
// handlers (never run here) would use. The settings that shape it (the
// request limits) take their documented defaults.
func Spec() (*huma.OpenAPI, error) {
	cfg := &config.Config{PairStartsPerAddress: 1, RW: config.ReadWriteDefaults()}
	log := slog.Default()
	a := api.New(http.NewServeMux(), api.Deps{Config: cfg, Log: log})
	d := rw.NewDeps(cfg, nil, nil, &clients.Set{}, nil, a, log)
	if err := routes.RegisterAll(d); err != nil {
		return nil, err
	}
	return a.OpenAPI(), nil
}
