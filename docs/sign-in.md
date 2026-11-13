# Sign-in

People sign in with a username and password, with an OIDC provider, or with either: you choose in `.env`. The website and the phone app offer the same choices. Kodi, the Jellyfin plugin and other players use their own tokens.

## Username and password

```dotenv
PASSWORD_SIGNIN=true
PASSWORD_MIN_LENGTH=10   # optional
```

There is no sign-up page: you make the accounts on the server.

```sh
docker compose exec api admin user add alice --name "Alice" --email alice@example.com
docker compose exec api admin user password alice     # set or reset a password
docker compose exec api admin user nopassword alice   # remove it (OIDC only)
docker compose exec api admin user list
```

Passwords are asked for without echo, or read from standard input with `--stdin`. They are stored as scrypt hashes.

- A signed-out browser goes to `/signin`, a username and password form (with your OIDC button under it when OIDC is set up too).
- People change their own password at Profile → Account (`/account`), where they can also sign out.
- Setting, changing or resetting a password signs that person's other browsers out. Phones and TVs keep their device tokens; sign them out under Profile → Signed-in devices.
- After five wrong passwords from one address, that username is refused to that address for 15 minutes. The website and the phone app count together; the person at their own address is not locked out by someone else's guesses.
- Admins are the `OWNER_USERNAME` and the usernames in `ADMIN_USERS`.

### In the phone app

The app asks your server which sign-ins it offers (`GET /api/v1/auth/methods`, no sign-in needed). With passwords on, it shows username and password fields above the browser sign-in button; they post to `POST /api/v1/auth/password` and get the same device token as a browser sign-in (listed under Signed-in devices, running out the same way). A wrong password, the 15-minute lockout and "turned off" come back as the server's own sentence. With passwords off, the app shows only the browser sign-in.

On an iPhone the fields can offer the password saved for your site (in Passwords or a password manager). That needs two things, because Apple ties the app to the site in both directions:

- your own build of the app with `webcredentials:<your host>` in `ios.associatedDomains` (`mobile/app.json`), and
- `APPLE_APP_ID` in `.env`: your Apple team id and the app's bundle id, e.g. `ABCDE12345.com.example.movienight`.

Movie Night then serves `/.well-known/apple-app-site-association` itself (no reverse-proxy rule needed); iOS reads it when the app is installed or updated. Without `APPLE_APP_ID` that address is a 404 and sign-in works the same, without autofill.

## OIDC (Authentik, Keycloak, Pocket ID, ...)

1. On your provider, make an OAuth2/OpenID client (confidential) with this redirect URI:

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
    Use a dedicated OAuth2/OpenID Provider for Movie Night, not a proxy/forward-auth provider: Authentik rewrites a forward-auth provider's redirect URIs.

## Both

Set `PASSWORD_SIGNIN=true` and the `OIDC_*` settings. The sign-in page shows the username and password form with your OIDC button under it, and the phone app shows both. A person who signed in with OIDC can also set a password under Profile → Account.

## Sessions

Browser sessions are kept on the server, in Postgres, so signing out ends a session for good, and resetting a password ends the person's other sessions. A browser stays signed in for `OIDC_SESSION_DAYS` (30) days, whichever way it signed in. Phone and TV sign-ins stop after 90 days unused, or a year after they were made (`DEVICE_IDLE_DAYS`, `DEVICE_MAX_DAYS`).
