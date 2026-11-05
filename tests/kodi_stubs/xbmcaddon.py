settings = {"server": "https://mn.test", "token": "mn_kodi_x", "threshold": "85", "notify": "true", "user": ""}


class Addon:
    def __init__(self, addon_id=None):
        pass

    def getSetting(self, name):
        return settings.get(name, "")

    def getSettingBool(self, name):
        return settings.get(name) == "true"

    def setSetting(self, name, value):
        settings[name] = value

    def getAddonInfo(self, name):
        return "icon.png"
