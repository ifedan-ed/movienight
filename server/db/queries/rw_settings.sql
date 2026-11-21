-- Per-person settings (user_settings: one JSON value per key, e.g. a Home
-- tab's rows, dismissed rate prompts, digest choices) and install-wide ones
-- (app_settings: the status notices, the MDBList copy, the catalog's
-- vocabulary and status, digest run markers). Package settings reads and
-- writes them; the worker's jobs use the same queries.

-- name: GetUserSetting :one
SELECT value FROM user_settings WHERE user_id = $1 AND key = $2;

-- name: SetUserSetting :exec
INSERT INTO user_settings (user_id, key, value) VALUES ($1, $2, $3)
ON CONFLICT (user_id, key) DO UPDATE SET value = excluded.value;

-- MergeUserSetting is MergeAppSetting for one person's setting.
-- name: MergeUserSetting :exec
INSERT INTO user_settings (user_id, key, value) VALUES (sqlc.arg(user_id), sqlc.arg(key), sqlc.arg(patch)::text)
ON CONFLICT (user_id, key) DO UPDATE
SET value = (CASE WHEN user_settings.value IS JSON OBJECT THEN user_settings.value::jsonb ELSE '{}'::jsonb END
             || EXCLUDED.value::jsonb)::text;

-- name: DeleteUserSetting :exec
DELETE FROM user_settings WHERE user_id = $1 AND key = $2;

-- name: GetAppSetting :one
SELECT value FROM app_settings WHERE key = $1;

-- name: SetAppSetting :exec
INSERT INTO app_settings (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = excluded.value;

-- MergeAppSetting merges a JSON object into the one kept under key (or
-- stores it), in one statement: two writers cannot lose each other's
-- fields. A value that is not a JSON object (never written so, but a hand
-- edit could) is replaced rather than failing every later write.
-- name: MergeAppSetting :one
INSERT INTO app_settings (key, value) VALUES (sqlc.arg(key), sqlc.arg(patch)::text)
ON CONFLICT (key) DO UPDATE
SET value = (CASE WHEN app_settings.value IS JSON OBJECT THEN app_settings.value::jsonb ELSE '{}'::jsonb END
             || EXCLUDED.value::jsonb)::text
RETURNING value;

-- name: DeleteAppSetting :exec
DELETE FROM app_settings WHERE key = $1;

-- AppSettingsWithPrefix reads every install-wide setting under a prefix
-- (the MDBList copy's saved lists).
-- name: AppSettingsWithPrefix :many
SELECT key, value FROM app_settings WHERE key LIKE @prefix::text || '%' ORDER BY key;

-- FailedPlaysStatuses: each linked person's last plays-read status
-- ("status:plays:<user id>"), with their name, for the admins' notices.
-- name: PlaysStatuses :many
SELECT s.value, u.id, u.username, u.display_name
FROM app_settings s JOIN users u ON s.key = 'status:plays:' || u.id::text
WHERE s.key LIKE 'status:plays:%' AND u.jellyfin_user_id IS NOT NULL
ORDER BY u.id;
