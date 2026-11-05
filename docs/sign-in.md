# Sign-in

The browser can sign in with a **username and password**, with an **OIDC provider**, or with either — you choose in `.env`. The phone app signs in through the same page; Kodi, the Jellyfin plugin and other players use their own tokens.

## Username and password

```dotenv
PASSWORD_SIGNIN=true
PASSWORD_MIN_LENGTH=10   # optional
```

There is no sign-up page: you make the accounts on the server.

```sh
docker compose exec web python -m app.cli user add alice --name "Alice" --email alice@example.com
docker compose exec web python -m app.cli user password alice     # set or reset a password
docker compose exec web python -m app.cli user nopassword alice   # remove it (OIDC only)
docker compose exec web python -m app.cli user list
```

Passwords are asked for without echo, or read from standard input with `--stdin`. They are stored as scrypt hashes.

- People change their own password at **Profile → Account** (`/account`), where they can also sign out.
- Setting, changing or resetting a password signs that person's other browsers out. Phones and TVs keep their device tokens; sign them out under **Profile → Signed-in devices**.
- After five wrong passwords, a username is refused for 15 minutes.
- Admins are the `OWNER_USERNAME` and the usernames in `ADMIN_USERS`.

## OIDC (Authentik, Keycloak, Pocket ID, …)

1. On your provider, make an **OAuth2/OpenID** client (confidential) with this redirect URI:

    ```text
    {PUBLIC_URL}/api/v1/auth/oidc/callback
    ```

2. In `.env`:

    ```dotenv
    OIDC_ISSUER=https://sso.example.com/application/o/movienight/
    OIDC_CLIENT_ID=...
    OIDC_CLIENT_SECRET=...
    OIDC_BUTTON_LABEL=Sign in with SSO
    ```

3. `docker compose up -d`.

People are matched by the provider's stable subject (`sub`), so a changed email keeps the same account. The username comes from `preferred_username` (or `OIDC_USERNAME_CLAIM`). To limit who may use Movie Night, do it at your provider (for example, bind the application to a group).

!!! tip "Authentik"
    Use a dedicated **OAuth2/OpenID Provider** for Movie Night, not a proxy/forward-auth provider: Authentik rewrites a forward-auth provider's redirect URIs.

## Both

Set `PASSWORD_SIGNIN=true` **and** the `OIDC_*` settings. The sign-in page shows the username and password form with your OIDC button under it. A person who signed in with OIDC can also set a password under **Profile → Account**.

## Sessions

A browser stays signed in for `OIDC_SESSION_DAYS` (30) days. Phone and TV sign-ins stop after 90 days unused, or a year after they were made (`DEVICE_IDLE_DAYS`, `DEVICE_MAX_DAYS`).
