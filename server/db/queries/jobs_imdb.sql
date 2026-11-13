-- IMDb's own ratings (imdb_ratings), package imdb. The nightly load itself
-- (COPY into a new table and a swap) is plain pgx in internal/imdb: sqlc
-- cannot express COPY FROM a stream or DDL.

-- name: ImdbRatingGet :one
SELECT rating, votes FROM imdb_ratings WHERE tconst = $1;

-- name: ImdbLoaded :one
SELECT EXISTS (SELECT 1 FROM imdb_ratings) AS loaded;
