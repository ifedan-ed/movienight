NOTIFICATION_ERROR = "error"
shown = []


class Dialog:
    def notification(self, heading, message, icon=None, time=0):
        shown.append(message)

    def ok(self, heading, message):
        shown.append(message)
        return True

    def yesno(self, heading, message):
        return True
