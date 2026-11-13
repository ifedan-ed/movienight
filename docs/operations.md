# Backups and upgrades

## Upgrading

Take a backup first (below), then:

```sh
cd movienight
git pull
docker compose up -d --build --remove-orphans
docker compose ps                  # migrate exited 0, api healthy
docker compose logs --tail=50 api worker
```

`migrate` runs before `api` and `worker` on every start and applies any new database migrations. `api` and `worker` refuse to start on a schema they weren't built for, so a failed migration stops the upgrade instead of running new code on an old database.

Rolling back: migrations go forward only. To go back to an older version after an upgrade that added migrations (new files in `server/db/migrations/`), restore the backup you took before it, then check out the older commit and `docker compose up -d --build`. Without new migrations, checking out the older commit and rebuilding is enough.

## Upgrading from the Python version

Earlier versions of Movie Night were a Python app (one `web` container). The Go server reads the same database, the same `.env` names and the same volumes, and takes over the database in place: `migrate` recognises the schema the Python version left (Alembic revision 0016, which every public release reached), records it, and only adds to it (tables for sessions, job runs and each person's sent digests, a time zone per person, and indexes).

1. Back up the database:

    ```sh
    docker compose exec -T postgres pg_dump -U movienight -Fc movienight > movienight-before-go.dump
    ```

2. Stop the Python app, so its background jobs don't run beside the new worker: `docker compose stop web`.

3. Update the code: `git pull`.

4. Check `.env`. The Go server has no placeholder defaults, so settings the Python version filled in by itself must now be in `.env`: `TIMEZONE` (the Python used `America/New_York`), `PUBLIC_URL`, `OWNER_USERNAME`, `MEILI_BASE_URL` (`http://meilisearch:7700`), the four `JELLYFIN_*` settings, and `LITELLM_BASE_URL` if you use AI (`http://litellm:4000/v1`). Add the one new setting, `TRUSTED_PROXY` (see [Self-hosting](self-hosting.md#3-put-it-behind-your-reverse-proxy)). `DATABASE_URL` and `VALKEY_URL` are set in the Compose file.

5. Start the new stack and remove the old `web` container:

    ```sh
    docker compose up -d --build --remove-orphans
    docker compose ps                  # migrate exited 0, api healthy, worker running
    ```

The Compose file now names its project `movienight`, so the volumes are `movienight_movienight-pg` and so on. If your checkout's folder has another name, your existing volumes carry that name instead: add `COMPOSE_PROJECT_NAME=<your folder name>` to `.env` before step 5, or the new stack would start with an empty database. `docker volume ls` shows which you have.

What people notice:

- Browsers sign in once more: the Python version kept sessions in signed cookies, the Go server keeps them on the server, so signing out really ends the session. Phones, Kodi and other players keep working: their tokens carry over, and so do Jellyfin links (same `SECRET_KEY`).
- Each person's dates follow their own time zone, reported by their browser at the next page load or by the phone app: history's days, "this year", Home's morning and evening rows, and the weeks and months of their digest emails. The Python version used one zone for everyone. History now merges watches per title and per day in that zone, so a rewatch on either side of local midnight shows as two entries.
- Times in the API carry the person's offset (`+01:00`) instead of UTC: the same instant.
- Votes round halves up (7.55 shows as 7.6), and "In your library, not watched yet" has a different daily shuffle.
- A few routes no client used were dropped: `GET /auth/config`, `GET /auth/oidc/config`, `POST /auth/oidc/logout`, `GET /me/digests`, `GET /ratings`, `POST /history/remove`, and the `POST` form of an episode's rating (`PUT` stays). The website, the phone app, Kodi and the Jellyfin plugin use none of them.

Going back to the Python version: the Go migrations only add, and leave Alembic's record at 0016, so the Python app runs on the migrated database as it is. Run `docker compose down`, check out the last Python commit (`git log` shows where the Go server came in), and `docker compose up -d --build --remove-orphans`. Restore the dump only if data itself went wrong. The Python version ignores the per-person time zones, and may send the latest digest again to people the Go server already sent one with other week boundaries.

## Backups

Everything people did is in the `movienight-pg` Docker volume (Postgres). The Meilisearch and Valkey data need no backup: the library sync and the weekly catalog fill Meilisearch again, and Valkey holds only caches and short queues.

Take a copy any time, and from cron every night:

```sh
docker compose exec -T postgres pg_dump -U movienight -Fc movienight > movienight-$(date +%F).dump
```

Restore one:

```sh
docker compose stop api worker
docker compose exec -T postgres pg_restore -U movienight -d movienight --clean --if-exists < movienight-YYYY-MM-DD.dump
docker compose up -d
```

Keep your `.env` too: `SECRET_KEY` decrypts the stored Jellyfin tokens.

## Logs

`docker compose logs api` and `docker compose logs worker` show one JSON line per event. Each request has an id (sent back as the `X-Request-ID` header, and `rid` on every line it wrote), so `grep <id>` finds one request's lines. The logs carry no search text, titles, names, usernames or email addresses: people appear by numeric id, query strings are cut, and email addresses are replaced. Logs rotate by size (`LOG_MAX_SIZE`, `LOG_MAX_FILES`; 5 × 10 MB per container by default).

## Admin commands

| Command | What it does |
|---|---|
| `docker compose exec api admin user list` | Lists accounts |
| `docker compose exec api admin user add NAME` | Makes an account with a password (`--name`, `--email`, `--stdin`) |
| `docker compose exec api admin user password NAME` | Sets or resets a password, and signs that person's browsers out |
| `docker compose exec api admin user nopassword NAME` | Removes a password (OIDC sign-in only) |
| `docker compose exec api admin devices signout NAME` | Signs out every phone and Kodi of one person |
| `docker compose run --rm worker -run catalog` | Runs scheduled jobs once now, by name, in order (`plays`, `library`, `imdb`, `mdblist`, `titles`, `catalog`), and exits |
| `docker compose exec postgres psql -U movienight -c "SELECT max(version) FROM schema_migrations"` | Shows which migration the database is at |

On the website, admins also get Profile → Rescan Jellyfin library and Sync MDBList lists now.

## More than one server process

`api` keeps nothing of its own: sessions are in Postgres, caches and limits in Valkey. You can run several (`docker compose up -d --scale api=2` behind a proxy that balances between them; the fixed port in the Compose file then needs changing). Several `worker`s share the work too: one holds the schedule at a time (another takes over within seconds if it stops), each scheduled run is claimed in the database so none runs twice, and the queues are shared.

## Deleting someone

There is no self-service delete button. Deleting a person's `users` row deletes everything of theirs:

```sh
docker compose exec -T postgres psql -U movienight -d movienight -c "DELETE FROM users WHERE username = 'alice'"
```

Their data remains in your backups until those expire.
