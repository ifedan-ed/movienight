// Everything you can do to a title, shared by the title page, search results
// and the rate bar: request, watchlist, watched, rating, favourite, lists.
import { del, get, post, put } from "./api.js";
import { playSheet } from "./play.js";
import { setState, sheet, stars, ratingWord, states, toast, promptSheet } from "./ui.js";
import { fmtDate, h, icon, sourceLabel, swap, until } from "./util.js";

const base = (c) => `/api/v1/title/${c.media_type}/${c.tmdb_id}`;

// The request and status replies carry the whole title state, but only these
// fields are theirs to change. Watchlist, favorite, rating and watched come
// from your taps: a status reply worked out a moment before a tap must not
// switch it back (that was the "tap twice to add" bug).
const REQUEST_FIELDS = ["in_library", "jellyfin_id", "jellyfin_url", "request", "progress", "requested_at"];

function requestPart(st) {
  const out = {};
  for (const f of REQUEST_FIELDS) if (st && f in st) out[f] = st[f];
  return out;
}

// One write at a time per title and field; a second tap while the first is
// still saving is ignored instead of racing it.
const inflight = new Set();

async function once(id, fn) {
  if (inflight.has(id)) return null;
  inflight.add(id);
  try { return await fn(); } finally { inflight.delete(id); }
}

// ---------------------------------------------------------------------------
// The big button: Play / Request / Requested / Downloading
// ---------------------------------------------------------------------------

// next: for shows, {season, episode, label} of the episode Play starts.
export function primaryButton(c, s, { compact = false, onChange = null, next = null } = {}) {
  const wrap = h("div");
  const paint = (st) => {
    swap(wrap, ...buttonFor(c, st, compact, (n) => { paint(n); onChange && onChange(n); }, wrap.next));
  };
  wrap.next = next;
  paint(s);
  wrap.paint = paint;
  return wrap;
}

function buttonFor(c, s, compact, repaint, next) {
  if (s.in_library) {
    const label = next ? next.label : (compact ? "Play" : "Play");
    const btn = h("button.btn.btn-play", { type: "button" }, icon("play"), label);
    btn.addEventListener("click", (e) => {
      e.preventDefault();
      e.stopPropagation();
      playSheet(c, next ? { season: next.season, episode: next.episode } : {});
    });
    return [btn];
  }
  const req = s.request;
  if (req === "processing" || req === "partial") {
    const p = s.progress;
    const label = p ? `Downloading ${p.percent}%${p.eta ? " · " + until(p.eta) : ""}` : (compact ? "Requested" : "Requested · waiting for a download");
    const btn = h("div.btn.btn-status", { role: "status" },
      p ? h("i.fill", { style: { width: `${p.percent}%` } }) : null,
      icon(p ? "download" : "clock"), h("span", label));
    return compact ? [btn] : [btn, h("div.btn-note", req === "partial" ? "Some episodes are already in Jellyfin" : "It appears in Jellyfin by itself when it's done")];
  }
  if (req === "pending") {
    const btn = h("div.btn.btn-status", { role: "status" }, icon("clock"), h("span", compact ? "Requested" : "Requested · waiting for approval"));
    return [btn];
  }
  if (req === "dry_run") {
    return [h("div.btn.btn-status", icon("info"), h("span", "Test mode: request not sent"))];
  }
  const btn = h("button.btn.btn-request", { type: "button" }, icon("download"), h("span", "Request"));
  btn.addEventListener("click", async (e) => {
    e.preventDefault();
    e.stopPropagation();
    btn.disabled = true;
    swap(btn, h("span.spin"), h("span", "Requesting…"));
    try {
      const res = await post(`${base(c)}/request`);
      const next = setState(c.key, requestPart(res.state));
      toast(res.already ? "Already requested — showing its status" : `Requested ${c.title || ""}`.trim());
      repaint(next);
      startPolling(c, repaint);
    } catch (err) {
      toast(err.message, { error: true });
      btn.disabled = false;
      swap(btn, icon("download"), h("span", "Request"));
    }
  });
  return compact ? [btn] : [btn, h("div.btn-note", "Sends it to Seerr, which downloads it into Jellyfin")];
}

// Live status while a title is requested/downloading and its page is open.
const pollers = new Map();
export function startPolling(c, repaint) {
  stopPolling(c.key);
  const tick = async () => {
    // Stop once the title page is gone; its onShow restarts polling.
    if (!document.querySelector(`[data-poll="${c.key}"]`)) return stopPolling(c.key);
    if (!document.hidden) {
      try {
        const st = await get(`${base(c)}/status`, { fresh: true });
        repaint(setState(c.key, requestPart(st)));
        if (st.in_library || !st.request) return stopPolling(c.key);
      } catch { /* try again next tick */ }
    }
    pollers.set(c.key, setTimeout(tick, 8000));
  };
  pollers.set(c.key, setTimeout(tick, 5000));
}
export function stopPolling(key) {
  clearTimeout(pollers.get(key));
  pollers.delete(key);
}

// ---------------------------------------------------------------------------
// Watchlist, favourite
// ---------------------------------------------------------------------------

export function toggleWatchlist(c) {
  return once(`${c.key}:watchlist`, async () => {
    const on = !(states.get(c.key) || {}).watchlist;
    setState(c.key, { watchlist: on });
    try {
      if (on) await post(`${base(c)}/watchlist`);
      else await del(`${base(c)}/watchlist`);
      toast(on ? `Added ${c.title || ""} to your watchlist`.replace("  ", " ") : `Removed ${c.title || ""} from your watchlist`.replace("  ", " "), {
        action: { label: "Undo", run: () => toggleWatchlist(c) },
      });
    } catch (e) {
      setState(c.key, { watchlist: !on });
      toast(e.message, { error: true });
    }
    return on;
  });
}

export function toggleFavorite(c) {
  return once(`${c.key}:favorite`, async () => {
    const on = !(states.get(c.key) || {}).favorite;
    setState(c.key, { favorite: on });
    try {
      if (on) await post(`${base(c)}/favorite`);
      else await del(`${base(c)}/favorite`);
      toast(on ? `Added ${c.title || ""} to your favorites`.replace("  ", " ") : `Removed ${c.title || ""} from your favorites`.replace("  ", " "), {
        action: { label: "Undo", run: () => toggleFavorite(c) },
      });
    } catch (e) {
      setState(c.key, { favorite: !on });
      toast(e.message, { error: true });
    }
    return on;
  });
}

// ---------------------------------------------------------------------------
// Rating
// ---------------------------------------------------------------------------

// review: undefined leaves the saved comment as it is; "" clears it.
export async function saveRating(c, value, review) {
  const prev = (states.get(c.key) || {}).rating || null;
  setState(c.key, { rating: value });
  try {
    if (value) await post(`${base(c)}/rating`, review === undefined ? { rating: value } : { rating: value, review });
    else await del(`${base(c)}/rating`);
  } catch (e) {
    setState(c.key, { rating: prev });
    toast(e.message, { error: true });
    throw e;
  }
}

// Stars and an optional comment (people on this Movie Night can read it;
// it also tells the AI picks what you thought). An episode's rating (the
// season page) uses the same sheet with its own `current`, `save`, wording
// and no comment.
export function rateSheet(c, onDone, { review = "", current = null, save: saveFn = null, heading = null, sub = null, name = null, comment = true } = {}) {
  if (current == null) current = (states.get(c.key) || {}).rating || 0;
  const write = saveFn || ((v, text) => saveRating(c, v, text));
  const what = name || c.title;
  let value = current;
  sheet((close) => {
    const label = h("div.rate-label", ratingWord(current) || "Tap a star — left half is half a star");
    const save = h("button.btn.btn-request", { disabled: !value }, current ? "Save" : "Save rating");
    const st = stars(current, (v) => { value = v; label.textContent = ratingWord(v); save.disabled = false; }, { big: true });
    // No maxlength (it would cut pasted text without a word): a comment over
    // the install's TEXT_MAX_CHARS is refused in plain words and stays here.
    const note = comment ? h("textarea.input.note-in", { rows: "3", placeholder: "What did you think? (optional)" }, review || "") : null;
    save.addEventListener("click", async () => {
      if (!value) return;
      save.disabled = true;
      const text = note ? note.value.trim() : undefined;
      try {
        await write(value, text);
        toast(`Rated ${what}: ${value}/10`);
        onDone && onDone(value, text || null);
        close();
      } catch { save.disabled = false; }
    });
    return h("div.rate-sheet",
      h("h2", heading || `Rate ${c.title}`),
      h("div.sub", sub || "Your rating out of 10"),
      st, label,
      note ? h("div.field", note) : null,
      h("div", { style: { height: "10px" } }),
      save,
      current ? h("button.btn.btn-quiet", { style: { marginTop: "8px" }, onclick: async () => {
        try { await write(null); } catch { return; }
        toast("Rating removed"); onDone && onDone(null, null); close();
      } }, icon("trash"), "Remove rating") : null);
  });
}

// ---------------------------------------------------------------------------
// One episode: its own heart and stars (the season page, Up next on a show's
// page). They act on that episode only, never on the show.
// ---------------------------------------------------------------------------

const epUrl = (showId, season, n, what) => `/api/v1/title/tv/${showId}/episode/${season}/${n}/${what}`;

// e: {number, favorite}. The button repaints only itself; a second tap while
// the first is still saving is ignored instead of racing it.
export function episodeHeart(showId, season, e, { onChange = null } = {}) {
  const btn = h("button.ep-like");
  const paint = (v) => {
    e.favorite = v;
    btn.classList.toggle("on", v);
    btn.setAttribute("aria-pressed", String(v));
    btn.setAttribute("aria-label", v ? `Unlike episode ${e.number}` : `Like episode ${e.number}`);
    swap(btn, icon(v ? "heartFill" : "heart"));
  };
  paint(!!e.favorite);
  btn.addEventListener("click", (ev) => {
    ev.preventDefault();
    ev.stopPropagation();
    once(`ep:${showId}:${season}:${e.number}:favorite`, async () => {
      const on = !e.favorite;
      paint(on);
      try {
        if (on) await post(epUrl(showId, season, e.number, "favorite"));
        else await del(epUrl(showId, season, e.number, "favorite"));
        onChange && onChange(on);
      } catch (err) {
        paint(!on);
        toast(err.message, { error: true });
      }
    });
  });
  return btn;
}

// e: {number, name, rating}. Opens the title's rating sheet for this episode
// (stars only, no comment); the button shows the episode's rating under the star.
export function episodeStars(showId, season, e, { showTitle = "", onChange = null } = {}) {
  const btn = h("button.ep-rate");
  const paint = () => {
    const v = e.rating || null;
    btn.classList.toggle("on", !!v);
    btn.setAttribute("aria-label", v ? `Episode ${e.number} rated ${v}/10; change the rating` : `Rate episode ${e.number}`);
    swap(btn, icon(v ? "starFill" : "star"), v ? h("span.ep-rate-n", String(v)) : null);
  };
  paint();
  btn.addEventListener("click", (ev) => {
    ev.preventDefault();
    ev.stopPropagation();
    const url = epUrl(showId, season, e.number, "rating");
    rateSheet({ title: showTitle }, null, {
      current: e.rating || 0,
      heading: `Rate episode ${e.number}`,
      sub: `${[showTitle, `S${season}·E${e.number}`, e.name].filter(Boolean).join(" · ")}. Your rating out of 10, for this episode only`,
      name: `episode ${e.number}`,
      comment: false,
      save: async (v) => {
        const before = e.rating || null;
        e.rating = v;
        paint();
        try {
          if (v) await put(url, { rating: v });
          else await del(url);
          onChange && onChange(v);
        } catch (err) {
          e.rating = before;
          paint();
          toast(err.message, { error: true });
          throw err;
        }
      },
    });
  });
  return btn;
}

// ---------------------------------------------------------------------------
// Watched
// ---------------------------------------------------------------------------

export function watchedSheet(c, watches, onDone) {
  // That day's evening here, with this browser's offset. A bare date would be
  // read as the evening in the server's time zone (New York), which in Paris is
  // already the next day.
  const pad = (n) => String(n).padStart(2, "0");
  const evening = (d) => {
    const e = new Date(d.getFullYear(), d.getMonth(), d.getDate(), 20);
    const off = -e.getTimezoneOffset();
    return `${e.getFullYear()}-${pad(e.getMonth() + 1)}-${pad(e.getDate())}T20:00:00${off < 0 ? "-" : "+"}${pad(Math.floor(Math.abs(off) / 60))}:${pad(Math.abs(off) % 60)}`;
  };
  sheet((close) => {
    const mark = async (when) => {
      try {
        const res = await post(`${base(c)}/watched`, { watched_at: when });
        const count = res.watches.length;
        setState(c.key, { watched: count, ...(c.media_type === "movie" ? { watchlist: false } : {}) });
        toast(`Marked ${c.title} as watched`);
        onDone && onDone(res.watches);
        close();
      } catch (e) { toast(e.message, { error: true }); }
    };
    const yest = new Date(Date.now() - 86400000);
    const list = h("div.watches");
    for (const w of watches || []) {
      list.append(h("div.watch",
        h("div.w-main", h("b", fmtDate(w.watched_at)), h("span", (w.sources || []).map(sourceLabel).join(" + "))),
        h("button.iconbtn", { "aria-label": "Remove this watch", onclick: async () => {
          try {
            const res = await post(`${base(c)}/unwatch`, { ids: w.ids });
            setState(c.key, { watched: res.watches.length });
            toast("Removed from history");
            onDone && onDone(res.watches);
            close();
          } catch (e) { toast(e.message, { error: true }); }
        } }, icon("trash"))));
    }
    return h("div",
      h("h2", (watches || []).length ? "Watched again?" : "Mark as watched"),
      h("div.sub", c.title),
      h("button.opt", { onclick: () => mark(null) }, icon("check"), "Just now"),
      h("button.opt", { onclick: () => mark(evening(yest)) }, icon("calendar"), "Yesterday"),
      h("div.sec-title", { style: { marginTop: "14px" } }, "Another day"),
      dayPicker(mark, evening),
      (watches || []).length ? [h("div.sec-title", { style: { marginTop: "18px" } }, "Your watches"), list] : null);
  });
}

// A month of days drawn in the sheet, as big as the sheet: the browser's own
// date picker is a small grey box on a computer. Sunday first; nothing after
// today; the month and year chosen from lists, or a month at a time.
const WEEKDAYS = ["S", "M", "T", "W", "T", "F", "S"];

function dayPicker(onPick, evening) {
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  let month = new Date(today.getFullYear(), today.getMonth(), 1);
  let picked = new Date(today);
  const box = h("div.cal");
  const go = (y, m) => { month = new Date(y, m, 1); render(); };
  const render = () => {
    const y = month.getFullYear(), m = month.getMonth();
    const cells = [];
    for (let i = 0; i < month.getDay(); i++) cells.push(h("span"));
    for (let d = 1, n = new Date(y, m + 1, 0).getDate(); d <= n; d++) {
      const day = new Date(y, m, d);
      const on = day.getTime() === picked.getTime();
      cells.push(h("button.cal-day" + (on ? ".on" : "") + (day.getTime() === today.getTime() ? ".today" : ""), {
        type: "button", disabled: day > today, "aria-pressed": on ? "true" : "false",
        "aria-label": day.toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long", year: "numeric" }),
        onclick: () => { picked = day; render(); },
      }, String(d)));
    }
    const thisYear = today.getFullYear();
    const monthSel = h("select.cal-sel", { "aria-label": "Month", onchange: (e) => go(y, Number(e.target.value)) },
      Array.from({ length: 12 }, (_, i) => h("option", { value: i, selected: i === m, disabled: y === thisYear && i > today.getMonth() },
        new Date(2000, i, 1).toLocaleDateString(undefined, { month: "short" }))));
    const yearSel = h("select.cal-sel", { "aria-label": "Year", onchange: (e) => { const yr = Number(e.target.value); go(yr, yr === thisYear ? Math.min(m, today.getMonth()) : m); } },
      Array.from({ length: thisYear - 1949 }, (_, i) => thisYear - i).map((yr) => h("option", { value: yr, selected: yr === y }, String(yr))));
    swap(box,
      h("div.cal-head",
        h("button.iconbtn", { type: "button", "aria-label": "Previous month", onclick: () => go(y, m - 1) }, icon("left")),
        h("div.cal-title", monthSel, yearSel),
        h("button.iconbtn", { type: "button", "aria-label": "Next month", disabled: new Date(y, m + 1, 1) > today, onclick: () => go(y, m + 1) }, icon("right"))),
      h("div.cal-grid.cal-week", WEEKDAYS.map((w) => h("span", w))),
      h("div.cal-grid", cells),
      h("button.btn.btn-request.cal-save", { type: "button", onclick: () => onPick(evening(picked)) },
        `Save ${picked.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" })}`));
  };
  render();
  return box;
}

// ---------------------------------------------------------------------------
// Lists
// ---------------------------------------------------------------------------

export async function listsSheet(c) {
  let data;
  try { data = await get(`${base(c)}/lists`, { fresh: true }); }
  catch (e) { toast(e.message, { error: true }); return; }
  sheet((close) => {
    const box = h("div");
    const render = () => {
      swap(box, ...data.lists.map((l) => {
        const check = h("span.check" + (l.in_list ? ".on" : ""), l.in_list ? icon("check") : null);
        return h("button.opt", { onclick: async () => {
          const was = l.in_list;
          l.in_list = !was;
          render();
          try {
            if (was) await del(`/api/v1/lists/${l.id}/items/${c.media_type}/${c.tmdb_id}`);
            else await post(`/api/v1/lists/${l.id}/items`, { media_type: c.media_type, tmdb_id: c.tmdb_id });
            toast(was ? `Removed from ${l.name}` : `Added to ${l.name}`);
          } catch (e) { l.in_list = was; render(); toast(e.message, { error: true }); }
        } }, icon("lists"), h("span", { style: { flex: "1" } }, l.name), check);
      }));
    };
    render();
    return h("div",
      h("h2", "Add to a list"),
      h("div.sub", data.lists.length ? c.title : "Shared and MDBList lists are fixed; make a list of your own to add titles."),
      h("button.opt", { style: { color: "var(--accent)" }, onclick: async () => {
        close();
        const name = await promptSheet("New list", { placeholder: "e.g. Rainy Sunday films", ok: "Create and add" });
        if (!name) return;
        try {
          const res = await post("/api/v1/lists", { name });
          await post(`/api/v1/lists/${res.id}/items`, { media_type: c.media_type, tmdb_id: c.tmdb_id });
          toast(`Added to ${name}`);
        } catch (e) { toast(e.message, { error: true }); }
      } }, icon("plus"), "New list"),
      box);
  });
}
