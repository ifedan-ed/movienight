-- Play sessions ("Finished it?"), the scrobble token (package playback).

-- name: PlaySessionGet :one
SELECT * FROM play_sessions WHERE id = $1 AND user_id = $2;

-- PlaySessionCloseOpen: starting a film closes the ones before it, so
-- nobody is asked about a film they have moved on from.
-- name: PlaySessionCloseOpen :exec
UPDATE play_sessions SET finished_at = now(), outcome = 'not_finished'
WHERE user_id = $1 AND finished_at IS NULL;

-- name: PlaySessionCreate :one
INSERT INTO play_sessions (id, user_id, media_type, tmdb_id, season, episode, title, jellyfin_item_id, player, started_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
RETURNING *;

-- PlaySessionLatestOpen is the latest open Play in an outside player
-- (not one of the apps that play through Jellyfin, which record their own
-- plays) started since the given time.
-- name: PlaySessionLatestOpen :one
SELECT * FROM play_sessions
WHERE user_id = @user_id AND finished_at IS NULL AND NOT (player = ANY(@jellyfin_apps::text[]))
  AND started_at >= @since
ORDER BY started_at DESC, id DESC
LIMIT 1;

-- PlaySessionFinish closes a Play once: of two answers at once (a double
-- tap, two tabs) only one gets the row back, so a watch is recorded once.
-- name: PlaySessionFinish :one
UPDATE play_sessions SET finished_at = $3, outcome = $4
WHERE id = $1 AND user_id = $2 AND finished_at IS NULL
RETURNING *;

-- PlaySessionAsk marks a Play asked about, once: "Finished it?" shows in
-- one place, not in every open tab.
-- name: PlaySessionAsk :one
UPDATE play_sessions SET asked_at = $3
WHERE id = $1 AND user_id = $2 AND asked_at IS NULL AND finished_at IS NULL
RETURNING id;

-- PlayWatchedEpisodes: every regular episode of a show the person has
-- ticked (season 0, the specials, never decides what comes next).
-- name: PlayWatchedEpisodes :many
SELECT DISTINCT season::int AS season, episode::int AS episode FROM history
WHERE user_id = $1 AND media_type = 'episode' AND tmdb_id = $2 AND season >= 1 AND episode IS NOT NULL;

