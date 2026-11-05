// One list: Movies / Shows, sorting, star, show on Home; your own lists can
// be edited and renamed; any list can be copied into your own lists.
import { del, get, patch, post } from "../api.js";
import { back, go, setQuery } from "../nav.js";
import { confirmSheet, fillPosters, posterCard, promptSheet, sheet, toast } from "../ui.js";
import { ago, h, icon, notShown, plain, swap } from "../util.js";
import { byLine } from "./lists.js";

const SORTS = [["list", "List order"], ["added", "Recently added"], ["title", "Title"], ["year", "Newest"], ["rating", "Highest rated"]];

export default async function render(root, { params, query }) {
  const id = params[0];
  let media = query.get("media") || "";
  let sort = query.get("sort") || "list";
  const title = h("h1", "List");
  const sub = h("div.list-sub");
  const desc = h("p.overview.list-desc", { hidden: true });
  // MDBList items without a TMDB id can't be shown: say how many.
  const missing = h("p.soft.row-note", { hidden: true });
  const seg = h("div.seg", { hidden: true });
  const bar = h("div.chips");
  const grid = h("div.grid");
  const foot = h("div.loadmore");
  root.append(
    h("header.back-head", h("button.iconbtn", { onclick: () => back("/lists"), "aria-label": "Back" }, icon("left")), title,
      h("button.iconbtn", { "aria-label": "List options", onclick: () => menu() }, icon("more"))),
    sub, desc, missing, seg, bar, grid, foot);

  let meta = null, page = 0, more = true, busy = false, editing = false, token = 0;

  async function loadPage() {
    if (busy || !more) return;
    busy = true;
    const mine = token;
    swap(foot, h("div.spin"));
    try {
      const qs = new URLSearchParams({ page: String(++page), sort });
      if (media) qs.set("media", media);
      const res = await get(`/api/v1/lists/${id}?${qs}`, { fresh: page === 1 });
      if (mine !== token) return;
      meta = res;
      if (page === 1) paintHead();
      for (const c of res.items) grid.append(card(c));
      fillPosters(grid);
      more = res.more;
      if (page === 1 && !res.items.length) {
        grid.append(h("div.empty.span-all", h("b", media ? `No ${media === "tv" ? "shows" : "movies"} in this list` : "This list is empty"),
          res.kind === "smart" ? "Nothing matches its filters right now." : res.editable ? "Add titles from any title page with “List”." : ""));
      }
    } catch (e) {
      swap(foot, h("div.err-box", e.message));
      more = false;
    } finally { busy = false; }
    if (!more && !foot.querySelector(".err-box")) swap(foot);
    if (more) { io.unobserve(foot); io.observe(foot); }
  }

  function card(c) {
    const el = posterCard(c);
    if (editing) {
      el.querySelector(".poster").append(h("button.badge.remove", {
        "aria-label": `Remove ${c.title}`,
        onclick: async (e) => {
          e.preventDefault(); e.stopPropagation();
          try {
            await del(`/api/v1/lists/${id}/items/${c.media_type}/${c.tmdb_id}`);
            el.remove();
            toast(`Removed ${c.title}`);
          } catch (err) { toast(err.message, { error: true }); }
        },
      }, icon("x")));
    }
    return el;
  }

  function paintHead() {
    title.textContent = meta.name;
    sub.textContent = [byLine(meta), meta.group === "mdblist" && meta.synced_at ? `synced ${ago(meta.synced_at)}` : null].filter(Boolean).join(" · ");
    desc.hidden = !meta.description;
    const text = plain(meta.description);
    desc.textContent = text.length > 300 ? text.slice(0, 300) + "…" : text;
    missing.hidden = !meta.not_shown;
    missing.textContent = meta.not_shown ? notShown(meta.not_shown) : "";
    // Movies / Shows only when the list has both.
    const mixed = meta.kind !== "smart" && meta.movies && meta.shows;
    seg.hidden = !mixed;
    if (mixed) {
      swap(seg, ...[["", `All ${meta.count}`], ["movie", `Movies ${meta.movies}`], ["tv", `Shows ${meta.shows}`]].map(([m, label]) =>
        h("button" + (m === media ? ".on" : ""), { onclick: () => { if (m !== media) { media = m; reload(); } } }, label)));
    }
    swap(bar,
      h("button.chip" + (meta.starred ? ".on" : ""), { onclick: () => toggle("starred") }, icon(meta.starred ? "starFill" : "star"), meta.starred ? "Starred" : "Star"),
      h("button.chip" + (meta.pinned ? ".on" : ""), { onclick: () => toggle("pinned") }, icon("home"), meta.pinned ? "On Home" : "Show on Home"),
      meta.kind !== "smart" ? h("button.chip" + (sort !== "list" ? ".on" : ""), { onclick: pickSort }, icon("sort"), SORTS.find(([v]) => v === sort)?.[1] || "Sort") : null,
      meta.editable ? h("button.chip" + (editing ? ".on" : ""), { onclick: toggleEdit }, icon("edit"), editing ? "Done" : "Edit") : null);
  }

  function pickSort() {
    sheet((close) => h("div", h("h2", "Sort by"),
      SORTS.map(([v, label]) => h("button.opt", { onclick: () => { close(); if (v !== sort) { sort = v; reload(); } } },
        h("span.grow", label), h("span.check" + (v === sort ? ".on" : ""), v === sort ? icon("check") : null)))));
  }

  async function toggle(field) {
    try {
      const res = await patch(`/api/v1/lists/${id}`, { [field]: !meta[field] });
      meta.pinned = res.pinned;
      meta.starred = res.starred;
      toast(field === "pinned" ? (meta.pinned ? "It's on your Home now" : "Removed from Home") : (meta.starred ? "Starred" : "Unstarred"));
      paintHead();
    } catch (e) { toast(e.message, { error: true }); }
  }

  function toggleEdit() { editing = !editing; grid.classList.toggle("editing", editing); reload(); }

  function reload() {
    token++;
    setQuery({ media, sort: sort === "list" ? null : sort });
    page = 0; more = true; busy = false;
    swap(grid);
    loadPage();
  }

  function menu() {
    if (!meta) return;
    sheet((close) => h("div", h("h2", meta.name), h("div.sub", byLine(meta)),
      meta.editable ? h("button.opt", { onclick: async () => {
        close();
        const name = await promptSheet("Rename list", { value: meta.name, ok: "Rename" });
        if (!name) return;
        try { await patch(`/api/v1/lists/${id}`, { name }); meta.name = name; paintHead(); toast("Renamed"); }
        catch (e) { toast(e.message, { error: true }); }
      } }, icon("edit"), "Rename") : null,
      h("button.opt", { onclick: () => { close(); toggle("starred"); } }, icon(meta.starred ? "starFill" : "star"), meta.starred ? "Unstar" : "Star this list"),
      h("button.opt", { onclick: () => { close(); toggle("pinned"); } }, icon("home"), meta.pinned ? "Remove from Home" : "Show on Home"),
      meta.kind === "list" && !meta.editable ? h("button.opt", { onclick: async () => {
        close();
        try { const res = await post(`/api/v1/lists/${id}/copy`); toast("Copied to your lists"); go(`/list/${res.id}`); }
        catch (e) { toast(e.message, { error: true }); }
      } }, icon("copy"), "Copy to my lists") : null,
      meta.group === "mdblist" ? h("button.opt", { onclick: async () => {
        close();
        try { const res = await post(`/api/v1/lists/${id}/sync`); toast(`Up to date: ${res.count} titles`); reload(); }
        catch (e) { toast(e.message, { error: true }); }
      } }, icon("refresh"), "Sync with MDBList now") : null,
      meta.origin_url ? h("a.opt", { href: meta.origin_url, target: "_blank", rel: "noopener", onclick: close }, icon("external"), "Open on MDBList") : null,
      meta.deletable ? h("button.opt.danger", { onclick: () => {
        close();
        confirmSheet(meta.group === "mine" ? "Delete this list?" : "Remove this list for everyone?",
          meta.group === "mine" ? "The list goes away. The titles stay everywhere else." : "It disappears from everyone's Lists tab and Home.",
          meta.group === "mine" ? "Delete list" : "Remove list", async () => {
            try { await del(`/api/v1/lists/${id}`); toast("List removed"); go("/lists", { replace: true }); }
            catch (e) { toast(e.message, { error: true }); }
          });
      } }, icon("trash"), meta.group === "mine" ? "Delete list" : "Remove list") : null));
  }

  const io = new IntersectionObserver((ents) => { if (ents.some((e) => e.isIntersecting)) loadPage(); }, { rootMargin: "800px" });
  io.observe(foot);
  await loadPage();
}
