// Shared UI: cards, rows, grids, sheets, toasts, stars, title-state sync.
import { limits, postRead } from "./api.js";
import { append, clear, episodeHref, h, icon, img, swap, titleHref } from "./util.js";

// ---------------------------------------------------------------------------
// Title state: one place that knows each title's library/request/watchlist
// state, so a change on the detail page updates every card showing it.
// ---------------------------------------------------------------------------

export const states = new Map();
export const pageRoots = new Set(); // live + cached page elements

const STATE_FIELDS = ["in_library", "jellyfin_id", "request", "progress", "watchlist", "watched", "rating", "favorite"];

// Rows and pages load in the background. One fetched just before a tap can
// arrive just after it, carrying the old state; a field changed here in the
// last two minutes keeps the value from the tap.
const localEdits = new Map(); // key -> { field: time changed here }
const LOCAL_WINS_MS = 120_000;

export function remember(c) {
  if (!c || !c.key) return c;
  const next = { ...(states.get(c.key) || {}) };
  const edits = localEdits.get(c.key);
  const now = Date.now();
  for (const f of STATE_FIELDS) {
    if (!(f in c)) continue;
    if (edits && edits[f] && now - edits[f] < LOCAL_WINS_MS) continue;
    next[f] = c[f];
  }
  states.set(c.key, next);
  return c;
}

export function setState(key, patch) {
  const edits = localEdits.get(key) || {};
  const now = Date.now();
  for (const f of Object.keys(patch)) edits[f] = now;
  localEdits.set(key, edits);
  const next = { ...(states.get(key) || {}), ...patch };
  states.set(key, next);
  for (const root of pageRoots) {
    root.querySelectorAll(`[data-key="${key}"]`).forEach((el) => {
      const b = el.querySelector(".poster");
      if (b) paintBadges(b, next, b.episode);
    });
  }
  document.dispatchEvent(new CustomEvent("mn:state", { detail: { key, state: next } }));
  return next;
}

// ---------------------------------------------------------------------------
// Cards
// ---------------------------------------------------------------------------

// ep: an episode's card (Recently watched): the corner says the episode's
// own rating and heart, not the show's ({favorite, rating}; it was watched).
function paintBadges(poster, s, ep = null) {
  poster.querySelectorAll(".badge, .seen").forEach((e) => e.remove());
  if (ep) s = { ...s, rating: ep.rating || null, watched: 1 };
  let badge = null;
  if (s.in_library) badge = h("span.badge.lib", { title: "In your library" }, icon("check"));
  else if (s.request === "processing" || s.request === "partial") badge = h("span.badge.dl", { title: "Downloading" }, icon("download"));
  else if (s.request === "pending") badge = h("span.badge.req", { title: "Requested" }, icon("clock"));
  else if (s.watchlist) badge = h("span.badge.wl", { title: "On your watchlist" }, icon("bookmarkFill"));
  if (badge) poster.append(badge);
  const heart = ep && ep.favorite ? h("span.ep-heart", { title: "You liked this episode" }, icon("heartFill")) : null;
  if (s.rating) poster.append(h("span.seen.rated", heart, icon("starFill"), String(s.rating)));
  else if (s.watched) poster.append(h("span.seen", heart, icon("check"), "Seen"));
}

// episode: {season, episode, favorite, rating} for an episode of the show
// (Recently watched): the card opens that episode and its corner shows the
// episode's own rating and heart.
export function posterCard(c, { meta = null, onclick = null, episode = null } = {}) {
  remember(c);
  const s = states.get(c.key) || c;
  const poster = h("div.poster");
  if (c.poster) {
    poster.append(h("img", { src: img(c.poster), alt: "", loading: "lazy", decoding: "async" }));
  } else {
    poster.append(h("div.noimg", c.title));
    poster.dataset.fill = c.key;
  }
  const pillBits = [];
  if (c.year) pillBits.push(String(c.year));
  if (c.vote) pillBits.push(h("span.star", "★"), String(c.vote));
  if (pillBits.length) poster.append(h("span.pill", pillBits));
  poster.episode = episode;
  paintBadges(poster, s, episode);
  return h("a.card", { href: episode ? episodeHref(c.tmdb_id, episode.season, episode.episode) : titleHref(c), dataset: { key: c.key }, onclick },
    poster,
    h("div.card-title", c.title),
    meta ? h("div.card-meta", meta) : null);
}

export function landscapeCard(e) {
  const src = e.backdrop ? img(e.backdrop, "w780") : (e.image_item ? `/api/v1/img/jf/${e.series_item || e.image_item}?kind=Backdrop&w=780` : "");
  const lp = h("div.lposter",
    src ? h("img", { src, alt: "", loading: "lazy", decoding: "async" }) : null,
    h("div.lt", h("b", e.title || ""), e.subtitle ? h("span", e.subtitle) : (e.runtime_min ? h("span", `${e.runtime_min} min`) : null)),
    h("span.play", icon("play")),
    e.progress ? h("div.bar", h("i", { style: { width: `${Math.max(3, e.progress)}%` } })) : null);
  // An episode (Continue watching, Next up) opens that episode; a film its page.
  const href = !e.key ? null : e.media_type === "tv" && e.season != null && e.episode != null
    ? episodeHref(e.tmdb_id, e.season, e.episode) : `/title/${e.media_type}/${e.tmdb_id}`;
  return h("a.lcard", { href: href || "#", dataset: e.key ? { key: e.key } : {} }, lp);
}

// ---------------------------------------------------------------------------
// Rows and grids
// ---------------------------------------------------------------------------

export function rowHeader(title, { sub = null, kicker = null, moreHref = null, extra = null } = {}) {
  return h("div.row-head",
    h("div.row-titles", kicker ? h("div.row-kicker", kicker) : null, h("h2.row-title", title), sub ? h("div.row-sub", sub) : null),
    extra,
    moreHref ? h("a.more-btn", { href: moreHref, "aria-label": `See all: ${title}` }, icon("right")) : null);
}

export function rowEl(row, { moreHref = null } = {}) {
  const scroller = h("div.scroller");
  for (const it of row.items) scroller.append(row.style === "landscape" ? landscapeCard(it) : posterCard(it));
  return h("section.row", rowHeader(row.title, { sub: row.subtitle, kicker: row.kicker, moreHref }), scroller);
}

export function skeletonRow(title) {
  const sc = h("div.scroller");
  for (let i = 0; i < 6; i++) sc.append(h("div.sk.sk-card"));
  return h("section.row", title ? rowHeader(title) : h("div.sk.sk-title"), sc);
}

export function skeletonGrid(n = 12) {
  const g = h("div.grid");
  for (let i = 0; i < n; i++) g.append(h("div", h("div.sk", { style: { aspectRatio: "2/3" } })));
  return g;
}

// Infinite grid: load(page) -> {items, more}. decorate(cardEl, card) can add
// controls to each card (edit mode).
export function infiniteGrid(load, { empty = "Nothing here yet.", decorate = null } = {}) {
  const grid = h("div.grid");
  const foot = h("div.loadmore");
  const wrap = h("div", grid, foot);
  let page = 0, busy = false, done = false, note = null;
  const seen = new Set();
  const io = new IntersectionObserver((ents) => { if (ents.some((e) => e.isIntersecting)) next(); }, { rootMargin: "800px" });
  async function next() {
    if (busy || done) return;
    busy = true;
    swap(foot, h("div.spin"));
    try {
      const res = await load(++page);
      for (const c of res.items) {
        if (seen.has(c.key)) continue; // pages of recommendations can overlap
        seen.add(c.key);
        const el = posterCard(c);
        if (decorate) decorate(el, c);
        grid.append(el);
      }
      fillPosters(grid);
      // Goes on until the list ends, however long (no page cap; docs/limits.md).
      done = !res.more || !res.items.length;
      // Where it ends short of the list (TMDB serves only 500 pages), it says why.
      if (done && res.note) note = res.note;
      if (page === 1 && !res.items.length) swap(wrap, h("div.empty", empty));
    } catch (e) {
      swap(foot, h("div.err-box", e.message));
      done = true;
    }
    busy = false;
    if (done) {
      io.disconnect();
      if (foot.isConnected && !foot.querySelector(".err-box")) swap(foot, note ? h("p.soft.row-note", note) : null);
      return;
    }
    // The observer only reports changes: when a short page leaves the
    // sentinel in view, it never fires again. Observing it afresh reports
    // where it is now, so the next page loads.
    swap(foot);
    io.unobserve(foot);
    io.observe(foot);
  }
  io.observe(foot);
  next();
  return wrap;
}

// Posters for cards that arrived without one (fresh Trakt lists).
const fillQueue = new Set();
let fillTimer = null;
const fillIO = new IntersectionObserver((ents) => {
  for (const e of ents) {
    if (!e.isIntersecting) continue;
    fillIO.unobserve(e.target);
    fillQueue.add(e.target.dataset.fill);
  }
  clearTimeout(fillTimer);
  fillTimer = setTimeout(flushFill, 120);
}, { rootMargin: "400px" });

export function fillPosters(root) {
  root.querySelectorAll(".poster[data-fill]").forEach((p) => fillIO.observe(p));
}

async function flushFill() {
  // 40 titles a call, or fewer if BATCH_MAX_ITEMS is lower (docs/limits.md).
  const keys = [...fillQueue].slice(0, Math.min(40, (await limits()).batch_max_items || 40));
  keys.forEach((k) => fillQueue.delete(k));
  if (!keys.length) return;
  try {
    const res = await postRead("/api/v1/titles/fill", { keys });
    for (const [k, t] of Object.entries(res.titles)) {
      for (const root of pageRoots) {
        root.querySelectorAll(`.poster[data-fill="${k}"]`).forEach((p) => {
          if (!t.poster) return;
          p.querySelector(".noimg")?.remove();
          p.prepend(h("img", { src: img(t.poster), alt: "", decoding: "async" }));
          p.removeAttribute("data-fill");
          if (!p.querySelector(".pill") && (t.year || t.vote)) {
            p.append(h("span.pill", t.year ? String(t.year) : null, t.vote ? [h("span.star", "★"), String(t.vote)] : null));
          }
        });
      }
    }
  } catch { /* posters are best-effort */ }
  if (fillQueue.size) flushFill();
}

// ---------------------------------------------------------------------------
// Toasts and sheets
// ---------------------------------------------------------------------------

export function toast(message, { error = false, action = null, ms = 3200 } = {}) {
  const t = h("div.toast" + (error ? ".err" : ""), h("span", message),
    action ? h("button", { onclick: () => { action.run(); t.remove(); } }, action.label) : null);
  document.getElementById("toasts").append(t);
  setTimeout(() => t.remove(), action ? ms + 2500 : ms);
}

export function sheet(build, { onClose = null } = {}) {
  const root = document.getElementById("sheet-root");
  const bg = h("div.sheet-bg");
  const panel = h("div.sheet", { role: "dialog", "aria-modal": "true" }, h("div.grab"));
  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    bg.classList.remove("open");
    panel.classList.remove("open");
    setTimeout(() => { bg.remove(); panel.remove(); }, 260);
    window.removeEventListener("popstate", close);
    if (onClose) onClose();
  };
  bg.addEventListener("click", close);
  append(panel, [build(close)]);
  root.append(bg, panel);
  requestAnimationFrame(() => { bg.classList.add("open"); panel.classList.add("open"); });
  window.addEventListener("popstate", close);
  return close;
}

export function confirmSheet(title, message, okLabel, onOk, { danger = true } = {}) {
  sheet((close) => h("div",
    h("h2", title), h("div.sub", message),
    h("button.btn" + (danger ? ".btn-danger" : ".btn-request"), { onclick: () => { close(); onOk(); } }, okLabel),
    h("div", { style: { height: "8px" } }),
    h("button.btn.btn-quiet", { onclick: close }, "Cancel")));
}

// Resolves with the typed text, or null if the sheet is dismissed.
export function promptSheet(title, { value = "", placeholder = "", ok = "Save", sub = null } = {}) {
  return new Promise((resolve) => {
    let answer = null;
    sheet((close) => {
      const input = h("input.input", { value, placeholder, enterkeyhint: "done" }); // no maxlength: nothing cut without a word
      const submit = () => { answer = input.value.trim() || null; close(); };
      input.addEventListener("keydown", (e) => { if (e.key === "Enter") submit(); });
      setTimeout(() => input.focus(), 260);
      return h("div", h("h2", title), sub ? h("div.sub", sub) : h("div", { style: { height: "10px" } }),
        h("div.field", input),
        h("button.btn.btn-request", { onclick: submit }, ok));
    }, { onClose: () => resolve(answer) });
  });
}

// ---------------------------------------------------------------------------
// Stars: 5 stars, each half is one point of 10.
// ---------------------------------------------------------------------------

const WORDS = { 1: "Awful", 2: "Very bad", 3: "Bad", 4: "Poor", 5: "Meh", 6: "Fine", 7: "Good", 8: "Great", 9: "Superb", 10: "Masterpiece" };

export function ratingWord(r) { return r ? `${r}/10 · ${WORDS[r]}` : ""; }

export function stars(value, onPick, { big = false } = {}) {
  const wrap = h("div.stars" + (big ? ".big" : ""), { role: "radiogroup", "aria-label": "Rating" });
  const paint = (v) => {
    [...wrap.children].forEach((b, i) => {
      const full = v >= (i + 1) * 2, half = !full && v === i * 2 + 1;
      b.className = full ? "full" : half ? "half" : "";
      swap(b, icon(full || half ? "starFill" : "star"));
    });
  };
  for (let i = 0; i < 5; i++) {
    const b = h("button", { type: "button", "aria-label": `${i + 1} stars` });
    b.addEventListener("click", (e) => {
      const r = b.getBoundingClientRect();
      const v = e.clientX - r.left < r.width / 2 ? i * 2 + 1 : i * 2 + 2;
      paint(v);
      onPick(v);
    });
    wrap.append(b);
  }
  paint(value || 0);
  wrap.paint = paint;
  return wrap;
}

export { clear };
