# Movie Night for iPhone and Android

The phone app for Movie Night, built with Expo (SDK 57) and Expo Router. It
talks only to your Movie Night server's API (`/api/v1`), the same one the
web app uses, so everything you do in one shows up in the other.

What's in it: Home (the hero, Up next, your rows), Search (titles, people,
or describe a movie, with the AI's best guess when the server has one),
Discover, Lists (filters, list pages, MDBList finder), title pages with
scores, ratings and comments (a show's Up next with that episode's own
heart and stars), seasons with episode ticks, hearts and stars (like or
rate one episode, not the show), Play in VLC /
Infuse / Outplayer / your own player with subtitles as their own track,
"Finished it?" when you come back, a Watched tile that asks when (just now,
yesterday, or a day picked on a calendar) and can undo a past watch, and
Profile (stats, Recently watched, Connect Jellyfin, players, signed-in
devices). An episode in Recently watched, Continue watching or Up next
opens that episode: its season page (`/season/[id]/[n]` with the
`episode` parameter), scrolled to it and opened; the show's name there
opens the show. There is no History screen in the app (History is on the
website).

Every limit and timer the app has (cache, outbox, timeouts, updates) is in
[../docs/limits.md](../docs/limits.md#phone-app).

## Signing in

The app opens `/app/connect` on your server in a sign-in sheet. That page
needs a signed-in person, so if you are signed out it sends the sheet to the
server's sign-in page (`/signin`, with password sign-in on) or straight to
your OIDC provider, and comes back to the page when you are in. You confirm (the page is valid for 10 minutes). The page
hands the app a one-time code (valid 5 minutes, usable once), and the app
swaps it for its own token (PKCE: only the app that asked can swap the code).
The token is kept in the iOS Keychain / Android Keystore and sent as
`Authorization: Bearer mn_app_...`.

**Username and password** (where the server has `PASSWORD_SIGNIN=true`): the
sign-in screen asks `GET /api/v1/auth/methods` for the address typed (after a
400 ms pause) and, when `password` is true, shows username and password
fields above the browser button (labelled with the server's
`OIDC_BUTTON_LABEL`; left out when OIDC isn't set up). They post to
`POST /api/v1/auth/password` and get the same kind of token. The server's
sentence is shown under the fields: wrong pair, locked for 15 minutes after
five wrong passwords, or password sign-in turned off. No answer, or a server
from before this, shows the browser sign-in only. The fields carry
`textContentType`/`autoComplete` username and password, and `app.json` has
`ios.associatedDomains: ["webcredentials:movienight.example.com"]`, so iOS
offers the password saved for the site: the server answers
`/.well-known/apple-app-site-association` with the app id in its
`APPLE_APP_ID` setting (your Apple team id and this `bundleIdentifier`, e.g.
`ABCDE12345.com.example.movienight`; unset, no autofill). A build for
another server names that server's host in `associatedDomains`. The
entitlement is native: it reaches phones only with a new build, not an
over-the-air update.

**The sheet is private on iPhone** (`preferEphemeralSession`, since
2026-10-08): it shares no cookies with Safari, so every sign-in asks for the
provider login and Sign out really signs out. On Android the sign-in tab is
Chrome's and shares its cookies; to sign in as someone else there, sign out of
the provider in Chrome first.

Each phone appears in Profile → Signed-in devices, in the app, which lists
every phone and Kodi of the account and signs any of them out. (The
website's Profile → Kodi and other players lists only the Kodis.) Signing
out — on the phone, from another phone, or by running out — also clears
everything that phone kept of that person: the token, the cache, anything
still queued to sync, and the Home badges, so the next person to sign in on
it starts clean. A device token (the phone's, or Kodi's) runs out after 90
days unused and a year after sign-in. A device that ran out is deleted when
it is next used and whenever Signed-in devices is opened, so the list only
shows devices that still work. An admin signs out every device of one person
with `docker compose exec api admin devices signout <username>`.

Nothing has to be let through a proxy gate any more: sign-in is the app's own
OIDC, and the phone's token calls (`/api/v1/app/token`, and `/api/v1/*` with
`Authorization: Bearer mn_app_...`) are checked by the app itself. The token
paths are listed in the main [README](../README.md#the-token-paths).

## Offline, syncing and updates

- **Offline:** what the server said is kept on the phone for a week, so the
  app opens at once and works without a connection; a screen counts as fresh
  for 60 seconds. The app refreshes what's on screen every 60 seconds while
  it is open, when it comes to the front and when the connection comes back.
- **Changes wait in an outbox** (watchlist, favourites, ratings, lists,
  watched, episode ticks, hearts and ratings, "Finished it?") until the server has them, sent
  in order, retried after 2 seconds and then less often, up to once a
  minute. They never expire. A change the server refuses for good (a 4xx
  other than 401, 408 or 429, say a deleted list) is dropped without a
  message. A request with no answer in 20 seconds counts as offline.
- **Over-the-air updates:** at start the splash asks Expo for 1.5 seconds and
  downloads for up to 10, then restarts on the update; a slower one goes in
  at the next start. An update waiting when the app comes back after 5
  minutes or more away is put in at once, but only on a tab's own page with
  nothing in progress (an open sheet, "Finished it?", a change still
  sending). How to publish one: see Releasing below.

## Privacy, support and accounts

- Privacy policy: <https://movienight.example.com/privacy>. Support:
  <https://movienight.example.com/support>. Both are static pages served by
  the host's Caddy from `/path/to/legal`, not by this repository.
- There is no Delete account button: people email **admin@example.com** to
  have their account deleted (the admin's steps are in the main
  [README](../README.md#privacy-and-accounts)).

## Developing

```bash
npm install
npx expo start          # then press i / a, or scan with Expo Go
npm run typecheck       # tsc --noEmit
npm run lint            # expo lint
npx expo-doctor         # dependency and config checks
npm run types           # regenerate src/lib/openapi.d.ts from ../docs/openapi.json
```

`npm run types` reads `../docs/openapi.json`, so regenerate that from the
server first after an API change (main [README](../README.md#api)).

Expo Go signs in through an `exp://` address. The server refuses those
unless `APP_DEV_REDIRECTS=true` is set in its `.env` (development only;
the PKCE check still applies).

Install packages with `npm install` (or `npx expo install <package>`) in this
folder only. Never symlink or copy `node_modules` from another checkout: a
nested `node_modules/node_modules` puts two copies of React and expo-router
into an over-the-air bundle (see the main README's Troubleshooting).

The web build (`npx expo export --platform web`) is only for checking
screens in a browser; the web app at `/` remains the one to use on the web.

## Building

Builds use EAS (`npx eas-cli@latest build`), which needs an Expo account,
and for iPhone an Apple Developer account. `ios/` and `android/` are
generated from `app.json` (Continuous Native Generation); don't edit them by
hand.

- Android: `npx eas-cli@latest build -p android --profile preview` gives an
  APK you can install directly.
- iPhone: `npx eas-cli@latest build -p ios` then TestFlight
  (`npx eas-cli@latest submit -p ios`).

Bundle ID / package: `com.example.movienight`. URL scheme: `movienight://`.

## Releasing

Build with EAS as above. A JavaScript-only change can go out over the air with
`npx eas-cli@latest update --channel production`; it reaches only installed builds whose runtime
version matches (`runtimeVersion` in `app.json`).
