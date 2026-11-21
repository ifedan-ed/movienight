-- 0024: Movie Night's own media libraries.
--
-- An admin names folders (as the media service sees them) and says whether
-- they hold films or shows. The media service scans them; every video it
-- finds is one row of media_files with what ffprobe read (probe) and the
-- film or show it was matched to (tmdb_id). A file that could not be read
-- has no probe; one that matched nothing has no tmdb_id. Either way it has
-- a problem, shown to admins, so nothing is dropped without a word.
--
-- A file is "in the library" when it has both, and library_titles puts those
-- next to Jellyfin's titles (library_items), one row per Jellyfin title and
-- per library file (a show's episodes are a row each, so a new episode
-- counts as an addition). It is not grouped, so a lookup by title reaches
-- ix_media_files_title instead of reading every file:
-- Home's library rows, "new in library", the cards' in-library mark and the
-- digests read the view, so they work with Jellyfin, libraries, or both.

CREATE TABLE media_libraries (
    id          serial PRIMARY KEY,
    name        text NOT NULL,
    kind        varchar(10) NOT NULL CHECK (kind IN ('movies', 'shows')),
    paths       text[] NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    -- The last scan that went through, and why the last one did not (null
    -- when it did). A folder that could not be read is a problem of the
    -- scan, kept in scan_problems: [{"path": ..., "error": ...}].
    scanned_at    timestamptz,
    scan_error    text,
    scan_problems jsonb NOT NULL DEFAULT '[]'
);

CREATE TABLE media_files (
    id          bigserial PRIMARY KEY,
    library_id  integer NOT NULL REFERENCES media_libraries (id) ON DELETE CASCADE,
    -- The path as the media service sees it, with its size, modification
    -- time (Unix nanoseconds, as reported) and its subtitle files'
    -- signature: a rescan probes again only when one of these changed.
    path        text NOT NULL,
    size        bigint NOT NULL,
    mtime       bigint NOT NULL,
    subs_sig    text NOT NULL DEFAULT '',
    probe       jsonb,
    problem     text,
    -- What the name says, and the film or show it was matched to.
    media_type   varchar(10) NOT NULL CHECK (media_type IN ('movie', 'tv')),
    parsed_title text NOT NULL,
    parsed_year  integer,
    season       integer,
    episode      integer,
    episode_end  integer,
    tmdb_id      integer,
    -- How: "name" (an id in the folder or file name), "search" (TMDB's
    -- search), "hand" (an admin; kept over every rescan).
    matched_by  varchar(10),
    added_at    timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (library_id, path)
);

-- Playback finds a title's (or an episode's) file; the view and the
-- in-library marks look titles up by (media_type, tmdb_id).
CREATE INDEX ix_media_files_title ON media_files (media_type, tmdb_id, season, episode)
    WHERE tmdb_id IS NOT NULL AND probe IS NOT NULL;
-- The Libraries page's problems list.
CREATE INDEX ix_media_files_problem ON media_files (library_id, id) WHERE problem IS NOT NULL;

CREATE VIEW library_titles AS
SELECT media_type::text AS media_type, tmdb_id, date_added AS added_at, jellyfin_id::text AS jellyfin_id
FROM library_items
WHERE tmdb_id IS NOT NULL
UNION ALL
SELECT media_type::text, tmdb_id, added_at, NULL::text
FROM media_files
WHERE tmdb_id IS NOT NULL AND probe IS NOT NULL;
