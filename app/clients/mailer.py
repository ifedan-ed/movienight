"""Outgoing email (weekly digest, month in review, year in review) over SMTP.

A send ends one of three ways, and the digests treat each differently
(app/services/digest.py, send):

- sent: the server answered 250 to the email. A failure after that (its
  answer to QUIT, the connection closing) changes nothing: it was accepted.
- MaybeSent: the connection broke (a timeout, a reset, a disconnect) after
  the email was handed over (from the DATA command on) and before the
  server answered. It may well have been accepted and delivered, so it is
  counted as sent and never sent again: at most once, never twice.
- any other exception: a clear failure, nothing was accepted (no
  connection, TLS or sign-in failed, the server refused the sender, the
  address or the email with an error code), so trying again later is safe.
"""
from __future__ import annotations

import asyncio
import logging
import smtplib
import ssl
from email.message import EmailMessage
from email.utils import make_msgid

log = logging.getLogger("movienight.mailer")

# Seconds smtplib waits for the server at each step (connect, every reply).
# docs/limits.md, "Caches and timeouts".
SMTP_TIMEOUT = 30


class MaybeSent(Exception):
    """The connection broke after the email was handed to the server and
    before it answered: it may have been accepted. Not to be retried."""


class _Handover:
    """Notes when the email itself starts going to the server: smtplib's
    send_message calls data() only after the sender and the address were
    accepted, so a failure before it is a clear one."""

    handed_over = False

    def data(self, msg):
        self.handed_over = True
        return super().data(msg)


class _SMTP(_Handover, smtplib.SMTP):
    pass


class _SMTPSSL(_Handover, smtplib.SMTP_SSL):
    pass


class Mailer:
    def __init__(self, host: str, port: int, user: str, password: str, sender: str, starttls: bool = True,
                 timeout: float = SMTP_TIMEOUT):
        self.host, self.port, self.user, self.password = host, port, user, password
        self.sender, self.starttls, self.timeout = sender, starttls, timeout

    @property
    def enabled(self) -> bool:
        return bool(self.host)

    def _send(self, msg: EmailMessage) -> None:
        context = ssl.create_default_context()
        smtp: _Handover | None = None
        # "accepted" once the server answered 250, "refused" once it answered
        # with an error code (SMTPDataError, say): either way its answer is
        # known, whatever QUIT does after (smtplib's `with` sends QUIT even
        # after a failure, and a failing QUIT replaces the first exception).
        answer = None
        try:
            # Connects here: a failure to connect leaves smtp None (clear).
            if self.port == 465:
                smtp = _SMTPSSL(self.host, self.port, context=context, timeout=self.timeout)
            else:
                smtp = _SMTP(self.host, self.port, timeout=self.timeout)
            with smtp as s:
                try:
                    if self.port != 465 and self.starttls:
                        s.starttls(context=context)
                    if self.user:
                        s.login(self.user, self.password)
                    s.send_message(msg)
                except (smtplib.SMTPResponseException, smtplib.SMTPRecipientsRefused):
                    answer = "refused"
                    raise
                answer = "accepted"
        except Exception as exc:
            if answer == "accepted":
                # Only QUIT or the close failed (smtplib raises a non-221
                # answer to QUIT from its `with`). Sent all the same.
                log.warning("The mail server accepted the email; closing the connection failed (%s)", type(exc).__name__)
                return
            # Once the email was on its way, a timeout, reset or disconnect
            # before the server answered leaves its fate unknown.
            if answer is None and smtp is not None and smtp.handed_over:
                raise MaybeSent(f"the connection broke after the email was handed over ({type(exc).__name__})") from exc
            raise

    async def send(self, to: str, subject: str, html: str, text: str, headers: dict[str, str] | None = None) -> str:
        msg = EmailMessage()
        msg["From"] = self.sender
        msg["To"] = to
        msg["Subject"] = subject
        for name, value in (headers or {}).items():
            msg[name] = value
        msg["Message-ID"] = make_msgid(domain=self.sender.rsplit("@", 1)[-1].rstrip(">") or None)
        msg.set_content(text)
        msg.add_alternative(html, subtype="html")
        await asyncio.to_thread(self._send, msg)
        return msg["Message-ID"]
