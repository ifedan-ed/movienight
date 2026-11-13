-- The person marks on a title and its episodes (package rw/title), and what
-- a title page reads besides TMDB. Every write is one statement (an upsert
-- or a delete), so taps from two devices, or a phone's outbox replaying,
-- cannot race into an error: the last to reach the database wins. The
-- Python read the row first and then wrote it, which two requests at once
-- could turn into a unique-constraint 500.

-- name: WatchlistAdd :exec
INSERT INTO watchlist (user_id, media_type, tmdb_id, added_at, source)
VALUES ($1, $2, $3, now(), 'app')
ON CONFLICT (user_id, media_type, tmdb_id) DO UPDATE
SET removed_at = NULL,
    added_at = CASE WHEN watchlist.removed_at IS NULL THEN watchlist.added_at ELSE now() END;

-- name: WatchlistRemove :exec
UPDATE watchlist SET removed_at = now()
WHERE user_id = $1 AND media_type = $2 AND tmdb_id = $3 AND removed_at IS NULL;

-- name: FavoriteAdd :exec
INSERT INTO favorites (user_id, media_type, tmdb_id, added_at, source)
VALUES ($1, $2, $3, now(), 'app')
ON CONFLICT (user_id, media_type, tmdb_id) DO UPDATE
SET removed_at = NULL,
    added_at = CASE WHEN favorites.removed_at IS NULL THEN favorites.added_at ELSE now() END;

-- name: FavoriteRemove :exec
UPDATE favorites SET removed_at = now()
WHERE user_id = $1 AND media_type = $2 AND tmdb_id = $3 AND removed_at IS NULL;

-- RatingSet rates a title. @set_review false keeps the comment already
-- there (a rating sent without one changes only the stars); true replaces
-- it (NULL clears it). Returns the comment kept.
-- name: RatingSet :one
INSERT INTO user_ratings (user_id, media_type, tmdb_id, rating, review, rated_at, source)
VALUES (@user_id, @media_type, @tmdb_id, @rating, @review, now(), 'app')
ON CONFLICT (user_id, media_type, tmdb_id) DO UPDATE
SET rating = excluded.rating, rated_at = now(), source = 'app',
    review = CASE WHEN @set_review::boolean THEN excluded.review ELSE user_ratings.review END
RETURNING review;

-- name: RatingClear :exec
DELETE FROM user_ratings WHERE user_id = $1 AND media_type = $2 AND tmdb_id = $3;

-- name: EpisodeFavoriteAdd :exec
INSERT INTO episode_favorites (user_id, tmdb_id, season, episode, added_at, source)
VALUES ($1, $2, $3, $4, now(), 'app')
ON CONFLICT ON CONSTRAINT uq_episode_favorite DO NOTHING;

-- name: EpisodeFavoriteRemove :exec
DELETE FROM episode_favorites WHERE user_id = $1 AND tmdb_id = $2 AND season = $3 AND episode = $4;

-- name: EpisodeRatingSet :exec
INSERT INTO episode_ratings (user_id, tmdb_id, season, episode, rating, rated_at, source)
VALUES ($1, $2, $3, $4, $5, now(), 'app')
ON CONFLICT ON CONSTRAINT uq_episode_rating DO UPDATE
SET rating = excluded.rating, rated_at = now(), source = 'app';

-- name: EpisodeRatingRemove :exec
DELETE FROM episode_ratings WHERE user_id = $1 AND tmdb_id = $2 AND season = $3 AND episode = $4;

-- EpisodeMarksForShows is a person's episode hearts (rating NULL, liked
-- true) and ratings of the given shows, for the places that show an episode
-- on its own (season page, History, Up next).
-- name: EpisodeMarksForShows :many
SELECT tmdb_id, season, episode, true AS liked, NULL::integer AS rating
FROM episode_favorites f WHERE f.user_id = @user_id AND f.tmdb_id = ANY(@shows::integer[])
UNION ALL
SELECT tmdb_id, season, episode, false, rating
FROM episode_ratings r WHERE r.user_id = @user_id AND r.tmdb_id = ANY(@shows::integer[]);

-- Community is everyone's rating of a title on this install, newest first,
-- with their names (all of them: no cap). ix_user_ratings_title (migration 0020).
-- name: Community :many
SELECT r.user_id, r.rating, r.review, r.rated_at, u.username, u.display_name
FROM user_ratings r JOIN users u ON u.id = r.user_id
WHERE r.media_type = $1 AND r.tmdb_id = $2
ORDER BY r.rated_at DESC, r.id DESC;

-- name: TitleExtra :one
SELECT * FROM title_extras WHERE key = $1;

-- TitleExtraSave keeps the scores MDBList sent; a score it left out keeps
-- its stored value (COALESCE), so an answer with fewer scores loses none.
-- name: TitleExtraSave :one
INSERT INTO title_extras (key, imdb_rating, imdb_votes, rt_critics, rt_audience, rt_url, metacritic, letterboxd,
                          trakt_rating, trakt_votes, mdblist_score, fetched_at)
VALUES (@key, @imdb_rating, @imdb_votes, @rt_critics, @rt_audience, @rt_url, @metacritic, @letterboxd,
        @trakt_rating, @trakt_votes, @mdblist_score, now())
ON CONFLICT (key) DO UPDATE SET
    imdb_rating = COALESCE(excluded.imdb_rating, title_extras.imdb_rating),
    imdb_votes = COALESCE(excluded.imdb_votes, title_extras.imdb_votes),
    rt_critics = COALESCE(excluded.rt_critics, title_extras.rt_critics),
    rt_audience = COALESCE(excluded.rt_audience, title_extras.rt_audience),
    rt_url = COALESCE(excluded.rt_url, title_extras.rt_url),
    metacritic = COALESCE(excluded.metacritic, title_extras.metacritic),
    letterboxd = COALESCE(excluded.letterboxd, title_extras.letterboxd),
    trakt_rating = COALESCE(excluded.trakt_rating, title_extras.trakt_rating),
    trakt_votes = COALESCE(excluded.trakt_votes, title_extras.trakt_votes),
    mdblist_score = COALESCE(excluded.mdblist_score, title_extras.mdblist_score),
    fetched_at = now()
RETURNING *;

-- IMDbRatingFor is IMDb's own rating of a title, by the IMDb id TMDB gave
-- it (the nightly IMDb file, imdb_ratings).
-- name: IMDbRatingFor :one
SELECT i.rating, i.votes FROM titles t JOIN imdb_ratings i ON i.tconst = t.imdb_id WHERE t.key = $1;

-- Resume is where the person is in a film (key movie:<id>) or in a show's
-- latest started episode.
-- name: ResumeFor :one
SELECT * FROM watch_progress
WHERE user_id = @user_id
  AND ((@media::text = 'movie' AND key = @movie_key::text)
       OR (@media::text = 'tv' AND media_type = 'episode' AND tmdb_id = @tmdb_id::integer))
ORDER BY updated_at DESC LIMIT 1;

-- LibraryItemFor is a title's Jellyfin item id, when it is in the library.
-- name: LibraryItemFor :one
SELECT jellyfin_id FROM library_items WHERE media_type = $1 AND tmdb_id = $2 ORDER BY jellyfin_id LIMIT 1;

-- name: RecordRequest :exec
INSERT INTO requests (user_id, media_type, tmdb_id, seerr_request_id, status, dry_run, created_at)
VALUES ($1, $2, $3, $4, $5, $6, now());

-- name: SetSeerrUser :exec
UPDATE users SET seerr_user_id = $2 WHERE id = $1;
