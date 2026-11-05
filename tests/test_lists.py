"""Own lists, curated (this install) lists, MDBList lists, stars and Home pins."""
from sqlalchemy import insert

from app.core.db import session
from app.models import ListItem, UserList


async def curated_list() -> int:
    async with session() as db:
        res = await db.execute(insert(UserList).values(owner_id=None, name="Reddit Top 100", kind="list", source="copy",
                                                       origin="trakt").returning(UserList.id))
        list_id = res.scalar_one()
        await db.execute(insert(ListItem), [{"list_id": list_id, "media_type": "movie", "tmdb_id": 603, "position": 0},
                                            {"list_id": list_id, "media_type": "tv", "tmdb_id": 1399, "position": 1}])
        await db.commit()
    return list_id


async def test_own_list_is_private_and_editable(alice, bob):
    lid = (await alice.post("/api/v1/lists", json={"name": "Rainy Sunday"})).json()["id"]
    assert (await alice.post(f"/api/v1/lists/{lid}/items", json={"media_type": "movie", "tmdb_id": 603})).json()["added"]
    assert (await alice.post(f"/api/v1/lists/{lid}/items", json={"media_type": "tv", "tmdb_id": 1399})).json()["added"]
    l = (await alice.get(f"/api/v1/lists/{lid}")).json()
    assert (l["movies"], l["shows"], l["group"], l["editable"]) == (1, 1, "mine", True)
    assert [c["key"] for c in (await alice.get(f"/api/v1/lists/{lid}?media=tv")).json()["items"]] == ["tv:1399"]
    assert (await bob.get(f"/api/v1/lists/{lid}")).status_code == 404
    assert (await bob.post(f"/api/v1/lists/{lid}/items", json={"media_type": "movie", "tmdb_id": 1})).status_code == 404
    assert (await bob.delete(f"/api/v1/lists/{lid}")).status_code == 404


async def test_curated_list_is_fixed_but_can_be_starred_pinned_and_copied(alice, bob, owner):
    lid = await curated_list()
    assert (await alice.post(f"/api/v1/lists/{lid}/items", json={"media_type": "movie", "tmdb_id": 1})).status_code == 409
    f = (await alice.patch(f"/api/v1/lists/{lid}", json={"starred": True, "pinned": True})).json()
    assert f["starred"] and f["pinned"]
    lists_b = (await bob.get("/api/v1/lists")).json()["lists"]
    assert [x["starred"] for x in lists_b if x["id"] == lid] == [False]  # stars are per person
    rows = (await alice.get("/api/v1/home/config?tab=for_you")).json()["rows"]
    pinned = [r for r in rows if r.get("list") and r["on"]]
    assert pinned[0]["key"] == f"list:{lid}" and pinned[0]["group"] == "shared"
    copy = (await alice.post(f"/api/v1/lists/{lid}/copy")).json()["id"]
    c = (await alice.get(f"/api/v1/lists/{copy}")).json()
    assert c["group"] == "mine" and c["count"] == 2 and c["editable"]
    assert (await alice.delete(f"/api/v1/lists/{lid}")).status_code == 403  # only admins remove curated lists
    assert (await owner.delete(f"/api/v1/lists/{lid}")).status_code == 200


async def test_sorting(alice):
    lid = (await alice.post("/api/v1/lists", json={"name": "Sorted"})).json()["id"]
    for tid in (700, 500, 600):
        await alice.post(f"/api/v1/lists/{lid}/items", json={"media_type": "movie", "tmdb_id": tid})
    order = lambda r: [c["tmdb_id"] for c in r.json()["items"]]  # noqa: E731
    assert order(await alice.get(f"/api/v1/lists/{lid}")) == [700, 500, 600]
    assert order(await alice.get(f"/api/v1/lists/{lid}?sort=title")) == [500, 600, 700]
    assert order(await alice.get(f"/api/v1/lists/{lid}?sort=added")) == [600, 500, 700]


async def test_mdblist_explore_and_follow(alice):
    top = (await alice.get("/api/v1/mdblist/lists")).json()
    assert top["mode"] == "browse" and top["results"][0]["name"] == "Latest TV Shows"
    link = (await alice.get("/api/v1/mdblist/lists?q=https://mdblist.com/lists/gary/latest-tv-shows")).json()
    assert link["mode"] == "link"
    out = (await alice.post("/api/v1/mdblist/follow", json={"ref": "2194"})).json()
    assert out["count"] == 2
    again = (await alice.post("/api/v1/mdblist/follow", json={"ref": "2194"})).json()
    assert again["id"] == out["id"]  # following twice is the same list
    l = (await alice.get(f"/api/v1/lists/{out['id']}")).json()
    assert l["group"] == "mdblist" and l["by"] == "MDBList · gary" and l["origin_url"].endswith("/gary/latest-tv-shows")
    assert (await alice.post("/api/v1/mdblist/follow", json={"ref": "https://example.com/x"})).status_code == 422


async def test_lists_tab(alice):
    await curated_list()
    data = (await alice.get("/api/v1/lists")).json()
    assert {"watchlist", "favorites", "ratings", "lists"} <= set(data)
    assert data["lists"][0]["group"] == "shared" and data["lists"][0]["by"] == "Shared list"
