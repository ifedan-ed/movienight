-- The Jellyfin library and plays syncs (package jfsync).

-- name: SyncLibraryCount :one
SELECT count(*) FROM library_items;

-- SyncLibraryUpsert writes one title of a complete library read (a batch:
-- one round trip for the whole library, inside the sync's transaction).
-- name: SyncLibraryUpsert :batchexec
INSERT INTO library_items (jellyfin_id, media_type, tmdb_id, title, year, date_added, synced_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (jellyfin_id) DO UPDATE SET
    media_type = EXCLUDED.media_type, tmdb_id = EXCLUDED.tmdb_id, title = EXCLUDED.title,
    year = EXCLUDED.year, date_added = EXCLUDED.date_added, synced_at = EXCLUDED.synced_at;

-- SyncLibraryDeleteMissing removes what a complete read no longer lists.
-- name: SyncLibraryDeleteMissing :execrows
DELETE FROM library_items WHERE NOT (jellyfin_id = ANY(@keep::text[]));

-- name: SyncSeriesTMDB :many
SELECT jellyfin_id, tmdb_id FROM library_items WHERE media_type = 'tv' AND tmdb_id IS NOT NULL;

-- name: SyncLibraryTMDBForItem :one
SELECT tmdb_id FROM library_items WHERE jellyfin_id = $1 AND tmdb_id IS NOT NULL;

-- name: SyncPlayRefs :many
SELECT source_ref::text FROM history WHERE user_id = $1 AND source = 'jellyfin' AND source_ref IS NOT NULL;

-- SyncUnclaimed: watches marked here (by hand, or "Finished it?") within the
-- claim horizon that a Jellyfin play may now report too.
-- name: SyncUnclaimed :many
SELECT id, media_type, tmdb_id, season, episode, watched_at FROM history
WHERE user_id = $1 AND source IN ('manual', 'player') AND source_ref IS NULL AND watched_at >= $2
ORDER BY watched_at, id;

-- SyncClaimWatch folds a Jellyfin play into the watch marked here, unless
-- that reference is recorded already (the unique index would refuse it and
-- fail the whole sync).
-- name: SyncClaimWatch :batchexec
UPDATE history SET source = 'jellyfin', source_ref = @ref
WHERE history.id = @id AND history.source_ref IS NULL
  AND NOT EXISTS (SELECT 1 FROM history h WHERE h.user_id = @user_id AND h.source = 'jellyfin' AND h.source_ref = @ref);

-- SyncAddPlay records one Jellyfin play; a reference recorded already (the
-- plugin's "played" event and the sync can race) is left alone.
-- name: SyncAddPlay :batchexec
INSERT INTO history (user_id, media_type, tmdb_id, season, episode, episode_title, title, watched_at, source, source_ref)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'jellyfin', $9)
ON CONFLICT (user_id, source, source_ref) DO NOTHING;

-- name: SyncLinkedUsers :many
SELECT id, jellyfin_user_id::text FROM users WHERE jellyfin_user_id IS NOT NULL ORDER BY id;

-- SyncUserByJellyfinID: the person linked to a Jellyfin account (ids are
-- kept without dashes, lower case). Lowest id first, deterministic.
-- name: SyncUserByJellyfinID :one
SELECT * FROM users WHERE jellyfin_user_id = $1 ORDER BY id LIMIT 1;
