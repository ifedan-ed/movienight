// Everything you watched, newest first, grouped by month. Same-day duplicates
// (a Jellyfin play that Trakt also logged) show once with both sources.
import { get, post } from "../api.js";
import { back, setQuery } from "../nav.js";
import { confirmSheet, setState, toast } from "../ui.js";
import { episodeHref, fmtDate, h, icon, img, sourceLabel, swap } from "../util.js";


export default async function render(root, { query }) {
  let media = query.get("media") || "";
  const seg = h("div.seg");
  const list = h("div.rows");
  const foot = h("div.loadmore");
  const sub = h("div.sub");
  root.append(
    h("header.back-head", h("button.iconbtn", { onclick: () => back("/profile"), "aria-label": "Back" }, icon("left")),
      h("div", { style: { flex: "1" } }, h("h1", "History"), sub)),
    seg, list, foot);

  let page = 0, more = true, busy = false, lastMonth = null, token = 0;
  // Each episode row's slot for its own heart and rating, by its first watch
  // id: repainted when the page shows again (a rating changed on the season
  // page shows after Back).
  const ownSlots = new Map();

  function ownMarks(x) {
    return x.episode_favorite || x.episode_rating
      ? h("span.h-own", x.episode_favorite ? h("span.h-heart", { title: "You liked this episode" }, icon("heartFill")) : null,
        x.episode_rating ? h("span.h-rated", { title: "Your rating of this episode" }, icon("starFill"), `${x.episode_rating}/10`) : null) : null;
  }

  function row(x) {
    const c = x.card;
    const title = c ? c.title : x.title;
    const line1 = x.episode != null ? `S${x.season} · E${x.episode}${x.episode_title ? " – " + x.episode_title : ""}` : (c && c.year ? String(c.year) : "");
    const when = new Date(x.watched_at);
    // An episode opens that episode (its season page, which links back to
    // the show) and shows its own heart and rating; a film opens its page.
    const isEp = c && x.media_type === "episode" && x.season != null && x.episode != null;
    const href = c ? (isEp ? episodeHref(c.tmdb_id, x.season, x.episode) : `/title/${c.media_type}/${c.tmdb_id}`) : null;
    const own = isEp ? h("span.h-slot", ownMarks(x)) : null;
    if (own) ownSlots.set(x.ids[0], own);
    const el = h("div.trow",
      h(c ? "a.thumb" : "span.thumb", c ? { href } : {}, c && c.poster ? h("img", { src: img(c.poster, "w154"), alt: "", loading: "lazy" }) : null),
      h(c ? "a.tx" : "span.tx", c ? { href } : {},
        h("b", title),
        line1 ? h("span", line1, own) : own,
        h("span", `${fmtDate(x.watched_at, { weekday: "short", month: "short", day: "numeric" })}, ${when.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })} `,
          ...x.sources.map((s) => h("span.src", sourceLabel(s))))),
      h("button.iconbtn", { "aria-label": "Remove from history", onclick: () => confirmSheet("Remove this watch?", `${title}, ${fmtDate(x.watched_at)}`, "Remove", async () => {
        try {
          const media = x.media_type === "episode" ? "tv" : "movie";
          const res = await post(`/api/v1/title/${media}/${c ? c.tmdb_id : 0}/unwatch`, { ids: x.ids });
          if (c) setState(c.key, { watched: res.watches.length });
          el.remove();
          toast("Removed from history");
        } catch (e) { toast(e.message, { error: true }); }
      }) }, icon("trash")));
    return el;
  }

  async function loadPage() {
    if (busy || !more) return;
    busy = true;
    const mine = token;
    swap(foot, h("div.spin"));
    try {
      const res = await get(`/api/v1/history?page=${++page}${media ? `&media=${media}` : ""}`);
      if (mine !== token) return;
      sub.textContent = `${res.total.toLocaleString()} watches`;
      for (const x of res.items) {
        const m = new Date(x.watched_at).toLocaleDateString(undefined, { month: "long", year: "numeric" });
        if (m !== lastMonth) { list.append(h("div.month", m)); lastMonth = m; }
        list.append(row(x));
      }
      more = res.more;
      if (page === 1 && !res.items.length) list.append(h("div.empty", h("b", "Nothing watched yet"), "Plays from Jellyfin and Trakt show up here, and anything you mark as watched."));
    } catch (e) {
      swap(foot, h("div.err-box", e.message));
      more = false;
    } finally { busy = false; }
    if (!more && !foot.querySelector(".err-box")) swap(foot);
  }

  function paintSeg() {
    swap(seg, ...[["", "All"], ["movie", "Movies"], ["tv", "Episodes"]].map(([id, label]) =>
      h("button" + (id === media ? ".on" : ""), { onclick: () => {
        if (id === media) return;
        media = id; setQuery({ media }); token++; page = 0; more = true; busy = false; lastMonth = null; ownSlots.clear();
        swap(list); paintSeg(); loadPage();
      } }, label)));
  }
  paintSeg();
  new IntersectionObserver((ents) => { if (ents.some((e) => e.isIntersecting)) loadPage(); }, { rootMargin: "900px" }).observe(foot);
  await loadPage();

  // Back from a season page: the loaded pages' episode hearts and ratings
  // again, fresh, in place (the list and its scroll position stay).
  async function refreshOwn() {
    const mine = token;
    for (let p = 1; p <= page; p++) {
      let res;
      try { res = await get(`/api/v1/history?page=${p}${media ? `&media=${media}` : ""}`, { fresh: true }); } catch { return; }
      if (mine !== token) return;
      for (const x of res.items) {
        const slot = ownSlots.get(x.ids[0]);
        if (slot) swap(slot, ownMarks(x));
      }
    }
  }
  return { onShow: refreshOwn };
}
