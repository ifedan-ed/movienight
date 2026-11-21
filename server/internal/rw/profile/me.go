package profile

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"movienight/server/internal/apierr"
	"movienight/server/internal/auth"
	"movienight/server/internal/episodes"
	"movienight/server/internal/history"
	"movienight/server/internal/rw"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
	"movienight/server/internal/users"
	"movienight/server/internal/userstate"
)

// Page is history, ratings and requests rows per page, every page
// reachable (docs/limits.md).
const Page = 40

// requestStatus is Seerr's request status, as the requests page names it.
var requestStatus = map[int]string{1: "pending", 2: "approved", 3: "declined", 4: "failed", 5: "completed"}

type me struct {
	*account
	history  *history.Service
	episodes *episodes.Service
	users    *users.Service
}

func (m *me) register() {
	d := m.d
	h := d.Huma()

	huma.Register(h, rw.Op("me_api_v1_me_get", http.MethodGet, "/api/v1/me", "Me", "profile"),
		func(ctx context.Context, _ *struct{}) (*rw.Body[schema.MeOut], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			out, err := m.me(ctx, id.User.ID)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(out), nil
		})

	// The web app and the phone app call this once per start when the zone
	// they run in differs from the one /me says is stored and the person
	// has not picked one by hand, so a person's days follow wherever their
	// device is set to, including after travel. A report is refused (409)
	// once they have picked one: the device's zone must not undo their
	// choice, and an older app that does not know about choosing still sends
	// it.
	huma.Register(h, rw.Op("put_time_zone_api_v1_me_time_zone_put", http.MethodPut, "/api/v1/me/time-zone", "Put Time Zone", "profile",
		"Report the time zone this browser or phone runs in (an IANA name such as\nEurope/Berlin). History's days, \"this year\", the daily rows and the\ndigest periods follow it. An unknown name is refused (422); so is any\nreport while the person has a zone picked in Profile (409)."),
		func(ctx context.Context, in *rw.Body[schema.TimeZoneIn]) (*rw.Body[schema.TimeZoneOut], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			u, err := m.users.ReportTimeZone(ctx, id.User.ID, in.Body.TimeZone)
			return zoneOut(ctx, d, u, err)
		})

	// Profile's "Time zone": pick one by hand, or back to Automatic. A
	// separate operation from the report above, so the server, not the
	// client, decides that a report never overrides a choice.
	huma.Register(h, rw.Op("choose_time_zone_api_v1_me_time_zone_choice_put", http.MethodPut, "/api/v1/me/time-zone/choice", "Choose Time Zone", "profile",
		"The person's own choice in Profile. mode \"manual\": keep time_zone (an IANA\nname from GET /time-zones) whatever their devices report. mode\n\"automatic\": follow their devices again, starting with time_zone, the zone\nof the device this is sent from. An unknown name is refused (422)."),
		func(ctx context.Context, in *rw.Body[schema.TimeZoneChoiceIn]) (*rw.Body[schema.TimeZoneOut], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			u, err := m.users.ChooseTimeZone(ctx, id.User.ID, in.Body.TimeZone, in.Body.Mode == modeManual)
			return zoneOut(ctx, d, u, err)
		})

	huma.Register(h, rw.Op("time_zones_api_v1_time_zones_get", http.MethodGet, "/api/v1/time-zones", "Time Zones", "profile",
		"Every time zone a person can pick in Profile (IANA names, sorted), with\neach one's offset from UTC now."),
		func(ctx context.Context, _ *struct{}) (*rw.Body[schema.TimeZonesOut], error) {
			if _, err := d.User(ctx); err != nil {
				return nil, err
			}
			zones, err := m.users.Zones(ctx, time.Now())
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			out := schema.TimeZonesOut{Zones: make([]schema.TimeZoneItem, len(zones))}
			for i, z := range zones {
				out.Zones[i] = schema.TimeZoneItem{Name: z.Name, Offset: z.Offset}
			}
			return rw.Out(out), nil
		})

	huma.Register(h, rw.Op("get_history_api_v1_history_get", http.MethodGet, "/api/v1/history", "Get History", "profile"),
		func(ctx context.Context, in *struct {
			Page  int    `query:"page" default:"1" minimum:"1"`
			Media string `query:"media" enum:"movie,tv"`
		}) (*rw.Body[schema.HistoryOut], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			out, err := m.historyPage(ctx, id.User, in.Media, in.Page)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(out), nil
		})

	huma.Register(h, rw.Op("get_requests_api_v1_requests_get", http.MethodGet, "/api/v1/requests", "Get Requests", "profile",
		"Seerr requests, newest first (everyone's, or yours), with live status\nand download progress: 40 a page, every page reachable (\"more\"), and\nhow many in all."),
		func(ctx context.Context, in *struct {
			Mine bool `query:"mine" default:"false"`
			Page int  `query:"page" default:"1" minimum:"1"`
		}) (*rw.Body[schema.RequestsOut], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			out, err := m.requests(ctx, id, in.Mine, in.Page)
			if err != nil {
				return nil, err
			}
			return rw.Out(out), nil
		})

	huma.Register(h, rw.Op("rate_prompt_api_v1_rate_prompt_get", http.MethodGet, "/api/v1/rate-prompt", "Rate Prompt", "profile"),
		func(ctx context.Context, _ *struct{}) (*rw.Body[schema.RatePromptOut], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			p, err := m.history.RatePrompt(ctx, id.User.ID)
			if err != nil || p == nil {
				if err != nil {
					return nil, d.Fail(ctx, err)
				}
				return rw.Out(schema.RatePromptOut{}), nil
			}
			metas, err := d.Titles.Ensure(ctx, []string{p.Key}, 1)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			item := schema.RatePromptItem{Key: p.Key, MediaType: "movie", TMDBID: p.TMDBID, Title: p.Title}
			if meta := metas[p.Key]; meta != nil {
				if meta.Title != "" {
					item.Title = meta.Title
				}
				item.Poster = meta.PosterPath
			}
			return rw.Out(schema.RatePromptOut{Item: &item}), nil
		})

	huma.Register(h, rw.Op("dismiss_rate_prompt_api_v1_rate_prompt_dismiss_post", http.MethodPost, "/api/v1/rate-prompt/dismiss",
		"Dismiss Rate Prompt", "profile"),
		func(ctx context.Context, in *rw.Body[schema.KeyIn]) (*rw.Body[schema.Ok], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			if err := m.history.DismissRatePrompt(ctx, id.User.ID, in.Body.Key); err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(schema.Ok{Ok: true}), nil
		})
}

// me is GET /me. The person row is read again: the identity was resolved
// at the start of the request, and /me is what the apps read after a
// change (a new scrobble token, a Jellyfin link).
func (m *me) me(ctx context.Context, uid int32) (schema.MeOut, error) {
	d := m.d
	u, err := d.Q.GetUserByID(ctx, uid)
	if err != nil {
		return schema.MeOut{}, err
	}
	zone := d.Zone(u)
	stats, err := m.history.Stats(ctx, u.ID, zone)
	if err != nil {
		return schema.MeOut{}, err
	}
	link, err := m.link(ctx, u)
	if err != nil {
		return schema.MeOut{}, err
	}
	notices := []string{}
	if u.IsAdmin {
		if notices, err = m.status.Notices(ctx, zone); err != nil {
			return schema.MeOut{}, err
		}
	}
	c := d.Cfg
	return schema.MeOut{
		User: u.Username, DisplayName: u.DisplayName, Email: u.Email, IsAdmin: u.IsAdmin, Stats: stats, Jellyfin: link,
		Features: schema.Features{Requests: d.Clients.Seerr.Enabled() || c.DryRun, MDBList: c.MDBListEnabled(),
			AI: c.AIEnabled(), Email: c.EmailEnabled(), DryRun: c.DryRun, Jellyfin: c.JellyfinEnabled(),
			Libraries: c.Media.Enabled()},
		ScrobbleTokenSet: u.ScrobbleTokenHash != nil && *u.ScrobbleTokenHash != "",
		Limits: schema.TextLimits{TextMaxChars: c.RW.TextMaxChars, SearchMaxChars: c.RW.SearchMaxChars,
			NameMaxChars: c.RW.NameMaxChars, ReportedMaxChars: c.ReportedMaxChars, BatchMaxItems: c.RW.BatchMaxItems},
		AdminNotices: notices,
		TimeZone:     u.TimeZone,
		TimeZoneMode: zoneMode(u),
	}, nil
}

// The two values of time_zone_mode in /me and TimeZoneOut.mode.
const (
	modeAutomatic = "automatic"
	modeManual    = "manual"
)

func zoneMode(u store.User) string {
	if u.TimeZoneManual {
		return modeManual
	}
	return modeAutomatic
}

// zoneOut answers both time-zone writes: what is stored now, or the
// refusal in words.
func zoneOut(ctx context.Context, d *rw.Deps, u store.User, err error) (*rw.Body[schema.TimeZoneOut], error) {
	switch {
	case errors.Is(err, users.ErrBadTimeZone):
		return nil, rw.Err(http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, users.ErrTimeZoneChosen):
		return nil, rw.Err(http.StatusConflict, err.Error())
	case err != nil:
		return nil, d.Fail(ctx, err)
	}
	return rw.Out(schema.TimeZoneOut{TimeZone: *u.TimeZone, Mode: zoneMode(u)}), nil
}

// historyPage is one page of merged watches, grouped and paged in SQL
// (history.Page; the Python grouped the whole history in memory on every
// page), with cards and each episode's own heart and rating.
func (m *me) historyPage(ctx context.Context, u store.User, media string, page int) (schema.HistoryOut, error) {
	d := m.d
	uid := u.ID
	window, total, err := m.history.Page(ctx, uid, d.Zone(u), media, page, Page)
	if err != nil {
		return schema.HistoryOut{}, err
	}
	var keys []string
	var eps [][3]int
	for _, w := range window {
		if w.Key == nil {
			continue
		}
		keys = append(keys, *w.Key)
		if ep, ok := episodeOf(w); ok {
			eps = append(eps, ep)
		}
	}
	metas, err := d.Titles.Ensure(ctx, keys, Page)
	if err != nil {
		return schema.HistoryOut{}, err
	}
	st, err := d.State.Load(ctx, u, keys)
	if err != nil {
		return schema.HistoryOut{}, err
	}
	marks, err := m.episodes.Marks(ctx, uid, eps)
	if err != nil {
		return schema.HistoryOut{}, err
	}
	items := make([]schema.HistoryItem, 0, len(window))
	for _, w := range window {
		it := schema.HistoryItem{Watch: w}
		if w.Key != nil {
			if meta := metas[*w.Key]; meta != nil {
				c := st.Card(meta)
				it.Card = &c
			}
		}
		if ep, ok := episodeOf(w); ok {
			it.EpisodeFavorite, it.EpisodeRating = marks[ep].Favorite, marks[ep].Rating
		}
		items = append(items, it)
	}
	return schema.HistoryOut{Items: items, More: page*Page < total, Page: page, Total: total}, nil
}

// episodeOf is (show, season, episode) for an episode's watch.
func episodeOf(w schema.Watch) ([3]int, bool) {
	if w.MediaType != "episode" || w.Key == nil || w.Season == nil || w.Episode == nil {
		return [3]int{}, false
	}
	_, show, ok := titles.Split(*w.Key)
	return [3]int{show, *w.Season, *w.Episode}, ok
}

// requests is a page of Seerr's requests as cards. A request Seerr has no
// TMDB id for, or that TMDB doesn't know, can't be a card: not_shown says
// how many, rather than hiding them. Seerr off: an empty page.
func (m *me) requests(ctx context.Context, id *auth.Identity, mine bool, page int) (schema.RequestsOut, error) {
	d := m.d
	out := schema.RequestsOut{Items: []schema.Card{}, Page: page}
	s := d.Clients.Seerr
	if !s.Enabled() {
		return out, nil
	}
	skip := (page - 1) * Page
	by := 0
	if mine {
		var err error
		if by, err = d.SeerrUser(ctx, id.User); err != nil {
			d.Log.WarnContext(ctx, "Seerr user lookup failed", "err", err)
			return out, apierr.New(http.StatusBadGateway, "Couldn't reach Seerr")
		}
		if by == 0 && !id.User.IsAdmin {
			return out, nil // no Jellyfin account, so no Seerr user: nothing of theirs
		}
	}
	got, err := s.RequestsPage(ctx, Page, skip, "all", by)
	if err != nil {
		d.Log.WarnContext(ctx, "Seerr requests failed", "err", err)
		return out, apierr.New(http.StatusBadGateway, "Couldn't reach Seerr")
	}
	results, _ := got["results"].([]any)
	info, _ := got["pageInfo"].(map[string]any)
	total := skip + len(results)
	if t, ok := info["results"].(float64); ok && t > 0 {
		total = int(t)
	}
	type entry struct {
		key string
		r   map[string]any
		m   map[string]any
	}
	var entries []entry
	var keys []string
	for _, x := range results {
		r, _ := x.(map[string]any)
		media, _ := r["media"].(map[string]any)
		tid, _ := media["tmdbId"].(float64)
		if tid <= 0 {
			continue
		}
		kind := "movie"
		if media["mediaType"] == "tv" {
			kind = "tv"
		}
		k := titles.Key(kind, int(tid))
		entries = append(entries, entry{k, r, media})
		keys = append(keys, k)
	}
	metas, err := d.Titles.Ensure(ctx, keys, Page)
	if err != nil {
		return out, d.Fail(ctx, err)
	}
	st, err := d.State.Load(ctx, id.User, keys)
	if err != nil {
		return out, d.Fail(ctx, err)
	}
	for _, e := range entries {
		meta := metas[e.key]
		if meta == nil {
			continue // counted in not_shown
		}
		c := st.Card(meta)
		if at, ok := e.r["createdAt"].(string); ok {
			c.RequestedAt = &at
		}
		if rs, ok := e.r["status"].(float64); ok {
			if label, ok := requestStatus[int(rs)]; ok {
				c.RequestStatus = &label
			}
		}
		ms, _ := e.m["status"].(float64)
		c.MediaStatus = userstate.StatusLabel(int(ms))
		c.Progress = userstate.DownloadProgress(e.m["downloadStatus"])
		if by, ok := e.r["requestedBy"].(map[string]any); ok {
			if n, ok := by["displayName"].(string); ok {
				c.RequestedBy = &n
			}
		}
		out.Items = append(out.Items, c)
	}
	out.More = skip+len(results) < total
	out.Total = total
	out.NotShown = len(results) - len(out.Items)
	return out, nil
}
