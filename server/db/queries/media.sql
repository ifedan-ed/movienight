-- Movie Night's own media libraries (package libraries).

-- MediaLibraries is every library with its counts: files, titles in the
-- library, and files with a problem.
-- name: MediaLibraries :many
SELECT l.id, l.name, l.kind, l.paths, l.created_at, l.scanned_at, l.scan_error, l.scan_problems,
       count(f.id)::integer AS files,
       count(DISTINCT f.tmdb_id) FILTER (WHERE f.probe IS NOT NULL)::integer AS titles,
       count(f.id) FILTER (WHERE f.problem IS NOT NULL)::integer AS problems
FROM media_libraries l LEFT JOIN media_files f ON f.library_id = l.id
GROUP BY l.id
ORDER BY l.name, l.id;

-- name: MediaLibrary :one
SELECT * FROM media_libraries WHERE id = $1;

-- name: MediaLibraryIDs :many
SELECT id FROM media_libraries ORDER BY id;

-- name: MediaLibraryCreate :one
INSERT INTO media_libraries (name, kind, paths) VALUES ($1, $2, @paths::text[]) RETURNING *;

-- MediaLibraryUpdate changes the name and folders (the kind is fixed: the
-- files were read as films or as episodes).
-- name: MediaLibraryUpdate :one
UPDATE media_libraries SET name = $2, paths = @paths::text[] WHERE id = $1 RETURNING *;

-- name: MediaLibraryDelete :execrows
DELETE FROM media_libraries WHERE id = $1;

-- MediaLibraryScanned records how a scan ended: error null when it went
-- through (then scanned_at moves), with the folders that couldn't be read.
-- name: MediaLibraryScanned :exec
UPDATE media_libraries SET
    scanned_at = CASE WHEN sqlc.narg(error)::text IS NULL THEN now() ELSE scanned_at END,
    scan_error = sqlc.narg(error)::text,
    scan_problems = @problems::jsonb
WHERE id = @id;

-- MediaKnown is what a scan sends the media service: each file's size,
-- time and subtitle signature, so unchanged files are not probed again.
-- name: MediaKnown :many
SELECT path, size, mtime, subs_sig FROM media_files WHERE library_id = $1;

-- MediaFileUpsert writes one scanned file (a batch, one round trip per
-- few hundred files). A match made by hand stays over every rescan.
-- name: MediaFileUpsert :batchexec
INSERT INTO media_files (library_id, path, size, mtime, subs_sig, probe, problem, media_type, parsed_title,
                         parsed_year, season, episode, episode_end, tmdb_id, matched_by)
VALUES (@library_id, @path, @size, @mtime, @subs_sig, @probe, @problem, @media_type, @parsed_title,
        @parsed_year, @season, @episode, @episode_end, @tmdb_id, @matched_by)
ON CONFLICT (library_id, path) DO UPDATE SET
    size = EXCLUDED.size, mtime = EXCLUDED.mtime, subs_sig = EXCLUDED.subs_sig, probe = EXCLUDED.probe,
    media_type = EXCLUDED.media_type, parsed_title = EXCLUDED.parsed_title, parsed_year = EXCLUDED.parsed_year,
    season = EXCLUDED.season, episode = EXCLUDED.episode, episode_end = EXCLUDED.episode_end,
    tmdb_id = CASE WHEN media_files.matched_by = 'hand' THEN media_files.tmdb_id ELSE EXCLUDED.tmdb_id END,
    matched_by = CASE WHEN media_files.matched_by = 'hand' THEN 'hand' ELSE EXCLUDED.matched_by END,
    problem = CASE WHEN media_files.matched_by = 'hand' AND EXCLUDED.probe IS NOT NULL THEN NULL ELSE EXCLUDED.problem END,
    updated_at = now();

-- name: MediaFilesDelete :execrows
DELETE FROM media_files WHERE library_id = $1 AND path = ANY(@paths::text[]);

-- MediaHandMatches are a library's matches made by hand, by what the name
-- says, so a new episode of a show fixed by hand joins it.
-- name: MediaHandMatches :many
SELECT DISTINCT parsed_title, parsed_year, tmdb_id::integer AS tmdb_id FROM media_files
WHERE library_id = $1 AND matched_by = 'hand' AND tmdb_id IS NOT NULL;

-- MediaProblems is a library's files with a problem, for the Libraries page.
-- name: MediaProblems :many
SELECT id, path, problem::text AS problem, parsed_title, parsed_year, season, episode, tmdb_id,
       (probe IS NOT NULL)::boolean AS readable
FROM media_files
WHERE library_id = $1 AND problem IS NOT NULL
ORDER BY path
LIMIT @lim;

-- name: MediaFile :one
SELECT * FROM media_files WHERE id = $1;

-- MediaMatchByHand sets the film or show of every file named like the one
-- fixed (the same title and year in the same library: a show's episodes,
-- a film in two parts).
-- name: MediaMatchByHand :execrows
UPDATE media_files SET tmdb_id = @tmdb_id, matched_by = 'hand',
    problem = CASE WHEN probe IS NULL THEN problem ELSE NULL END, updated_at = now()
WHERE library_id = @library_id AND media_type = @media_type AND parsed_title = @parsed_title
  AND parsed_year IS NOT DISTINCT FROM sqlc.narg(parsed_year)::integer;

-- MediaFileFor is the file to play for a film (season and episode null)
-- or an episode: a readable, matched file; the biggest when there are
-- several copies (the better one, as a rule), then the oldest.
-- name: MediaFileFor :one
SELECT f.id, f.path, f.probe, f.season, f.episode, f.library_id FROM media_files f
WHERE f.media_type = @media_type AND f.tmdb_id = @tmdb_id AND f.probe IS NOT NULL
  AND (sqlc.narg(season)::integer IS NULL OR f.season = sqlc.narg(season)::integer)
  AND (sqlc.narg(episode)::integer IS NULL OR
       (f.episode <= sqlc.narg(episode)::integer AND coalesce(f.episode_end, f.episode) >= sqlc.narg(episode)::integer))
ORDER BY f.size DESC, f.id
LIMIT 1;

-- MediaEpisodes are the episodes of a show the library holds, in order (a
-- file of two episodes counts for both).
-- name: MediaEpisodes :many
SELECT DISTINCT f.season::integer AS season, g.n::integer AS episode
FROM media_files f, generate_series(f.episode, coalesce(f.episode_end, f.episode)) AS g(n)
WHERE f.media_type = 'tv' AND f.tmdb_id = $1 AND f.probe IS NOT NULL AND f.season IS NOT NULL AND f.episode IS NOT NULL
ORDER BY 1, 2;

-- MediaTitles are the titles the libraries hold, for the search index.
-- name: MediaTitles :many
SELECT DISTINCT media_type::text AS media_type, tmdb_id::integer AS tmdb_id FROM media_files
WHERE tmdb_id IS NOT NULL AND probe IS NOT NULL;

-- MediaUnmatched are a library's readable files without a title that no
-- admin matched: matched again on every scan (TMDB may have been down, or
-- the matching got better).
-- name: MediaUnmatched :many
SELECT id, media_type, parsed_title, parsed_year, season, episode, path FROM media_files
WHERE library_id = $1 AND tmdb_id IS NULL AND probe IS NOT NULL AND matched_by IS DISTINCT FROM 'hand';

-- name: MediaSetMatch :batchexec
UPDATE media_files SET tmdb_id = sqlc.narg(tmdb_id)::integer, matched_by = sqlc.narg(matched_by)::text,
    problem = sqlc.narg(problem)::text, updated_at = now()
WHERE id = @id AND matched_by IS DISTINCT FROM 'hand';
