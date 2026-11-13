# Movie Night for iPhone and Android

The phone app for Movie Night, built with Expo (SDK 57) and Expo Router. It
talks only to your Movie Night server's API (`/api/v1`), the same one the
website uses, so what you do in one shows up in the other.

## What's in it

- Home: the hero, Up next and your rows.
- Search: titles, people, or a described film, with the AI's best guess
  when the server has one.
- Discover, and Lists with filters, list pages and the MDBList finder.
- Title pages with scores, ratings and comments. A show's Up next has that
  episode's own heart and stars.
- Season pages with episode ticks, hearts and stars. These like or rate one
  episode, not the show.
- Play in VLC, Infuse, Outplayer or your own player, with subtitles as their
  own track, and "Finished it?" when you come back.
- A Watched tile that asks when (just now, yesterday, or a day on a
  calendar) and can undo a past watch.
- Profile: stats, Recently watched, Connect Jellyfin, players, time zone
  and signed-in devices.

An episode in Recently watched, Continue watching or Up next opens its
season page (`/season/[id]/[n]` with the `episode` parameter), scrolled to
that episode and opened. The show's name there opens the show. The app has
no History screen; History is on the website.

Every limit and timer in the app (cache, outbox, timeouts, updates) is in
[docs/limits.md](../docs/limits.md#phone-app).

## Signing in

1. The app opens `/app/connect` on your server in a sign-in sheet.
2. If you are signed out, that page sends the sheet to the server's
   `/signin` page or straight to your OIDC provider, and comes back when
   you are in.
3. You confirm. The page is valid for 10 minutes.
4. The page hands the app a one-time code, valid 5 minutes and usable once.
   The app swaps it for its own token. PKCE makes sure only the app that
   asked can swap the code.

The token is kept in the iOS Keychain or Android Keystore and sent as
`Authorization: Bearer mn_app_...`.

### Username and password

This needs `PASSWORD_SIGNIN=true` on the server. The sign-in screen asks
`GET /api/v1/auth/methods` for the address typed, after a 400 ms pause.
When `password` is true, it shows username and password fields above the
browser button. The button carries the server's `OIDC_BUTTON_LABEL` and is
left out when OIDC isn't set up. The fields post to
`POST /api/v1/auth/password` and get the same kind of token. The server's
sentence shows under the fields: a wrong pair, a 15-minute lock after five
wrong passwords, or password sign-in turned off. With no answer, or an older
server, the app shows the browser sign-in only.

iOS offers the password saved for the site when three things line up:

- the fields carry `textContentType` and `autoComplete` for username and
  password;
- `app.json` has `ios.associatedDomains: ["webcredentials:movienight.example.com"]`.
  A build for another server names that server's host there;
- the server sets `APPLE_APP_ID` to your Apple team id and this
  `bundleIdentifier`, for example `ABCDE12345.com.example.movienight`. It
  then answers `/.well-known/apple-app-site-association`. Without it there
  is no autofill.

The entitlement is native. It reaches phones only with a new build, not an
over-the-air update.

### The sign-in sheet

On iPhone the sheet is private (`preferEphemeralSession`). It shares no
cookies with Safari, so every sign-in asks for the provider login and Sign
out really signs out. Don't add `prompt=login` instead: on one Authentik
install it sent other apps into a loop of sign-in codes. On Android the
sign-in tab is Chrome's and shares its cookies. To sign in as someone else
there, sign out of the provider in Chrome first.

### Devices

Profile → Signed-in devices, in the app, lists every phone and Kodi of the
account and signs any of them out. The website's Profile → Kodi and other
players lists only the Kodis.

Signing out clears everything that phone kept of the person: the token,
the cache, changes still waiting to sync, and the Home badges. This happens
whether the phone signs out itself, another phone signs it out, or its
token runs out. The next person to sign in on it starts clean.

A device token, the phone's or a Kodi's, runs out after 90 days unused and
a year after sign-in. A device that ran out is deleted when it is next used
and whenever Signed-in devices is opened, so the list shows only devices
that still work. An admin signs out every device of one person with
`docker compose exec api admin devices signout <username>`.

## Offline, syncing and updates

- Offline: what the server said is kept on the phone for a week, so the app
  opens at once and works without a connection. A screen counts as fresh
  for 60 seconds. The app refreshes what's on screen every 60 seconds while
  it is open, when it comes to the front and when the connection comes back.
- Changes wait in an outbox until the server has them: watchlist,
  favourites, ratings, lists, watched, episode ticks, hearts and ratings,
  and "Finished it?". They are sent in order and retried after 2 seconds,
  then less often, up to once a minute. They never expire. A change the
  server refuses for good, such as a 4xx other than 401, 408 or 429, is
  dropped. A request with no answer in 20 seconds counts as offline.
- Over-the-air updates: at start the splash asks Expo for 1.5 seconds and
  downloads for up to 10, then restarts on the update. A slower one goes in
  at the next start. An update that is waiting when the app comes back
  after 5 minutes or more goes in at once, but only on a tab's own page
  with nothing in progress: no open sheet, no "Finished it?", no change
  still sending. How to publish one: see Releasing below.

## Privacy, support and accounts

The App Store listing links to `/privacy` and `/support` on the server.
Movie Night doesn't serve them: the reverse proxy serves static pages
there. There is no Delete account button. People ask the admin, who
deletes the account as described in
[Deleting someone](../docs/operations.md#deleting-someone).

## Developing

```bash
npm install
npx expo start          # then press i / a, or scan with Expo Go
npm run typecheck       # tsc --noEmit
npm run lint            # expo lint
npx expo-doctor         # dependency and config checks
npm run types           # regenerate src/lib/openapi.d.ts from ../docs/openapi.json
```

`npm run types` reads `../docs/openapi.json`. After an API change,
regenerate that from the server first ([server/README.md](../server/README.md#commands),
`api -openapi`).

Expo Go signs in through an `exp://` address. The server refuses those
unless its `.env` has `APP_DEV_REDIRECTS=true`. That is for development
only, and the PKCE check still applies.

Install packages with `npm install` or `npx expo install <package>` in this
folder only. Never symlink or copy `node_modules` from another checkout. A
nested `node_modules/node_modules` puts two copies of React and expo-router
into an over-the-air bundle.

The web build (`npx expo export --platform web`) is only for checking
screens in a browser. On the web, use the website at `/`.

## Building

Builds use EAS (`npx eas-cli@latest build`), which needs an Expo account,
and for iPhone an Apple Developer account. `ios/` and `android/` are
generated from `app.json` (Continuous Native Generation). Don't edit them
by hand.

- Android: `npx eas-cli@latest build -p android --profile preview` gives an
  APK you can install directly.
- iPhone: `npx eas-cli@latest build -p ios`, then TestFlight with
  `npx eas-cli@latest submit -p ios`.

Bundle ID and package: `com.example.movienight`. URL scheme:
`movienight://`.

## Releasing

Build with EAS as above. A JavaScript-only change can go out over the air with
`npx eas-cli@latest update --channel production`; it reaches only installed builds whose runtime
version matches (`runtimeVersion` in `app.json`).
