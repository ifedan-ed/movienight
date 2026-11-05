"""Typos fixed before a search is embedded, against the catalog's own words."""
from app.services import spelling

VOCAB = {"kids": 700, "searching": 500, "treasure": 800, "map": 900, "amp": 2, "for": 5000, "the": 9000,
         "thieves": 300, "house": 1200, "sold": 400, "goonies": 5, "alien": 600, "with": 8000}


def test_fixes_typos_split_words_and_rare_lookalikes():
    assert spelling.correct("kids seraching fo r trasure with a amp", VOCAB) == "kids searching for treasure with a map"
    assert spelling.correct("alein", VOCAB) == "alien"


def test_leaves_good_and_unknown_words_alone():
    assert spelling.correct("The Goonies", VOCAB) == "The Goonies"  # nothing to fix: the query comes back as typed
    assert spelling.correct("zxqwv", VOCAB) == "zxqwv"
    assert spelling.correct("anything", {}) == "anything"


def test_vocabulary_counts_words_seen_often_enough():
    words = spelling.count_words(["A treasure map", "the treasure", "Treasure!", "rare word"])
    assert words == {"treasure": 3}


async def test_search_runs_both_wordings_and_says_so(alice, up):
    await spelling.save_vocab(dict(VOCAB))
    r = (await alice.get("/api/v1/search", params={"q": "trasure amp"})).json()
    assert r["corrected"] == "treasure map"
    assert up.meili.queries == ["trasure amp", "treasure map"]
    r = (await alice.get("/api/v1/search", params={"q": "treasure map"})).json()
    assert r["corrected"] is None
