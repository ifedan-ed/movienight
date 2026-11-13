-- People (the users table, as Alembic made it). created_at has no database
-- default (the Python model filled it in), so every insert sets it.

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = $1;

-- name: GetUserByOIDCSub :one
SELECT * FROM users WHERE oidc_sub = $1;

-- name: GetUserByScrobbleTokenHash :one
SELECT * FROM users WHERE scrobble_token_hash = $1;

-- CreateUser makes a person on their first visit. ON CONFLICT DO NOTHING:
-- two first requests at once must not fail; the loser reads the winner's row.
-- name: CreateUser :one
INSERT INTO users (username, email, display_name, is_admin, oidc_sub, created_at, last_seen_at)
VALUES ($1, $2, $3, $4, $5, now(), now())
ON CONFLICT DO NOTHING
RETURNING *;

-- UpdateUserSeen writes what changed on a visit: the name and email the
-- sign-in reported, admin rights (they follow ADMIN_USERS/OWNER_USERNAME both
-- ways) and last_seen_at. Called only when something did change, so an active
-- person does not cost a write per request.
-- name: UpdateUserSeen :one
UPDATE users
SET email = $2, display_name = $3, is_admin = $4, last_seen_at = $5
WHERE id = $1
RETURNING *;

-- LinkOIDCSub links an existing account to an OIDC subject, only while it has
-- none (see users.Service.SignInOIDC for why an account already linked to a
-- different subject is never re-linked).
-- name: LinkOIDCSub :one
UPDATE users
SET oidc_sub = $2,
    email = COALESCE(email, $3),
    display_name = COALESCE(display_name, $4),
    last_seen_at = now()
WHERE id = $1 AND oidc_sub IS NULL
RETURNING *;

-- SetPassword stores a new hash (or NULL to remove the password) and raises
-- session_epoch, which signs out every browser session made before it.
-- name: SetPassword :one
UPDATE users
SET password_hash = $2, session_epoch = session_epoch + 1
WHERE id = $1
RETURNING *;

-- name: ListUsers :many
SELECT * FROM users ORDER BY username;

-- SetUserTimeZone stores the zone the person's browser or phone reported.
-- internal/users checks it against Go's zone database first; the AT TIME
-- ZONE here checks it against Postgres's own, which the history queries
-- group days with: a name only one of the two knows fails the statement
-- (invalid_parameter_value) and nothing is stored.
-- name: SetUserTimeZone :one
UPDATE users SET time_zone = sqlc.arg(time_zone)::text
WHERE id = sqlc.arg(id) AND (now() AT TIME ZONE sqlc.arg(time_zone)::text) IS NOT NULL
RETURNING *;
