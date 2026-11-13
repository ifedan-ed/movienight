-- A person's watch history (package history). One watch "as shown" is the
-- rows of one title (or one episode) on one day, merged: the same watch can
-- arrive twice (a manual mark that Jellyfin later reports too), and it
-- shows once, with every row's id and every source. The day is the
-- person's own (@tz: their reported zone, else TIMEZONE; users.Location),
-- not UTC: the Python grouped by the UTC day, so an evening watch in New
-- York and a play reported after midnight UTC showed as two.
--
-- The grouping key: (media_type, tmdb_id, or the title for a row without
-- one, season, episode, local day). Each group's order is its newest row.

-- HistoryPage is one page of a person's merged watches, newest first,
-- grouped and paged in SQL (the Python loaded every watch they ever made,
-- grouped them in Python and sliced a page out). total is the number of
-- merged watches in all (every row carries it; an empty page reads it with
-- HistoryCount). @media: '' every watch, 'movie' films, 'episode' episodes.
-- name: HistoryPage :many
SELECT (array_agg(h.id ORDER BY h.watched_at DESC, h.id DESC))::integer[] AS ids,
       h.media_type::text AS media_type, h.tmdb_id,
       (array_agg(h.title ORDER BY h.watched_at DESC, h.id DESC))[1]::text AS title,
       h.season, h.episode,
       COALESCE((array_agg(h.episode_title ORDER BY h.watched_at DESC, h.id DESC))[1], '')::text AS episode_title,
       max(h.watched_at)::timestamptz AS watched_at,
       (array_agg(h.source::text ORDER BY h.watched_at DESC, h.id DESC))::text[] AS sources,
       count(*) OVER ()::integer AS total
FROM history h
WHERE h.user_id = @user_id AND (@media::text = '' OR h.media_type = @media::text)
GROUP BY h.media_type, h.tmdb_id, COALESCE(h.tmdb_id::text, h.title), h.season, h.episode,
         (h.watched_at AT TIME ZONE @tz::text)::date
ORDER BY max(h.watched_at) DESC, max(h.id) DESC
LIMIT @lim OFFSET @off;

-- name: HistoryCount :one
SELECT count(*)::integer FROM (
    SELECT 1 FROM history h
    WHERE h.user_id = @user_id AND (@media::text = '' OR h.media_type = @media::text)
    GROUP BY h.media_type, h.tmdb_id, COALESCE(h.tmdb_id::text, h.title), h.season, h.episode,
             (h.watched_at AT TIME ZONE @tz::text)::date
) g;

-- TitleWatches is one title's merged watches (a film's, or every episode
-- of a show: @media 'movie' or 'episode'), newest first. Read through
-- ix_history_user_tmdb (migration 0019).
-- name: TitleWatches :many
SELECT (array_agg(h.id ORDER BY h.watched_at DESC, h.id DESC))::integer[] AS ids,
       h.media_type::text AS media_type, h.tmdb_id,
       (array_agg(h.title ORDER BY h.watched_at DESC, h.id DESC))[1]::text AS title,
       h.season, h.episode,
       COALESCE((array_agg(h.episode_title ORDER BY h.watched_at DESC, h.id DESC))[1], '')::text AS episode_title,
       max(h.watched_at)::timestamptz AS watched_at,
       (array_agg(h.source::text ORDER BY h.watched_at DESC, h.id DESC))::text[] AS sources
FROM history h
WHERE h.user_id = @user_id AND h.tmdb_id = @tmdb_id AND h.media_type = @media::text
GROUP BY h.media_type, h.tmdb_id, h.season, h.episode, (h.watched_at AT TIME ZONE @tz::text)::date
ORDER BY max(h.watched_at) DESC, max(h.id) DESC;

-- name: InsertWatch :one
INSERT INTO history (user_id, media_type, tmdb_id, season, episode, episode_title, title, watched_at, source)
VALUES (@user_id, @media_type, @tmdb_id, @season, @episode, @episode_title, @title, @watched_at, @source)
RETURNING id;

-- A film marked watched here (by hand, or "Finished it?") leaves the
-- watchlist, as on Trakt. Plays from Jellyfin, Kodi or other players never
-- do this (they don't call it).
-- name: WatchlistDropMovie :exec
UPDATE watchlist SET removed_at = now()
WHERE user_id = $1 AND media_type = 'movie' AND tmdb_id = $2 AND removed_at IS NULL;

-- RemoveWatches deletes a person's watches by id; with @tmdb_id > 0 only
-- that title's (a title page's "remove"), so an id of another title (or of
-- another person) is never touched.
-- name: RemoveWatches :execrows
DELETE FROM history
WHERE user_id = @user_id AND id = ANY(@ids::integer[])
  AND (@tmdb_id::integer = 0 OR (tmdb_id = @tmdb_id::integer AND media_type = @media::text));

-- TickEpisodes records watches of episodes in one statement (so a season
-- is ticked all or nothing): one row per (season, episode) given that has
-- no watch yet. The arrays are zipped: seasons[i], episodes[i], names[i]
-- ('' for none). Returns the episodes written.
-- name: TickEpisodes :many
INSERT INTO history (user_id, media_type, tmdb_id, season, episode, episode_title, title, watched_at, source)
SELECT @user_id::integer, 'episode', @tmdb_id::integer, e.season, e.episode, NULLIF(e.name, ''), @title::text,
       @watched_at::timestamptz, 'manual'
FROM (SELECT unnest(@seasons::integer[]) AS season, unnest(@episodes::integer[]) AS episode,
             unnest(@names::text[]) AS name) e
WHERE NOT EXISTS (SELECT 1 FROM history h
                  WHERE h.user_id = @user_id::integer AND h.media_type = 'episode' AND h.tmdb_id = @tmdb_id::integer
                    AND h.season = e.season AND h.episode = e.episode)
RETURNING season, episode;

-- UntickEpisodes deletes every watch of the episodes given ("S:E" keys),
-- in one statement. Returns the rows removed (an episode watched twice
-- comes back twice).
-- name: UntickEpisodes :many
DELETE FROM history
WHERE user_id = @user_id AND media_type = 'episode' AND tmdb_id = @tmdb_id
  AND (season::text || ':' || episode::text) = ANY(@keys::text[])
RETURNING season, episode;

-- HistoryStats is Profile's numbers, from the merged watches (as the
-- Python counted them): films (distinct), film plays, episodes, shows,
-- hours (a film's runtime, an episode the show's runtime or 30 minutes),
-- films watched this year (@year, in the person's zone @tz), ratings and the
-- watchlist.
-- name: HistoryStats :one
WITH g AS (
    SELECT h.media_type, h.tmdb_id, min(h.title) AS title, h.season, h.episode,
           (h.watched_at AT TIME ZONE @tz::text)::date AS day
    FROM history h WHERE h.user_id = @user_id
    GROUP BY h.media_type, h.tmdb_id, COALESCE(h.tmdb_id::text, h.title), h.season, h.episode,
             (h.watched_at AT TIME ZONE @tz::text)::date
)
SELECT
    count(DISTINCT COALESCE(g.tmdb_id::text, g.title)) FILTER (WHERE g.media_type = 'movie')::integer AS movies,
    count(*) FILTER (WHERE g.media_type = 'movie')::integer AS movie_plays,
    count(*) FILTER (WHERE g.media_type = 'episode' AND g.episode IS NOT NULL)::integer AS episodes,
    count(DISTINCT g.tmdb_id) FILTER (WHERE g.media_type = 'episode' AND g.episode IS NOT NULL)::integer AS shows,
    round(COALESCE(sum(CASE
        WHEN g.media_type = 'movie' AND g.tmdb_id IS NOT NULL THEN COALESCE(t.runtime, 0)
        WHEN g.media_type = 'episode' AND g.episode IS NOT NULL AND g.tmdb_id IS NOT NULL THEN COALESCE(NULLIF(t.runtime, 0), 30)
        ELSE 0 END), 0) / 60.0)::integer AS hours,
    count(*) FILTER (WHERE g.media_type = 'movie' AND extract(year FROM g.day) = @year::integer)::integer AS this_year,
    (SELECT count(*) FROM user_ratings r WHERE r.user_id = @user_id)::integer AS ratings,
    (SELECT count(*) FROM watchlist w WHERE w.user_id = @user_id AND w.removed_at IS NULL)::integer AS watchlist
FROM g LEFT JOIN titles t
    ON t.key = (CASE WHEN g.media_type = 'episode' THEN 'tv:' ELSE 'movie:' END) || g.tmdb_id::text;

-- RatePrompt is the newest film watched since @since that the person has
-- not rated and not dismissed ("Not now"). Every watch of those days is
-- looked at (the Python's old cut at the 20 newest is gone), in SQL.
-- name: RatePrompt :one
SELECT h.tmdb_id::integer AS tmdb_id, h.title
FROM history h
WHERE h.user_id = @user_id AND h.media_type = 'movie' AND h.tmdb_id IS NOT NULL AND h.watched_at >= @since
  AND NOT EXISTS (SELECT 1 FROM user_ratings r
                  WHERE r.user_id = h.user_id AND r.media_type = 'movie' AND r.tmdb_id = h.tmdb_id)
  AND NOT (('movie:' || h.tmdb_id::text) = ANY(@dismissed::text[]))
ORDER BY h.watched_at DESC, h.id DESC
LIMIT 1;

-- DismissRatePrompt adds a key to the person's "Not now" list in one
-- statement (two taps at once cannot lose one; every one is kept, no cap).
-- name: DismissRatePrompt :exec
INSERT INTO user_settings (user_id, key, value)
VALUES (@user_id, 'rate_prompt_dismissed', jsonb_build_array(@key::text)::text)
ON CONFLICT (user_id, key) DO UPDATE SET value = CASE
    WHEN user_settings.value::jsonb ? @key::text THEN user_settings.value
    ELSE (user_settings.value::jsonb || jsonb_build_array(@key::text))::text END;
