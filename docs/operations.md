# Backups and upgrades

## Upgrading

```sh
cd movienight
git pull
docker compose up -d --build
curl -s http://127.0.0.1:8137/healthz && docker compose logs --tail=50 web
```

Database migrations run on start. Keep the app **one process** (the image runs plain `uvicorn`, no `--workers`): its scheduler and caches live in that process.

**Rolling back:** if the upgrade added migrations (new files in `migrations/versions/`), undo them while the new code is still running — `docker compose exec web alembic downgrade -1` once per new file (`alembic current` shows where the database is) — then check out the older commit and `docker compose up -d --build`. Or restore a backup.

## Backups

Everything people did is in the **`movienight-pg`** Docker volume (Postgres). The Meilisearch volume needs no backup: the library sync and the weekly catalog fill it again.

Take a copy any time, and from cron every night:

```sh
docker compose exec -T postgres pg_dump -U movienight -Fc movienight > movienight-$(date +%F).dump
```

Restore one:

```sh
docker compose stop web
docker compose exec -T postgres pg_restore -U movienight -d movienight --clean --if-exists < movienight-YYYY-MM-DD.dump
docker compose start web
```

Keep your `.env` too: `SECRET_KEY` decrypts the stored Jellyfin tokens.

## Logs

`docker compose logs web` shows one line per request with a request id; `grep <id>` finds one request's lines. The app logs no search text, titles, names, usernames or email addresses (people appear by numeric id). Logs rotate by size (`LOG_MAX_SIZE`, `LOG_MAX_FILES`; 5 × 10 MB by default).

## Admin commands

| Command | What it does |
|---|---|
| `docker compose exec web python -m app.cli user list` | Lists accounts |
| `docker compose exec web python -m app.cli user add NAME` | Makes an account with a password |
| `docker compose exec web python -m scripts.sign_out_devices NAME` | Signs out every phone and Kodi of one person |
| `docker compose exec web python -m app.services.catalog` | Rebuilds the search catalog now |
| `docker compose exec web alembic current` | Shows which migration the database is at |

On the website, admins also get **Profile → Rescan Jellyfin library** and **Sync MDBList lists now**.

## Deleting someone

There is no self-service delete button. Deleting a person's `users` row deletes everything of theirs:

```sh
docker compose exec -T postgres psql -U movienight -d movienight -c "DELETE FROM users WHERE username = 'alice'"
```

Their data remains in your backups until those expire.
