-- The digest emails (package digest): building, the atomic send claim, and
-- what each digest reads from a person's records.

-- name: DigestByPeriod :one
SELECT * FROM digests WHERE user_id = $1 AND kind = $2 AND period_start = $3;

-- name: DigestGet :one
SELECT * FROM digests WHERE id = $1 AND user_id = $2;

-- DigestInsert keeps the first digest written for a person and period: two
-- runs building at once (a catch-up beside the schedule) both end with the
-- same stored email, the one that is then sent. No row back: one was there.
-- name: DigestInsert :one
INSERT INTO digests (user_id, kind, period_start, period_end, subject, html, data_json, model, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
ON CONFLICT ON CONSTRAINT uq_digest_period DO NOTHING
RETURNING *;

-- DigestRewrite replaces a digest's content (a rebuild asked for on
-- purpose), never its send state.
-- name: DigestRewrite :one
UPDATE digests SET period_end = $2, subject = $3, html = $4, data_json = $5, model = $6, created_at = now()
WHERE id = $1
RETURNING *;

-- DigestClaimSend takes the one right to send a digest now, atomically: no
-- other send of it may be under way (sending_since NULL). The schedule's own
-- try (scheduled) also needs it unsent and under the try limit, and counts
-- the try. No row back: someone else is sending it, or the schedule must
-- leave it. Committed before the email goes, so a send that never reports
-- back is visible afterwards (DigestSettleStale).
-- name: DigestClaimSend :one
UPDATE digests
SET sending_since = now(),
    send_tries = send_tries + CASE WHEN sqlc.arg(scheduled)::bool THEN 1 ELSE 0 END
WHERE id = sqlc.arg(id)
  AND sending_since IS NULL
  AND (NOT sqlc.arg(scheduled)::bool OR (sent_at IS NULL AND send_tries < sqlc.arg(max_tries)::int))
RETURNING *;

-- DigestMarkSent records the outcome of this claim (sending_since as
-- claimed): sent, or may have been sent.
-- name: DigestMarkSent :exec
UPDATE digests SET sent_at = now(), sent_to = $3, sending_since = NULL
WHERE id = $1 AND sending_since = $2;

-- DigestReleaseClaim gives the claim back after a clear failure, so a later
-- try is possible.
-- name: DigestReleaseClaim :exec
UPDATE digests SET sending_since = NULL WHERE id = $1 AND sending_since = $2;

-- DigestSettleStale: a send claimed before `before` that never reported back
-- (the process stopped midway, or the write after a send failed) is taken
-- as sent, at the time it started: it may have gone out, so it is never
-- sent twice. No row back: nothing stale to settle.
-- name: DigestSettleStale :one
UPDATE digests
SET sent_at = COALESCE(sent_at, sending_since), sent_to = COALESCE(sent_to, sqlc.arg(sent_to)), sending_since = NULL
WHERE id = sqlc.arg(id) AND sending_since IS NOT NULL AND sending_since < sqlc.arg(before)
RETURNING *;

-- DigestPeople is everyone the schedule looks at for one kind: their zone
-- (NULL: never reported), when they joined and the latest period of the
-- kind done for them ("": none recorded yet; migration 0021). One query
-- per run, so the hourly catch-up costs one read when nothing is due.
-- name: DigestPeople :many
SELECT u.id, u.time_zone, u.created_at, COALESCE(d.period_start::text, '')::text AS done
FROM users u
LEFT JOIN digest_done d ON d.user_id = u.id AND d.kind = sqlc.arg(kind)
ORDER BY u.id;

-- DigestMarkDone records that a person's period of a kind is done. Never
-- moves back (GREATEST): a slower run of an older period finishing after a
-- newer one must not make the newer one look undone.
-- name: DigestMarkDone :exec
INSERT INTO digest_done (user_id, kind, period_start)
VALUES (sqlc.arg(user_id), sqlc.arg(kind), sqlc.arg(period_start)::text::date)
ON CONFLICT (user_id, kind) DO UPDATE
SET period_start = GREATEST(digest_done.period_start, EXCLUDED.period_start), done_at = now();

-- ---- What a digest reads ----

-- name: DigestHistory :many
SELECT * FROM history
WHERE user_id = sqlc.arg(user_id) AND watched_at >= sqlc.arg(start) AND watched_at < sqlc.arg(period_end)
ORDER BY watched_at, id;

-- name: DigestRatings :many
SELECT media_type, tmdb_id, rating, review, rated_at FROM user_ratings
WHERE user_id = $1 ORDER BY rated_at, id;

-- The newest twelve library additions of the period, and how many in all.
-- name: DigestLibraryAdded :many
SELECT media_type, tmdb_id::int AS tmdb_id FROM library_items
WHERE date_added >= sqlc.arg(start)::timestamptz AND date_added < sqlc.arg(period_end)::timestamptz AND tmdb_id IS NOT NULL
ORDER BY date_added DESC, jellyfin_id
LIMIT 12;

-- name: DigestLibraryAddedCount :one
SELECT count(*) FROM library_items
WHERE date_added >= sqlc.arg(start)::timestamptz AND date_added < sqlc.arg(period_end)::timestamptz AND tmdb_id IS NOT NULL;

-- The person's watchlist, newest first.
-- name: DigestWatchlist :many
SELECT media_type, tmdb_id FROM watchlist
WHERE user_id = $1 AND removed_at IS NULL
ORDER BY added_at DESC, id DESC;

-- name: DigestFavoritesAdded :one
SELECT count(*) FROM favorites
WHERE user_id = sqlc.arg(user_id) AND removed_at IS NULL AND added_at >= sqlc.arg(start) AND added_at < sqlc.arg(period_end);

-- Of these titles, the ones the person has watched (a show: any episode).
-- Asked only for the few candidates for "Up next", where the Python read
-- the person's whole history and the whole library into memory.
-- name: DigestWatchedAmong :many
SELECT DISTINCT CASE WHEN media_type = 'episode' THEN 'tv' ELSE 'movie' END::text AS media, tmdb_id::int AS tmdb_id
FROM history
WHERE user_id = $1 AND tmdb_id = ANY(sqlc.arg(ids)::int[]);

-- name: DigestInLibraryAmong :many
SELECT DISTINCT media_type, tmdb_id::int AS tmdb_id FROM library_items
WHERE tmdb_id = ANY(sqlc.arg(ids)::int[]);

-- The person's latest AI rows ("Picked for you"), for "Up next".
-- name: DigestLatestAIRows :one
SELECT payload_json FROM ai_row_cache WHERE user_id = $1 ORDER BY generated_at DESC, id DESC LIMIT 1;
