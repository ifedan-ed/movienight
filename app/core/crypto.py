"""Encryption for secrets kept per person (Jellyfin tokens) and signed
tokens for links that must work without a session (player callbacks)."""
from __future__ import annotations

import base64
import hashlib
import hmac
import secrets

from cryptography.fernet import Fernet, InvalidToken

from app.core.config import settings


def _fernet() -> Fernet:
    if not settings.secret_key:
        raise RuntimeError("SECRET_KEY is not set")
    key = hashlib.sha256(("movienight:fernet:" + settings.secret_key).encode()).digest()
    return Fernet(base64.urlsafe_b64encode(key))


def encrypt(value: str) -> str:
    return _fernet().encrypt(value.encode()).decode()


def decrypt(token: str | None) -> str | None:
    if not token:
        return None
    try:
        return _fernet().decrypt(token.encode()).decode()
    except (InvalidToken, ValueError):
        return None


def new_token(nbytes: int = 32) -> str:
    return secrets.token_urlsafe(nbytes)


def token_hash(token: str) -> str:
    return hashlib.sha256(token.encode()).hexdigest()


def same(a: str, b: str) -> bool:
    return hmac.compare_digest(a.encode(), b.encode())
