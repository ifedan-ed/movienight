// Router that behaves like a native app's tab bar:
// - Back re-attaches the previous page exactly as it was (scroll position,
//   every row's sideways position, loaded content), never rebuilding it.
// - Each tab remembers the page you were on. Tapping another tab returns
//   there; tapping the tab you're in goes back to its first page, as you left
//   it; tapping it again on that first page scrolls to the top.
import { pageRoots } from "./ui.js";
import { h, icon, swap } from "./util.js";

const ROUTES = [
  [/^\/$/, "home"],
  [/^\/browse$/, "browse"],
  [/^\/discover$/, "discover"],
  [/^\/search$/, "search"],
  [/^\/lists$/, "lists"],
  [/^\/list\/(\d+)$/, "list"],
  [/^\/collection\/(watchlist|favorites|rated|ratings)$/, "collection"],
  [/^\/mdblist$/, "mdblist"],
  [/^\/mdblist\/(\d+)$/, "mdblist"],
  [/^\/title\/(movie|tv)\/(\d+)$/, "title"],
  [/^\/title\/tv\/(\d+)\/season\/(\d+)$/, "season"],
  [/^\/person\/(\d+)$/, "person"],
  [/^\/play\/([A-Za-z0-9_-]{16,40})$/, "playdone"],
  [/^\/profile$/, "profile"],
  [/^\/history$/, "history"],
  [/^\/requests$/, "requests"],
  [/^\/settings\/home$/, "homerows"],
  [/^\/settings\/jellyfin$/, "jellyfin"],
  [/^\/settings\/players$/, "players"],
  [/^\/settings\/emails$/, "emails"],
  [/^\/settings\/kodi$/, "kodi"],
];

// [tab id, root URL, label, icon, views that belong to the tab, root view]
const TABS = [
  ["home", "/", "Home", "home", ["home", "browse", "homerows"], "home"],
  ["discover", "/discover", "Discover", "compass", ["discover"], "discover"],
  ["search", "/search", "Search", "search", ["search"], "search"],
  ["lists", "/lists", "Lists", "lists", ["lists", "list", "collection", "mdblist"], "lists"],
  ["profile", "/profile", "Profile", "user", ["profile", "history", "requests", "jellyfin", "players", "emails", "kodi"], "profile"],
];

const MAX_PAGES = 16; // rendered pages kept for an instant Back; the oldest unprotected ones go first
const SIDE_SCROLLERS = ".scroller, .hero-track, .chips, .people, .seasons";
const view = document.getElementById("view");
const pages = new Map();   // history entry id -> page (one page can sit under several ids)
const tabRoot = new Map(); // tab -> its first page
const tabTop = new Map();  // tab -> the last page shown in it
const listeners = [];
let current = null;
let seq = 0;
let activeTab = "home";
let visits = 0;

function match(pathname) {
  for (const [re, name] of ROUTES) {
    const m = pathname.match(re);
    if (m) return { name, params: m.slice(1) };
  }
  return { name: "home", params: [] };
}

function tabFor(name) {
  // Titles and people stay in whichever tab opened them.
  const t = TABS.find((x) => x[4].includes(name));
  return t ? t[0] : activeTab;
}

function paintTabbar() {
  const nav = document.getElementById("tabbar");
  if (!nav.childElementCount) {
    for (const [id, href, label, ic] of TABS) {
      nav.append(h("a", { href, dataset: { tab: id } }, icon(ic), h("span", label)));
    }
    nav.addEventListener("click", (e) => {
      const a = e.target.closest("a[data-tab]");
      if (!a) return;
      e.preventDefault();
      tapTab(a.dataset.tab);
    });
  }
  nav.querySelectorAll("a").forEach((a) => {
    const on = a.dataset.tab === activeTab;
    a.classList.toggle("on", on);
    if (on) a.setAttribute("aria-current", "page"); else a.removeAttribute("aria-current");
  });
}

// Taking a page out of the document resets its rows' sideways scroll, so
// remember each position on the way out and put it back on the way in.
function saveScroll(page) {
  page.scroll = window.scrollY;
  page.side = [...page.el.querySelectorAll(SIDE_SCROLLERS)].map((el) => el.scrollLeft);
}

function restoreScroll(page) {
  const apply = () => {
    window.scrollTo(0, page.scroll || 0);
    if (!page.side) return;
    page.el.querySelectorAll(SIDE_SCROLLERS).forEach((el, i) => {
      if (page.side[i] && el.scrollLeft !== page.side[i]) el.scrollLeft = page.side[i];
    });
  };
  apply();
  // WebKit can settle layout (and snap) a frame later; apply once more.
  requestAnimationFrame(() => requestAnimationFrame(apply));
}

function evict() {
  const protectedPages = new Set([current, ...tabRoot.values(), ...tabTop.values()]);
  const unique = [...new Set(pages.values())].sort((a, b) => a.visit - b.visit);
  let excess = unique.length - MAX_PAGES;
  for (const p of unique) {
    if (excess <= 0) break;
    if (protectedPages.has(p)) continue;
    for (const [id, q] of pages) if (q === p) pages.delete(id);
    pageRoots.delete(p.el);
    excess -= 1;
  }
}

function enter(page) {
  page.visit = ++visits;
  activeTab = page.tab;
  tabTop.set(page.tab, page);
  if (page.name === TABS.find((t) => t[0] === page.tab)?.[5]) tabRoot.set(page.tab, page);
  paintTabbar();
  listeners.forEach((fn) => fn(page.name));
}

async function show(id, url) {
  if (current) {
    saveScroll(current);
    current.hooks?.onHide?.();
    current.el.remove();
  }
  const cached = pages.get(id);
  if (cached && cached.url === url) {
    current = cached;
    applyGlow(cached.glow);
    view.append(cached.el);
    restoreScroll(cached);
    enter(cached);
    cached.hooks?.onShow?.();
    return;
  }

  const u = new URL(url, location.origin);
  const { name, params } = match(u.pathname);
  const page = { el: h("div.page"), scroll: 0, side: null, url, hooks: null, name, glow: null, tab: tabFor(name), visit: 0 };
  pages.set(id, page);
  pageRoots.add(page.el);
  current = page;
  applyGlow(null);
  view.append(page.el);
  window.scrollTo(0, 0);
  enter(page);
  evict();
  try {
    const mod = await import(`./views/${name}.js?v=${document.documentElement.dataset.v || ""}`);
    page.hooks = (await mod.default(page.el, { params, query: u.searchParams, url })) || null;
  } catch (e) {
    console.error(e);
    swap(page.el, h("div.err-box", { style: { marginTop: "calc(var(--safe-t) + 20px)" } },
      "This page didn't load: ", e.message || String(e)));
  }
}

// Show an existing page again under a new history entry.
function revisit(page) {
  seq += 1;
  pages.set(seq, page);
  history.pushState({ id: seq }, "", page.url);
  show(seq, page.url);
}

function tapTab(tab) {
  const root = tabRoot.get(tab);
  if (tab === activeTab) {
    if (current && current === root) {
      window.scrollTo({ top: 0, behavior: "smooth" });
      return;
    }
    if (root) return revisit(root);
    return go(TABS.find((t) => t[0] === tab)[1]);
  }
  const target = tabTop.get(tab) || root;
  if (target) return revisit(target);
  go(TABS.find((t) => t[0] === tab)[1]);
}

function applyGlow(color) {
  if (color) document.documentElement.style.setProperty("--glow", color);
  else document.documentElement.style.removeProperty("--glow");
}

export function go(url, { replace = false } = {}) {
  if (!replace && url === location.pathname + location.search) {
    window.scrollTo({ top: 0, behavior: "smooth" });
    return;
  }
  seq += 1;
  history[replace ? "replaceState" : "pushState"]({ id: seq }, "", url);
  show(seq, url);
}

// Back within the app, or to a sensible page when opened directly.
export function back(fallback = "/") {
  if (history.state && history.state.id > 1 && pages.size > 1) history.back();
  else go(fallback, { replace: true });
}

// Change the current page's URL (tab, filters) without leaving it.
export function setQuery(params) {
  const u = new URL(location.href);
  for (const [k, v] of Object.entries(params)) {
    if (v == null || v === "") u.searchParams.delete(k);
    else u.searchParams.set(k, v);
  }
  const url = u.pathname + u.search;
  history.replaceState(history.state, "", url);
  if (current) current.url = url;
}

export function setGlow(color) {
  if (current) current.glow = color;
  applyGlow(color);
}

export function currentName() {
  return current ? current.name : null;
}

export function onNavigate(fn) {
  listeners.push(fn);
}

export function start() {
  // We restore scroll ourselves; the browser's own restore would fight it.
  if ("scrollRestoration" in history) history.scrollRestoration = "manual";
  window.addEventListener("popstate", (e) => {
    const id = e.state && e.state.id ? e.state.id : ++seq;
    seq = Math.max(seq, id);
    show(id, location.pathname + location.search);
  });
  document.addEventListener("click", (e) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const a = e.target.closest("a[href]");
    // data-native: a server page outside the app shell (e.g. /account), loaded as a page.
    if (!a || a.target === "_blank" || a.hasAttribute("download") || a.hasAttribute("data-native")) return;
    const href = a.getAttribute("href");
    if (!href.startsWith("/") || href.startsWith("/api/") || href.startsWith("/static/")) return;
    e.preventDefault();
    go(href);
  });
  seq = (history.state && history.state.id) || 1;
  history.replaceState({ id: seq }, "", location.pathname + location.search);
  show(seq, location.pathname + location.search);
}
