// Package rw is what the read and write routes (phases 2 and 3) share: the
// services they use (Deps), the helpers every handler needs (the signed-in
// person and their time zone, errors, path parameters), and the request
// limits that are settings (ApplyLimits).
//
// The routes themselves are one package per area, each with a Register:
//
//	rw/home     Home tabs, rows, hero, "See all", Discover, Home rows settings
//	rw/title    a title page, its status, scores, related, request, the
//	            person's marks (watchlist, watched, rating, favourite),
//	            seasons and episodes
//	rw/search   search, the AI's best guess, people, /titles/fill, images
//	rw/lists    lists and their items, collections, MDBList explore/follow
//	rw/profile  /me, history, requests, rate prompt, export, account settings
//	            (Jellyfin link, players, scrobble token)
//	rw/devices  scrobbles, the Jellyfin webhook and plugin, Play and
//	            "Finished it?", Play here (the in-browser player), the
//	            manual syncs
//	rw/digests  digest emails: settings, the digests, preview and send
//
// Package api keeps only sign-in and the shared plumbing. server.go builds
// one Deps and calls every Register (RegisterAll); the worker (cmd/worker)
// builds the same services for its jobs.
package rw

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valkey-io/valkey-go"

	"movienight/server/internal/airows"
	"movienight/server/internal/api"
	"movienight/server/internal/apierr"
	"movienight/server/internal/auth"
	"movienight/server/internal/cache"
	"movienight/server/internal/clients"
	"movienight/server/internal/config"
	"movienight/server/internal/crypto"
	"movienight/server/internal/digest"
	"movienight/server/internal/history"
	"movienight/server/internal/jfplay"
	"movienight/server/internal/jfsync"
	"movienight/server/internal/mail"
	"movienight/server/internal/playback"
	"movienight/server/internal/players"
	"movienight/server/internal/scrobble"
	"movienight/server/internal/settings"
	"movienight/server/internal/status"
	"movienight/server/internal/store"
	"movienight/server/internal/stream"
	"movienight/server/internal/titles"
	"movienight/server/internal/users"
	"movienight/server/internal/userstate"
)

// Deps is every service the routes use. Built once (NewDeps); the
// handlers read Clients' fields per call, so tests can swap a client for a
// fake after the app is built.
type Deps struct {
	Cfg      *config.Config
	Pool     *pgxpool.Pool
	Q        *store.Queries
	KV       valkey.Client
	Clients  *clients.Set
	Cache    *cache.Cache
	Titles   *titles.Service
	State    *userstate.Service
	Settings *settings.Service
	Log      *slog.Logger
	API      *api.API
	// Keys encrypt what is kept secret at rest (a Jellyfin token).
	Keys *crypto.Keys

	// Services more than one area uses, built once.
	Status   *status.Service   // what Movie Night did by itself (syncs, catalog), for Profile
	Players  *players.Service  // player settings (/me/players and the Play sheet)
	History  *history.Service  // watches, however they are recorded
	JFSync   *jfsync.Service   // Jellyfin syncs, the webhook queue, Jellyfin marks
	Scrobble *scrobble.Service // watches other apps report
	Playback *playback.Service // Play, "Finished it?" and the in-browser player
	Tickets  *stream.Signer    // the in-browser player's stream tickets (the proxy checks them too)
	AIRows   *airows.Service   // "Picked for you": served here, built by the worker
	Digests  *digest.Service   // digest emails asked for by a person (the schedule's are the worker's)

	limits []limit
}

// NewDeps builds the shared services. keys may be nil only where no
// handler runs (Spec).
func NewDeps(cfg *config.Config, pool *pgxpool.Pool, kv valkey.Client, cl *clients.Set, keys *crypto.Keys, a *api.API,
	log *slog.Logger) *Deps {
	q := store.New(pool)
	d := &Deps{
		Cfg: cfg, Pool: pool, Q: q, KV: kv, Clients: cl, Log: log, API: a, Keys: keys,
		Cache:    cache.New(kv, log),
		Titles:   titles.New(q, func() titles.Fetcher { return cl.TMDB }, log),
		State:    userstate.New(q, cfg, userstate.NewSeerrMap(kv, func() *clients.Seerr { return cl.Seerr }, log), log),
		Settings: settings.New(q),
	}
	d.Status = status.New(d.Settings, func() *clients.MDBList { return cl.MDBList }, log)
	d.Players = players.New(d.Settings)
	d.History = history.New(q, d.Settings)
	d.JFSync = jfsync.New(pool, kv, cl, d.Status, d.Cache, cfg.JellyfinUserID, log)
	d.Scrobble = scrobble.New(pool, cl, d.Titles, d.Cache, d.JFSync, log)
	if keys != nil {
		d.Tickets = stream.NewSigner(keys.StreamMAC, nil)
	}
	d.Playback = playback.New(cfg, pool, cl, keys, playback.Deps{Titles: d.Titles, Cache: d.Cache, Players: d.Players,
		History: d.History, Tickets: d.Tickets,
		JFPlay: jfplay.New(cl.Upstream(), cfg.JellyfinBaseURL, cfg.JellyfinAPIKey, cfg.DryRun)}, log)
	d.AIRows = airows.New(cfg, pool, kv, cl, d.Titles, log)
	d.Digests = digest.New(cfg, pool, cl, mail.New(cfg), log)
	return d
}

// Huma is the API to register operations on.
func (d *Deps) Huma() huma.API { return d.API.Huma() }

// User is the signed-in person, or the 401 to return.
func (d *Deps) User(ctx context.Context) (*auth.Identity, error) { return d.API.User(ctx) }

// Fail turns an error into what a handler returns: an *apierr.Error as it
// is; anything else is logged and becomes a plain 500.
func (d *Deps) Fail(ctx context.Context, err error) error { return d.API.Fail(ctx, err) }

// Zone is the person's own time zone, the one every date and time
// computed for them uses (their days, "this year", the daily rotations):
// what their browser or phone reported, else the install's TIMEZONE
// (users.Location says why that default). Scheduling never uses it: the
// worker's jobs run on the install's zone.
func (d *Deps) Zone(u store.User) *time.Location { return users.Location(u, d.Cfg.Location) }

// Body wraps a response body.
type Body[T any] struct{ Body T }

// Out makes a response.
func Out[T any](v T) *Body[T] { return &Body[T]{Body: v} }

// Err is a plain error answer ({"detail": msg}).
func Err(status int, msg string) error { return apierr.New(status, msg) }

// NotFound is a 404 with a sentence.
func NotFound(msg string) error { return apierr.New(http.StatusNotFound, msg) }

// Op is an operation as FastAPI named it: the id is "<function>_<path
// with every non-word character as _>_<method>", copied from
// docs/openapi.json (the phone app's generated client uses it).
func Op(id, method, path, summary, tag string, description ...string) huma.Operation {
	op := huma.Operation{OperationID: id, Method: method, Path: path, Summary: summary, Tags: []string{tag}}
	if len(description) > 0 {
		op.Description = description[0]
	}
	return op
}

// ---------------------------------------------------------------------------
// Limits that are settings
// ---------------------------------------------------------------------------

// LimitKind names a request-shape setting.
type LimitKind int

const (
	TextChars   LimitKind = iota // TEXT_MAX_CHARS
	SearchChars                  // SEARCH_MAX_CHARS
	NameChars                    // NAME_MAX_CHARS
	BatchItems                   // BATCH_MAX_ITEMS
)

func (d *Deps) value(k LimitKind) int {
	switch k {
	case TextChars:
		return d.Cfg.RW.TextMaxChars
	case SearchChars:
		return d.Cfg.RW.SearchMaxChars
	case NameChars:
		return d.Cfg.RW.NameMaxChars
	default:
		return d.Cfg.RW.BatchMaxItems
	}
}

type limit struct {
	schema, field string // a body schema's field, or
	method, path  string // a query parameter of one operation (field: its name)
	kind          LimitKind
}

// Limit says a body schema's field takes its limit from a setting: a
// string's maxLength, or an array's maxItems. The tag on the field holds
// the default; ApplyLimits sets the configured value on the registered
// schema, so validation and the published description both use it.
func (d *Deps) Limit(schema, field string, k LimitKind) {
	d.limits = append(d.limits, limit{schema: schema, field: field, kind: k})
}

// LimitQuery is Limit for a query parameter's maxLength.
func (d *Deps) LimitQuery(method, path, param string, k LimitKind) {
	d.limits = append(d.limits, limit{method: method, path: path, field: param, kind: k})
}

// ApplyLimits sets every recorded limit; a name that does not exist is an
// error (a renamed field would otherwise silently keep the default).
func (d *Deps) ApplyLimits() error {
	reg := d.Huma().OpenAPI().Components.Schemas
	for _, l := range d.limits {
		n := d.value(l.kind)
		if l.schema != "" {
			s := reg.Map()[l.schema]
			if s == nil || s.Properties[l.field] == nil {
				return fmt.Errorf("limit on %s.%s: no such schema field", l.schema, l.field)
			}
			setLimit(s.Properties[l.field], n)
			continue
		}
		item := d.Huma().OpenAPI().Paths[l.path]
		if item == nil {
			return fmt.Errorf("limit on %s %s: no such path", l.method, l.path)
		}
		var op *huma.Operation
		switch l.method {
		case http.MethodGet:
			op = item.Get
		case http.MethodPost:
			op = item.Post
		}
		found := false
		for _, p := range opParams(op) {
			if p.Name == l.field && p.Schema != nil {
				setLimit(p.Schema, n)
				found = true
			}
		}
		if !found {
			return fmt.Errorf("limit on %s %s ?%s: no such parameter", l.method, l.path, l.field)
		}
	}
	return nil
}

func opParams(op *huma.Operation) []*huma.Param {
	if op == nil {
		return nil
	}
	return op.Parameters
}

func setLimit(s *huma.Schema, n int) {
	if s.Type == huma.TypeArray {
		s.MaxItems = &n
	} else {
		s.MaxLength = &n
	}
	s.PrecomputeMessages()
}

// ISOTime writes a time as the Python's isoformat() did for these UTC
// values ("2026-11-12T20:15:03.123456+00:00"), so clients parse it as
// before (the same format as package api's).
func ISOTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000-07:00")
}

// ISOTimePtr is ISOTime for an optional time.
func ISOTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ISOTime(*t)
	return &s
}
