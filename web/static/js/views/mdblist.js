// Find lists on MDBList: top lists, search, or a pasted mdblist.com link;
// open one to see its titles, then add it to the MDBList lists here (it stays
// in step with MDBList every day).
import { get, post } from "../api.js";
import { back, setQuery } from "../nav.js";
import { fillPosters, posterCard, toast } from "../ui.js";
import { debounce, h, icon, notShown, plain, swap } from "../util.js";

function listRow(l) {
  const bits = [l.user ? `by ${l.user}` : null, l.item_count != null ? `${l.item_count.toLocaleString()} titles` : null,
    l.media === "movie" ? "Movies" : l.media === "tv" ? "Shows" : null, l.dynamic ? "updates itself" : null].filter(Boolean);
  return h("a.trow", { href: `/mdblist/${l.mdblist_id}` },
    h("span.ic", icon("lists")),
    h("span.tx", h("b", l.name), h("span", bits.join(" · "))),
    l.followed_id ? h("span.chip.on.small", icon("check"), "Added") : null,
    l.likes ? h("span.likes", icon("heartFill"), l.likes.toLocaleString()) : null);
}

async function explore(root, query) {
  let q = query.get("q") || "";
  const input = h("input.input", { type: "search", value: q, placeholder: "Search lists, or paste an mdblist.com link", enterkeyhint: "search", "aria-label": "Search MDBList lists" });
  const results = h("div");
  root.append(
    h("header.back-head", h("button.iconbtn", { onclick: () => back("/lists"), "aria-label": "Back" }, icon("left")), h("h1", "MDBList lists")),
    h("div.searchbox", icon("search"), input), results);
  let token = 0;
  async function run() {
    q = input.value.trim();
    setQuery({ q });
    const mine = ++token;
    swap(results, h("div.loadmore", h("div.spin")));
    let res;
    try { res = await get(`/api/v1/mdblist/lists?q=${encodeURIComponent(q)}`); }
    catch (e) { if (mine === token) swap(results, h("div.err-box", e.message)); return; }
    if (mine !== token) return;
    if (res.error) { swap(results, h("div.empty", h("b", "Nothing found"), res.error)); return; }
    swap(results,
      h("div.section-label", res.mode === "browse" ? "Top lists on MDBList" : res.mode === "link" ? "That list" : "Lists"),
      res.results.length ? h("div.rows", res.results.map(listRow)) : h("div.empty", "No lists match."));
  }
  input.addEventListener("input", debounce(run, 450));
  input.addEventListener("keydown", (e) => { if (e.key === "Enter") { input.blur(); run(); } });
  await run();
}

async function preview(root, id) {
  const body = h("div", h("div.loadmore", h("div.spin")));
  root.append(h("header.back-head", h("button.iconbtn", { onclick: () => back("/mdblist"), "aria-label": "Back" }, icon("left")), h("h1", "List")), body);
  let res;
  try { res = await get(`/api/v1/mdblist/lists/${id}`); }
  catch (e) { swap(body, h("div.err-box", e.message)); return; }
  const l = res.list;
  root.querySelector("h1").textContent = l.name;
  const action = h("div", { style: { padding: "12px var(--gutter) 4px" } });
  const paintAction = () => swap(action, l.followed_id
    ? h("a.btn.btn-quiet", { href: `/list/${l.followed_id}` }, icon("check"), "Added — open it in your Lists")
    : h("button.btn.btn-request", { onclick: async (e) => {
      const btn = e.currentTarget;
      btn.disabled = true;
      try {
        const out = await post("/api/v1/mdblist/follow", { ref: String(l.mdblist_id) });
        l.followed_id = out.id;
        toast(`Added “${l.name}” (${out.count} titles)`);
        paintAction();
      } catch (err) { btn.disabled = false; toast(err.message, { error: true }); }
    } }, icon("plus"), "Add to MDBList lists"));
  paintAction();
  const desc = plain(l.description);
  // The first 120 of the titles the preview sends (all of the list); the rest are
  // counted below, and Follow keeps every one.
  const grid = h("div.grid", res.items.slice(0, 120).map((c) => posterCard(c)));
  swap(body,
    h("div.list-sub", [l.user ? `by ${l.user}` : null, `${res.count.toLocaleString()} titles`, l.likes ? `${l.likes.toLocaleString()} likes` : null].filter(Boolean).join(" · ")),
    desc ? h("p.overview.list-desc", desc.length > 300 ? desc.slice(0, 300) + "…" : desc) : null,
    action,
    grid,
    res.count > 120 ? h("div.empty", `…and ${(res.count - 120).toLocaleString()} more once it's added.`) : null,
    res.not_shown ? h("p.soft.row-note", notShown(res.not_shown)) : null);
  fillPosters(grid);
}

export default async function render(root, { params, query }) {
  if (params[0]) return preview(root, params[0]);
  return explore(root, query);
}

