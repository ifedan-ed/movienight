// Search: your library and every rated title on TMDB (by meaning and
// keywords, typo-tolerant), whichever holds the best match first, with a
// Request button right on the row; then people.
import { get } from "../api.js";
import { primaryButton } from "../actions.js";
import { setQuery } from "../nav.js";
import { fillPosters, posterCard, remember, states } from "../ui.js";
import { debounce, h, icon, img, initials, store, swap } from "../util.js";

const IDEAS = ["a heist that goes wrong", "80s comedies", "Tom Hanks", "space survival", "courtroom drama", "feel-good sports movie"];

function resultRow(c) {
  remember(c);
  const kind = c.media_type === "tv" ? "Show" : "Movie";
  return h("div.sres", { dataset: { key: c.key } },
    h("a.thumb", { href: `/title/${c.media_type}/${c.tmdb_id}` }, c.poster ? h("img", { src: img(c.poster, "w154"), alt: "", loading: "lazy" }) : null),
    h("a.tx", { href: `/title/${c.media_type}/${c.tmdb_id}` },
      h("b", c.title),
      h("span", [c.year, kind, c.vote ? `★ ${c.vote}` : null].filter(Boolean).join(" · "))),
    primaryButton(c, states.get(c.key) || c, { compact: true }));
}

export default async function render(root, { query }) {
  let q = query.get("q") || "";
  let media = query.get("media") || "";
  const input = h("input.input", {
    type: "search", value: q, placeholder: "Titles, people, or describe a movie", enterkeyhint: "search",
    autocapitalize: "off", autocorrect: "off", spellcheck: "false", "aria-label": "Search",
  });
  const clearBtn = h("button.clear", { "aria-label": "Clear", hidden: !q, onclick: () => { input.value = ""; input.focus(); run(""); } }, icon("x"));
  const seg = h("div.seg");
  const results = h("div");
  root.append(h("header.head", h("h1", "Search")),
    h("div.searchbox", icon("search"), input, clearBtn), seg, results);

  const paintSeg = () => swap(seg, ...[["", "All"], ["movie", "Movies"], ["tv", "Shows"]].map(([id, label]) =>
    h("button" + (id === media ? ".on" : ""), { onclick: () => { media = id; paintSeg(); run(input.value); } }, label)));
  paintSeg();

  // The last 8 searches, kept in this browser's localStorage ("mn:recent").
  // Signing out of Movie Night doesn't clear them (docs/limits.md).
  const saveRecent = (text) => {
    const recent = (store("recent") || []).filter((x) => x.toLowerCase() !== text.toLowerCase());
    store("recent", [text, ...recent].slice(0, 8));
  };

  const idle = () => {
    const recent = store("recent") || [];
    swap(results,
      recent.length ? h("div.section-label", "Recent") : null,
      recent.length ? h("div.chips", { style: { flexWrap: "wrap" } }, recent.map((r) => h("button.chip", { onclick: () => { input.value = r; run(r); } }, icon("clock"), r))) : null,
      h("div.section-label", "Try describing it"),
      h("div.chips", { style: { flexWrap: "wrap" } }, IDEAS.map((r) => h("button.chip", { onclick: () => { input.value = r; run(r); } }, icon("sparkles"), r))));
  };

  let token = 0;
  let searchId = 0; // the search (page 1) that "Show more" pages belong to
  let guess = null; // the "Best guess" section of the current search, kept across "Show more"
  // Everything the server sends is shown. A section with more matches gets
  // "Show more", which asks for the next page (each page holds the ones
  // before it too) and draws the sections again (docs/limits.md, Search).
  async function run(text, page = 1) {
    q = text.trim();
    clearBtn.hidden = !q;
    setQuery({ q, media });
    const mine = ++token;
    if (page === 1) searchId = mine;
    const search = searchId;
    if (!q) return idle();
    if (page === 1) swap(results, h("div.loadmore", h("div.spin")));
    let res;
    try { res = await get(`/api/v1/search?q=${encodeURIComponent(q)}${media ? `&media=${media}` : ""}${page > 1 ? `&page=${page}` : ""}`); }
    catch (e) {
      if (mine !== token) return;
      if (page > 1) {
        // Keep what is shown; say why there is no more.
        results.querySelectorAll(".search-more").forEach((b) => { b.disabled = false; b.textContent = "Show more"; });
        results.append(h("div.err-box", e.message));
      } else swap(results, h("div.err-box", e.message));
      return;
    }
    if (mine !== token) return;
    const more = res.more || {};
    const moreBtn = (what) => more[what] ? h("button.btn.btn-quiet.search-more", { onclick: (e) => {
      e.currentTarget.disabled = true;
      e.currentTarget.textContent = "Loading…";
      run(q, page + 1);
    } }, "Show more") : null;
    const out = [];
    if (res.corrected) out.push(h("p.soft", { style: { margin: "4px 0 0" } }, "Showing results for ", h("b", res.corrected)));
    const lib = res.library.length ? [h("div.section-label", "In your library"),
      h("div.grid", res.library.map((c) => posterCard(c, { onclick: () => saveRecent(q) }))), moreBtn("library")] : [];
    const others = res.tmdb.length ? [h("div.section-label", res.library.length ? "Not in your library" : "Results"),
      h("div.rows", res.tmdb.map(resultRow)), moreBtn("tmdb")] : [];
    // The section holding the best match goes first.
    out.push(...(res.first === "tmdb" ? [...others, ...lib] : [...lib, ...others]));
    if (res.people.length) {
      out.push(h("div.section-label", "People"),
        h("div.people", res.people.map((p) => h("a.person", { href: `/person/${p.id}`, onclick: () => saveRecent(q) },
          h("div.ph", p.photo ? h("img", { src: img(p.photo, "w185"), alt: "", loading: "lazy" }) : initials(p.name)),
          h("b", p.name), p.known_for ? h("span", p.known_for) : null))), moreBtn("people"));
    }
    if (res.note) out.push(h("p.soft.row-note", res.note));
    if (!out.length && !res.ai) out.push(h("div.empty", h("b", "Nothing found"), "Try other words, or describe the story."));
    if (page > 1) {
      // More of the same search: the best guess is already there (or on its way).
      swap(results, ...(guess ? [guess] : []), ...out);
      fillPosters(results);
      return;
    }
    // A described plot: the AI's best guess goes on top when it answers.
    guess = res.ai ? h("div", h("div.section-label", "Best guess"), h("div.loadmore", h("div.spin"))) : null;
    swap(results, ...(guess ? [guess] : []), ...out);
    fillPosters(results);
    if (guess) {
      get(`/api/v1/search/ai?q=${encodeURIComponent(q)}${media ? `&media=${media}` : ""}`).then((ai) => {
        if (search !== searchId) return;
        // No guess: say why (the AI didn't answer, or had none) rather than hide it.
        if (!ai.matches.length) {
          swap(guess, h("div.section-label", "Best guess"), h("p.soft.guess-note", ai.note || "The AI had no guess for this description."));
          if (!out.length) results.append(h("div.empty", h("b", "Nothing found"), "Try other words, or describe the story."));
          return;
        }
        swap(guess, h("div.section-label", "Best guess"), h("div.rows", ai.matches.map((m) => {
          const row = resultRow(m.card);
          if (m.why) row.querySelector(".tx").append(h("span.why", m.why));
          return row;
        })));
        fillPosters(guess);
      }).catch(() => { if (search === searchId) swap(guess, h("div.section-label", "Best guess"), h("p.soft.guess-note", "The AI didn't answer this time. Search again to ask it again.")); });
    }
  }

  const debounced = debounce((v) => run(v), 380);
  input.addEventListener("input", () => { clearBtn.hidden = !input.value; debounced(input.value); });
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter") { input.blur(); if (input.value.trim()) saveRecent(input.value.trim()); run(input.value); }
  });
  if (q) run(q); else idle();
  if (!q) setTimeout(() => input.focus(), 50);
  return { onShow: () => { if (!input.value) input.focus(); } };
}
