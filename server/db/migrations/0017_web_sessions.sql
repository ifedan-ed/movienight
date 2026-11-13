-- 0017: browser sessions kept on the server (the Go server's first migration).
--
-- The Python app kept the browser's sign-in in a signed Starlette cookie, so
-- "sign out" only forgot the cookie in that one browser: a copied cookie kept
-- working until it expired, and nothing on the server could end it. Each
-- browser session is now a row here. The cookie holds a random id; this table
-- keeps only its sha256 (id_hash), so a database dump cannot be replayed as a
-- cookie. Deleting a row signs that browser out at once, on every replica.
--
-- Why Postgres and not Valkey: a session must survive a Valkey restart (Valkey
-- runs without persistence here, as a cache), and every request that carries a
-- session reads the person's users row anyway, so the session check is the same
-- single query (a join), not an extra round trip.
--
-- epoch: users.session_epoch when the session was made. A password change or
-- reset raises users.session_epoch; any session made under an older number no
-- longer counts, which also covers sessions an admin's reset did not delete.
CREATE TABLE web_sessions (
    id_hash      character(64) PRIMARY KEY,
    user_id      integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    epoch        integer NOT NULL,
    created_at   timestamp with time zone NOT NULL DEFAULT now(),
    last_seen_at timestamp with time zone NOT NULL DEFAULT now(),
    expires_at   timestamp with time zone NOT NULL
);

-- Signing out every browser of one person, and listing them later.
CREATE INDEX web_sessions_user_id_idx ON web_sessions (user_id);
-- The worker's clean-up of expired rows (a later phase) walks this.
CREATE INDEX web_sessions_expires_at_idx ON web_sessions (expires_at);
