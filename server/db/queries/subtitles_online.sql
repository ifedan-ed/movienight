-- Subtitles found online from the player (internal/subtitles).

-- name: OnlineSubtitleAdd :one
-- Picking a subtitle already kept for this file gives back that row: the
-- no-op update makes RETURNING see it.
INSERT INTO online_subtitles (media_type, tmdb_id, season, episode, item_id, media_source_id, language, source,
    source_ref, release, hearing_impaired, format, body, added_by)
VALUES (@media_type, @tmdb_id, sqlc.narg(season), sqlc.narg(episode), @item_id, @media_source_id, @language, @source,
    @source_ref, @release, @hearing_impaired, @format, @body, sqlc.narg(added_by))
ON CONFLICT ON CONSTRAINT online_subtitles_once DO UPDATE SET source_ref = EXCLUDED.source_ref
RETURNING id, media_type, tmdb_id, season, episode, item_id, media_source_id, language, source, source_ref,
    release, hearing_impaired, format, delay_ms, added_by, added_at;

-- name: OnlineSubtitlesFor :many
-- The list leaves the text out: the player fetches a track's file only
-- when it is chosen.
SELECT id, media_type, tmdb_id, season, episode, item_id, media_source_id, language, source, source_ref,
    release, hearing_impaired, format, delay_ms, added_by, added_at
FROM online_subtitles
WHERE media_type = @media_type AND tmdb_id = @tmdb_id
  AND season IS NOT DISTINCT FROM sqlc.narg(season) AND episode IS NOT DISTINCT FROM sqlc.narg(episode)
  AND media_source_id = @media_source_id
ORDER BY added_at, id;

-- name: OnlineSubtitleGet :one
SELECT id, media_type, tmdb_id, season, episode, item_id, media_source_id, language, source, source_ref,
    release, hearing_impaired, format, delay_ms, added_by, added_at
FROM online_subtitles WHERE id = @id;

-- name: OnlineSubtitleBody :one
SELECT format, body FROM online_subtitles WHERE id = @id;

-- name: OnlineSubtitleSetDelay :execrows
UPDATE online_subtitles SET delay_ms = @delay_ms WHERE id = @id;

-- name: OnlineSubtitleDelete :execrows
DELETE FROM online_subtitles WHERE id = @id;
