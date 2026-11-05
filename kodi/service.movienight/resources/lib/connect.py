"""Connecting Kodi to Movie Night: the TV shows a short code, you enter it in
Movie Night (Profile, Kodi), and the add-on collects a token that can only
report what you watch."""
import time

import xbmc
import xbmcaddon
import xbmcgui

from resources.lib import movienight

ADDON_ID = "service.movienight"


def device_name():
    name = xbmc.getInfoLabel("System.FriendlyName") or "Kodi"
    return name if "kodi" in name.lower() else "Kodi (%s)" % name


def run():
    addon = xbmcaddon.Addon(ADDON_ID)
    dialog = xbmcgui.Dialog()
    server = movienight.server_url(addon.getSetting("server"))
    why = addon.getSetting("signed_out")
    if why and not addon.getSetting("token"):
        # Why this TV was signed out, in Movie Night's words (service.sign_out).
        dialog.ok("Movie Night", why)
        addon.setSetting("signed_out", "")
    if addon.getSetting("token"):
        who = addon.getSetting("user") or "your account"
        if not dialog.yesno("Movie Night", "This Kodi is connected as %s.[CR]Connect it again (for example as someone else)?" % who):
            return
    status, start = movienight.http("POST", server + "/api/v1/scrobble/pair", {"device": device_name()})
    if status != 200 or not start.get("code"):
        dialog.ok("Movie Night", "Couldn't reach Movie Night at %s.[CR]%s" % (server, start.get("detail") or "Check the address in the add-on's settings."))
        return
    code, poll = start["code"], start["poll"]
    life = float(start.get("expires_in") or 600)
    interval = float(start.get("interval") or 3)
    message = ("On your phone or computer, open Movie Night, go to Profile, then Kodi, and enter:[CR][CR]"
               "[B][COLOR gold]%s[/COLOR][/B]" % code)
    progress = xbmcgui.DialogProgress()
    progress.create("Connect to Movie Night", message)
    monitor = xbmc.Monitor()
    deadline = time.time() + life
    outcome = "expired"
    try:
        while time.time() < deadline:
            if monitor.abortRequested() or progress.iscanceled():
                outcome = "cancelled"
                break
            progress.update(int(100 * (1 - (deadline - time.time()) / life)), message)
            status, got = movienight.http("POST", server + "/api/v1/scrobble/pair/poll", {"poll": poll})
            if status == 200 and got.get("status") == "ok" and got.get("token"):
                # Who it is before the token: the service may read the token
                # at any moment, and must not pair it with the last account.
                addon.setSetting("account", got.get("account") or "")
                addon.setSetting("user", got.get("user") or "")
                addon.setSetting("token", got["token"])
                outcome = "connected"
                break
            if status == 410:
                break
            if monitor.waitForAbort(interval):
                outcome = "cancelled"
                break
    finally:
        progress.close()
    if outcome == "connected":
        dialog.notification("Movie Night", "Connected as %s" % (addon.getSetting("user") or "you"), addon.getAddonInfo("icon"), 4000)
    elif outcome == "expired":
        dialog.ok("Movie Night", "That code ran out before it was entered. Open the add-on again for a new one.")
