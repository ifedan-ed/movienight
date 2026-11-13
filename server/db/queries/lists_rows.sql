-- Home rows and the "See all" grids behind them (package rows): the
-- person's own collections and the library, one page at a time, with how
-- many there are in all. The Python read every key of a collection and then
-- sliced out the page; here the page is cut in SQL. media '' is both.

-- name: WatchlistPage :many
SELECT media_type::text AS media_type, tmdb_id, count(*) OVER ()::integer AS total
FROM watchlist
WHERE user_id = @user_id AND removed_at IS NULL AND (@media::text = '' OR media_type = @media::text)
ORDER BY added_at DESC, id DESC
LIMIT @lim OFFSET @off;

-- name: FavoritesPage :many
SELECT media_type::text AS media_type, tmdb_id, count(*) OVER ()::integer AS total
FROM favorites
WHERE user_id = @user_id AND removed_at IS NULL AND (@media::text = '' OR media_type = @media::text)
ORDER BY added_at DESC, id DESC
LIMIT @lim OFFSET @off;

-- RatingsPage: every rating newest first, or (top) the 7s and up, best
-- first ("Your top rated").
-- name: RatingsPage :many
SELECT media_type::text AS media_type, tmdb_id, count(*) OVER ()::integer AS total
FROM user_ratings
WHERE user_id = @user_id AND (@media::text = '' OR media_type = @media::text)
  AND (NOT @top::boolean OR rating >= 7)
ORDER BY CASE WHEN @top::boolean THEN rating END DESC, rated_at DESC, id DESC
LIMIT @lim OFFSET @off;

-- RecentLibraryPage: the library, newest additions first, one row per title
-- (Jellyfin may hold two copies of one film).
-- name: RecentLibraryPage :many
SELECT media_type, tmdb_id, count(*) OVER ()::integer AS total FROM (
    SELECT media_type::text AS media_type, tmdb_id::integer AS tmdb_id, max(date_added) AS added
    FROM library_items
    WHERE tmdb_id IS NOT NULL AND (@media::text = '' OR media_type = @media::text)
    GROUP BY media_type, tmdb_id
) lib
ORDER BY added DESC NULLS LAST, media_type, tmdb_id
LIMIT @lim OFFSET @off;

-- UnwatchedLibraryPage: library titles the person has never watched, in an
-- order that is shuffled per person and per day (@day: the person's own
-- date, in their zone) and stable within the day, so the row shows a different
-- slice each day and paging through it never repeats a title. The Python
-- loaded the whole library and the person's whole history to do this, and
-- used the server's date, not the person's.
-- name: UnwatchedLibraryPage :many
SELECT media_type, tmdb_id, count(*) OVER ()::integer AS total FROM (
    SELECT DISTINCT li.media_type::text AS media_type, li.tmdb_id::integer AS tmdb_id
    FROM library_items li
    WHERE li.tmdb_id IS NOT NULL AND (@media::text = '' OR li.media_type = @media::text)
      AND NOT EXISTS (
        SELECT 1 FROM history h
        WHERE h.user_id = @user_id AND h.tmdb_id = li.tmdb_id
          AND h.media_type = CASE WHEN li.media_type = 'tv' THEN 'episode' ELSE 'movie' END)
) lib
ORDER BY md5(media_type || ':' || tmdb_id::text || ':' || @day::text || ':' || @user_id::text), media_type, tmdb_id
LIMIT @lim OFFSET @off;

-- SeriesTMDBIDs maps Jellyfin series ids to TMDB ids (the Up next row).
-- name: SeriesTMDBIDs :many
SELECT jellyfin_id, tmdb_id::integer AS tmdb_id FROM library_items
WHERE media_type = 'tv' AND tmdb_id IS NOT NULL AND jellyfin_id = ANY(@ids::text[]);

-- RecentSeeds: the person's last @lim distinct titles watched, newest
-- first, for "Because you watched": a title's newest watch dates it, and
-- one they rated 5/10 or lower is left out. One query over their history
-- (the Python read it 600 rows at a time until it had found 15 titles).
-- Two titles last watched at the same moment (marked by hand for one day,
-- both at midnight) are ordered by which watch was recorded last
-- (last_id), as the Python's "watched_at DESC, id DESC" read them.
-- name: RecentSeeds :many
SELECT s.media, s.tmdb_id, s.title, s.last FROM (
    SELECT CASE WHEN h.media_type = 'episode' THEN 'tv' ELSE 'movie' END::text AS media,
           h.tmdb_id::integer AS tmdb_id,
           (array_agg(h.title ORDER BY h.watched_at DESC, h.id DESC))[1]::text AS title,
           max(h.watched_at)::timestamptz AS last,
           (array_agg(h.id ORDER BY h.watched_at DESC, h.id DESC))[1] AS last_id
    FROM history h
    WHERE h.user_id = @user_id AND h.tmdb_id IS NOT NULL
      AND (@media::text = '' OR h.media_type = CASE WHEN @media::text = 'tv' THEN 'episode' ELSE 'movie' END)
    GROUP BY 1, 2
) s
WHERE NOT EXISTS (SELECT 1 FROM user_ratings r WHERE r.user_id = @user_id AND r.media_type = s.media
                                                AND r.tmdb_id = s.tmdb_id AND r.rating <= 5)
ORDER BY s.last DESC, s.last_id DESC
LIMIT @lim;

-- name: LatestAIRows :one
SELECT * FROM ai_row_cache WHERE user_id = $1 ORDER BY generated_at DESC, id DESC LIMIT 1;

-- WatchesSince counts the person's watches after the AI rows were made (up
-- to now), capped at @cap: only "at least AI_REFRESH_AFTER_WATCHES" matters.
-- name: WatchesSince :one
SELECT count(*)::integer FROM (
    SELECT 1 FROM history WHERE user_id = @user_id AND watched_at > @since AND watched_at <= now() LIMIT @cap
) w;
