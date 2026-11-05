// Lists tab: yours first (Watchlist, Favorites, Ratings and the lists you
// keep), then Shared lists (fixed copies of curated lists), then MDBList
// lists. Filters: everything, the lists you starred, or one kind. Only your
// own lists take titles; any list can be starred and shown on Home.
import { get, patch, post } from "../api.js";
import { go, setQuery } from "../nav.js";
import { promptSheet, toast } from "../ui.js";
import { h, icon, img, store, swap } from "../util.js";

const FILTERS = [["all", "All"], ["starred", "Starred"], ["mine", "Yours"], ["shared", "Shared"], ["mdblist", "MDBList"]];
const GROUP_LABEL = { mine: "Your lists", shared: "Shared lists", mdblist: "MDBList lists" };

export function byLine(l) {
  const n = l.count == null ? "" : `${l.count.toLocaleString()} title${l.count === 1 ? "" : "s"}`;
  if (l.kind === "smart") return "Smart list · updates itself";
  return [l.group === "mine" ? null : l.by, n].filter(Boolean).join(" · ");
}

function collage(posters) {
  return h("div.collage", (posters.length ? posters : [null, null, null]).slice(0, 5).map((p) =>
    h("div.p", p ? h("img", { src: img(p, "w185"), alt: "", loading: "lazy" }) : null)));
}

function flagButton(l, field, onLabel, offLabel, ic, icOn, onChanged) {
  const on = !!l[field];
  const btn = h(`button.pin${on ? ".on" : ""}${field === "starred" ? ".star" : ""}`, {
    "aria-pressed": String(on), "aria-label": on ? onLabel : offLabel,
    onclick: async (e) => {
      e.preventDefault();
      e.stopPropagation();
      const next = !l[field];
      try {
        const res = await patch(`/api/v1/lists/${l.id}`, { [field]: next });
        l.pinned = res.pinned;
        l.starred = res.starred;
        btn.replaceWith(flagButton(l, field, onLabel, offLabel, ic, icOn, onChanged));
        toast(field === "pinned" ? (next ? `“${l.name}” is on Home now` : `Removed “${l.name}” from Home`)
          : (next ? `Starred “${l.name}”` : `Unstarred “${l.name}”`));
        onChanged && onChanged();
      } catch (err) { toast(err.message, { error: true }); }
    },
  }, icon(on ? icOn : ic), field === "pinned" ? (on ? "On Home" : "Home") : null);
  return btn;
}

function listCard(l, onChanged) {
  return h("a.lcard2", { href: `/list/${l.id}` },
    h("div.top",
      h("div.names", h("h3", l.name), h("div.by", byLine(l))),
      flagButton(l, "starred", "Unstar", "Star this list", "star", "starFill", onChanged),
      flagButton(l, "pinned", "Remove from Home", "Show on Home", "home", "home", onChanged)),
    collage(l.posters || []));
}

function specialCard(href, name, sub, posters, ic) {
  return h("a.lcard2", { href },
    h("div.top", h("div.names", h("h3", name), h("div.by", sub)), h("span.pin", icon(ic))),
    posters && posters.length ? collage(posters) : null);
}

export default async function render(root, { query }) {
  let filter = query.get("filter") || store("listsFilter") || "all";
  if (!FILTERS.some(([id]) => id === filter)) filter = "all";
  const chips = h("div.chips", { role: "tablist" });
  const body = h("div", h("div.loadmore", h("div.spin")));
  root.append(
    h("header.head", h("h1", "Lists"),
      h("a.iconbtn", { href: "/mdblist", "aria-label": "Find lists on MDBList" }, icon("search")),
      h("button.iconbtn", { "aria-label": "New list", onclick: newList }, icon("plus"))),
    chips, body);

  let data = null;

  async function newList() {
    const name = await promptSheet("New list", { placeholder: "e.g. Rainy Sunday films", ok: "Create" });
    if (!name) return;
    try {
      const res = await post("/api/v1/lists", { name });
      go(`/list/${res.id}`);
    } catch (e) { toast(e.message, { error: true }); }
  }

  function paint() {
    store("listsFilter", filter);
    setQuery({ filter: filter === "all" ? null : filter });
    swap(chips, ...FILTERS.map(([id, label]) => h("button.chip" + (id === filter ? ".on" : ""), {
      role: "tab", "aria-selected": String(id === filter), onclick: () => { if (id !== filter) { filter = id; paint(); } },
    }, id === "starred" ? icon(filter === id ? "starFill" : "star") : null, label)));
    if (!data) return;
    const shown = data.lists.filter((l) => filter === "all" || (filter === "starred" ? l.starred : l.group === filter));
    const section = (g) => {
      const items = shown.filter((l) => l.group === g);
      return items.length ? [h("div.section-label", GROUP_LABEL[g]), h("div.lists-grid", items.map((l) => listCard(l, () => { if (filter === "starred") paint(); })))] : [];
    };
    const yours = filter === "all" || filter === "mine";
    const out = [];
    if (yours) {
      out.push(h("div.section-label", "Your lists"),
        h("div.lists-grid",
          specialCard("/collection/watchlist", "Watchlist", `${data.watchlist.count} titles`, data.watchlist.posters, "bookmark"),
          specialCard("/collection/favorites", "Favorites", `${data.favorites.count} titles`, data.favorites.posters, "heart"),
          specialCard("/collection/ratings", "Ratings", `${data.ratings.count} rated`, data.ratings.posters, "starFill"),
          ...shown.filter((l) => l.group === "mine").map((l) => listCard(l))));
    } else {
      out.push(...section("mine"));
    }
    out.push(...section("shared"), ...section("mdblist"));
    if (filter === "mdblist" || filter === "all") {
      out.push(h("div", { style: { padding: "6px var(--gutter)" } },
        h("a.btn.btn-quiet", { href: "/mdblist" }, icon("search"), "Find more lists on MDBList")));
    }
    if (!shown.length && !yours) {
      out.unshift(h("div.empty", h("b", filter === "starred" ? "No starred lists yet" : "Nothing here yet"),
        filter === "starred" ? "Tap the star on any list to keep it here." : ""));
    }
    swap(body, ...out);
  }

  async function load() {
    try { data = await get("/api/v1/lists", { fresh: true }); }
    catch (e) { swap(body, h("div.err-box", e.message)); return; }
    paint();
  }
  paint();
  await load();
  return { onShow: load };
}
