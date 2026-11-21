-- 0023: subtitles found online from the player ("Search online").
--
-- A downloaded subtitle belongs to one file: the title, the episode, and the
-- media source (the file) it was picked for, since a subtitle timed for one
-- release is off on another. Everyone in the household sees it as a track
-- of that file. The text is kept here, converted once to WebVTT (or kept as
-- ASS for styled subtitles), so playing it costs no outside call. It is
-- small: a film's subtitle is 50 to 150 KB.
--
-- delay_ms is the subtitle's own delay, chosen in the player and shared by
-- every device. added_by goes NULL when that person is deleted: the track
-- is the household's, and nothing in the row names anyone after that.
--
-- One row per file and source subtitle: picking the same subtitle twice
-- gives back the first row (NULLS NOT DISTINCT, so films, whose season and
-- episode are NULL, are unique too). Its index also serves the player's
-- list for one file, which reads the same leading columns.
CREATE TABLE online_subtitles (
    id bigserial PRIMARY KEY,
    media_type varchar(10) NOT NULL CHECK (media_type IN ('movie', 'tv')),
    tmdb_id integer NOT NULL,
    season integer,
    episode integer,
    item_id varchar(64) NOT NULL,
    media_source_id varchar(64) NOT NULL,
    language varchar(10) NOT NULL,
    source varchar(20) NOT NULL,
    source_ref varchar(300) NOT NULL,
    release text NOT NULL,
    hearing_impaired boolean NOT NULL,
    format varchar(4) NOT NULL CHECK (format IN ('vtt', 'ass')),
    body text NOT NULL,
    delay_ms integer NOT NULL DEFAULT 0,
    added_by integer REFERENCES users (id) ON DELETE SET NULL,
    added_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT online_subtitles_once UNIQUE NULLS NOT DISTINCT (media_type, tmdb_id, season, episode, media_source_id, source, source_ref)
);
