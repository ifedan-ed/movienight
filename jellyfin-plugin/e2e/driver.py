"""Drive a throwaway Jellyfin 12.1 with the Movie Night plugin installed:
first-run setup, two libraries, plugin settings, then real playback through
Jellyfin's own API (start, progress, pause, stop, mark played/unplayed),
checking Movie Night's database after each step."""
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

JF = os.environ.get("JF_URL", "http://127.0.0.1:18096")
PLUGIN = "5bf59830f9944e338e310b4570f490b2"
HOOK, PG, PW, MN = os.environ["HOOK_SECRET"], os.environ["PG_CONTAINER"], os.environ["JF_PASSWORD"], os.environ["MN_URL"]
AUTH = 'MediaBrowser Client="PluginTest", Device="driver", DeviceId="plugintest-1", Version="1.0"'
token = None
failures = 0


def call(method, path, body=None, auth=True, ok=(200, 204)):
    headers = {"Content-Type": "application/json", "Authorization": AUTH + (f', Token="{token}"' if auth and token else "")}
    req = urllib.request.Request(JF + path, method=method, headers=headers,
                                 data=json.dumps(body).encode() if body is not None else None)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            raw = r.read()
            return json.loads(raw) if raw and raw[:1] in b"[{" else None
    except urllib.error.HTTPError as e:
        raise RuntimeError(f"{method} {path}: HTTP {e.code} {e.read()[:200]!r}") from None


def sql(q):
    return subprocess.check_output(["docker", "exec", PG, "psql", "-U", "movienight", "-d", "movienight", "-At", "-F", "|", "-c", q],
                                   text=True).strip()


def check(ok, what):
    global failures
    print(("  ok   " if ok else "  FAIL ") + what)
    failures += 0 if ok else 1


def wait_for(fn, what, timeout=40):
    end = time.time() + timeout
    while time.time() < end:
        v = fn()
        if v:
            return v
        time.sleep(1)
    check(False, f"{what} (timed out)")
    return None


# -- first run --------------------------------------------------------------------
# Jellyfin answers with its HTML start-up page before the API is ready: wait
# for the JSON with a version in it.
info = None
for _ in range(120):
    try:
        info = call("GET", "/System/Info/Public", auth=False)
    except Exception:  # noqa: BLE001
        info = None
    if info and info.get("Version"):
        break
    time.sleep(2)
if not (info and info.get("Version")):
    sys.exit("Jellyfin never came up")
print("Jellyfin", info.get("Version"))
call("POST", "/Startup/Configuration", {"UICulture": "en-US", "MetadataCountryCode": "US", "PreferredMetadataLanguage": "en"}, auth=False)
call("GET", "/Startup/User", auth=False)
call("POST", "/Startup/User", {"Name": "tester", "Password": PW}, auth=False)
call("POST", "/Startup/RemoteAccess", {"EnableRemoteAccess": True, "EnableAutomaticPortMapping": False}, auth=False)
call("POST", "/Startup/Complete", auth=False)
login = call("POST", "/Users/AuthenticateByName", {"Username": "tester", "Pw": PW}, auth=False)
token, uid = login["AccessToken"], login["User"]["Id"].replace("-", "")
plugins = call("GET", "/Plugins")
mine = next((p for p in plugins if p["Id"].replace("-", "") == PLUGIN), None)
check(mine is not None and mine.get("Status") == "Active", f"plugin loaded: {mine and (mine['Name'], mine['Version'], mine['Status'])}")
call("POST", f"/Plugins/{PLUGIN}/Configuration", {"MovieNightUrl": MN, "Token": HOOK, "ProgressSeconds": 10})
# The test videos are a minute long, and Jellyfin never resumes anything
# shorter than MinResumeDurationSeconds (5 min by default): stopping one
# counts as played. A second lets "stopped halfway" be tested.
conf = call("GET", "/System/Configuration")
conf.update({"MinResumeDurationSeconds": 1, "MinResumePct": 5, "MaxResumePct": 90})
call("POST", "/System/Configuration", conf)
check(call("GET", f"/Plugins/{PLUGIN}/Configuration")["MovieNightUrl"] == MN, "plugin settings saved")

for name, kind, path in (("Movies", "movies", "/media/movies"), ("Shows", "tvshows", "/media/tv")):
    call("POST", f"/Library/VirtualFolders?name={name}&collectionType={kind}&refreshLibrary=false",
         {"LibraryOptions": {"PathInfos": [{"Path": path}], "EnableRealtimeMonitor": False}})
call("POST", "/Library/Refresh")


def items():
    # Both videos identified: the film with its ids, the episode with its
    # season, number and a series that has its TMDB id.
    got = call("GET", f"/Items?userId={uid}&Recursive=true&IncludeItemTypes=Movie,Episode&Fields=ProviderIds")["Items"]
    film = next((i for i in got if i["Type"] == "Movie" and i.get("ProviderIds", {}).get("Tmdb")), None)
    ep = next((i for i in got if i["Type"] == "Episode" and i.get("ParentIndexNumber") and i.get("IndexNumber") and i.get("SeriesId")), None)
    if not film or not ep:
        return None
    series = call("GET", f"/Items/{ep['SeriesId']}?userId={uid}&Fields=ProviderIds")
    return got if (series or {}).get("ProviderIds", {}).get("Tmdb") else None


found = wait_for(items, "both test videos in the library", timeout=120) or []
movie = next(i for i in found if i["Type"] == "Movie")
episode = next(i for i in found if i["Type"] == "Episode")
print("movie", movie["Name"], movie.get("ProviderIds"), "| episode", episode.get("SeriesName"), episode.get("ParentIndexNumber"), episode.get("IndexNumber"))

# Movie Night's person for this Jellyfin account.
sql(f"insert into users (username, is_admin, created_at, jellyfin_user_id) values ('plugintest', false, now(), '{uid}') on conflict do nothing")


def progress_row(key):
    return sql(f"select state, position_s, coalesce(client,'') from watch_progress where key = '{key}'")


# -- the film: start, progress, pause, then a stop near the end ------------------
ps = {"ItemId": movie["Id"], "PlaySessionId": "film-1", "CanSeek": True, "PlayMethod": "DirectPlay"}
call("POST", "/Sessions/Playing", {**ps, "PositionTicks": 0})
wait_for(lambda: progress_row("movie:603"), "start reached Movie Night")
call("POST", "/Sessions/Playing/Progress", {**ps, "PositionTicks": 25 * 10**7, "IsPaused": False, "EventName": "TimeUpdate"})
time.sleep(11)  # the plugin sends progress at most every 10 s here
call("POST", "/Sessions/Playing/Progress", {**ps, "PositionTicks": 30 * 10**7, "IsPaused": False, "EventName": "TimeUpdate"})
# Jellyfin estimates the position itself between reports, so the film is
# at 30 s or a little past it.
row = wait_for(lambda: (r := progress_row("movie:603")) and r.startswith("playing") and int(r.split("|")[1]) >= 30 and r, "progress past 30 s")
check(bool(row) and row.startswith("playing|") and row.endswith("|PluginTest"), f"film progress kept: {row}")
call("POST", "/Sessions/Playing/Progress", {**ps, "PositionTicks": 31 * 10**7, "IsPaused": True, "EventName": "Pause"})
row = wait_for(lambda: (r := progress_row("movie:603")) and r.startswith("paused") and r, "pause")
check(bool(row), f"pause kept: {row}")
call("POST", "/Sessions/Playing/Stopped", {**ps, "PositionTicks": 59 * 10**7})
watched = wait_for(lambda: sql("select count(*) from history where source = 'jellyfin' and tmdb_id = 603 and media_type = 'movie'") == "1",
                   "the finished film in Movie Night's history")
check(bool(watched), "finished film recorded once in history")
check(progress_row("movie:603") == "", "film progress cleared once finished")

# -- the episode: stopped halfway, then marked played and unplayed by hand ----------
pe = {"ItemId": episode["Id"], "PlaySessionId": "ep-1", "CanSeek": True, "PlayMethod": "DirectPlay"}
call("POST", "/Sessions/Playing", {**pe, "PositionTicks": 0})
call("POST", "/Sessions/Playing/Stopped", {**pe, "PositionTicks": 30 * 10**7})
row = wait_for(lambda: (r := progress_row("tv:1399:1:5")) and r.startswith("stopped") and r, "stopped halfway")
check(bool(row) and 25 <= int(row.split("|")[1]) <= 40, f"episode left halfway: {row}")
check(sql("select count(*) from history where tmdb_id = 1399 and episode = 5") == "0", "an episode stopped halfway is not a watch")
call("POST", f"/UserPlayedItems/{episode['Id']}?userId={uid}")
ok = wait_for(lambda: sql("select count(*) from history where source = 'jellyfin' and tmdb_id = 1399 and season = 1 and episode = 5") == "1",
              "episode marked played")
check(bool(ok), "episode marked played in Jellyfin -> one watch in Movie Night")
check(progress_row("tv:1399:1:5") == "", "episode progress cleared when marked played")
call("DELETE", f"/UserPlayedItems/{episode['Id']}?userId={uid}")
time.sleep(3)
check(sql("select count(*) from history where tmdb_id = 1399 and episode = 5") == "1", "marking unplayed keeps the history")

print("FAILURES:", failures)
sys.exit(1 if failures else 0)
