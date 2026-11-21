# Limits and automatic behaviour

Every cap, allowance, timer, cache and thing Movie Night does by itself, in
one place. If something stops working "for no reason", or works differently
from what you expected, look here first.

The rule: the whole app, no usage limits. Nobody is capped in how much they
use Movie Night: no daily, hourly or per-minute counts on searches, AI calls
or anything else, and no install-wide allowances. The only exception is the
one listed next ([Limits the owner keeps](#limits-the-owner-keeps)). What
remains below is a timer or a cache, or one of the
[technical limits](#technical-limits-not-usage-caps): a security measure, a
size that keeps the app working, or a limit set by an outside service that
Movie Night can't raise. Each of Movie Night's own is a setting (an outside
service's is changed at that service, and the database's whole-number range
is the type of its columns), and each says in plain words when it is hit.

## Limits the owner keeps

The same on the website, the phone app and the API.

| Limit | Value | Where it's set | What people see |
|---|---|---|---|
| Players of your own (Profile → Players → Add a player) | at most 20 players of your own, and at most 30 players ticked (built-in and your own together) | hard-coded, `server/internal/players/players.go` (`MaxCustom`, `MaxEnabled`) | Saving a 21st is refused (422) with "You can have up to 20 players of your own."; ticking a 31st, "Up to 30 players can be ticked." (`server/internal/api/api.go`). The website and the phone show the sentence as is (the phone has no Add a player; it only ticks) |

How to read the tables:

- Where it's set says how to change it. An `ENV_VAR` goes in `.env` (then
  `docker compose up -d`). "hard-coded" means it is a value in the code
  (the file and the constant or function are given); changing it is a code
  change.
- Install-wide means shared by everyone on this Movie Night. Nothing here is
  per person unless it says so.
- Time zones: each person's dates (history's days, "this year", Home's
  morning/noon/night rows, a watch date given without a time, their digest
  periods) follow the zone their browser or phone reports, or one they
  picked in Profile → Time zone (then reports are refused until they pick
  Automatic again). `TIMEZONE` is the install's clock: the worker's
  schedule, and the zone of anyone who has neither reported nor picked one.
- Processes: the server is two programs from one image. `api` serves
  requests and never runs background work; `worker` runs the schedule and
  the queued work the API hands it. Caches, sign-in counters and queues are
  in Valkey, shared by every replica; sessions and everything people did are
  in Postgres.

## The ones most likely to surprise you

1. Nothing in the database is purged on a schedule. What is deleted, a
   deleted account included, stays in your backups until those copies
   expire (see [Data kept, and data deleted](#data-kept-and-data-deleted)).
   The logs are rotated and name nobody: see Logs there.
2. Valkey keeps nothing on disk. Restarting it forgets every cache (they
   fill again as pages are opened), the sign-in and pairing counters, the
   AI answer cache and back-off, and work still queued for the worker. What
   the queues held is caught up by the schedule (plays every 5 minutes),
   except episode ticks not yet sent to Jellyfin as played marks.

## Technical limits (not usage caps)

None of these counts how much anyone uses Movie Night. Each is there because
removing it would break the app or open it to abuse from the internet; each
says in plain words when it is hit.

| Limit | Why it stays | Where to change it | What people see when it is hit |
|---|---|---|---|
| A phone's or TV's sign-in (device token): 90 days unused, and 365 days after it signed in, whichever comes first | Security: your sign-in provider never sees these tokens, so a lost phone, or someone removed at the provider, must not keep access for good | `DEVICE_IDLE_DAYS`, `DEVICE_MAX_DAYS` in `.env` | Profile → Signed-in devices (phone) and Profile → Kodi and other players (website) show each device's date: "Signs out 12 Jan unless it is used before" or "…, a year after it signed in". When it runs out, the phone's sign-in screen says "This phone's sign-in ran out: it wasn't used for 90 days. Sign in again." (the API's 401 says the same); Kodi shows the same words on the TV and again when the add-on is opened |
| The only browser identity is Movie Night's own session cookie (`mn_session`, kept server-side in Postgres); no identity header is believed | Security: any container that can reach the app directly must not be able to claim to be someone | `server/internal/sessions`, `server/internal/auth` | A request with no valid session is signed out (401 "Not signed in"), whatever headers it sends. Phone, TV, scrobble and Jellyfin tokens are checked on their own and work from anywhere |
| A request body: 1 MB | Keeps one request from filling the server's memory; the largest real one (a batch of 1,000 items) is far below it | hard-coded, `server/internal/httpx/middleware.go` (`MaxBodyBytes`) | 413 |
| A comment (review), a list description, a player's link or the Jellyfin password typed to connect: 5,000 characters; a search or AI description: 200 | Keeps one value from growing without bound in the database and on every page that shows it | `TEXT_MAX_CHARS`, `SEARCH_MAX_CHARS` in `.env` (`server/internal/config/rw.go`) | Refused with a plain sentence: "Your comment is too long: at most 5,000 characters." / "A search is too long: at most 200 characters."; the website and the phone show it. The phone's comment and search boxes count down to it ("120 characters left") and, past it, say by how much ("35 characters too long: a comment is at most 5,000"); Save waits until the comment fits. No box cuts pasted text |
| The Jellyfin plugin's queue: 1,000 events | A buffer inside Jellyfin's own memory while Movie Night doesn't answer (it can't grow without bound there); plays are still read by Movie Night's plays sync | The plugin's settings page in Jellyfin, "Events kept while Movie Night doesn't answer": any number from 1 (a queue needs room for one; a lower number is saved as 1 and the page says so) | Each dropped event is a warning in Jellyfin's log (Dashboard → Logs) |
| The logs: 5 files of 10 MB per container, the oldest dropped first | Disk space: Docker's json-file driver would otherwise let each container's log grow until the disk is full | `LOG_MAX_SIZE`, `LOG_MAX_FILES` in `.env` (read by `docker-compose.yml`, the `x-logging` block) | Nobody is told when old lines go (it is continuous, like any rotated log). What is kept and for how long: see Data kept, Logs |
| A name a person types: 300 characters (a list's name, an MDBList list id or link, the list explorer's search, a player's name, a Jellyfin username, a Discover sort) | The same reason, for fields no normal use comes near | `NAME_MAX_CHARS` in `.env` | Refused with a plain sentence naming the field and the limit, e.g. "The name is too long: at most 300 characters." |
| What a player, the Kodi add-on or the Jellyfin plugin reports: 500 characters (a title, a series or episode name, the app and the device, the sender's own reference for a play, a provider id; a TV's or phone's device name when it signs in) | The same reason; Kodi and the plugin send what Jellyfin or Kodi hold, which is never near it | `REPORTED_MAX_CHARS` in `.env` | Refused with a plain sentence (a 422). The Jellyfin plugin logs a refusal as a warning in Jellyfin's log and doesn't retry it; the Kodi add-on says on the TV which report was refused. A reference longer than the 100 characters the history keeps is stored as its SHA-256, so it is still recorded once (`server/internal/scrobble/scrobble.go`, `refOf`) |
| Items in one call: 1,000 (watches to remove, episodes to mark, Home rows, seasons in a request, titles to fill posters for) | The same reason: one call can't carry an unbounded list | `BATCH_MAX_ITEMS` in `.env` | "Episodes: at most 1,000 at a time." The website and the phone read it from `/api/v1/me` (`limits`) and split a bigger season into several calls; the website fills posters 40 at a time (fewer if this is lower) |
| Whole numbers that are stored (a season, an episode, a position or a runtime in seconds): up to 2,147,483,647 | The type of the database's integer columns: a bigger number can't be stored. It is far past anything real (a season numbered by its year, 2023, and a day-long film are fine) | The column type, written as `maximum` on each field in `server/internal/rw/schema`; not a setting | Refused with a plain sentence naming the field |
| Formats and value ranges (not sizes) | They say what a valid value is: a rating 1–10; a date; Discover's and smart lists' filters (years, a runtime, a rating 0–10, ids, a 2–3 letter language); the image proxy's width; the fixed shapes of sign-in codes (a TV code, its poll secret, a phone's one-time code and PKCE verifier, a Quick Connect secret); an email address | The request shapes in `server/internal/rw/schema`; not settings: changing what a valid value is would be a code change | Refused with a plain sentence naming the field |
| New TV codes per address: 10 in 10 minutes (an IPv6 /64 counts as one) | Security: one caller mustn't be able to flood TV pairing. The address is the connection's own, or the last `X-Forwarded-For` entry only when the connection comes from `TRUSTED_PROXY` (`server/internal/httpx/caller.go`, `Bucket`), so nobody dodges the count by writing their own header | `PAIR_STARTS_PER_ADDRESS` in `.env` | 429 "Too many sign-ins from here; try again in a few minutes" |
| TV codes waiting at once: 200 for the whole install | Security: the same flood guard, across addresses | `PAIR_CODES_WAITING` in `.env` | 429 "Too many sign-ins are waiting; try again in a few minutes" |
| What "Picked for you" reads of a person: their 60 latest films and 60 best film ratings, their 20 latest comments (the first 300 characters of each) | A model's context window: the whole history of a heavy viewer wouldn't fit in one prompt | `AI_PROMPT_TITLES`, `AI_PROMPT_COMMENTS`, `AI_PROMPT_COMMENT_CHARS` in `.env` | Never cut without a word: the first "Picked for you" row says what was read, e.g. "From your 60 latest of 340 films, your 60 best of 210 film ratings and your 20 latest of 45 comments (3 shortened to their first 300 characters)"; a shortened comment ends with "…" in the prompt |
| The search AI's answer: 1,500 output tokens | A model's own output limit has to be set per call; a reasoning model thinks within it | `SEARCH_AI_MAX_TOKENS` in `.env` | When the model runs out before answering, "Best guess" says "The AI ran out of room before it answered (it thought for too long). Search again to ask it again; an admin can give it more room (SEARCH_AI_MAX_TOKENS)." and the log has a warning naming the setting |
| Matches one search can page through in a section: 1,000 | Meilisearch's own ceiling (`pagination.maxTotalHits`), which keeps one search from ranking and sending a whole index into memory; a search by meaning ranks every title, so the 1,000 best are already far from the words | `SEARCH_MAX_HITS` in `.env` (Movie Night sets Meilisearch's `maxTotalHits` on both indexes to it at each library sync and catalog build) | Only after about 40 "Show more"s in one section: "These are the 1,000 best matches in your library (the most one search gives); add words to narrow it down." |
| TMDB serves at most 500 pages (10,000 titles) of one list | TMDB's own limit, which Movie Night can't raise | — (at TMDB) | The search catalog reads a bigger year in date ranges that fit, so nothing is left out (`server/internal/catalog/catalog.go`, `MaxPages`); only a single day with more than 10,000 titles would count as a failed read, which admins are told of in Profile, and then no title is removed. A grid of a TMDB list (Popular, Top rated, Trending, Upcoming, a genre or a service, Discover, a smart list, "More like this") stops at page 500 and says under its last cards: "TMDB lists only the first 10,000 titles here (its 500 pages). Narrow it down (a genre, a year, a streaming service) to see others." (`server/internal/rows/rows.go`, `TMDBEnd`). Search stops well before, at page 25 (`SEARCH_MAX_PAGES`, the next row) |
| The last page of one search: 25 (`/api/v1/search?page=`) | Page N holds every match of pages 1 to N (the website and the phone show each answer in place of the one before), so it reads TMDB's search pages 1 to N for each wording (TMDB sends 20 a page): a deep page the 10-minute cache doesn't hold costs up to 2 x N TMDB calls. 25 keeps one request to at most 50, and it is where the other titles' catalog hits stop anyway (40 a page, `SEARCH_MAX_HITS` 1,000) | `SEARCH_MAX_PAGES` in `.env` (at most 500) | At page 25 "Show more" is no longer offered, and when a section had more the answer says "That's as far as one search goes (page 25); add words to narrow it down." (with the 1,000-hit note too when both apply). A higher page sent to the API is refused with a plain sentence |
| MDBList's own limit for the API key (its free plan: 1,000 requests a day; the key may be shared with other apps) | An outside service's limit that Movie Night can't raise. Movie Night has no allowance of its own on MDBList | At MDBList (a paid plan or another key), `MDBLIST_API_KEY` | Title pages keep the last known Rotten Tomatoes, Metacritic, Letterboxd and Trakt scores and say under them that MDBList refused and when the kept ones are from; lists keep their titles and the list explorer answers from Movie Night's copy, or says MDBList refused; admins see it at the top of Profile; the log has a warning. MDBList is asked again 10 minutes later by itself |

## Server

### Search and the AI's best guess

| What | Value | Where it's set | Why it exists / what happens when it's hit |
|---|---|---|---|
| When the AI is asked | 4 or more words and no title matched at 0.85 or better (1.0 exact title, 0.9 the start of one, 0.85 every word in one) | hard-coded, `server/internal/rw/search/identify.go` (`worthAsking`), `server/internal/matching` (`TitleMatch`, `Words`) | `/api/v1/search` answers `ai: true`; the website and the phone then call `/api/v1/search/ai` themselves. It is advice, not a rule: `/api/v1/search/ai` itself doesn't check, so anyone signed in, or holding a phone token, can have it asked about any text. A word is a run of letters or digits in any script (Cyrillic, Greek, Hangul, ... count like English); in scripts written without spaces (Chinese, Japanese, Thai, ...) every two characters count as a word. Off entirely unless `SEARCH_AI_MODEL` and `LITELLM_API_KEY` are both set |
| AI cap of any kind (per person, hourly, daily, install-wide) | none | — | Every description sent is asked |
| AI answer cache | a real answer 24 hours; a failed or empty one 1 minute; in Valkey (shared by every replica), per (Movies/Shows/All, normalised wording) | hard-coded, `server/internal/rw/search/identify.go` (`keepAnswer`, `retryAfter`) | The same description doesn't cost a second call. A failed or empty answer is only kept a minute, so a burst of the same search doesn't ask the model over and over, and the next search after that asks again. `/api/v1/search/ai` then answers with a `note` ("The AI didn't answer this time. Search again to ask it again." / "The AI had no guess for this description."), which the website and the phone show under "Best guess" |
| The AI call itself | at most 1,500 output tokens (`SEARCH_AI_MAX_TOKENS`), 180 s timeout, 1 attempt, at most 4 guesses | `server/internal/clients/litellm.go` (`AITimeout`), `server/internal/rw/search/identify.go` (`askModel`) | A reasoning model can think for minutes. The phone waits 190 s for it; the website waits as long as the browser lets it. A model that runs out of tokens before answering is said in plain words under "Best guess" (see Technical limits) |
| Search text length | 200 characters (the AI is sent all of it) | `SEARCH_MAX_CHARS` | A technical limit (see Technical limits). Longer is refused in plain words: "A search is too long: at most 200 characters.", which the website and the phone show where the results would be; the phone's search box also counts down to it |
| Search results | every match, best first, a page at a time: page 1 holds the best 24 library titles, 30 other titles and every person on TMDB's first page of results; each "Show more" (page 2, 3, ...) adds as many again (and TMDB's next page) | `server/internal/rw/search/search.go` (`LibraryPage`, `OthersPage`, `CatalogPage`) | Nothing is cut without a word: the answer says, per section, whether there are more (`more`), and the website and the phone then show "Show more" under it. A search by meaning ranks every title, so "Show more" goes on into weaker matches. A section that stops at `SEARCH_MAX_HITS` or at page `SEARCH_MAX_PAGES` says so (see Technical limits) |
| TMDB pages read for one search | page N reads TMDB's pages 1 to N for each wording (at most 25 each, 50 in all: `SEARCH_MAX_PAGES`), each kept 10 minutes; those not kept are asked 4 at a time (both wordings together) | hard-coded, `server/internal/rw/search/search.go` (`TMDBPagesAtOnce`, `tmdbSearchTTL`) | So one deep "Show more" can't burst calls at TMDB at once (TMDB limits how fast one key may ask) |
| MDBList search fallback | up to 8 more titles, only when fewer than 3 other titles came back and the query has 3 or more characters | `server/internal/rw/search/search.go` (`mdblist`) | Finds titles TMDB's own search misses, without spending MDBList on every search. Cached 1 h |
| Typo fixing | each search also runs a corrected wording; the word list is re-read at most hourly | `server/internal/spelling` (`NewVocab`) | The list is rebuilt with the weekly catalog |

### MDBList and outside scores

| What | Value | Where it's set | Why it exists / what happens when it's hit |
|---|---|---|---|
| Movie Night's own allowance on MDBList | none | — | Every MDBList call is made: following and syncing lists, the nightly copy, a title page whose outside scores are over a week old, the search fallback, the list explorer and previews |
| MDBList refusing (its own limit for the key) | HTTP 429, or a 4xx whose answer mentions a limit | `server/internal/clients/mdblist.go` (`MDBListRefused`) | Title pages keep the last known scores and say why under them; the list explorer, a preview and Follow answer from Movie Night's copy when it has one, else say "MDBList refused Movie Night's requests just now (its own limit for this install's key)" (503); a list's Sync says so and keeps its titles; the nightly copy stops and keeps the rest. Admins see it at the top of Profile until MDBList answers again (kept in Valkey for up to 7 days). See Technical limits |
| MDBList left alone after a failure or a refusal | 10 minutes, shared by every replica | hard-coded, `server/internal/clients/mdblist.go` (`MDBListDownFor`) | After a timeout, no connection, a 5xx, an answer that isn't JSON, or a refusal: one request every 10 minutes at most while it can't answer. Meanwhile lists and scores come from Movie Night's own copy and search goes on without the fallback |
| Titles taken from an MDBList list | all of them, 1,000 a request | `server/internal/clients/mdblist.go` (`ListItems`) | Followed lists, previews and the nightly top-list copy alike. A title MDBList lists without a TMDB id can't be shown (Movie Night knows titles by their TMDB id): those are counted and the list page and the preview, on the website and the phone, say "2 titles of this MDBList list can't be shown here: MDBList has no TMDB id for them." |
| An empty answer for a list that has titles | refused; the titles stay | `server/internal/lists/mdblist.go` | An empty list from MDBList is far likelier a hiccup than a list emptied on purpose |
| Outside scores (Rotten Tomatoes, Metacritic, Letterboxd, Trakt) | asked again after 7 days, when someone opens the title's page | hard-coded, `server/internal/rw/title/scores.go` (`ScoresFresh`) | One MDBList request each time. A stored score is never cleared, even if MDBList stops sending it. When MDBList refuses or doesn't answer, the page shows the kept scores and a plain note saying so (and from when they are) |
| IMDb rating | from IMDb's own file, loaded nightly | `IMDB_RATINGS_ENABLED` | Needs no outside service when a page opens |

### Sign-in, phones and TVs

| What | Value | Where it's set | Why it exists / what happens when it's hit |
|---|---|---|---|
| Browser sessions | kept in Postgres (`web_sessions`); the cookie holds only a random id. Good for `OIDC_SESSION_DAYS` (30) from sign-in, not extended by use | `OIDC_SESSION_DAYS`; `server/internal/sessions` | Sign out deletes the session on the server, so a copied cookie stops working too. Setting, changing or resetting a password ends that person's other browser sessions |
| Wrong passwords (`PASSWORD_SIGNIN`: the website's `/signin` and the phone's `POST /api/v1/auth/password`, one counter) | 5 in 15 minutes for one username from one address, then that username is refused to that address for the rest of the 15 minutes; the right password resets it | hard-coded, `server/internal/passwords` (`FailLimit`, `FailWindow`); kept in Valkey, shared by every replica | Stops guessing without letting a stranger lock the real person out at their own address. 429 "Too many wrong passwords for this username. Wait 15 minutes and try again.", on the page and in the app. With Valkey down no password is checked (503) |
| Phone sign-in page (`/app/connect`) | valid 10 minutes | hard-coded, `server/internal/devices/devices.go` (`FormTTL`) | After that: "This sign-in page has expired; start again from the app" |
| Phone one-time code | 5 minutes, works once, only with the app's PKCE verifier | hard-coded, `server/internal/devices/devices.go` (`CodeTTL`) | Expired codes are deleted by the worker's hourly clean-up |
| Device tokens (phone `mn_app_`, TV `mn_kodi_`) | stop working after 90 days unused or 365 days after sign-in, whichever comes first | `DEVICE_IDLE_DAYS`, `DEVICE_MAX_DAYS`; `server/internal/devices` | A security measure (see Technical limits). Each device's date is in the device list (`expires_at`, `expires_because`). An expired token answers 401 saying why ("This phone's sign-in ran out: it wasn't used for 90 days. Sign in again."), shown on the phone's sign-in screen and on the TV by the Kodi add-on. A token that was signed out answers "This device was signed out of Movie Night; sign in again" |
| "Last used" on a device, "last seen" on a person or a session | written at most every 10 minutes | hard-coded, `server/internal/devices/devices.go` (`touchEvery`), `server/internal/users/users.go` (`SeenEvery`), `server/internal/sessions` (`touchEvery`) | Saves a database write on every request |
| TV pairing code | 8 characters (ABCD-2345), valid 10 minutes; Kodi polls every 3 s; the token is handed over once, then forgotten | hard-coded, `server/internal/devices/devices.go` (`PairTTL`, `PollInterval`) | A code typed after 10 minutes: "That code isn't right or has run out" |
| Pairing starts per address | 10 per 10 minutes per address (an IPv6 /64 counts as one) | `PAIR_STARTS_PER_ADDRESS`; counted in Valkey, shared by every replica | A security measure (see Technical limits). 429 "Too many sign-ins from here" |
| Pairing codes waiting at once | 200, install-wide | `PAIR_CODES_WAITING` | A security measure (see Technical limits). 429 "Too many sign-ins are waiting" |
| Personal scrobble token (Profile, Kodi and other players, Other apps) | never expires | — | Turn it off by hand; making a new one turns the old one off |
| Admin rights | follow `OWNER_USERNAME` and `ADMIN_USERS` on every request, both ways | `server/internal/users/users.go` (`Seen`) | Someone taken off the list loses admin rights at their next request |
| Email address and display name | taken from the OIDC ID token at sign-in (`email`, `name`/`preferred_username`) and refreshed on each sign-in; once set, an empty claim doesn't clear them | `server/internal/users` | Your provider is the source of truth. A digest address set in Profile, Emails is kept separately |
| A person's time zone | the IANA name their browser or phone reports (`PUT /api/v1/me/time-zone`) or they pick in Profile (`PUT /api/v1/me/time-zone/choice`), at most 64 characters, checked against the zone database | `server/internal/users/zone.go` | Anything else is refused (422); a report while a zone is picked by hand is refused (409). Someone who has neither uses `TIMEZONE`. Profile's list (`GET /api/v1/time-zones`) is read once per process |

### Watches, "Finished it?" and progress

| What | Value | Where it's set | Why it exists / what happens when it's hit |
|---|---|---|---|
| Watch date | at most 1 day in the future, not before 1900. A date without a time means 20:00 that day in the person's own zone | hard-coded, `server/internal/rw/title/when.go`; reports from other players `server/internal/scrobble/scrobble.go` (`FutureSlack`, future check only) | 422 "watched_at is in the future" / "watched_at is too early" |
| "Finished it?": which Plays | only one started in the last 12 hours | hard-coded, `server/internal/playback/sessions.go` (`AskWithin`) | An older open Play is never asked about; it stays open until the next Play closes it |
| "Finished it?": when | not in the first 3 minutes; then only once 60% of the runtime has passed, never under 10 minutes (runtime 100 min for a film, 30 for an episode when TMDB doesn't know) | hard-coded, `server/internal/playback/sessions.go` (`Grace`, `DueShare`, `MinDue`, `defaultMinutes`) | Coming back mid-film asks nothing. Each person can switch it off: Profile, Players, "Ask if I finished it" |
| Starting another Play | closes the open one as "not finished" | hard-coded, `server/internal/playback` | Nobody is asked about a film they moved on from |
| A Jellyfin play next to a watch marked here | merged into that watch when within 36 hours; only watches marked in the last 60 days are looked at | hard-coded, `server/internal/jfsync/jfsync.go` (`ClaimWindow`, `ClaimHorizon`) | Stops one watch showing twice (marked by hand, then reported by Jellyfin) |
| Watch progress counts as finished | a stop past 90% of the runtime | hard-coded, `server/internal/scrobble/plugin.go` (`DoneShare`) | The resume point is cleared, as it is by a played or unplayed mark. Otherwise progress is kept for good |
| Play here: when the watch is recorded | a stop past 90% of the runtime, or past the start of the credits when Jellyfin has a credits segment | hard-coded, `server/internal/playback/browser.go` (`WatchedAtEnd`) | Recorded as a yes to "Finished it?" (history, the watchlist, Jellyfin's played mark), once per play. A stop earlier keeps the position. Plays here are never asked "Finished it?" |
| Play here: stream link | 6 hours; its own reports are still taken for 48 hours after | hard-coded, `server/internal/stream/ticket.go` (`TicketLife`, `ReportGrace`) | The player asks for a fresh link when one runs out mid-film and carries on from the same second |
| Play here: one write to the browser | may stall up to 2 min; the whole response has no time limit | hard-coded, `server/internal/stream/proxy.go` (`writeIdle`) | The server's 5-minute `WriteTimeout` would cut a film; a stalled browser still lets go |
| Play here: HLS playlist | read up to 8 MB to rewrite it | hard-coded, `server/internal/stream/proxy.go` (`maxPlaylist`) | A three-hour film is about 600 KB |
| Play here: connections to Jellyfin for media | at most 128, kept alive between segments, 90 s for Jellyfin to start answering | hard-coded, `server/internal/stream/proxy.go` (`NewClient`) | A pool of its own, so people watching never use up the connections pages need |
| A film marked watched in Movie Night (by hand, or yes to "Finished it?") | takes it off the watchlist | hard-coded, `server/internal/history/history.go` (`MarkWatched`) | As on Trakt. Plays that come from Jellyfin (the plays sync, the webhook, the plugin), Kodi or another player don't: the film stays on the watchlist |
| Jellyfin plays read per person | all of them (no cap), 1,000 a page | `server/internal/clients/jellyfin.go` (`playedPage`), `server/internal/jfsync` | Read whole or not at all: a page that fails or stops short adds nothing, and the next sync (5 min) reads again. A read that fails or isn't whole is kept per person (`app_settings` key `status:plays:<user id>`, `server/internal/status`): the person sees it at the top of Profile and on its Jellyfin page (with a hint to connect again on a 404), admins see it at the top of Profile, and the log has one warning line a try. The next complete read, or disconnecting or reconnecting Jellyfin, clears it |
| A Jellyfin play without a date | not added (it can't go on a timeline); counted and said | `server/internal/jfsync` | Jellyfin lists it as played with no `LastPlayedDate` (marked played without one). The person sees "2 of your Jellyfin plays have no date in Jellyfin …, so they aren't in your history here" in Profile, on the Jellyfin page and in the answer to "Get my Jellyfin plays now"; marking them watched here, with a date, adds them |
| Jellyfin library read | all movies and series (no cap), 500 a page, 120 s a page | `server/internal/clients/jellyfin.go` (`libraryPage`, `libraryWait`, `IncompleteRead`) | Complete means: every page a 200 with a JSON item list and count, the items add up to Jellyfin's count, the count didn't change between pages and no item came back twice. Pages are read oldest title first (`SortBy=DateCreated`), so a title added meanwhile doesn't shift them |
| A library read that fails, is incomplete or lists nothing | nothing changes: no title is removed, none added | `server/internal/jfsync` (`Library`, `NotRead`) | A 404, an error, an empty or non-JSON answer, a page that stops short, a list that changed while it was read, or a complete read with no titles (say the `JELLYFIN_USER_ID` user no longer sees the libraries) all leave the library and its search exactly as they were. The log gets a warning and admins see it at the top of Profile (website and phone) until the next complete sync; "Rescan Jellyfin library" answers with the same words. Only a complete read removes titles it no longer lists. If Jellyfin's library really is empty now, Movie Night keeps the old titles: delete them by hand (`DELETE FROM library_items`) |
| Seerr's media read for request status | all of Seerr's media, 500 a request, pulled by one replica at a time | `server/internal/userstate/userstate.go` (`seerrPage`) | Every title shows its request and download status |
| Episode ticks sent to Jellyfin as played marks | queued for the worker, never sent from the request; waiting marks are kept 24 hours | `server/internal/jfsync/marks.go` (`marksKeep`) | A tick answers at once; the mark reaches Jellyfin within seconds while a worker runs. With `DRY_RUN=true` no mark is sent |

### Pages, lists and what others see

| What | Value | Where it's set | Why it exists / what happens when it's hit |
|---|---|---|---|
| Home row | 24 titles | hard-coded, `server/internal/rows/rows.go` (`RowSize`) | "See all" opens the rest |
| "Continue watching" | every film and episode in progress and every show's next episode, read from Jellyfin 100 a page | `server/internal/clients/jellyfin.go` (`continuePage`) | The whole row scrolls sideways |
| "See all" and list pages | 40 titles a page, every page | hard-coded, `server/internal/rows/rows.go` (`PageSize`) | No page cap; the website and the phone scroll to the end. TMDB's own lists (Popular, Top rated, Trending, Discover, smart lists) end at TMDB's page 500, and the grid says so (see Technical limits) |
| History and ratings pages | 40 a page, every page | hard-coded, `server/internal/rw/profile/me.go` (`Page`) | No page cap |
| A title's page | every streaming service in `WATCH_REGION`, the whole cast (scrolled sideways), every director, writer, studio, network and spoken language TMDB lists; "More like this" shows TMDB's first page of recommendations with "See all" for the rest | `server/internal/rw/title/detail.go` | |
| Liked episodes (the heart on each episode of a season page) | every one (no cap), any episode, aired or not | `server/internal/episodes`, `server/internal/rw/title/episodes.go` | Per person: nobody else sees them. Shown as the filled heart on the season page, on Up next (a show's page) and in the corner of the episode's card in Recently watched (Profile) and its line in History (website): they are not in Favorites, Home rows, the year in review or digests, and liking an episode doesn't favourite its show. In Download my data (`episode_favorites`). Unliking deletes the row |
| Episode ratings (the stars next to the heart on each episode of a season page, and on Up next) | every one (no cap), any episode, aired or not; 1 to 10 (two points a star, a half star is one), like a title, but stars only, no comment | `server/internal/episodes`, `server/internal/rw/title/episodes.go` | Per person: nobody else sees them (unlike a title's rating, which everyone on the install sees). An episode's rating is not the show's, and it is not counted in Profile's "Ratings", Your ratings, the community average, the rate bar, the AI picks, the weekly digest or the month and year in review. Rating again replaces it (one row per person and episode); "Remove rating" deletes the row. Each rating, heart, removal and unlike is one database statement, so taps from two devices or the phone's outbox replaying never fail: the last to arrive wins. A 0, 11 or 4.5 is refused (422). In Download my data (`episode_ratings`) |
| Other people's ratings and comments on a title | all of them, newest first; the average counts all | `server/internal/rw/title/detail.go` | Everyone on the install sees everyone's ratings and comments, by display name |
| Requests page | 40 a page, newest first, everyone's with the requester's name, "Show more" to the last one; says how many in all, and how many can't be shown (Seerr has no TMDB id for them) | `server/internal/rw/profile` | "Yours" shows only yours |
| "Rate it" prompt | films watched in the last 21 days (21 x 24 hours), every one of them; every "Not now" is remembered | hard-coded, `server/internal/history/history.go` (`RatePromptDays`) | A film dismissed once is never offered again |
| "Top rated" rows | films need 2,000 votes, shows 500 | hard-coded, `server/internal/rows/rows.go` (`topRatedFilms`, `topRatedShows`) | So a 10/10 from a handful of people doesn't top the row |
| Minimum rating in Discover or a smart list | also needs 100 votes | hard-coded, `server/internal/lists/lists.go` | Same reason |
| "Upcoming" rows | the next 120 days only | hard-coded, `server/internal/clients/tmdb.go` (`Upcoming`) | |
| Input sizes | a comment (review), a list description and a player's link 5,000 characters (`TEXT_MAX_CHARS`); a name a person types 300 (`NAME_MAX_CHARS`); players of your own: at most 20, 30 ticked (a limit the owner keeps, see [Limits the owner keeps](#limits-the-owner-keeps)); what a player reports 500 (`REPORTED_MAX_CHARS`); 1,000 items a call (`BATCH_MAX_ITEMS`); a whole request body 1 MB | `server/internal/config/rw.go`; the request shapes in `server/internal/rw/schema` read these | Technical limits (see there). Longer is refused with a plain sentence ("Your comment is too long: at most 5,000 characters."); the website's boxes and the phone's comment box don't cut text at their own length, the phone's counts down to it. Names and titles are stored as text of any length, so nothing is cut to fit a column. The limits are written in the API description too (`/api/v1/openapi.json`) |

### Recommendations and the AI rows

| What | Value | Where it's set | Why it exists / what happens when it's hit |
|---|---|---|---|
| "Picked for you" rebuild | when 24 h old, after 3 new watches, or when `LITELLM_MODEL` changes; Home queues the rebuild for the worker and shows the old rows meanwhile ("being made" when there are none yet) | `AI_ROWS_CACHE_TTL_HOURS`, `AI_REFRESH_AFTER_WATCHES`; `server/internal/airows` (`Serve`) | Needs `LITELLM_API_KEY` and `LITELLM_MODEL`. There is no allowance on these model calls (nor on the search AI's). A good rebuild replaces the person's rows: only the newest are kept |
| A failed "Picked for you" rebuild | backs off: not tried again for 10 minutes, then 20, 40, ... at most 6 hours; a good rebuild resets it | hard-coded, `server/internal/airows/airows.go` (`RetryFirst`, `RetryMax`); kept in Valkey (`ai-rows:fail:<id>`) | Failed means the model call failed, or none of its picks was found on TMDB (TMDB down, say); nothing is stored then and the rows before stay. With no rows to show, Home (website and phone) shows "Picked for you" with a plain note saying why and when it is tried again. The log gets a warning with the next try's wait |
| What the AI is asked for | 15 rows (3 sets of 5) of up to 12 films (films only), up to `LITELLM_MAX_TOKENS` (8,000) output tokens, 180 s, one retry if the reply isn't usable JSON | `server/internal/airows/airows.go` (`Sets`, `RowsPerSet`, `MaxItemsPerRow`); `server/internal/clients/litellm.go` (`AITimeout`, `ChatJSON`) | |
| What the AI is sent | the 60 latest distinct films watched, the 60 best film ratings, the 20 latest comments (the first 300 characters of each, with "…" where one was longer) | `AI_PROMPT_TITLES`, `AI_PROMPT_COMMENTS`, `AI_PROMPT_COMMENT_CHARS`; `server/internal/airows` | Keeps the prompt within the model's context window (see Technical limits). Not cut without a word: the first "Picked for you" row says what was read. Never the person's name or email |
| Row rotation | morning 05–12, afternoon 12–18, night 18–05 in the person's own zone; each shows a different set of 5 "Because you watched" and 5 "Picked for you" rows, moving on daily | hard-coded, `server/internal/rw/home/rows.go` (`rotationIndex`) | Someone who only opens at night still sees every set within three days |
| "Because you watched" | the last 15 distinct titles watched; skips titles rated 5 or lower; anything watched in the last 24 h leads every set | hard-coded, `server/internal/rw/home/rows.go` (`seedsWanted`, `pinRecent`), `server/db/queries/lists_rows.sql` (`RecentSeeds`) | One query over the whole history, so a long binge of one show doesn't hide what was watched before it |
| "In your library, not watched yet" row | reshuffled once a day, in the person's own zone | hard-coded, `server/internal/rw/home` | A different slice each day, stable within the day |

### Caches and timeouts

Server caches live in Valkey, shared by every API replica and the worker;
only one call per key is made at a time, so a burst of the same page asks
the outside service once. A restart of the API keeps them; a restart of
Valkey empties them (it keeps nothing on disk). The compose file caps Valkey
at 256 MB and evicts only keys with a lifetime (the caches), never the
queues. `server/internal/cache`.

| What | Value | Where it's set | Why it exists / what happens when it's hit |
|---|---|---|---|
| TMDB search | 10 min | `server/internal/rw/search/search.go` (`tmdbSearchTTL`) | |
| TMDB discover, trending, upcoming, smart lists | 30 min; a copy is kept a day longer, served only when TMDB fails | `server/internal/rows/rows.go` (`discoverTTL`, `keptCopy`) | With no copy, nothing ends without a word: a "See all" grid, Discover or a smart list says where its next cards would be "TMDB didn't answer just now, so this couldn't be loaded. Try again in a moment." (a 502), and a Home row says "TMDB didn't answer just now; this row comes back when it does." under its title. A title's "More like this" and a "Because you watched" row are left out for that view; the Home banner shows the library's newest alone |
| TMDB recommendations ("More like this", "Because you watched") | 12 h | `server/internal/rows/rows.go` (`recsTTL`), `server/internal/rw/title/title.go` (`RecsTTL`) | |
| TMDB details, seasons, people, collections | 6 h | `server/internal/tmdbcache` (`TTL`), `server/internal/rw/title/title.go` (`CollectionTTL`), `server/internal/rw/search/search.go` (`personTTL`) | Title details are also kept in the database (refreshed nightly, see Background jobs) |
| Seerr's request status, all titles | 90 s, one shared map pulled by one replica at a time | `server/internal/userstate/userstate.go` (`SeerrMap`) | A request made here updates its title at once; a pull that brings no media never replaces the map |
| Jellyfin "Continue watching" / a season's episodes | 60 s / 2 min | `server/internal/rw/home/rows.go` (`continueTTL`), `server/internal/episodes/episodes.go` (`JellyfinEpisodesTTL`) | Forgotten early by what makes them stale: a tick (after the worker's marks), "Finished it?", a plays sync that added plays, plugin events; every Jellyfin cache by a library sync |
| Jellyfin critic score / server id | 24 h | `server/internal/rw/title/scores.go` (`CriticTTL`), `server/internal/playback/play.go` (`serverIDFor`) | |
| MDBList list search / top lists / a list's preview / search fallback | 1 h / 6 h / 1 h / 1 h | `server/internal/rw/lists/lists.go` (`mdbSearchTTL`, `mdbTopTTL`, `mdbListTTL`), `server/internal/rw/search/search.go` (`mdbSearchTTL`) | |
| AI best guess | 24 h; a failed or empty answer 1 minute | `server/internal/rw/search/identify.go` | See Search |
| Upstream timeouts | 25 s for a call that names no other; 10 s to connect; at most 40 connections to one service; answers read up to 64 MB | `server/internal/clients/http.go` (`DefaultTimeout`, `Upstream`, `maxBody`) | Longer where set: Jellyfin's library read 120 s a page; MDBList 10 s, 30 s for list items, 6 s for search (`server/internal/clients/mdblist.go`); the AI 180 s; Meilisearch uploads 120 s and reads 60 s (`server/internal/clients/meili.go`); SMTP and SMTP2GO 30 s (`server/internal/mail`, `Timeout`); IMDb's file 10 minutes in all (`server/internal/imdb`, `downloadTimeout`) |
| TMDB rate limit (429) | retried twice, after the wait TMDB asks for (at most 10 s) | `server/internal/clients/tmdb.go` (`maxRetryAfter`) | Then the call fails and the page shows what it has |

### Subtitles online

| What | Value | Where it's set | Why it exists / what happens when it's hit |
|---|---|---|---|
| Searches | 30 an hour per person | hard-coded, `server/internal/subtitles/service.go` (`SearchesPerHour`), counted in Valkey (`rl:subtitle-search:*`) | 429 "That's a lot of subtitle searches. Try again in an hour." Keeps one account from using up a site's allowance for everyone |
| Downloads | 20 an hour per person; picking a subtitle the file already has costs none | hard-coded, `server/internal/subtitles/service.go` (`DownloadsPerHour`), `rl:subtitle-download:*` | 429 with the same wording |
| OpenSubtitles' own quota | whatever the account allows a day (20 on a free one) | OpenSubtitles | Its results stay listed but can't be picked, and the player says when downloads come back (the time OpenSubtitles names). Kept in Valkey until then (`subs:os:quota`) |
| SubDL busy or out of downloads | a "rate_limit" or "service_busy" answer is waited out once (its Retry-After, 5 to 10 s), then said as busy; a daily limit is said as one | hard-coded, `server/internal/subtitles/subdl.go` (`subdlWait`, `subdlWaitMax`) | Only a 429 is ever called busy. The log line has the site's status and its error code |
| One source's search | 8 s | hard-coded, `server/internal/subtitles/service.go` (`sourceTimeout`) | Sources are asked at once; a slow one is left out and named in the answer |
| A search's answer | kept an hour; 2 minutes when a source failed | hard-coded, `server/internal/subtitles/service.go` (`searchTTL`, `problemTTL`) | Asking again costs the sites nothing. A pick must come within the hour (410 "Search again") |
| A download | 30 s, at most 16 MB, at most 8 MB of text once unpacked | hard-coded, `server/internal/subtitles` (`downloadTimeout`, `maxFile`, `maxText`) | A bigger one is refused rather than read; a zip is read through the limit, never trusting its own sizes |
| The file's name and hash | two 64 KB Range reads through the stream proxy, kept a day per file (`subs:file:*`) | hard-coded, `server/internal/subtitles/service.go` (`fileTTL`) | The whole file is never fetched |
| OpenSubtitles login | kept until a minute before its token expires (`subs:os:login`); a refused one is not tried again for 15 minutes | hard-coded, `server/internal/subtitles/opensubtitles.go` (`refusedFor`) | OpenSubtitles asks clients not to log in more than needed, and to stop after a refusal |
| A track's delay | at most 10 minutes either way | hard-coded, `server/internal/subtitles/service.go` (`maxDelay`) | 422 |

## Background jobs

They run in the `worker` (never in the API), at times in `TIMEZONE`
(`server/cmd/worker/jobs.go`, `server/internal/jobs`). `RUN_JOBS=false`
turns the schedule off; the queued work (webhook plays syncs, Jellyfin
marks, AI rows, library scans) still runs. A job whose feature is off (no
`MEDIA_URL`, no Jellyfin, no `MDBLIST_API_KEY`, email not set up,
`IMDB_RATINGS_ENABLED=false`, `CATALOG_ENABLED=false`) is not scheduled,
and the log says so at start.

How runs are kept honest:

- Every run is claimed in Postgres (`job_runs`) under its job and due time
  before it starts, so no due run is made twice, even with several workers
  or during a leader change. The table also records how each run ended;
  rows older than 30 days are deleted by the hourly clean-up.
- The schedule runs on one worker at a time, the holder of a Postgres
  advisory lock; a standby takes over within 5 s. The queues run on every
  worker, and each request is taken by exactly one.
- At start, a job whose latest due run was missed (the worker was down)
  makes it within the window shown under "Catch-up"; older missed runs are
  skipped, since the next run brings everything up to date anyway.

Digests are sent at most once each: a send is claimed in Postgres before
the email goes (`server/internal/digest`). A clear failure (no connection,
TLS or sign-in failed, a refusal with an error code) is tried again by the
hourly catch-up, at most `DIGEST_SEND_TRIES` (3) times per person and
period ("Send it to me now" isn't counted), then given up on for that
period with an error in the log. A send whose fate is unknown counts as
sent and is never repeated: the connection broke after the email was
handed over, or a send started and never reported back (the worker stopped
midway; a claim older than 10 minutes with no outcome is taken as that).
So a person may, rarely, miss a digest, but never gets one twice.

| Job | When | Catch-up at start | What it does |
|---|---|---|---|
| `media-scan` | every 30 min, at :02 and :32 | the missed slot | Every media library rescanned, one after another. Only new and changed files are read with ffprobe (`MEDIA_PROBE_WORKERS` at once); files gone from a folder that was read leave the library; a folder that can't be read, or is empty where files were, keeps its files. Unmatched files are matched again. Scan now (Profile, Libraries) and adding or changing a library queue one library's scan for a worker |
| `plays` | every 5 min | the missed slot | Each person linked to Jellyfin: all their plays. Catches whatever the webhook or plugin missed. Profile, Jellyfin, "Get my Jellyfin plays now" does it at once for one person. A read that fails is shown to that person and to admins in Profile until a read is complete (see Watches) |
| `library` | every 30 min | the missed slot | Updates the library table and the search index from a complete read of the whole library; removes titles Jellyfin no longer lists only after such a read; a failed, incomplete or empty read changes nothing and admins see why in Profile (see Watches). An admin can run it now: Profile, "Rescan Jellyfin library" |
| Jellyfin webhook and plugin | that one person's plays synced 5 s after the event, by a worker | — | Events arriving together are merged into one sync (`server/internal/jfsync/webhook.go`, `WebhookDelay`). Needs `JELLYFIN_WEBHOOK_SECRET` (empty: 503) |
| `imdb` | daily 02:30 | when none are loaded, or missed within 20 h | IMDb's ratings file (about 9 MB). A new file with fewer than 90% of the titles already loaded is refused and the old ratings stay (`server/internal/imdb`, `KeepShare`) |
| `mdblist` | daily 04:30 | missed within 12 h | Every followed list re-synced, then MDBList's top lists copied (every title of each). Stops when MDBList refuses (its own limit) and says so in the log; the rest waits for the next night. An admin can sync all now: Profile, "Sync MDBList lists now" |
| `titles` | daily 05:00 | missed within 12 h | Every stale title's details refreshed from TMDB, oldest first, 400 at a time, no nightly cap. A title is stale after 30 days, or after 3 days if released in the last 120 days (`server/internal/titles/titles.go`, `FreshOld`, `FreshRecent`, `RecentDays`). One TMDB can't send is left as it was and tried at the next run; the log says how many |
| `catalog` | Sunday 03:00 | when never built, or missed within 12 h | The search catalog: every TMDB title with 5+ votes (`CATALOG_ENABLED`, `CATALOG_MOVIE_MIN_VOTES`, `CATALOG_TV_MIN_VOTES`, `HIDE_ADULT_TITLES`). About 4,000 TMDB calls, 6 at a time, 8 years at a time (`server/internal/catalog/catalog.go`). A year with more titles than TMDB's 500 pages serve is read in date ranges, halved until each fits, so every title is read. Titles are removed only after a fetch with nothing failed; a page that couldn't be read, or a single day with more than 10,000 titles, counts as failed, and admins see it at the top of Profile. Also rebuilds the typo word list. Meilisearch then embeds new titles in the background (the first build takes a long while) |
| `digest-week` | Monday 09:00 | no; the hourly catch-up covers it | Everyone whose week (Monday to Sunday in their own zone) is due by then: due at 09:00 on their own Monday |
| `digest-month` | the 1st at 09:30 | the same | The month in review, due at 09:30 on the 1st in each person's zone |
| `digest-year` | 1 January 10:30 | the same | The year in review, due at 10:30 on 1 January in each person's zone |
| `digest-catch-up` | hourly at :04 | the missed slot | Everyone whose period is due in their zone and not done: people ahead of or behind `TIMEZONE`, runs missed while no worker ran, failed sends. A person whose zone changes is not sent the same period again |
| `cleanup` | hourly at :07 | the missed slot | Deletes expired browser sessions, phone codes and TV codes, and run records older than 30 days |
| "Picked for you" rebuild | not scheduled: queued by a Home view when the rows are stale | — | See Recommendations |

About the digests:

| What | Value | Where it's set | Why it exists / what happens when it's hit |
|---|---|---|---|
| Who gets digests | everyone with an email address, on by default, unless they switch it off: Profile → Emails (website and phone), or the "Turn this email off" link at the foot of every digest (`/settings/emails?off=week`, also sent as `List-Unsubscribe`), which switches that kind off at once | `server/internal/digest` (`Wants`, `UnsubscribeURL`); needs `SMTP_HOST` (or `SMTP2GO_API_KEY_FILE` with `MAIL_SENDER=smtp2go`) | The address comes from the OIDC ID token at sign-in, or one set by hand. Each digest is built once per period and kept for good with its HTML. "Coming from your watchlist" looks 21, 45 or 60 days ahead (week, month, year). The AI writes the wording when `LITELLM_*` is set, plain wording otherwise |
| Empty digests | never sent by the schedule: nothing watched, rated or favourited in the period, and (for a week or a month) nothing new in the library. A year counts only the person's own watching, rating and favourites | `server/internal/digest/data.go` (`IsEmpty`) | Not written either (no model call) nor kept. "Preview the last one" and "Send it to me now" still make one on request. Picks and "coming from your watchlist" alone don't count |
| What a digest lists | every film watched; shows, new library titles and "coming from your watchlist" up to 12, 8 and 6, then "…and N more" | `server/internal/digest/gather.go` | The year in review sums the year up instead (its best 8 films and 6 most-watched shows are rankings, not lists) |

## Website

| What | Value | Where it's set | Why it exists / what happens when it's hit |
|---|---|---|---|
| Page data kept in the tab | 60 s; any change you make clears it all | hard-coded, `web/static/js/api.js:6`, `:50` | Back and tab switches are instant. A reload starts fresh |
| "Rate it" bar | asked again at most every 5 min | hard-coded, `web/static/js/app.js:22` | |
| Genres and streaming services (Discover) | 1 h | hard-coded, `web/static/js/views/discover.js:30` | |
| Time zone list (Profile → Time zone) | 1 h | hard-coded, `web/static/js/views/timezone.js` (`ttl`) | Only the offsets can change (daylight saving) |
| Pages kept for Back | 16 | hard-coded, `web/static/js/nav.js:43` | Older ones are drawn again when you go back to them |
| Your tap wins over older server data | 2 min | hard-coded, `web/static/js/ui.js:19` | A row loaded just before a tap can't undo it |
| Endless grids | go on to the end of the list | `web/static/js/ui.js:141` (`infiniteGrid`) | No longer stop after 100 pages; one that ends at TMDB's page 500 says so under the last cards |
| Your watches on a film's page | the 8 newest, then "…and N more in Full history" | `web/static/js/views/title.js:144` | Full history has every one |
| Search while typing | waits 380 ms after the last key | hard-coded, `web/static/js/views/search.js:134` | Shows every title and person the server sends, with "Show more" under a section that has more |
| Recent searches | the last 8, in this browser's localStorage (`mn:recent`) | hard-coded, `web/static/js/views/search.js:44` | Not cleared when someone signs out. Other things kept there: the Home tab, the last player used, the Lists filter |
| AI best guess | asked when search says `ai: true`; no timeout of its own | `web/static/js/views/search.js:116` | With no guess, the "Best guess" section stays and says why: the server's note, or "The AI didn't answer this time" when the call itself failed |
| A requested title's status | checked after 5 s, then every 8 s while its page is open and visible | hard-coded, `web/static/js/actions.js:112`, `:110` | Stops once it is in the library or no longer requested |
| Requests page | refreshed every 15 s while visible | hard-coded, `web/static/js/views/requests.js:41` | |
| Jellyfin Quick Connect | checked every 2.5 s for up to 5 min | hard-coded, `web/static/js/views/jellyfin.js:98`, `:97` | Then "That code expired. Get a new one." |
| "Finished it?" | checked 1.5 s after the page loads and each time you come back to the tab | hard-coded, `web/static/js/app.js:50` | |
| Play here: reports | start, every 10 s while playing, pause, stop (also as the tab closes) | hard-coded, `web/static/js/player/player.js` (`REPORT_EVERY`) | |
| Play here: resume prompt | only for a place past 30 s and before 95% of the runtime | hard-coded, `web/static/js/player/player.js` (`RESUME_MIN`, `RESUME_MAX_SHARE`) | |
| Play here: subtitle delay and size | the delay of the file's own tracks per title, the size for every title, the volume, in this browser's localStorage (`mn:subdelay:<media>:<id>`, `mn:subsize`, `mn:volume`). A track added online keeps its delay on the server, for every device | `web/static/js/player/player.js`, `web/static/js/player/online.js` | The server's copy is saved 0.6 s after the last change |
| MDBList list preview | draws the first 120 titles, then "…and N more once it's added" | hard-coded, `web/static/js/views/mdblist.js:71` | Following keeps every title |
| Offline copy (service worker) | the app shell and static files, network first; never `/api` | `web/static/sw.js:29` | The site opens without a connection but shows no data |
| Signed out (session ran out) | the page reloads and goes to sign-in (the sign-in page, or your OIDC provider when passwords are off) | `web/static/js/api.js:33` | |

## Phone app

| What | Value | Where it's set | Why it exists / what happens when it's hit |
|---|---|---|---|
| Server answers kept on the phone | 1 week | hard-coded, `mobile/src/app/_layout.tsx:24` | The app opens at once and works offline. Wiped on sign-out |
| Answers counted as fresh | 60 s; one retry on failure | hard-coded, `mobile/src/app/_layout.tsx:31` | The time zone list (Profile → Time zone): 1 h, `mobile/src/components/TimeZoneSheet.tsx` (`staleTime`) |
| Refresh | every 60 s while open, when the app comes to the front, and when the connection returns | hard-coded, `mobile/src/app/_layout.tsx:54` | Changes waiting on the phone go first |
| A request with no answer | counts as offline after 20 s | hard-coded, `mobile/src/lib/api.ts:45` | The change waits in the outbox |
| Outbox (watchlist, ratings, ticks, episode hearts and ratings, "Finished it?", lists) | never expires; retries after 2 s, doubling up to 60 s | hard-coded, `mobile/src/lib/outbox.ts:20`, `:86` | A change the server refuses with a 4xx other than 401, 408 or 429 is dropped, and a toast says so with the server's words: "A change couldn't be saved: …" (`:74`). Cleared on sign-out, never without a word: Sign out (Profile) first sends what is waiting and, if some can't go (no connection), asks "Sign out of this phone? 3 changes … haven't reached Movie Night yet. Signing out now loses them" (`mobile/src/app/(app)/(tabs)/profile.tsx`); a sign-in that runs out (a 401) with changes waiting says on the sign-in screen how many weren't saved (`mobile/src/lib/session.tsx`, `lostNote`) |
| AI best guess | waits up to 190 s; a real answer isn't asked again for a day and stays in the phone's cache for up to a week (wiped on sign-out); a failed or empty one is asked again on the next search | hard-coded, `mobile/src/app/(app)/(tabs)/search.tsx:52`, `:53` | With no guess the section says why (the server's note) |
| Search while typing | waits 380 ms | hard-coded, `mobile/src/app/(app)/(tabs)/search.tsx:37` | |
| Opening one episode (Recently watched, Continue watching, Up next) | the season page scrolls to it 50 ms after its season has loaded; if that episode isn't laid out yet, it jumps near it by the average card height and tries again 120 ms later | hard-coded, `mobile/src/app/(app)/season/[id]/[n].tsx:72`, `:101` | Once per opening; the episode is shown opened (its summary). Nothing is retried after the second try: at worst the list stops near the episode |
| A requested title's status | checked every 8 s while its page is open | hard-coded, `mobile/src/components/TitleActions.tsx:52` | |
| Jellyfin Quick Connect | checked every 2.5 s for up to 5 min | hard-coded, `mobile/src/app/(app)/settings/jellyfin.tsx:43` | |
| Your tap wins over older server data | 2 min | hard-coded, `mobile/src/lib/titleState.ts:10` | |
| Over-the-air update at start | the splash asks for 1.5 s and downloads for up to 10 s, then restarts on it | hard-coded, `mobile/src/lib/updates.ts:8`, `:9` | A slower update goes in at the next start |
| Over-the-air update on return | after 5 or more minutes away; restarts only on a tab's own page with nothing in progress | hard-coded, `mobile/src/lib/updates.ts:33` | Otherwise at the next start |
| Sign-in sheet | private on iPhone (no cookies shared with Safari) | hard-coded, `mobile/src/lib/auth.ts:47` | Sign out really signs out; every sign-in asks at your provider. On Android the sign-in tab shares Chrome's cookies, so the next sign-in may go straight in |
| Username and password in the app | shown only when `GET /api/v1/auth/methods` says the server has `PASSWORD_SIGNIN` on; the browser sign-in is always offered | `mobile/src/lib/auth.ts` | The same lockout as the website's form (see Sign-in). iPhone offers saved passwords for your Movie Night address when the server publishes `APPLE_APP_ID` (see Configuration) |
| Device token | 90 days unused, 1 year at most | server, see Sign-in | Profile → Signed-in devices shows each device's date. When it runs out, the sign-in screen says why (`mobile/src/app/signin.tsx`) |


## Kodi add-on

| What | Value | Where it's set | Why it exists / what happens when it's hit |
|---|---|---|---|
| Counts as watched | 85% played | Kodi setting (50–100%), `kodi/service.movienight/resources/settings.xml:34` | |
| Too short to report | under 5 minutes | hard-coded, `kodi/service.movienight/resources/lib/movienight.py:18` | Trailers and extras never land in the history |
| Waiting reports | retried every 5 min; all of them kept (add-on 1.0.3; the old 500 cap is gone) | hard-coded, `kodi/service.movienight/resources/lib/service.py:17`, `kodi/service.movienight/resources/lib/movienight.py:126` | Kept in the add-on's profile folder, so a restart doesn't lose them |
| A report Movie Night refuses (a 4xx other than 401, 408, 429) | dropped, and the TV says so: "Movie Night couldn't add <title>: <why>" | hard-coded, `kodi/service.movienight/resources/lib/movienight.py:183`, `kodi/service.movienight/resources/lib/service.py:37` | Kodi's own log gets a line without the title |
| Reports kept for one account | dropped once a different account connects, and the TV says so | `kodi/service.movienight/resources/lib/movienight.py` (`flush`) | Never sent into someone else's history |
| Each call | 15 s timeout | hard-coded, `kodi/service.movienight/resources/lib/movienight.py:17` | |
| Pairing code and token | code 10 min; token 90 days unused / 1 year | server, see Sign-in | The website's Kodi page shows each Kodi's date. When it runs out (or is signed out) the TV says why in Movie Night's words, then to open the add-on and connect again; the add-on says it again when opened (`kodi/service.movienight/resources/lib/service.py`, `sign_out`) |

## Jellyfin plugin

| What | Value | Where it's set | Why it exists / what happens when it's hit |
|---|---|---|---|
| Progress sent | every 30 s per person and title (never more often than 10 s; the settings page allows up to 600) | plugin setting "Send progress every", `jellyfin-plugin/Jellyfin.Plugin.MovieNight/Configuration/PluginConfiguration.cs:16`, `jellyfin-plugin/Jellyfin.Plugin.MovieNight/PlaybackForwarder.cs:120` | Pause and stop are always sent |
| Queue | 1,000 events, the oldest dropped, each drop a warning in Jellyfin's log (plugin 1.0.2) | plugin setting "Events kept while Movie Night doesn't answer", any number from 1 (read when Jellyfin starts), `jellyfin-plugin/Jellyfin.Plugin.MovieNight/Configuration/PluginConfiguration.cs:22`, `jellyfin-plugin/Jellyfin.Plugin.MovieNight/PlaybackForwarder.cs:57` | A technical limit: a buffer in Jellyfin's own memory while Movie Night doesn't answer. Plays are never lost by it: Movie Night's plays sync reads them from Jellyfin every 5 minutes; only progress updates (where you are in a film) can go |
| A failed send | 4 attempts, 5, 20 and 80 s apart, 20 s timeout each, then dropped with a warning in Jellyfin's log; a 4xx is not retried (also a warning) | hard-coded, `jellyfin-plugin/Jellyfin.Plugin.MovieNight/PlaybackForwarder.cs:265`, `:282`, `:299` | With `JELLYFIN_WEBHOOK_SECRET` empty Movie Night answers 503, so every event is tried 4 times and dropped |
| Short videos | Jellyfin never resumes anything under its `MinResumeDurationSeconds` (5 min by default); stopping one counts as played | Jellyfin's own setting | |

## Data kept, and data deleted

| What | Value | Where it's set | Why it exists / what happens when it's hit |
|---|---|---|---|
| Kept for good | history, ratings and comments, watchlist and favourites (removed ones are marked, not deleted), own lists, digests with their HTML, `ai_row_cache` (each person's newest rows only), Play sessions, request records, unfinished watch progress, the titles and scores cache, MDBList copies, subtitles added online (`online_subtitles`, until someone removes them) | the schema, `server/db/schema.sql` and `server/db/migrations` | Nothing is purged on a schedule. Copies stay in your backups until they expire (see Backups) |
| Deleted by the app | expired browser sessions, phone codes and TV codes (the hourly clean-up), a session at sign-out, library rows Jellyfin no longer lists (only after a complete read), a person's older "Picked for you" rows (each good rebuild), finished watch progress, a liked episode's row when it is unliked, an episode's rating when it is removed, job run records after 30 days | see the rows above | |
| Lost when Valkey restarts | every server cache, the wrong-password and pairing counters, the AI answer cache, the "Picked for you" back-off, the MDBList refusal notice for admins, the OIDC sign-ins under way, the subtitle searches, picks, OpenSubtitles login and quota notice, and work queued for the worker (webhook syncs, which the 5-minute sync catches up; episode marks not yet sent to Jellyfin) | Valkey runs without persistence (the compose file) | Nothing people did is in Valkey: sessions, tokens and records are in Postgres |
| Deleting a person | no self-service button; an admin deletes the person's `users` row | Postgres | Deleting their `users` row deletes everything of theirs (every per-person table cascades). Subtitles they added online stay for the household, no longer naming anyone. Their data stays in your backups until those expire. The logs never had their name, only their numeric id |
| Logs | Docker's json-file driver, rotated by size: at most 5 files of 10 MB per container, the oldest dropped first; all of it goes when the container is replaced (each deploy) | `LOG_MAX_SIZE`, `LOG_MAX_FILES` in `.env` (read by `docker-compose.yml`, the `x-logging` block); what is written: `server/internal/logging` | JSON lines. One access line per request, with its request id (also sent back as the `X-Request-ID` header), the method, the route's pattern (`/api/v1/title/{media}/{id}`: no ids, no query string), the status, the time it took and the person's numeric id; warnings and errors with their message, internal ids (person, list, digest, history row) and status codes; and what the background jobs did, in counts. Never search text, titles watched, names, usernames or email addresses. As a safety net every string of every line has query strings, email addresses and the row values a database error quotes cut out before it is written (`Redact`), and Postgres runs with `log_error_verbosity=terse` and `log_min_error_statement=panic`. Logs are not in the database backups (Docker keeps them under `/var/lib/docker/containers`) |
| Backups | the host's job, not this repository's | your backup tool | Back up the `movienight-pg` volume (or a nightly `pg_dump`); the Meilisearch volume needs none. Whatever you keep, deleted data stays in it until those copies expire |
| On the phone | the token (Keychain / Keystore), a week of answers, the outbox | phone app | All wiped on sign-out (the poster images the app caches on disk are not); changes still waiting in the outbox are counted first and the person is told (see Phone app, Outbox) |
| In the browser | the last 8 searches and a few view choices (localStorage), the offline copy of the app | website | Not cleared on sign-out |
