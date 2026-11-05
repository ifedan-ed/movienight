// Small DOM and formatting helpers shared by every view.

export const TMDB = "https://image.tmdb.org/t/p/";

export function img(path, size = "w342") {
  return path ? `${TMDB}${size}${path}` : "";
}

// h("a.card", {href: "/x", onclick: fn}, child, "text", [more])
export function h(tag, attrs, ...children) {
  const [name, ...classes] = tag.split(".");
  const el = document.createElement(name || "div");
  if (classes.length) el.className = classes.join(" ");
  if (attrs && (typeof attrs !== "object" || attrs instanceof Node || Array.isArray(attrs))) {
    children.unshift(attrs);
    attrs = null;
  }
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith("on") && typeof v === "function") el.addEventListener(k.slice(2), v);
    else if (k === "class") el.className += (el.className ? " " : "") + v;
    else if (k === "html") el.innerHTML = v;
    else if (k === "dataset") Object.assign(el.dataset, v);
    else if (k === "style" && typeof v === "object") Object.assign(el.style, v);
    else if (v === true) el.setAttribute(k, "");
    else el.setAttribute(k, v);
  }
  append(el, children);
  return el;
}

export function append(el, children) {
  for (const c of children.flat(Infinity)) {
    if (c == null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

// Like el.replaceChildren(...), but skips null/false (which replaceChildren
// would print as the text "null").
export function swap(el, ...children) {
  el.replaceChildren();
  return append(el, children);
}

export function clear(el) {
  while (el.firstChild) el.firstChild.remove();
  return el;
}

const P = {
  home: '<path d="M3 10.5 12 3l9 7.5"/><path d="M5 9v12h14V9"/><path d="M10 21v-6h4v6"/>',
  compass: '<circle cx="12" cy="12" r="9"/><path d="m15.6 8.4-2.1 5.1-5.1 2.1 2.1-5.1z"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m20 20-3.6-3.6"/>',
  lists: '<path d="M9 6h12M9 12h12M9 18h12"/><path d="M4 6h.01M4 12h.01M4 18h.01" stroke-width="3"/>',
  user: '<circle cx="12" cy="8" r="4"/><path d="M4 21c0-4.2 3.6-7 8-7s8 2.8 8 7"/>',
  play: '<path d="M7 4.6v14.8L19.5 12z" fill="currentColor" stroke="none"/>',
  download: '<path d="M12 3v12"/><path d="m7 10 5 5 5-5"/><path d="M5 20h14"/>',
  check: '<path d="m4.5 12.5 5 5 10-11"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3.2 2"/>',
  bookmark: '<path d="M6 3h12v18l-6-4.2L6 21z"/>',
  bookmarkFill: '<path d="M6 3h12v18l-6-4.2L6 21z" fill="currentColor"/>',
  star: '<path d="m12 2.8 2.8 5.8 6.3.9-4.6 4.4 1.1 6.3L12 17.2l-5.6 3 1.1-6.3-4.6-4.4 6.3-.9z"/>',
  starFill: '<path d="m12 2.8 2.8 5.8 6.3.9-4.6 4.4 1.1 6.3L12 17.2l-5.6 3 1.1-6.3-4.6-4.4 6.3-.9z" fill="currentColor"/>',
  heart: '<path d="M12 20.3s-7.6-4.6-9.6-9.2C.9 7.6 3.1 4 6.7 4c2.2 0 3.6 1.2 5.3 3.1C13.7 5.2 15.1 4 17.3 4c3.6 0 5.8 3.6 4.3 7.1-2 4.6-9.6 9.2-9.6 9.2z"/>',
  heartFill: '<path d="M12 20.3s-7.6-4.6-9.6-9.2C.9 7.6 3.1 4 6.7 4c2.2 0 3.6 1.2 5.3 3.1C13.7 5.2 15.1 4 17.3 4c3.6 0 5.8 3.6 4.3 7.1-2 4.6-9.6 9.2-9.6 9.2z" fill="currentColor"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  listPlus: '<path d="M3 6h12M3 12h12M3 18h7"/><path d="M18 13v8M14 17h8"/>',
  right: '<path d="m9 5 7 7-7 7"/>',
  left: '<path d="m15 5-7 7 7 7"/>',
  up: '<path d="m5 15 7-7 7 7"/>',
  down: '<path d="m5 9 7 7 7-7"/>',
  x: '<path d="M6 6l12 12M18 6 6 18"/>',
  sliders: '<path d="M4 7h9M17 7h3M4 12h3M11 12h9M4 17h11M19 17h1"/><circle cx="15" cy="7" r="2"/><circle cx="9" cy="12" r="2"/><circle cx="17" cy="17" r="2"/>',
  refresh: '<path d="M20 11.5A8 8 0 1 1 17.7 6"/><path d="M20 4v6h-6"/>',
  external: '<path d="M14 4h6v6"/><path d="M20 4 11 13"/><path d="M18 14v6H4V6h6"/>',
  film: '<rect x="3" y="4" width="18" height="16" rx="2.5"/><path d="M7.5 4v16M16.5 4v16M3 9h4.5M3 15h4.5M16.5 9H21M16.5 15H21"/>',
  tv: '<rect x="3" y="6.5" width="18" height="12.5" rx="2.5"/><path d="m8.5 2.5 3.5 4 3.5-4"/>',
  sparkles: '<path d="M11 3.5 12.9 8l4.6 1.9-4.6 1.9L11 16.4l-1.9-4.6L4.5 9.9 9.1 8z"/><path d="M18.5 14.5l.9 2.1 2.1.9-2.1.9-.9 2.1-.9-2.1-2.1-.9 2.1-.9z"/>',
  trash: '<path d="M4 7h16M9.5 7V4h5v3M6.5 7l1 13h9l1-13"/>',
  more: '<circle cx="5" cy="12" r="1.7" fill="currentColor" stroke="none"/><circle cx="12" cy="12" r="1.7" fill="currentColor" stroke="none"/><circle cx="19" cy="12" r="1.7" fill="currentColor" stroke="none"/>',
  calendar: '<rect x="3" y="5" width="18" height="16" rx="2.5"/><path d="M3 10h18M8 3v4M16 3v4"/>',
  eye: '<path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
  trailer: '<circle cx="12" cy="12" r="9"/><path d="m10 8.3 5.6 3.7-5.6 3.7z" fill="currentColor"/>',
  link: '<path d="M10 14a4 4 0 0 0 5.7 0l3-3A4 4 0 0 0 13 5.3l-1 1"/><path d="M14 10a4 4 0 0 0-5.7 0l-3 3A4 4 0 0 0 11 18.7l1-1"/>',
  filter: '<path d="M3 5h18l-7 8.5V20l-4-2v-4.5z"/>',
  edit: '<path d="M4 20h4L19 9l-4-4L4 16z"/><path d="m13.5 6.5 4 4"/>',
  people: '<circle cx="9" cy="8" r="3.5"/><path d="M2.5 20c0-3.6 2.9-6 6.5-6s6.5 2.4 6.5 6"/><path d="M16 4.6a3.5 3.5 0 0 1 0 6.8M18 14.3c2.2.7 3.5 2.7 3.5 5.7"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v6M12 7.5v.01"/>',
  tick: '<circle cx="12" cy="12" r="9.5"/><path d="m7.5 12.3 3 3 6-6.6"/>',
  tickFill: '<circle cx="12" cy="12" r="10" fill="currentColor" stroke="none"/><path d="m7.5 12.3 3 3 6-6.6" stroke="#0d0f14" stroke-width="2.4"/>',
  mail: '<rect x="3" y="5" width="18" height="14" rx="2.5"/><path d="m3.5 6.5 8.5 7 8.5-7"/>',
  key: '<circle cx="8" cy="15" r="4.5"/><path d="m11.2 11.8 8.8-8.8M17 6l2.5 2.5M14.5 8.5 16.5 10.5"/>',
  plug: '<path d="M9 3v5M15 3v5"/><path d="M6 8h12v3a6 6 0 0 1-12 0z"/><path d="M12 17v4"/>',
  sort: '<path d="M7 4v16M3.5 16.5 7 20l3.5-3.5"/><path d="M13 6h8M13 11h6M13 16h4"/>',
  copy: '<rect x="8" y="8" width="12" height="12" rx="2.5"/><path d="M16 8V5.5A1.5 1.5 0 0 0 14.5 4h-9A1.5 1.5 0 0 0 4 5.5v9A1.5 1.5 0 0 0 5.5 16H8"/>',
  subtitles: '<rect x="3" y="5" width="18" height="14" rx="2.5"/><path d="M7 15h4M13 15h4M7 11h2M11 11h6"/>',
};

export function icon(name, cls = "") {
  const span = document.createElement("span");
  span.innerHTML = `<svg class="i ${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${P[name] || ""}</svg>`;
  return span.firstChild;
}

export function runtime(min) {
  if (!min) return "";
  const hr = Math.floor(min / 60), m = min % 60;
  return hr ? `${hr}h ${m ? m + "m" : ""}`.trim() : `${m}m`;
}

export function fmtDate(iso, opts = { month: "short", day: "numeric", year: "numeric" }) {
  if (!iso) return "";
  const d = new Date(iso.length <= 10 ? iso + "T12:00:00" : iso);
  return isNaN(d) ? "" : d.toLocaleDateString(undefined, opts);
}

export function ago(iso) {
  if (!iso) return "";
  const s = (Date.now() - new Date(iso).getTime()) / 1000;
  if (s < 90) return "just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  if (s < 86400 * 30) return `${Math.round(s / 86400)} d ago`;
  return fmtDate(iso);
}

export function until(iso) {
  if (!iso) return "";
  const s = (new Date(iso).getTime() - Date.now()) / 1000;
  if (s <= 60) return "almost done";
  if (s < 3600) return `${Math.round(s / 60)} min left`;
  if (s < 86400) return `${Math.round(s / 3600)} h left`;
  return `${Math.round(s / 86400)} d left`;
}

export function debounce(fn, ms) {
  let t;
  return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
}

export function store(key, value) {
  try {
    if (value === undefined) return JSON.parse(localStorage.getItem("mn:" + key));
    localStorage.setItem("mn:" + key, JSON.stringify(value));
  } catch { return null; }
  return value;
}

// Where a watch came from, as shown to you. Trakt history is this install's own copy now.
export const SOURCE_LABEL = {
  manual: "Marked here", jellyfin: "Jellyfin", trakt: "Shared", api: "Other app",
  player: "Played from here", kodi: "Kodi",
};

export function sourceLabel(s) {
  return SOURCE_LABEL[s] || s;
}

export function titleHref(c) {
  return `/title/${c.media_type}/${c.tmdb_id}`;
}

// Where one episode opens: its season page, scrolled to it and opened
// (Recently watched, History, Continue watching, Up next).
export function episodeHref(showId, season, episode) {
  return `/title/tv/${showId}/season/${season}?episode=${episode}`;
}

// Trakt list descriptions are Markdown; show them as plain text.
export function plain(md) {
  return (md || "")
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/(\*\*|__)(.*?)\1/g, "$2")
    .replace(/(^|\s)[*_]([^*_\n]+)[*_](?=\s|$|[.,!?;:])/g, "$1$2")
    .replace(/^#+\s*/gm, "")
    .replace(/^\s*>\s?/gm, "")
    .replace(/`([^`]*)`/g, "$1")
    .replace(/[*_]{2,}/g, "")
    .replace(/\s+/g, " ")
    .trim();
}

export function initials(name) {
  return (name || "?").trim().split(/\s+/).map((w) => w[0]).slice(0, 2).join("").toUpperCase();
}

// "2 titles of this MDBList list can't be shown here: MDBList has no TMDB id for them."
export function notShown(n) {
  return `${n.toLocaleString()} ${n === 1 ? "title" : "titles"} of this MDBList list can't be shown here: MDBList has no TMDB id for ${n === 1 ? "it" : "them"}.`;
}
