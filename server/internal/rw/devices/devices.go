// Package devices is the routes other apps and Movie Night's own players
// call: scrobbles from the Kodi add-on and other players, the Jellyfin
// Webhook plugin and the Movie Night plugin for Jellyfin, playing a title in
// an outside player and "Finished it?", Movie Night's own player, and the
// manual syncs.
//
// The handlers authenticate and validate; packages scrobble, playback and
// jfsync decide what is recorded. Background work (a person's plays after a
// webhook event) is only asked for here; a worker does it.
package devices

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"movienight/server/internal/apierr"
	"movienight/server/internal/auth"
	"movienight/server/internal/clients"
	"movienight/server/internal/jfsync"
	"movienight/server/internal/rows"
	"movienight/server/internal/rw"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/scrobble"
)

// Register adds the routes.
func Register(d *rw.Deps) {
	registerScrobble(d)
	registerPlay(d)
	registerWatch(d)
	registerSyncs(d)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// emptyBodyIsObject turns an empty request body into "{}".
func emptyBodyIsObject(hctx huma.Context, next func(huma.Context)) {
	r, _ := humago.Unwrap(hctx)
	if r.ContentLength == 0 {
		r.Body = io.NopCloser(strings.NewReader("{}"))
		r.ContentLength = 2
	}
	next(hctx)
}

// accepted is the 202 {"ok": true} of the server-to-server calls.
type accepted struct {
	Status int
	Body   schema.Ok
}

func accept() *accepted { return &accepted{Status: http.StatusAccepted, Body: schema.Ok{Ok: true}} }

func registerScrobble(d *rw.Deps) {
	huma.Register(d.Huma(), huma.Operation{
		OperationID: "scrobble_api_v1_scrobble_post", Method: http.MethodPost, Path: "/api/v1/scrobble",
		Summary: "Scrobble", Tags: []string{"scrobble"},
	}, func(ctx context.Context, in *rw.Body[schema.ScrobbleIn]) (*rw.Body[schema.ScrobbleOut], error) {
		u, err := d.API.Auth.Scrobbler(ctx)
		if err != nil {
			return nil, d.Fail(ctx, err)
		}
		b := in.Body
		got, err := d.Scrobble.Record(ctx, u.ID, scrobble.Report{MediaType: b.MediaType, TMDBID: b.TMDBID,
			IMDbID: deref(b.IMDbID), TVDBID: b.TVDBID, Title: deref(b.Title), Season: b.Season, Episode: b.Episode,
			WatchedAt: deref(b.WatchedAt), Source: b.Source, Ref: deref(b.Ref)})
		if err != nil {
			return nil, d.Fail(ctx, err)
		}
		return rw.Out(schema.ScrobbleOut{Ok: true, Recorded: got.Recorded, Detail: got.Detail}), nil
	})

	huma.Register(d.Huma(), huma.Operation{
		OperationID: "jellyfin_webhook_api_v1_scrobble_jellyfin_post", Method: http.MethodPost,
		Path: "/api/v1/scrobble/jellyfin", Summary: "Jellyfin Webhook", Tags: []string{"scrobble"}, DefaultStatus: http.StatusAccepted,
		// Huma always requires a raw body; the Python read an empty one as {}
		// (json.loads(raw or b"{}")), and the Webhook plugin's test call can
		// send none. So an empty body is given as {} before Huma reads it.
		Middlewares: huma.Middlewares{emptyBodyIsObject},
	}, func(ctx context.Context, in *struct{ RawBody []byte }) (*accepted, error) {
		if err := d.API.Auth.JellyfinSecret(ctx); err != nil {
			return nil, d.Fail(ctx, err)
		}
		// The plugin's content type depends on its version: the body is
		// read as JSON either way.
		raw := in.RawBody
		if len(strings.TrimSpace(string(raw))) == 0 {
			raw = []byte("{}")
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, apierr.New(http.StatusBadRequest, "Body must be JSON")
		}
		payload, ok := v.(map[string]any)
		if !ok {
			return nil, apierr.New(http.StatusBadRequest, "Body must be a JSON object")
		}
		if err := d.Scrobble.Webhook(ctx, payload); err != nil {
			return nil, d.Fail(ctx, err)
		}
		return accept(), nil
	})

	huma.Register(d.Huma(), huma.Operation{
		OperationID: "jellyfin_plugin_event_api_v1_scrobble_jellyfin_events_post", Method: http.MethodPost,
		Path: "/api/v1/scrobble/jellyfin/events", Summary: "Jellyfin Plugin Event", Tags: []string{"scrobble"},
		DefaultStatus: http.StatusAccepted,
		Description: "Events from the Movie Night plugin for Jellyfin, for every app that\nplays through Jellyfin: " +
			"playback start, progress, pause and stop keep watch\nprogress; played and unplayed marks keep history. " +
			"Same shared\nsecret as the webhook (X-MovieNight-Token).",
	}, func(ctx context.Context, in *rw.Body[schema.JellyfinPluginEvent]) (*accepted, error) {
		if err := d.API.Auth.JellyfinSecret(ctx); err != nil {
			return nil, d.Fail(ctx, err)
		}
		b, it := in.Body, in.Body.Item
		err := d.Scrobble.PluginEvent(ctx, scrobble.PluginEvent{Event: b.Event, UserID: b.UserID,
			PositionS: b.PositionS, PlayedToCompletion: b.PlayedToCompletion, LastPlayed: deref(b.LastPlayed),
			Client: deref(b.Client), Device: deref(b.Device),
			Item: scrobble.PluginItem{ID: it.ID, Type: it.Type, Name: it.Name, SeriesID: deref(it.SeriesID),
				SeriesName: deref(it.SeriesName), Season: it.Season, Episode: it.Episode, RuntimeS: it.RuntimeS,
				TMDB: deref(it.TMDB), IMDb: deref(it.IMDb), TVDB: deref(it.TVDB), SeriesTMDB: deref(it.SeriesTMDB),
				SeriesIMDb: deref(it.SeriesIMDb), SeriesTVDB: deref(it.SeriesTVDB)}})
		if err != nil {
			return nil, d.Fail(ctx, err)
		}
		return accept(), nil
	})
}

func registerSyncs(d *rw.Deps) {
	huma.Register(d.Huma(), huma.Operation{
		OperationID: "sync_plays_api_v1_sync_plays_post", Method: http.MethodPost, Path: "/api/v1/sync/plays",
		Summary: "Sync Plays", Tags: []string{"profile"},
		Description: "Pull your Jellyfin plays now (it also happens every few minutes).",
	}, func(ctx context.Context, _ *struct{}) (*rw.Body[schema.SyncOut], error) {
		id, err := d.User(ctx)
		if err != nil {
			return nil, err
		}
		u := id.User
		if u.JellyfinUserID == nil {
			return nil, apierr.New(http.StatusConflict, "Connect your Jellyfin account first")
		}
		var res jfsync.PlaysResult
		// Under the person's sync lock, as the worker's: never two syncs of
		// one person at once.
		err = d.JFSync.WithLock(ctx, u.ID, func() error {
			r, err := d.JFSync.Plays(ctx, u.ID, *u.JellyfinUserID)
			res = r
			return err
		})
		var inc *clients.IncompleteRead
		switch {
		case errors.As(err, &inc):
			d.Log.WarnContext(ctx, "manual plays sync: nothing added", "why", inc.Msg)
			return nil, apierr.New(http.StatusBadGateway, inc.Msg+". Nothing was added — try again")
		case err != nil:
			d.Log.WarnContext(ctx, "manual plays sync failed", "err", err)
			return nil, apierr.New(http.StatusBadGateway, "Jellyfin didn't answer — try again")
		}
		return rw.Out(schema.SyncOut{Ok: true, Counts: map[string]int{"plays": res.Added}, Note: jfsync.UndatedNote(res.Undated)}), nil
	})

	huma.Register(d.Huma(), huma.Operation{
		OperationID: "run_sync_api_v1_sync__what__post", Method: http.MethodPost, Path: "/api/v1/sync/{what}",
		Summary: "Run Sync", Tags: []string{"profile"},
		Description: "Install-wide: rescan the Jellyfin library, or re-sync MDBList lists.",
	}, func(ctx context.Context, in *struct {
		What string `path:"what" enum:"library,lists"`
	}) (*rw.Body[schema.SyncOut], error) {
		id, err := d.User(ctx)
		if err != nil {
			return nil, err
		}
		if err := auth.RequireAdmin(id); err != nil {
			return nil, err
		}
		counts := map[string]int{}
		if in.What == "library" {
			res, err := d.JFSync.Library(ctx)
			var nr *jfsync.NotRead
			switch {
			case errors.As(err, &nr):
				return nil, apierr.New(http.StatusBadGateway, nr.Msg)
			case err != nil:
				d.Log.WarnContext(ctx, "manual library sync failed", "err", err)
				return nil, apierr.New(http.StatusBadGateway, "library sync failed ("+err.Error()+")")
			}
			counts["items"], counts["removed"], counts["indexed"] = res.Items, res.Removed, res.Indexed
			if res.WithoutID > 0 {
				counts["without_id"] = res.WithoutID
			}
		} else {
			// The plain client, as the nightly job: a sync answered from
			// the saved copy would only rewrite the same titles.
			_, ls, _ := rows.ForDeps(d)
			n, err := ls.SyncAll(ctx, d.Clients.MDBList)
			if err != nil {
				d.Log.WarnContext(ctx, "manual lists sync failed", "err", err)
				return nil, apierr.New(http.StatusBadGateway, "lists sync failed ("+err.Error()+")")
			}
			counts["lists"] = n
		}
		return rw.Out(schema.SyncOut{Ok: true, Counts: counts}), nil
	})
}
