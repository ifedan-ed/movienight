-- The person's own account (package rw/profile): the Jellyfin link, the
-- scrobble token, and the full data export.

-- UserWithJellyfin finds who else (if anyone) has this Jellyfin account
-- connected: one Jellyfin account belongs to one person here.
-- name: OtherUserWithJellyfin :one
SELECT id FROM users WHERE jellyfin_user_id = @jellyfin_user_id AND id <> @user_id LIMIT 1;

-- LinkJellyfin saves a Jellyfin sign-in. A different Jellyfin account
-- means a different Seerr user, so the remembered one is dropped then.
-- name: LinkJellyfin :one
UPDATE users SET
    seerr_user_id = CASE WHEN jellyfin_user_id IS DISTINCT FROM @jellyfin_user_id::text THEN NULL ELSE seerr_user_id END,
    jellyfin_user_id = @jellyfin_user_id::text, jellyfin_user_name = @jellyfin_user_name,
    jellyfin_token_enc = @jellyfin_token_enc, jellyfin_linked_at = now()
WHERE id = @id
RETURNING *;

-- name: UnlinkJellyfin :exec
UPDATE users SET jellyfin_user_id = NULL, jellyfin_user_name = NULL, jellyfin_token_enc = NULL,
                 jellyfin_linked_at = NULL, seerr_user_id = NULL
WHERE id = $1;

-- name: SetScrobbleTokenHash :exec
UPDATE users SET scrobble_token_hash = $2 WHERE id = $1;

-- ---- The export: everything kept for one person ----

-- name: ExportWatchlist :many
SELECT media_type, tmdb_id, added_at, source FROM watchlist
WHERE user_id = $1 AND removed_at IS NULL ORDER BY added_at, id;

-- name: ExportRatings :many
SELECT media_type, tmdb_id, rating, review, rated_at, source FROM user_ratings
WHERE user_id = $1 ORDER BY rated_at, id;

-- name: ExportFavorites :many
SELECT media_type, tmdb_id FROM favorites WHERE user_id = $1 AND removed_at IS NULL ORDER BY added_at, id;

-- name: ExportEpisodeFavorites :many
SELECT tmdb_id, season, episode, added_at, source FROM episode_favorites
WHERE user_id = $1 ORDER BY tmdb_id, season, episode;

-- name: ExportEpisodeRatings :many
SELECT tmdb_id, season, episode, rating, rated_at, source FROM episode_ratings
WHERE user_id = $1 ORDER BY tmdb_id, season, episode;

-- name: ExportHistory :many
SELECT media_type, tmdb_id, title, season, episode, watched_at, source FROM history
WHERE user_id = $1 ORDER BY watched_at, id;

-- name: ExportLists :many
SELECT id, name, kind, description, filters_json FROM lists WHERE owner_id = $1 ORDER BY created_at, id;

-- name: ExportListItems :many
SELECT li.list_id, li.media_type, li.tmdb_id FROM list_items li JOIN lists l ON l.id = li.list_id
WHERE l.owner_id = $1 ORDER BY li.list_id, li.position, li.id;
