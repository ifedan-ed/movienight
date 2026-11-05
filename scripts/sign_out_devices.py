"""Sign out every phone and TV of one person: someone removed from the
sign-in, or the App Review account once review is over.

    docker compose exec web python -m scripts.sign_out_devices <username>

Their next request from any of those devices gets "This device was signed
out"; they can sign in again only through the sign-in proxy.
"""
from __future__ import annotations

import asyncio
import sys

from app.core.db import session
from app.services import devices


async def main(username: str) -> None:
    async with session() as db:
        try:
            n = await devices.sign_out_everywhere(db, username)
        except devices.DeviceError as exc:
            sys.exit(str(exc))
    print(f"Signed out {n} device{'' if n == 1 else 's'} of {username}")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: python -m scripts.sign_out_devices <username>")
    asyncio.run(main(sys.argv[1]))
