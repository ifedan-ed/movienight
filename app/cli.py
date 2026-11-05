"""Command-line account management for password sign-in (PASSWORD_SIGNIN).

Run inside the container:

    docker compose exec web python -m app.cli user add alice [--name "Alice"] [--email a@example.com]
    docker compose exec web python -m app.cli user password alice      # set or reset
    docker compose exec web python -m app.cli user nopassword alice    # remove (OIDC only)
    docker compose exec web python -m app.cli user list

The password is asked for twice without echo, or read from standard input
with --stdin (one line), so it never appears in the shell history or `ps`.
Setting or resetting a password signs that person's browsers out. Admin
rights come from ADMIN_USERS / OWNER_USERNAME, as for every account.
"""
from __future__ import annotations

import argparse
import asyncio
import getpass
import sys

from sqlalchemy import select

from app.core.config import settings
from app.core.db import SessionLocal
from app.models import User
from app.services import passwords, users


def _read_password(stdin: bool) -> str:
    if stdin:
        return sys.stdin.readline().rstrip("\n")
    first = getpass.getpass("New password: ")
    if first != getpass.getpass("Again: "):
        raise SystemExit("The two passwords aren't the same.")
    return first


async def _find(db, name: str) -> User:
    clean = users.clean_username(name)
    user = await db.scalar(select(User).where(User.username == clean)) if clean else None
    if user is None:
        raise SystemExit(f"No user {name!r}. Make one with: python -m app.cli user add {name}")
    return user


async def main(argv: list[str]) -> None:
    ap = argparse.ArgumentParser(prog="python -m app.cli")
    sub = ap.add_subparsers(dest="cmd", required=True)
    user = sub.add_parser("user", help="accounts").add_subparsers(dest="action", required=True)
    add = user.add_parser("add", help="make an account with a password")
    add.add_argument("username")
    add.add_argument("--name")
    add.add_argument("--email")
    add.add_argument("--stdin", action="store_true", help="read the password from standard input")
    pw = user.add_parser("password", help="set or reset a password")
    pw.add_argument("username")
    pw.add_argument("--stdin", action="store_true")
    nopw = user.add_parser("nopassword", help="remove a password")
    nopw.add_argument("username")
    user.add_parser("list", help="list accounts")
    a = ap.parse_args(argv)

    async with SessionLocal() as db:
        if a.action == "list":
            for u in (await db.scalars(select(User).order_by(User.username))).all():
                kind = ", ".join(k for k, on in (("password", u.password_hash), ("oidc", u.oidc_sub), ("admin", u.is_admin)) if on)
                print(f"{u.username}\t{u.display_name or ''}\t{kind}")
            return
        if a.action == "add":
            name = users.clean_username(a.username)
            if not name:
                raise SystemExit("That username can't be used.")
            if await db.scalar(select(User).where(User.username == name)):
                raise SystemExit(f"{name} already exists. To set their password: python -m app.cli user password {name}")
            secret = _read_password(a.stdin)
            passwords.validate(secret)
            u = User(username=name, display_name=a.name or None, email=a.email or None, is_admin=name in settings.admins)
            db.add(u)
            await db.flush()
            await passwords.set_password(db, u, secret)
            print(f"Made {name}{' (admin)' if u.is_admin else ''}. They can sign in at /signin.")
            return
        u = await _find(db, a.username)
        if a.action == "password":
            try:
                await passwords.set_password(db, u, _read_password(a.stdin))
            except passwords.PasswordError as exc:
                raise SystemExit(str(exc)) from exc
            print(f"Password set for {u.username}; their browsers were signed out.")
        elif a.action == "nopassword":
            await passwords.clear_password(db, u)
            print(f"Removed {u.username}'s password.")


if __name__ == "__main__":
    try:
        asyncio.run(main(sys.argv[1:]))
    except passwords.PasswordError as exc:
        raise SystemExit(str(exc)) from exc
