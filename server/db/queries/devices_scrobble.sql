-- Scrobbles, the Jellyfin plugin's events and watch progress (package
-- scrobble).

-- ScrobbleAdd records a reported watch once per (person, source, ref): the
-- unique index decides, in the same statement, so two reports of one play
-- at once (Kodi retrying while the first is still being answered) cannot
-- both be recorded. No row back: recorded before.
-- name: ScrobbleAdd :one
INSERT INTO history (user_id, media_type, tmdb_id, season, episode, title, watched_at, source, source_ref)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (user_id, source, source_ref) DO NOTHING
RETURNING id;

-- name: ProgressSave :exec
INSERT INTO watch_progress (user_id, key, media_type, tmdb_id, season, episode, title, episode_title,
                            position_s, duration_s, state, client, device, jellyfin_item_id, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, now())
ON CONFLICT (user_id, key) DO UPDATE SET
    media_type = EXCLUDED.media_type, tmdb_id = EXCLUDED.tmdb_id, season = EXCLUDED.season,
    episode = EXCLUDED.episode, title = EXCLUDED.title, episode_title = EXCLUDED.episode_title,
    position_s = EXCLUDED.position_s, duration_s = EXCLUDED.duration_s, state = EXCLUDED.state,
    client = EXCLUDED.client, device = EXCLUDED.device, jellyfin_item_id = EXCLUDED.jellyfin_item_id,
    updated_at = EXCLUDED.updated_at;

-- name: ProgressClear :exec
DELETE FROM watch_progress WHERE user_id = $1 AND key = $2;
