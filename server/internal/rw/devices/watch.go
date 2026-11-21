package devices

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"movienight/server/internal/jfplay"
	"movienight/server/internal/playback"
	"movienight/server/internal/rw"
	"movienight/server/internal/rw/schema"
)

// registerWatch adds Movie Night's own player ("Play here"): starting a
// play and its reports. The media itself is the stream proxy's
// (internal/stream, /api/v1/stream/...), which is not part of the API
// description: it serves bytes, not JSON.
func registerWatch(d *rw.Deps) {
	huma.Register(d.Huma(), huma.Operation{
		OperationID: "watch_api_v1_title__media___tmdb_id__watch_post", Method: http.MethodPost,
		Path: "/api/v1/title/{media}/{tmdb_id}/watch", Summary: "Watch", Tags: []string{"play"},
		Description: "Play in the browser: how to stream the film or episode, its tracks, chapters and segments.",
	}, func(ctx context.Context, in *struct {
		PlayTitlePath
		Body schema.WatchIn
	}) (*rw.Body[schema.WatchOut], error) {
		id, err := d.User(ctx)
		if err != nil {
			return nil, err
		}
		b := in.Body
		c := b.Caps
		w, err := d.Playback.Watch(ctx, id.User, in.Media, in.TMDBID, playback.WatchRequest{
			Season: b.Season, Episode: b.Episode, Audio: b.Audio, Burn: b.BurnSubtitle, Replace: deref(b.Replace),
			Caps: jfplay.Caps{DirectContainers: c.DirectContainers, DirectVideo: c.DirectVideo, DirectAudio: c.DirectAudio,
				HLSVideo: c.HLSVideo, HLSAudio: c.HLSAudio}})
		if err != nil {
			return nil, d.Fail(ctx, err)
		}
		return rw.Out(watchOut(w)), nil
	})

	huma.Register(d.Huma(), huma.Operation{
		OperationID: "watch_report_api_v1_watch_report_post", Method: http.MethodPost,
		Path: "/api/v1/watch/report", Summary: "Watch Report", Tags: []string{"play"},
		Description: "Where the player is: start, every few seconds, pause and stop. A stop at the end records the watch.",
	}, func(ctx context.Context, in *struct{ Body schema.WatchReportIn }) (*rw.Body[schema.WatchReportOut], error) {
		id, err := d.User(ctx)
		if err != nil {
			return nil, err
		}
		b := in.Body
		watched, err := d.Playback.WatchReport(ctx, id.User, playback.Report{Ticket: b.Ticket, Event: b.Event,
			Position: b.PositionS, Duration: b.DurationS, Paused: b.Paused, Audio: b.Audio, Subtitle: b.Subtitle,
			Transcoded: b.Transcoded})
		if err != nil {
			return nil, d.Fail(ctx, err)
		}
		return rw.Out(schema.WatchReportOut{Ok: true, Watched: watched}), nil
	})
}

func watchTracks(ts []playback.Track) []schema.WatchTrack {
	out := make([]schema.WatchTrack, len(ts))
	for i, t := range ts {
		out[i] = schema.WatchTrack{Index: t.Index, Label: t.Label, Language: t.Language, Codec: t.Codec, Kind: t.Kind,
			Default: t.Default, Forced: t.Forced, External: t.External, Channels: t.Channels, URL: t.URL}
	}
	return out
}

func watchOut(w playback.Watch) schema.WatchOut {
	out := schema.WatchOut{Ticket: w.Ticket, PlayID: w.Play, Mode: w.Mode, URL: w.URL, DurationS: w.Duration,
		ResumeS: w.Resume, Title: w.Title, MediaType: w.Media, TMDBID: w.TMDBID, Season: w.Season, Episode: w.Episode,
		Key: w.Key, ItemID: w.ItemID, Backdrop: w.Backdrop, Audio: watchTracks(w.Audio), AudioIndex: w.AudioIdx,
		Subtitles: watchTracks(w.Subs), Subtitle: w.Subtitle, BurntIndex: w.Burnt,
		Chapters: make([]schema.WatchChapter, len(w.Chapters)), Segments: make([]schema.WatchSegment, len(w.Segments)),
		Fonts: w.Fonts}
	for i, c := range w.Chapters {
		out.Chapters[i] = schema.WatchChapter{Start: c.Start, Name: c.Name}
	}
	for i, s := range w.Segments {
		out.Segments[i] = schema.WatchSegment{Kind: s.Kind, Start: s.Start, End: s.End}
	}
	if w.Next != nil {
		out.Next = &schema.WatchNext{Season: w.Next.Season, Episode: w.Next.Episode, Title: w.Next.Title}
	}
	return out
}
