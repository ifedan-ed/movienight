"""Version 1 of the Movie Night API, mounted at /api/v1. The web app is just
one client of it; the contract is app/api/schemas.py."""
from fastapi import APIRouter

from app.api.v1 import account, auth_oidc, catalog, devices, home, lists, play, profile, scrobble, titles

router = APIRouter(prefix="/api/v1")
for module in (home, titles, lists, catalog, profile, account, play, scrobble, devices, auth_oidc):
    router.include_router(module.router)
