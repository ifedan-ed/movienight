"""Just enough of Kodi's xbmc module to run the add-on's code in tests."""
LOGINFO, LOGWARNING, LOGERROR = 1, 2, 3
logged = []
rpc_replies = {}
info_labels = {}


def log(message, level=LOGINFO):
    logged.append(message)


def executeJSONRPC(request):
    import json
    method = json.loads(request)["method"]
    return json.dumps({"result": rpc_replies.get(method, {})})


def getInfoLabel(name):
    return info_labels.get(name, "")


class Player:
    playing = True
    time = 0.0
    total = 0.0

    def __init__(self):
        pass

    def isPlayingVideo(self):
        return Player.playing

    def getTime(self):
        return Player.time

    def getTotalTime(self):
        return Player.total


class Monitor:
    def abortRequested(self):
        return False

    def waitForAbort(self, seconds=0):
        return False
