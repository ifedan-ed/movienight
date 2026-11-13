-- The title cache (titles): TMDB's metadata for every title Movie Night has
-- shown, so cards for watchlists, lists and history need no TMDB call.
-- Package titles is the only writer, for the API and the worker alike (the
-- worker's nightly refresh, digests, AI rows, scrobbles and syncs).

-- name: TitlesByKeys :many
SELECT * FROM titles WHERE key = ANY(@keys::text[]);

-- TitleFreshness is the small read that decides which titles passing
-- through a page need writing at all (package titles, Remember): a key that
-- is missing, or a list-sourced row that went stale, or one still without a
-- poster. Everything else is skipped, so a warm page writes nothing.
-- name: TitleFreshness :many
SELECT key, detailed, fetched_at, (poster_path IS NULL)::boolean AS no_poster
FROM titles WHERE key = ANY(@keys::text[]);

-- UpsertTitles writes many titles in one statement. The rows come as one
-- JSON array (jsonb_to_recordset) because sqlc cannot type arrays with NULL
-- elements, and a title's optional fields are often NULL. Callers send the
-- rows sorted by key, so two requests writing overlapping titles lock them
-- in the same order and cannot deadlock (the Python wrote in page order).
--
-- What a write may change: a richer answer never loses to a poorer one. A
-- field the new row lacks keeps the stored value (COALESCE), an empty title
-- keeps the stored name, `detailed` once true stays true, and fetched_at
-- moves only for a detailed write or a list-sourced row (the worker's
-- refresh keys off fetched_at of detailed rows).
-- name: UpsertTitles :exec
INSERT INTO titles AS t (key, media_type, tmdb_id, title, original_title, year, release_date, poster_path,
                         backdrop_path, overview, genres_json, runtime, vote_average, vote_count, imdb_id,
                         detailed, fetched_at)
SELECT r.key, r.media_type, r.tmdb_id, r.title, r.original_title, r.year, r.release_date, r.poster_path,
       r.backdrop_path, r.overview, r.genres_json, r.runtime, r.vote_average, r.vote_count, r.imdb_id,
       r.detailed, now()
FROM jsonb_to_recordset(@rows::jsonb) AS r(
    key text, media_type text, tmdb_id integer, title text, original_title text, year integer,
    release_date text, poster_path text, backdrop_path text, overview text, genres_json text,
    runtime integer, vote_average double precision, vote_count integer, imdb_id text, detailed boolean)
ORDER BY r.key
ON CONFLICT (key) DO UPDATE SET
    title          = COALESCE(NULLIF(excluded.title, ''), t.title),
    original_title = COALESCE(excluded.original_title, t.original_title),
    year           = COALESCE(excluded.year, t.year),
    release_date   = COALESCE(excluded.release_date, t.release_date),
    poster_path    = COALESCE(excluded.poster_path, t.poster_path),
    backdrop_path  = COALESCE(excluded.backdrop_path, t.backdrop_path),
    overview       = COALESCE(excluded.overview, t.overview),
    genres_json    = COALESCE(excluded.genres_json, t.genres_json),
    runtime        = COALESCE(excluded.runtime, t.runtime),
    vote_average   = COALESCE(excluded.vote_average, t.vote_average),
    vote_count     = COALESCE(excluded.vote_count, t.vote_count),
    imdb_id        = COALESCE(excluded.imdb_id, t.imdb_id),
    detailed       = t.detailed OR excluded.detailed,
    fetched_at     = CASE WHEN excluded.detailed OR NOT t.detailed THEN excluded.fetched_at ELSE t.fetched_at END;

-- StaleTitles is the next page of detailed titles whose details have gone
-- stale, oldest first, after the (fetched_at, key) last looked at (keyset
-- paging: a title that failed to refresh is passed by, not read again
-- forever). Released lately (on or after recent_since) goes stale after
-- recent_before, the rest after old_before.
-- name: StaleTitles :many
SELECT key, fetched_at FROM titles
WHERE detailed
  AND (fetched_at < @old_before::timestamptz
       OR (release_date >= @recent_since::text AND fetched_at < @recent_before::timestamptz))
  AND (fetched_at, key) > (@after_fetched::timestamptz, @after_key::text)
ORDER BY fetched_at, key
LIMIT @page_size::int;

-- TitleTMDBByIMDb finds a title of one kind by its IMDb id, the lowest
-- TMDB id first so the answer is the same every time (the Python's
-- limit(1) had no ORDER BY).
-- name: TitleTMDBByIMDb :one
SELECT tmdb_id FROM titles WHERE imdb_id = $1 AND media_type = $2 ORDER BY tmdb_id LIMIT 1;
