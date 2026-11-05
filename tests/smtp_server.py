"""A tiny SMTP server on loopback for the mailer's tests: it speaks just
enough SMTP for smtplib, keeps every email it was handed in full, and can
misbehave the ways a real one does (refuse, hang up, go quiet, answer
QUIT badly)."""
from __future__ import annotations

import socket
import threading


class SMTPServer:
    """mode: "ok"; "refuse_rcpt" (550 to the address); "refuse_data" (554
    after the email); "drop_after_data" (hangs up once it has the email,
    before answering); "quiet_after_data" (has the email, never answers);
    "bad_quit" (250 to the email, then 421 to QUIT)."""

    def __init__(self, mode: str = "ok"):
        self.mode = mode
        self.received: list[bytes] = []  # every email handed over in full
        self._sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self._sock.bind(("127.0.0.1", 0))
        self._sock.listen(8)
        self.port = self._sock.getsockname()[1]
        self._thread = threading.Thread(target=self._serve, daemon=True)
        self._thread.start()

    def close(self) -> None:
        self._sock.close()

    def _serve(self) -> None:
        while True:
            try:
                conn, _ = self._sock.accept()
            except OSError:
                return
            with conn:
                try:
                    self._talk(conn)
                except OSError:
                    pass

    def _talk(self, conn: socket.socket) -> None:
        f = conn.makefile("rb")

        def say(*lines: str) -> None:
            conn.sendall(b"".join(line.encode() + b"\r\n" for line in lines))

        say("220 test ESMTP")
        while True:
            line = f.readline()
            if not line:
                return
            cmd = line.strip().upper()
            if cmd.startswith(b"EHLO"):
                say("250-test", "250 8BITMIME")
            elif cmd.startswith(b"HELO") or cmd.startswith(b"MAIL") or cmd in (b"RSET", b"NOOP"):
                say("250 OK")
            elif cmd.startswith(b"RCPT"):
                say("550 No such person here" if self.mode == "refuse_rcpt" else "250 OK")
            elif cmd == b"DATA":
                say("354 Go ahead")
                body = b""
                while (got := f.readline()) not in (b".\r\n", b""):
                    body += got
                self.received.append(body)
                if self.mode == "drop_after_data":
                    return
                if self.mode == "quiet_after_data":
                    while f.readline():  # until the client gives up and closes
                        pass
                    return
                say("554 Rejected as spam" if self.mode == "refuse_data" else "250 Queued")
            elif cmd == b"QUIT":
                say("421 Going away" if self.mode == "bad_quit" else "221 Bye")
                return
            else:
                say("502 Not here")
