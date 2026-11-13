-- "Picked for you": the AI rows cache (package airows).

-- AIRowsWatchesSince counts the person's watches after a moment, up to a
-- cap (only "at least N" matters, so no more rows are read than that).
-- name: AIRowsWatchesSince :one
SELECT count(*) FROM (
    SELECT 1 FROM history WHERE user_id = @user_id AND watched_at > @since AND watched_at <= now() LIMIT @cap::int
) AS recent;

-- name: AIRowsMovieTitles :many
SELECT title FROM history WHERE user_id = $1 AND media_type = 'movie'
ORDER BY watched_at DESC, id DESC LIMIT $2;

-- name: AIRowsMovieRatings :many
SELECT tmdb_id, rating, review FROM user_ratings WHERE user_id = $1 AND media_type = 'movie'
ORDER BY rated_at DESC, id DESC;

-- name: AIRowsFilmsTotal :one
SELECT count(DISTINCT title) FROM history WHERE user_id = $1 AND media_type = 'movie';

-- name: AIRowsInsert :one
INSERT INTO ai_row_cache (user_id, model, payload_json, generated_at) VALUES ($1, $2, $3, now()) RETURNING id;

-- Only the newest is kept: the ones before it are never read again.
-- name: AIRowsDeleteOthers :exec
DELETE FROM ai_row_cache WHERE user_id = $1 AND id <> $2;
