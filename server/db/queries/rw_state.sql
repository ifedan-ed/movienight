-- A person's state for the titles on one page (package userstate): their
-- watchlist, watches, ratings and favourites, and the library, for exactly
-- the titles the page shows. One round trip, one row per fact.
--
-- The Python loaded the person's whole history, watchlist, ratings and
-- favourites (and the whole library) for every Home row: twelve rows on Home
-- meant twelve full reads of everything they had ever watched. Here each
-- part is an index lookup on the page's (media, id) pairs:
--   watchlist, user_ratings, favorites: their (user_id, media_type, tmdb_id)
--     unique indexes;
--   history: ix_history_user_tmdb (migration 0019);
--   library_items: ix_library_items_tmdb_id; media_files: ix_media_files_title.
--
-- kind: w watchlist, r rating (n), f favourite, h watched (n: plays of a
-- film on distinct days in the person's zone (@tz), or distinct episodes of a
-- show seen: the same watch arriving twice on one day, a manual mark and a
-- Jellyfin play, counts once), l library (jf: the Jellyfin item id, '' when
-- only Movie Night's own library has it).
-- name: StateForTitles :many
WITH k AS (
    -- Two arrays of the same length, zipped (sqlc's parser does not know the
    -- two-argument unnest; set-returning calls in one select list zip).
    SELECT DISTINCT unnest(@medias::text[]) AS media_type, unnest(@ids::integer[]) AS tmdb_id
)
SELECT 'w'::text AS kind, w.media_type::text AS media_type, w.tmdb_id, 0::integer AS n, ''::text AS jf
FROM watchlist w JOIN k ON k.media_type = w.media_type AND k.tmdb_id = w.tmdb_id
WHERE w.user_id = @user_id AND w.removed_at IS NULL
UNION ALL
SELECT 'r', r.media_type::text, r.tmdb_id, r.rating, ''
FROM user_ratings r JOIN k ON k.media_type = r.media_type AND k.tmdb_id = r.tmdb_id
WHERE r.user_id = @user_id
UNION ALL
SELECT 'f', f.media_type::text, f.tmdb_id, 0, ''
FROM favorites f JOIN k ON k.media_type = f.media_type AND k.tmdb_id = f.tmdb_id
WHERE f.user_id = @user_id AND f.removed_at IS NULL
UNION ALL
SELECT 'h', k.media_type, k.tmdb_id,
       count(DISTINCT CASE WHEN h.media_type = 'episode' THEN h.season::text || ':' || h.episode::text
                           ELSE (h.watched_at AT TIME ZONE @tz::text)::date::text END)::integer, ''
FROM history h JOIN k ON k.tmdb_id = h.tmdb_id
                     AND h.media_type = CASE WHEN k.media_type = 'tv' THEN 'episode' ELSE 'movie' END
WHERE h.user_id = @user_id
GROUP BY k.media_type, k.tmdb_id
UNION ALL
SELECT 'l', k.media_type, k.tmdb_id, 0,
       coalesce((SELECT min(li.jellyfin_id) FROM library_items li
                 WHERE li.media_type = k.media_type AND li.tmdb_id = k.tmdb_id), '')::text
FROM k
-- Each table on its own, by title (not through the library_titles view,
-- which the planner reads whole before joining).
WHERE EXISTS (SELECT 1 FROM library_items li WHERE li.media_type = k.media_type AND li.tmdb_id = k.tmdb_id)
   OR EXISTS (SELECT 1 FROM media_files f WHERE f.media_type = k.media_type AND f.tmdb_id = k.tmdb_id
                                           AND f.probe IS NOT NULL);
