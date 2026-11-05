"""The background part: follows what Kodi plays and, once a movie or an
episode has played past the chosen point (85% by default), reports it to
Movie Night. Reports that can't be sent wait in a queue and go later."""
import datetime as dt
import json
import os
import time

import xbmc
import xbmcaddon
import xbmcgui
import xbmcvfs

from resources.lib import movienight

ADDON_ID = "service.movienight"
FLUSH_EVERY = 300  # seconds between tries to send the reports still waiting


def log(message, level=xbmc.LOGINFO):
    xbmc.log("[%s] %s" % (ADDON_ID, message), level)


def setting(name):
    # A fresh Addon each time, so changes made in the settings apply at once.
    return xbmcaddon.Addon(ADDON_ID).getSetting(name)


def notify(message, error=False):
    addon = xbmcaddon.Addon(ADDON_ID)
    if error or addon.getSettingBool("notify"):
        icon = xbmcgui.NOTIFICATION_ERROR if error else addon.getAddonInfo("icon")
        # Longer for a sentence that says why (a sign-in that ran out).
        xbmcgui.Dialog().notification("Movie Night", message, icon, 8000 if len(message) > 60 else 4000)


def dropped(report, why):
    """A waiting report that will never go (flush): say so on the TV, and in
    Kodi's log without the title."""
    log("a waiting report was not sent: %s" % why, xbmc.LOGWARNING)
    notify("Movie Night couldn't add %s: %s" % (report.get("title") or "a watch", why), error=True)


def signed_out_message(why=None):
    """Movie Night's own words for why this TV was signed out (its sign-in
    ran out after 90 days unused, or a year; or it was signed out in
    Profile), then what to do on the TV."""
    why = (why or "").strip()
    if why.endswith("Sign in again."):
        why = why[:-len("Sign in again.")].rstrip()
    elif why.endswith("; sign in again"):
        why = why[:-len("; sign in again")].rstrip()
    if why and not why.endswith((".", "!", "?")):
        why += "."
    return ("%s Open the Movie Night add-on to connect it again." % why) if why else "Open the Movie Night add-on to connect it"


def sign_out(why=None):
    """The device's token doesn't work any more (Movie Night answered 401):
    forget it and go back to the normal not-connected state, same as before
    this Kodi was ever paired, and say why, in Movie Night's words, on the
    TV and in the log; the add-on shows it again when it is opened."""
    addon = xbmcaddon.Addon(ADDON_ID)
    addon.setSetting("token", "")
    addon.setSetting("user", "")
    addon.setSetting("account", "")
    message = signed_out_message(why)
    addon.setSetting("signed_out", message)
    log("signed out of Movie Night: %s" % message, xbmc.LOGWARNING)
    notify(message, error=True)


def rpc(method, **params):
    reply = json.loads(xbmc.executeJSONRPC(json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params})))
    return reply.get("result") or {}


def now_playing():
    """Kodi's own description of the video that is playing (and its show)."""
    players = rpc("Player.GetActivePlayers")
    video = next((p for p in players if p.get("type") == "video"), None) if isinstance(players, list) else None
    if video is None:
        return None, None
    item = rpc("Player.GetItem", playerid=video["playerid"],
               properties=["title", "year", "uniqueid", "season", "episode", "tvshowid", "showtitle", "file"]).get("item") or {}
    show = None
    if item.get("type") == "episode" and (item.get("tvshowid") or -1) >= 0:
        show = rpc("VideoLibrary.GetTVShowDetails", tvshowid=item["tvshowid"], properties=["uniqueid", "title", "year"]).get("tvshowdetails")
    if item.get("type") not in ("movie", "episode"):
        # Not from Kodi's library (an add-on stream): what the player knows.
        ids = {}
        for kind in ("tmdb", "imdb", "tvdb"):
            value = xbmc.getInfoLabel("VideoPlayer.UniqueID(%s)" % kind)
            if value:
                ids[kind] = value
        imdb = xbmc.getInfoLabel("VideoPlayer.IMDBNumber")
        if imdb.startswith("tt"):
            ids.setdefault("imdb", imdb)
        item = dict(item, uniqueid=ids or item.get("uniqueid") or {},
                    title=item.get("title") or xbmc.getInfoLabel("VideoPlayer.Title"),
                    showtitle=item.get("showtitle") or xbmc.getInfoLabel("VideoPlayer.TVShowTitle"),
                    season=item.get("season") if (item.get("season") or -1) >= 0 else _num(xbmc.getInfoLabel("VideoPlayer.Season")),
                    episode=item.get("episode") if (item.get("episode") or -1) >= 0 else _num(xbmc.getInfoLabel("VideoPlayer.Episode")))
    return item, show


def _num(text):
    return int(text) if (text or "").isdigit() else None


class Watcher(xbmc.Player):
    def __init__(self, queue):
        super().__init__()
        self.queue = queue
        self.current = None

    def onAVStarted(self):
        try:
            if not self.isPlayingVideo():
                return
            item, show = now_playing()
        except Exception as exc:  # never let a bad reply stop the service
            log("couldn't read what is playing: %s" % exc, xbmc.LOGWARNING)
            return
        if item:
            self.current = {"item": item, "show": show, "pos": 0.0, "total": 0.0, "done": False,
                            "started": dt.datetime.now(dt.timezone.utc)}

    def onPlayBackStopped(self):
        self.finish()

    def onPlayBackEnded(self):
        self.finish()

    def onPlayBackError(self):
        self.current = None

    def tick(self):
        """Every few seconds while playing: note how far it got, and report
        once it passes the point."""
        c = self.current
        if c is None or c["done"] or not self.isPlayingVideo():
            return
        try:
            c["pos"] = max(c["pos"], self.getTime())
            c["total"] = self.getTotalTime() or c["total"]
        except RuntimeError:
            return
        if movienight.progress_done(c["pos"], c["total"], self.threshold()):
            self.report(c)

    def finish(self):
        c, self.current = self.current, None
        if c is not None and not c["done"] and movienight.progress_done(c["pos"], c["total"], self.threshold()):
            self.report(c)

    def threshold(self):
        try:
            return float(setting("threshold") or 85)
        except ValueError:
            return 85.0

    def report(self, c):
        c["done"] = True
        report = movienight.build_report(c["item"], c["show"], c["started"])
        if report is None:
            # A stream or home video with no id: never reported on purpose (log without its title).
            log("a video with nothing to identify it by was not reported")
            return
        outcome, detail = movienight.send(setting("server"), setting("token"), report)
        name = detail or report.get("title") or "it"
        # Kept for later under the account it belongs to (movienight.flush):
        # the username, which never changes, not the name shown.
        kept = dict(report, owner=setting("account"))
        if outcome == "sent":
            notify("Added %s to your history" % name)
        elif outcome == "signed_out":
            self.queue.add(kept)
            sign_out(detail)
        elif outcome == "retry":
            self.queue.add(kept)
            log("kept for later: %s" % detail)
            if not setting("token"):
                notify("Open the Movie Night add-on to connect it", error=True)
        else:
            # Refused for good: say so on the TV, not only in the log (which never has the title).
            log("Movie Night refused a report: %s" % detail, xbmc.LOGWARNING)
            notify("Movie Night couldn't add %s: %s" % (report.get("title") or "a watch", detail), error=True)


def run():
    profile = xbmcvfs.translatePath("special://profile/addon_data/%s/" % ADDON_ID)
    queue = movienight.Queue(os.path.join(profile, "queue.json"))
    monitor = xbmc.Monitor()
    watcher = Watcher(queue)
    log("started")
    last_flush = 0.0
    while not monitor.abortRequested():
        watcher.tick()
        if time.time() - last_flush > FLUSH_EVERY:
            last_flush = time.time()
            token = setting("token")
            if token and queue.load():
                sent = movienight.flush(setting("server"), token, queue, on_signed_out=sign_out, account=setting("account"),
                                        on_dropped=dropped)
                if sent:
                    log("sent %d waiting report(s)" % sent)
        if monitor.waitForAbort(5):
            break
    log("stopped")
