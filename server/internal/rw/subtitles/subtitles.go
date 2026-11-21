// Package subtitles is the routes of the player's "Search online"
// (internal/subtitles): the languages, a search for the playing file, the
// file's stored subtitles, adding one, its text, its delay, removing it.
//
// Every route that concerns a file takes the play's stream ticket: it says
// which Jellyfin item and media source are playing, and that the play is
// the caller's own. The title and episode come from the play itself, so a
// subtitle can only be filed under what is really playing.
package subtitles

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"movienight/server/internal/api"
	"movienight/server/internal/apierr"
	"movienight/server/internal/rw"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/store"
	"movienight/server/internal/subtitles"
)

// TitlePath is a title's path parameters. Exported: Huma fills embedded
// structs through reflection.
type TitlePath struct {
	Media  string `path:"media" enum:"movie,tv"`
	TMDBID int    `path:"tmdb_id" exclusiveMinimum:"0" maximum:"10000000000"`
}

// TrackPath names one stored subtitle.
type TrackPath struct {
	ID int64 `path:"subtitle_id" exclusiveMinimum:"0"`
}

const tag = "subtitles"

var errEnded = apierr.New(http.StatusForbidden, "This play has ended. Start it again.")

// Register adds the routes.
func Register(d *rw.Deps) {
	h := huma.API(d.Huma())

	huma.Register(h, rw.Op("subtitle_languages_api_v1_subtitles_languages_get", http.MethodGet,
		"/api/v1/subtitles/languages", "Subtitle Languages", tag,
		"The languages Search online offers, and the person's own (Profile → Players)."),
		func(ctx context.Context, _ *struct{}) (*rw.Body[schema.SubtitleLanguagesOut], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			prefs, err := d.Players.Get(ctx, id.User.ID)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			lang := ""
			if prefs.SubtitleLanguage != nil {
				lang = *prefs.SubtitleLanguage
			}
			out := schema.SubtitleLanguagesOut{Enabled: d.Subtitles.Enabled(), Default: subtitles.DefaultLanguage(lang).Code,
				Languages: make([]schema.SubtitleLanguage, len(subtitles.Languages))}
			for i, l := range subtitles.Languages {
				out.Languages[i] = schema.SubtitleLanguage{Code: l.Code, Name: l.Name}
			}
			return rw.Out(out), nil
		})

	huma.Register(h, rw.Op("subtitles_api_v1_title__media___tmdb_id__subtitles_get", http.MethodGet,
		"/api/v1/title/{media}/{tmdb_id}/subtitles", "Subtitles", tag,
		"The subtitles added online for the file a play is showing, for everyone in the household."),
		func(ctx context.Context, in *struct {
			TitlePath
			Ticket string `query:"ticket" required:"true" maxLength:"2000"`
		}) (*rw.Body[schema.OnlineSubtitlesOut], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			it, err := item(ctx, d, id.User, in.Media, in.TMDBID, in.Ticket)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			ts, err := d.Subtitles.List(ctx, id.User, it)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			out := schema.OnlineSubtitlesOut{Tracks: make([]schema.OnlineSubtitle, len(ts))}
			for i, t := range ts {
				out.Tracks[i] = trackOut(t)
			}
			return rw.Out(out), nil
		})

	huma.Register(h, rw.Op("subtitle_search_api_v1_title__media___tmdb_id__subtitles_search_get", http.MethodGet,
		"/api/v1/title/{media}/{tmdb_id}/subtitles/search", "Subtitle Search", tag,
		"Search every subtitle source at once for the file a play is showing: made for this file first, then the "+
			"release closest to its name, then the most downloaded. Machine-translated ones are left out unless machine=true. "+
			"30 searches an hour per person."),
		func(ctx context.Context, in *struct {
			TitlePath
			Ticket   string `query:"ticket" required:"true" maxLength:"2000"`
			Language string `query:"language" required:"true" maxLength:"10"`
			Machine  bool   `query:"machine" doc:"include machine-translated subtitles"`
		}) (*rw.Body[schema.SubtitleSearchOut], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			l, ok := subtitles.LanguageByCode(in.Language)
			if !ok {
				return nil, apierr.New(http.StatusUnprocessableEntity, "Unknown language")
			}
			it, err := item(ctx, d, id.User, in.Media, in.TMDBID, in.Ticket)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			t := subtitles.Title{}
			if m, err := d.Titles.One(ctx, in.Media, in.TMDBID); err != nil {
				return nil, d.Fail(ctx, err)
			} else if m != nil {
				t.Name = m.Title
				if m.Year != nil {
					t.Year = *m.Year
				}
				if m.IMDbID != nil {
					t.IMDbID = *m.IMDbID
				}
			}
			found, err := d.Subtitles.Search(ctx, id.User, it, t, l, in.Machine, d.Zone(id.User))
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			out := schema.SubtitleSearchOut{Results: make([]schema.SubtitleResult, len(found.Results)), Hidden: found.Hidden,
				Notes: found.Notes}
			if out.Notes == nil {
				out.Notes = []string{}
			}
			for i, r := range found.Results {
				out.Results[i] = schema.SubtitleResult{Source: r.Source, SourceLabel: subtitles.Labels[r.Source], ID: r.ID,
					Language: r.Language, Release: r.Release, HearingImpaired: r.HearingImpaired, Machine: r.Machine,
					HashMatch: r.HashMatch, Downloads: r.Downloads, Rating: r.Rating, Available: !found.UsedUp[r.Source]}
			}
			return rw.Out(out), nil
		})

	huma.Register(h, rw.Op("subtitle_add_api_v1_title__media___tmdb_id__subtitles_post", http.MethodPost,
		"/api/v1/title/{media}/{tmdb_id}/subtitles", "Subtitle Add", tag,
		"Download a search result and keep it as a track of the playing file. 20 downloads an hour per person."),
		func(ctx context.Context, in *struct {
			TitlePath
			Body schema.SubtitleAddIn
		}) (*rw.Body[schema.OnlineSubtitle], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			it, err := item(ctx, d, id.User, in.Media, in.TMDBID, in.Body.Ticket)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			t, err := d.Subtitles.Add(ctx, id.User, it, in.Body.Source, in.Body.ID, d.Zone(id.User))
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(trackOut(t)), nil
		})

	huma.Register(h, rw.Op("subtitle_file_api_v1_subtitles__subtitle_id__file_get", http.MethodGet,
		"/api/v1/subtitles/{subtitle_id}/file", "Subtitle File", tag,
		"A stored subtitle's text: WebVTT, or ASS for a styled one."),
		func(ctx context.Context, in *struct{ TrackPath }) (*huma.StreamResponse, error) {
			if _, err := d.User(ctx); err != nil {
				return nil, err
			}
			format, body, err := d.Subtitles.Body(ctx, in.ID)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			return api.Stream(func(w http.ResponseWriter, _ *http.Request) {
				ct := "text/vtt; charset=utf-8"
				if format == subtitles.FormatASS {
					ct = "text/x-ssa; charset=utf-8"
				}
				w.Header().Set("Content-Type", ct)
				// The text never changes under its id (the delay is applied
				// by the player), so a device keeps it.
				w.Header().Set("Cache-Control", "private, max-age=86400")
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				_, _ = w.Write([]byte(body))
			})
		})

	huma.Register(h, rw.Op("subtitle_delay_api_v1_subtitles__subtitle_id__delay_put", http.MethodPut,
		"/api/v1/subtitles/{subtitle_id}/delay", "Subtitle Delay", tag,
		"Set a stored subtitle's delay for every device."),
		func(ctx context.Context, in *struct {
			TrackPath
			Body schema.SubtitleDelayIn
		}) (*rw.Body[schema.Ok], error) {
			if _, err := d.User(ctx); err != nil {
				return nil, err
			}
			if err := d.Subtitles.SetDelay(ctx, in.ID, in.Body.DelayS); err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(schema.Ok{Ok: true}), nil
		})

	huma.Register(h, rw.Op("subtitle_delete_api_v1_subtitles__subtitle_id__delete", http.MethodDelete,
		"/api/v1/subtitles/{subtitle_id}", "Subtitle Delete", tag,
		"Remove a stored subtitle for everyone: the person who added it, or an admin."),
		func(ctx context.Context, in *struct{ TrackPath }) (*rw.Body[schema.Ok], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			if err := d.Subtitles.Delete(ctx, id.User, in.ID); err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(schema.Ok{Ok: true}), nil
		})
}

// item is the file a ticket plays, checked against the caller and the
// title in the path. The episode is the play's own.
func item(ctx context.Context, d *rw.Deps, u store.User, media string, tmdbID int, ticket string) (subtitles.Item, error) {
	c, err := d.Tickets.Owned(ticket, u.ID)
	if err != nil {
		return subtitles.Item{}, errEnded
	}
	ps, err := d.Q.PlaySessionGet(ctx, store.PlaySessionGetParams{ID: c.Play, UserID: u.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return subtitles.Item{}, errEnded
	}
	if err != nil {
		return subtitles.Item{}, err
	}
	kind := "episode"
	if media == "movie" {
		kind = "movie"
	}
	if ps.TmdbID == nil || int(*ps.TmdbID) != tmdbID || ps.MediaType != kind {
		return subtitles.Item{}, apierr.New(http.StatusUnprocessableEntity, "This play is of another title")
	}
	it := subtitles.Item{Media: media, TMDBID: tmdbID, File: c}
	if kind == "episode" {
		if ps.Season == nil || ps.Episode == nil {
			return subtitles.Item{}, apierr.New(http.StatusUnprocessableEntity, "This play names no episode")
		}
		s, e := int(*ps.Season), int(*ps.Episode)
		it.Season, it.Episode = &s, &e
	}
	return it, nil
}

func trackOut(t subtitles.Track) schema.OnlineSubtitle {
	name := t.Language
	if l, ok := subtitles.LanguageByCode(t.Language); ok {
		name = l.Name
	}
	kind := "text"
	if t.Format == subtitles.FormatASS {
		kind = "ass"
	}
	return schema.OnlineSubtitle{ID: t.ID, Label: name + " · " + subtitles.Labels[t.Source], Language: t.Language,
		Source: t.Source, Release: t.Release, HearingImpaired: t.HearingImpaired, Kind: kind,
		URL: "/api/v1/subtitles/" + strconv.FormatInt(t.ID, 10) + "/file", DelayS: t.Delay, CanRemove: t.CanRemove}
}
