// One season of a show: every episode with a tick (tap to mark it watched;
// it is marked played in your Jellyfin too), a heart (tap to like it) and
// stars (tap to rate it, 1 to 10 like a title) — that episode only, not the
// show — Play for episodes in the library, and Request for a season that
// isn't there yet. ?episode=N (from Recently watched and History) opens
// that episode and scrolls to it.
import { episodeHeart, episodeStars } from "../actions.js";
import { get, limits, post } from "../api.js";
import { back, go } from "../nav.js";
import { playSheet } from "../play.js";
import { setState, states, toast } from "../ui.js";
import { fmtDate, h, icon, img, swap } from "../util.js";

export default async function render(root, { params, query }) {
  const [showId, number] = params.map(Number);
  // The episode to open (from Recently watched or History), once.
  let focus = Number(query.get("episode")) || null;
  const key = `tv:${showId}`;
  const title = h("h1", `Season ${number}`);
  // The show's name links to its page (an episode opened from Recently
  // watched comes here first; the show's heart and rating are there).
  const sub = h("a.sub.show-link", { href: `/title/tv/${showId}` });
  const chips = h("div.chips");
  const tools = h("div.season-tools");
  const list = h("div.episodes", h("div.loadmore", h("div.spin")));
  root.append(
    h("header.back-head", h("button.iconbtn", { onclick: () => back(`/title/tv/${showId}`), "aria-label": "Back" }, icon("left")),
      h("div.grow", title, sub)),
    chips, tools, list);

  let show, season;
  try {
    [show, season] = await Promise.all([
      get(`/api/v1/title/tv/${showId}`),
      get(`/api/v1/title/tv/${showId}/season/${number}`, { fresh: true }),
    ]);
  } catch (e) { swap(list, h("div.err-box", e.message)); return; }
  const c = { key, media_type: "tv", tmdb_id: showId, title: show.title, poster: show.poster };
  title.textContent = season.name || `Season ${number}`;
  sub.textContent = show.title;
  const info = (show.seasons || []).find((s) => s.number === number) || {};

  swap(chips, ...(show.seasons || []).map((s) => h("a.chip" + (s.number === number ? ".on" : ""), {
    href: `/title/tv/${showId}/season/${s.number}`,
    onclick: (e) => { e.preventDefault(); if (s.number !== number) go(`/title/tv/${showId}/season/${s.number}`, { replace: true }); },
  }, s.number === 0 ? "Specials" : `Season ${s.number}`)));

  const eps = season.episodes;
  const seenCount = () => eps.filter((e) => e.watched).length;

  async function mark(list, watched) {
    const before = list.map((e) => e.watched);
    list.forEach((e) => { e.watched = watched ? Math.max(1, e.watched) : 0; });
    paint();
    try {
      // BATCH_MAX_ITEMS episodes a call (the server's request size), as many calls as the season needs.
      const per = (await limits()).batch_max_items || 500;
      for (let i = 0; i < list.length; i += per) {
        await post(`/api/v1/title/tv/${showId}/episodes`, { episodes: list.slice(i, i + per).map((e) => ({ season: number, episode: e.number })), watched });
      }
      const st = states.get(key) || {};
      const delta = list.reduce((n, e, i) => n + (watched ? (before[i] ? 0 : 1) : (before[i] ? -1 : 0)), 0);
      setState(key, { watched: Math.max(0, (st.watched || 0) + delta) });
      if (list.length > 1) toast(watched ? `Marked ${list.length} episodes as watched` : `Unmarked ${list.length} episodes`);
    } catch (err) {
      list.forEach((e, i) => { e.watched = before[i]; });
      paint();
      toast(err.message, { error: true });
    }
  }

  async function requestSeason(btn) {
    btn.disabled = true;
    try {
      const res = await post(`/api/v1/title/tv/${showId}/request`, { seasons: [number] });
      toast(res.already ? "Already requested" : `Requested ${show.title}, season ${number}`);
      info.status = "pending";
      paintTools();
    } catch (err) { btn.disabled = false; toast(err.message, { error: true }); }
  }

  function paintTools() {
    const aired = eps.filter((e) => e.aired);
    const allSeen = aired.length && aired.every((e) => e.watched);
    const anyInLib = eps.some((e) => e.in_library);
    const status = info.status;
    swap(tools,
      h("div.season-count", `${seenCount()} of ${eps.length} watched`),
      aired.length ? h("button.chip" + (allSeen ? ".on" : ""), { onclick: () => mark(allSeen ? aired : aired.filter((e) => !e.watched), !allSeen) },
        icon(allSeen ? "tickFill" : "tick"), allSeen ? "Season watched" : "Mark season watched") : null,
      !anyInLib && status !== "available" ? (status === "pending" || status === "processing" || status === "partial"
        ? h("span.chip.on", icon("clock"), status === "pending" ? "Requested" : "Downloading")
        : h("button.chip", { onclick: (e) => requestSeason(e.currentTarget) }, icon("download"), `Request season ${number}`)) : null);
  }

  function episodeRow(e) {
    const tick = h("button.tick" + (e.watched ? ".on" : ""), {
      "aria-label": e.watched ? `Mark episode ${e.number} unwatched` : `Mark episode ${e.number} watched`,
      "aria-pressed": String(!!e.watched), disabled: !e.aired && !e.watched,
      onclick: (ev) => { ev.preventDefault(); ev.stopPropagation(); mark([e], !e.watched); },
    }, icon(e.watched ? "tickFill" : "tick"));
    // The heart and the stars repaint only themselves, so an episode opened
    // to read its overview stays open.
    const heart = episodeHeart(showId, number, e);
    const stars = episodeStars(showId, number, e, { showTitle: show.title });
    const ov = e.overview ? h("p.ep-ov", e.overview) : null;
    const meta = [e.aired ? fmtDate(e.air_date) : e.air_date ? `Airs ${fmtDate(e.air_date, { weekday: "short", month: "short", day: "numeric" })}` : "Date to come",
      e.runtime ? `${e.runtime} min` : null, e.vote ? `★ ${e.vote}` : null].filter(Boolean).join(" · ");
    const row = h("div.ep" + (e.watched ? ".watched" : "") + (e.aired ? "" : ".upcoming"),
      h("div.ep-still", e.still ? h("img", { src: img(e.still, "w300"), alt: "", loading: "lazy" }) : null,
        e.in_library ? h("button.ep-play", { "aria-label": `Play episode ${e.number}`, onclick: (ev) => {
          ev.preventDefault(); ev.stopPropagation(); playSheet(c, { season: number, episode: e.number });
        } }, icon("play")) : null),
      h("div.ep-tx", h("b", `${e.number}. ${e.name}`), h("span", meta), ov),
      h("div.ep-marks", tick, heart, stars));
    row.id = `e${e.number}`;
    row.addEventListener("click", () => row.classList.toggle("open"));
    return row;
  }

  function paint() {
    paintTools();
    swap(list, ...(eps.length ? eps.map(episodeRow) : [h("div.empty", "No episodes listed yet.")]));
  }
  paint();
  if (focus) {
    const row = list.querySelector(`#e${focus}`);
    focus = null;
    if (row) {
      row.classList.add("open");
      // Below the sticky top bar, once the page is laid out.
      requestAnimationFrame(() => row.scrollIntoView({ block: "center" }));
    }
  }
  return { onShow: () => paintTools() };
}
