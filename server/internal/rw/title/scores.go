package title

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"movienight/server/internal/cache"
	"movienight/server/internal/clients"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
)

// The outside scores of a title (app/services/scores.py):
//
//   - IMDb's own rating from the nightly IMDb file (imdb_ratings), by the
//     title's IMDb id, so it shows even when MDBList is down or not set up;
//   - Rotten Tomatoes, Metacritic, Letterboxd, Trakt (and IMDb for a title
//     the file lacks) from MDBList, kept in title_extras and asked again
//     after ScoresFresh. When MDBList can't answer or refuses, the kept
//     scores show and the page says why in a plain note; a score MDBList
//     stops sending is kept, not cleared;
//   - Rotten Tomatoes critics for a library title MDBList has none for,
//     from Jellyfin (its OMDb plugin's CriticRating).

// ScoresFresh is how long MDBList's scores are kept before asking again.
const ScoresFresh = 7 * 24 * time.Hour

// CriticTTL is how long Jellyfin's critic score of an item is kept.
const CriticTTL = 24 * time.Hour

func fromExtra(e store.TitleExtra) *schema.Scores {
	s := &schema.Scores{IMDbRating: e.ImdbRating, IMDbVotes: i32(e.ImdbVotes), RTCritics: i32(e.RtCritics),
		RTAudience: i32(e.RtAudience), RTURL: e.RtUrl, Metacritic: i32(e.Metacritic), Letterboxd: e.Letterboxd,
		TraktRating: e.TraktRating, TraktVotes: i32(e.TraktVotes), MDBListScore: i32(e.MdblistScore)}
	if s.Empty() {
		return nil
	}
	return s
}

// withIMDb puts IMDb's own rating over MDBList's IMDb one.
func (t *Title) withIMDb(ctx context.Context, s *schema.Scores, key string) (*schema.Scores, error) {
	r, err := t.d.Q.IMDbRatingFor(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if s == nil {
		s = &schema.Scores{}
	}
	votes := int(r.Votes)
	s.IMDbRating, s.IMDbVotes = &r.Rating, &votes
	return s, nil
}

func (t *Title) extra(ctx context.Context, key string) (*store.TitleExtra, error) {
	e, err := t.d.Q.TitleExtra(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// keptScores is what is kept here, asking no one (the title page).
func (t *Title) keptScores(ctx context.Context, media string, id int) (*schema.Scores, error) {
	key := titles.Key(media, id)
	e, err := t.extra(ctx, key)
	if err != nil {
		return nil, err
	}
	var s *schema.Scores
	if e != nil {
		s = fromExtra(*e)
	}
	return t.withIMDb(ctx, s, key)
}

func scoresNote(e *store.TitleExtra, why string, loc *time.Location) string {
	tail := "there are none kept for this title yet"
	if e != nil && fromExtra(*e) != nil {
		at := e.FetchedAt.In(loc)
		tail = fmt.Sprintf("showing the ones kept from %s %d, %d", at.Format("Jan"), at.Day(), at.Year())
	}
	return fmt.Sprintf("Rotten Tomatoes, Metacritic, Letterboxd and Trakt scores couldn't be refreshed: %s. "+
		"Movie Night asks again later; %s.", why, tail)
}

func fl(v any) *float64 {
	if f, ok := v.(float64); ok {
		return &f
	}
	return nil
}

func in32(v any) *int32 {
	switch n := v.(type) {
	case float64:
		x := int32(n)
		return &x
	case int:
		x := int32(n)
		return &x
	}
	return nil
}

func str(v any) *string {
	if s, ok := v.(string); ok && s != "" {
		return &s
	}
	return nil
}

// fromMDBList is the kept scores while fresh, else a new MDBList lookup
// (the kept ones with a plain note when it can't).
func (t *Title) fromMDBList(ctx context.Context, loc *time.Location, media string, id int) (*schema.Scores, *string, error) {
	key := titles.Key(media, id)
	e, err := t.extra(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	kept := func() *schema.Scores {
		if e == nil {
			return nil
		}
		return fromExtra(*e)
	}
	mdb := t.d.Clients.MDBList
	if (e != nil && time.Since(e.FetchedAt) < ScoresFresh) || !mdb.Enabled() {
		return kept(), nil, nil
	}
	info, err := mdb.MediaInfo(ctx, media, id)
	if err != nil {
		why := "MDBList didn't answer"
		var refused *clients.MDBListRefused
		if errors.As(err, &refused) {
			why = "MDBList refused Movie Night's requests (its own limit for this install's key)"
		} else {
			t.d.Log.WarnContext(ctx, "MDBList scores failed", "err", err)
		}
		note := scoresNote(e, why, loc)
		return kept(), &note, nil
	}
	v := clients.JSON{}
	if info != nil {
		v = clients.RatingsOf(info)
	}
	// Only the scores MDBList sent: one it leaves out keeps its kept value.
	saved, err := t.d.Q.TitleExtraSave(ctx, store.TitleExtraSaveParams{Key: key,
		ImdbRating: fl(v["imdb_rating"]), ImdbVotes: in32(v["imdb_votes"]), RtCritics: in32(v["rt_critics"]),
		RtAudience: in32(v["rt_audience"]), RtUrl: str(v["rt_url"]), Metacritic: in32(v["metacritic"]),
		Letterboxd: fl(v["letterboxd"]), TraktRating: fl(v["trakt_rating"]), TraktVotes: in32(v["trakt_votes"]),
		MdblistScore: in32(v["mdblist_score"])})
	if err != nil {
		return nil, nil, err
	}
	return fromExtra(saved), nil, nil
}

// jellyfinCritics is the Rotten Tomatoes critics score Jellyfin keeps for a
// library title (nil when not in the library or Jellyfin has none).
// Jellyfin trouble reads as none (logged): it is only a fallback.
func (t *Title) jellyfinCritics(ctx context.Context, media string, id int) *int {
	tid := int32(id)
	jfID, err := t.d.Q.LibraryItemFor(ctx, store.LibraryItemForParams{MediaType: media, TmdbID: &tid})
	if err != nil || t.d.Cfg.JellyfinUserID == "" {
		return nil
	}
	jf, uid := t.d.Clients.Jellyfin, t.d.Cfg.JellyfinUserID
	item, err := cache.Cached(ctx, t.d.Cache, "jf-critic:"+jfID, cache.Options{TTL: CriticTTL},
		func(ctx context.Context) (clients.JSON, error) { return jf.Item(ctx, uid, jfID, "ProviderIds") })
	if err != nil {
		t.d.Log.WarnContext(ctx, "Jellyfin critic score failed", "err", err)
		return nil
	}
	if f, ok := item["CriticRating"].(float64); ok {
		n := int(f)
		return &n
	}
	return nil
}

// scoresAndNote is the scores for the scores call: MDBList's (fresh or
// kept) with IMDb's own rating over its IMDb one, Jellyfin's critics score
// when there is no Rotten Tomatoes one, and the note when MDBList's scores
// couldn't be refreshed (its date in loc, the reader's zone).
func (t *Title) scoresAndNote(ctx context.Context, loc *time.Location, media string, id int) (schema.ScoresOut, error) {
	s, note, err := t.fromMDBList(ctx, loc, media, id)
	if err != nil {
		return schema.ScoresOut{}, err
	}
	if s, err = t.withIMDb(ctx, s, titles.Key(media, id)); err != nil {
		return schema.ScoresOut{}, err
	}
	if s == nil || s.RTCritics == nil {
		if c := t.jellyfinCritics(ctx, media, id); c != nil {
			if s == nil {
				s = &schema.Scores{}
			}
			s.RTCritics = c
		}
	}
	return schema.ScoresOut{Scores: s, Note: note}, nil
}
