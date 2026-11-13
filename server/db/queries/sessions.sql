-- Browser sessions (migration 0017). id_hash is the sha256 of the cookie.

-- name: CreateWebSession :exec
INSERT INTO web_sessions (id_hash, user_id, epoch, created_at, last_seen_at, expires_at)
VALUES ($1, $2, $3, now(), now(), $4);

-- SessionUser is the one query a signed-in browser request costs: the session
-- (unexpired) and its person. The epoch is compared in Go so an outdated
-- session can be deleted and reported as signed out.
-- name: SessionUser :one
SELECT sqlc.embed(web_sessions), sqlc.embed(users)
FROM web_sessions
JOIN users ON users.id = web_sessions.user_id
WHERE web_sessions.id_hash = $1 AND web_sessions.expires_at > now();

-- name: TouchWebSession :exec
UPDATE web_sessions SET last_seen_at = now() WHERE id_hash = $1;

-- name: SetWebSessionEpoch :exec
UPDATE web_sessions SET epoch = $2 WHERE id_hash = $1;

-- name: DeleteWebSession :exec
DELETE FROM web_sessions WHERE id_hash = $1;

-- DeleteOtherWebSessions signs every other browser of a person out (after a
-- password change, the browser that changed it stays signed in).
-- name: DeleteOtherWebSessions :exec
DELETE FROM web_sessions WHERE user_id = $1 AND id_hash <> $2;

-- name: DeleteExpiredWebSessions :execrows
DELETE FROM web_sessions WHERE expires_at <= now();

-- DeleteUserWebSessions signs every browser of a person out (an admin's
-- password reset; the epoch rise alone would also end them, this also
-- removes the rows).
-- name: DeleteUserWebSessions :execrows
DELETE FROM web_sessions WHERE user_id = $1;
