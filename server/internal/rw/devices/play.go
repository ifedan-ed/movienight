package devices

import (
	"context"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"movienight/server/internal/playback"
	"movienight/server/internal/rw"
	"movienight/server/internal/rw/schema"
)

func sessionOut(s playback.Session) schema.PlaySessionOut {
	return schema.PlaySessionOut{ID: s.ID, MediaType: s.MediaType, TMDBID: s.TmdbID, Season: s.Season, Episode: s.Episode,
		Title: s.Title, Player: s.Player, StartedAt: rw.ISOTime(s.StartedAt), Key: s.Key, Poster: s.Poster}
}

// optionalInt is a query parameter that may be left out (Huma's parameters
// cannot be pointers): IsSet tells a season 0 sent from none sent.
type optionalInt struct {
	Value int
	IsSet bool
}

func (o optionalInt) Schema(r huma.Registry) *huma.Schema {
	return huma.SchemaFromType(r, reflect.TypeOf(0))
}

func (o *optionalInt) Receiver() reflect.Value { return reflect.ValueOf(o).Elem().Field(0) }

func (o *optionalInt) OnParamSet(isSet bool, _ any) { o.IsSet = isSet }

func (o optionalInt) ptr() *int {
	if !o.IsSet {
		return nil
	}
	v := o.Value
	return &v
}

// PlaySessionPath and PlayTitlePath are path parameters shared by several
// routes. Exported: Huma fills parameters through reflection, which cannot
// set the fields of an unexported embedded struct (it left them empty).
type PlaySessionPath struct {
	SessionID string `path:"session_id" pattern:"^[A-Za-z0-9_\\-]{16,40}$"`
}

type PlayTitlePath struct {
	Media  string `path:"media" enum:"movie,tv"`
	TMDBID int    `path:"tmdb_id" exclusiveMinimum:"0" maximum:"10000000000"`
}

func registerPlay(d *rw.Deps) {
	huma.Register(d.Huma(), huma.Operation{
		OperationID: "open_session_api_v1_play_open_get", Method: http.MethodGet, Path: "/api/v1/play/open",
		Summary: "Open Session", Tags: []string{"play"},
		Description: "Back in Movie Night: the Play to ask \"Finished it?\" about now, if any.",
	}, func(ctx context.Context, _ *struct{}) (*rw.Body[schema.OpenSessionOut], error) {
		id, err := d.User(ctx)
		if err != nil {
			return nil, err
		}
		ps, ask, err := d.Playback.Returned(ctx, id.User.ID, "")
		if err != nil {
			return nil, d.Fail(ctx, err)
		}
		out := schema.OpenSessionOut{}
		if ps != nil && ask {
			s, err := d.Playback.Get(ctx, id.User.ID, ps.ID)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			so := sessionOut(s)
			out.Session = &so
		}
		return rw.Out(out), nil
	})

	huma.Register(d.Huma(), huma.Operation{
		OperationID: "back_api_v1_play__session_id__back_post", Method: http.MethodPost, Path: "/api/v1/play/{session_id}/back",
		Summary: "Back", Tags: []string{"play"},
		Description: "The player sent the person back from this Play: whether to ask now.",
	}, func(ctx context.Context, in *PlaySessionPath) (*rw.Body[schema.BackOut], error) {
		id, err := d.User(ctx)
		if err != nil {
			return nil, err
		}
		_, ask, err := d.Playback.Returned(ctx, id.User.ID, in.SessionID)
		if err != nil {
			return nil, d.Fail(ctx, err)
		}
		s, err := d.Playback.Get(ctx, id.User.ID, in.SessionID)
		if err != nil {
			return nil, d.Fail(ctx, err)
		}
		return rw.Out(schema.BackOut{Ask: ask, Session: sessionOut(s)}), nil
	})

	huma.Register(d.Huma(), huma.Operation{
		OperationID: "get_session_api_v1_play__session_id__get", Method: http.MethodGet, Path: "/api/v1/play/{session_id}",
		Summary: "Get Session", Tags: []string{"play"},
	}, func(ctx context.Context, in *PlaySessionPath) (*rw.Body[schema.PlaySessionOut], error) {
		id, err := d.User(ctx)
		if err != nil {
			return nil, err
		}
		s, err := d.Playback.Get(ctx, id.User.ID, in.SessionID)
		if err != nil {
			return nil, d.Fail(ctx, err)
		}
		return rw.Out(sessionOut(s)), nil
	})

	huma.Register(d.Huma(), huma.Operation{
		OperationID: "finish_api_v1_play__session_id__finish_post", Method: http.MethodPost, Path: "/api/v1/play/{session_id}/finish",
		Summary: "Finish", Tags: []string{"play"},
	}, func(ctx context.Context, in *struct {
		PlaySessionPath
		Body schema.FinishIn
	}) (*rw.Body[schema.Ok], error) {
		id, err := d.User(ctx)
		if err != nil {
			return nil, err
		}
		if err := d.Playback.Finish(ctx, id.User, in.SessionID, in.Body.Outcome); err != nil {
			return nil, d.Fail(ctx, err)
		}
		return rw.Out(schema.Ok{Ok: true}), nil
	})

	huma.Register(d.Huma(), huma.Operation{
		OperationID: "play_info_api_v1_title__media___tmdb_id__play_get", Method: http.MethodGet,
		Path: "/api/v1/title/{media}/{tmdb_id}/play", Summary: "Play Info", Tags: []string{"titles"},
	}, func(ctx context.Context, in *struct {
		PlayTitlePath
		Season  optionalInt `query:"season" minimum:"0" maximum:"2147483647"`
		Episode optionalInt `query:"episode" minimum:"0" maximum:"2147483647"`
	}) (*rw.Body[schema.PlayInfo], error) {
		id, err := d.User(ctx)
		if err != nil {
			return nil, err
		}
		info, err := d.Playback.PlayInfo(ctx, id.User, in.Media, in.TMDBID, in.Season.ptr(), in.Episode.ptr())
		if err != nil {
			return nil, d.Fail(ctx, err)
		}
		subs := make([]schema.SubtitleTrack, len(info.Subtitles))
		for i, s := range info.Subtitles {
			subs[i] = schema.SubtitleTrack{Index: s.Index, Label: s.Label, Language: s.Language, External: s.External, Text: s.Text, Default: s.Default}
		}
		return rw.Out(schema.PlayInfo{Available: info.Available, InJellyfin: info.InJellyfin, InLibrary: info.InLibrary, Linked: info.Linked, JellyfinItemID: info.JellyfinItemID,
			WebURL: info.WebURL, Title: info.Title, Season: info.Season, Episode: info.Episode, Subtitles: subs,
			ChosenSubtitle: info.ChosenSubtitle, ResumeSeconds: info.ResumeSeconds, Players: info.Players,
			DefaultPlayer: info.DefaultPlayer, SubtitleLanguage: info.SubtitleLanguage}), nil
	})

	huma.Register(d.Huma(), huma.Operation{
		OperationID: "play_api_v1_title__media___tmdb_id__play_post", Method: http.MethodPost,
		Path: "/api/v1/title/{media}/{tmdb_id}/play", Summary: "Play", Tags: []string{"titles"},
	}, func(ctx context.Context, in *struct {
		PlayTitlePath
		Body schema.PlayIn
	}) (*rw.Body[schema.PlayOut], error) {
		id, err := d.User(ctx)
		if err != nil {
			return nil, err
		}
		b := in.Body
		got, err := d.Playback.Start(ctx, id.User, in.Media, in.TMDBID, b.Season, b.Episode, b.Player, b.Subtitle, b.ReturnTo)
		if err != nil {
			return nil, d.Fail(ctx, err)
		}
		return rw.Out(schema.PlayOut{SessionID: got.SessionID, URL: got.URL, WebURL: got.WebURL}), nil
	})
}
