# Phone app and Kodi

## iPhone and Android

The app is in [`mobile/`](https://github.com/ifedan-ed/movienight/tree/main/mobile) (Expo, React Native). It has Home, Search, Discover, Lists, title and season pages, Play with "Finished it?", and Profile, and it keeps working offline: changes wait on the phone until your server has them.

**Signing in:** type your Movie Night address on the first screen and tap **Sign in**. Your sign-in page opens in a private sheet; after you sign in, you confirm "Sign in the app?" and you're back in the app. **Profile → Signed-in devices** lists every phone and TV and signs any of them out. A phone's sign-in ends after 90 days unused, or a year after it was made.

**Building it yourself** (an [Expo](https://expo.dev) account; for iPhone, an Apple Developer account):

```sh
cd mobile
npm install
npx eas-cli@latest build -p android --profile preview   # an APK you can install directly
npx eas-cli@latest build -p ios                           # then TestFlight / App Store
```

Set your own bundle ID in `mobile/app.json` first. Details: [mobile/README.md](https://github.com/ifedan-ed/movienight/blob/main/mobile/README.md).

## Kodi

The Movie Night add-on puts what you finish in Kodi into your history.

1. Kodi: **Settings → System → Add-ons → Unknown sources** on.
2. **Settings → File manager → Add source**: `https://<your movie night>/kodi/`, named *Movie Night*.
3. **Settings → Add-ons → Install from zip file → Movie Night** → the zip.
4. Open the add-on: it shows a code like `ABCD-2345` for 10 minutes. Type it on the website in **Profile → Kodi and other players → Connect a Kodi**.

It reports a film or episode once 85% has played (adjustable), never anything under 5 minutes, and keeps reports that can't be sent until your server is back.

## Other apps

**Profile → Kodi and other players → Other apps** makes a personal token. Anything can then report a watch:

```http
POST /api/v1/scrobble
Authorization: Bearer mn_…
Content-Type: application/json

{"media_type": "movie", "tmdb_id": 603, "source": "kodi", "ref": "<your id for this play>"}
```
