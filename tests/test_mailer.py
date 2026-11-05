"""The mailer tells three endings apart: sent, a clear failure (safe to try
again) and a send whose fate is unknown because the connection broke after
the email was handed over (MaybeSent: counted as sent, never repeated).
Against a real SMTP conversation on loopback (tests/smtp_server.py)."""
import socket
import smtplib

import pytest

from app.clients.mailer import Mailer, MaybeSent
from tests.smtp_server import SMTPServer


def _mailer(port: int) -> Mailer:
    return Mailer("127.0.0.1", port, "", "", "Movie Night <mn@example.org>", starttls=False, timeout=1)


async def _send(mailer: Mailer) -> str:
    return await mailer.send("alice@example.org", "Your week", "<p>Hi</p>", "Hi")


@pytest.fixture
def smtp():
    servers = []

    def start(mode="ok"):
        servers.append(SMTPServer(mode))
        return servers[-1]

    yield start
    for s in servers:
        s.close()


async def test_a_plain_send(smtp):
    server = smtp()
    assert (await _send(_mailer(server.port))).startswith("<")
    assert len(server.received) == 1 and b"alice@example.org" in server.received[0]


@pytest.mark.parametrize("mode", ["drop_after_data", "quiet_after_data"])
async def test_a_connection_lost_after_the_email_was_handed_over_may_have_sent_it(smtp, mode):
    # Before: smtplib's SMTPServerDisconnected came out as a failure, and the
    # digest was sent again at every hourly catch-up, though the server had
    # the email each time (a timeout after DATA, a hang-up after it).
    server = smtp(mode)
    with pytest.raises(MaybeSent):
        await _send(_mailer(server.port))
    assert len(server.received) == 1


async def test_a_bad_answer_to_quit_after_the_email_was_accepted_is_a_send(smtp):
    # Before: smtplib raises a non-221 answer to QUIT from its `with`, after
    # the 250, and the accepted email counted as failed.
    server = smtp("bad_quit")
    await _send(_mailer(server.port))
    assert len(server.received) == 1


@pytest.mark.parametrize("mode, error", [("refuse_rcpt", smtplib.SMTPRecipientsRefused),
                                         ("refuse_data", smtplib.SMTPDataError)])
async def test_a_refusal_is_a_clear_failure(smtp, mode, error):
    # The server answered with an error code: nothing was accepted, so the
    # schedule may try again.
    with pytest.raises(error):
        await _send(_mailer(smtp(mode).port))


async def test_no_server_is_a_clear_failure():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()  # nothing listens there
    with pytest.raises(OSError) as got:
        await _send(_mailer(port))
    assert not isinstance(got.value, MaybeSent)
