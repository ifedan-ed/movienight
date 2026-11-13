-- Phones (app_codes, app_tokens with scope 'app') and TVs (pair_codes,
-- app_tokens with scope 'scrobble'). Every token and code is kept only as its
-- sha256 hex, never in the clear.

-- name: DeleteExpiredAppCodes :exec
DELETE FROM app_codes WHERE expires_at < now();

-- name: CreateAppCode :exec
INSERT INTO app_codes (code_hash, user_id, challenge, device, expires_at)
VALUES ($1, $2, $3, $4, $5);

-- TakeAppCode redeems a code in one statement: it is deleted only when it is
-- unexpired AND the PKCE challenge matches, so a wrong verifier does not burn
-- the code, and two redeems at once cannot both succeed (the Python read the
-- row, then deleted it, leaving a window for a double redeem).
-- name: TakeAppCode :one
DELETE FROM app_codes
WHERE code_hash = $1 AND challenge = $2 AND expires_at > now()
RETURNING user_id, device;

-- name: GetAppCode :one
SELECT * FROM app_codes WHERE code_hash = $1;

-- name: CreateAppToken :one
INSERT INTO app_tokens (user_id, name, token_hash, scope, created_at)
VALUES ($1, $2, $3, $4, now())
RETURNING *;

-- name: GetAppTokenByHash :one
SELECT * FROM app_tokens WHERE token_hash = $1;

-- name: TouchAppToken :exec
UPDATE app_tokens SET last_used_at = now() WHERE id = $1;

-- name: DeleteAppToken :exec
DELETE FROM app_tokens WHERE id = $1;

-- name: DeleteUserAppToken :execrows
DELETE FROM app_tokens WHERE id = $1 AND user_id = $2;

-- name: DeleteAllUserAppTokens :execrows
DELETE FROM app_tokens WHERE user_id = $1;

-- name: ListUserAppTokens :many
SELECT * FROM app_tokens WHERE user_id = $1 ORDER BY created_at DESC, id DESC;

-- DropExpiredAppTokens forgets this person's devices whose token can never
-- work again (older than max_age, or unused for idle), so the device list
-- shows only live ones. The cut-offs are computed in Go from DEVICE_MAX_DAYS
-- and DEVICE_IDLE_DAYS (devices.Service), the same rule as Expiry.
-- name: DropExpiredAppTokens :exec
DELETE FROM app_tokens
WHERE user_id = @user_id
  AND (created_at <= @created_before::timestamptz
       OR COALESCE(last_used_at, created_at) <= @used_before::timestamptz);

-- name: DeleteExpiredPairCodes :exec
DELETE FROM pair_codes WHERE expires_at < now();

-- name: CountPairCodes :one
SELECT count(*) FROM pair_codes;

-- name: CreatePairCode :exec
INSERT INTO pair_codes (code_hash, poll_hash, device, expires_at)
VALUES ($1, $2, $3, $4);

-- ConfirmPairCode claims a TV's code for a person in one statement (only an
-- unclaimed, unexpired code), so two people confirming the same code at once
-- cannot both win.
-- name: ConfirmPairCode :one
UPDATE pair_codes SET user_id = $2
WHERE code_hash = $1 AND user_id IS NULL AND expires_at > now()
RETURNING device;

-- name: GetPairCode :one
SELECT * FROM pair_codes WHERE code_hash = $1;

-- CollectPairCode hands a confirmed code over exactly once: the row goes in
-- the same statement that reads it.
-- name: CollectPairCode :one
DELETE FROM pair_codes
WHERE poll_hash = $1 AND user_id IS NOT NULL AND expires_at > now()
RETURNING user_id, device;

-- name: GetPairCodeByPoll :one
SELECT * FROM pair_codes WHERE poll_hash = $1;
