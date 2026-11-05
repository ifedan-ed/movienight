// Home: For You / Movies / Shows, a hero, then the rows chosen in Home rows.
import { get } from "../api.js";
import { primaryButton } from "../actions.js";
import { setGlow, setQuery } from "../nav.js";
import { fillPosters, remember, rowEl, rowHeader, skeletonRow, states } from "../ui.js";
import { h, icon, img, store, swap } from "../util.js";

const TABS = [["for_you", "For You", "sparkles"], ["movies", "Movies", "film"], ["shows", "Shows", "tv"]];
const GLOW = { for_you: "rgba(245,183,59,.26)", movies: "rgba(232,72,96,.26)", shows: "rgba(46,196,182,.24)" };

function moreHref(row, tab) {
  if (row.list_id) return `/list/${row.list_id}`;
  if (row.id === "watchlist") return "/collection/watchlist";
  if (row.id === "favorites") return "/collection/favorites";
  if (row.id === "rated") return "/collection/rated";
  if (row.id === "ratings") return "/collection/ratings";
  if (row.more) return `/browse?key=${encodeURIComponent(row.more.key)}&tab=${tab}&title=${encodeURIComponent(row.more.title)}`;
  return null;
}

function hero(items, tab) {
  if (!items.length) return null;
  const track = h("div.hero-track");
  const dots = h("div.dots");
  items.forEach((c, i) => {
    remember(c);
    const meta = [c.year, c.vote ? `★ ${c.vote}` : null, (c.genres || []).slice(0, 2).join(", ")].filter(Boolean).join(" · ");
    track.append(h("div.slide", { dataset: { key: c.key } },
      h("img.bd", { src: img(c.backdrop, "w1280"), alt: "", loading: i ? "lazy" : "eager", decoding: "async" }),
      h("div.info",
        h("div.kicker", c.hero_label || "Featured"),
        h("h2", c.title),
        h("div.meta", meta),
        c.overview ? h("p.ov", c.overview) : null,
        h("div.btns",
          primaryButton(c, states.get(c.key) || c, { compact: true }),
          h("a.btn.btn-ghost", { href: `/title/${c.media_type}/${c.tmdb_id}` }, icon("info"), "Details")))));
    dots.append(h("i", { class: i ? "" : "on" }));
  });
  track.addEventListener("scroll", () => {
    const i = Math.round(track.scrollLeft / track.clientWidth);
    [...dots.children].forEach((d, j) => d.classList.toggle("on", i === j));
  }, { passive: true });
  return h("section.hero", track, items.length > 1 ? dots : null);
}

export default async function render(root, { query }) {
  let tab = query.get("tab") || store("tab") || "for_you";
  if (!GLOW[tab]) tab = "for_you";

  const chips = h("div.chips", { role: "tablist" });
  const body = h("div");
  root.append(
    h("header.head", h("h1", "Movie Night"),
      h("a.iconbtn", { href: "/settings/home", "aria-label": "Choose Home rows" }, icon("sliders"))),
    chips, body);

  let token = 0;
  const load = async () => {
    const mine = ++token;
    setGlow(GLOW[tab]);
    store("tab", tab);
    setQuery({ tab: tab === "for_you" ? null : tab });
    swap(chips, ...TABS.map(([id, label, ic]) => h("button.chip" + (id === tab ? ".on" : ""), {
      role: "tab", "aria-selected": String(id === tab),
      onclick: () => { if (id !== tab) { tab = id; load(); } },
    }, icon(ic), label)));
    swap(body, h("div.hero", h("div.sk", { style: { aspectRatio: "4/3.3", borderRadius: "20px" } })));

    let spec;
    try { spec = await get(`/api/v1/home?tab=${tab}`); }
    catch (e) { if (mine === token) swap(body, h("div.err-box", e.message)); return; }
    if (mine !== token) return;

    const heroSlot = h("div");
    const rowsBox = h("div");
    swap(body, heroSlot, rowsBox);
    get(`/api/v1/hero?tab=${tab}`).then((res) => {
      if (mine !== token) return;
      const el = hero(res.items, tab);
      swap(heroSlot, ...(el ? [el] : []));
    }).catch(() => swap(heroSlot));

    if (!spec.rows.length) {
      rowsBox.append(h("div.empty", h("b", "No rows on this tab"), "Turn some on in ",
        h("a", { href: "/settings/home", style: { color: "var(--accent)" } }, "Home rows"), "."));
      return;
    }
    const slots = spec.rows.map((r) => {
      const slot = skeletonRow(r.multi ? null : r.title);
      rowsBox.append(slot);
      return [r, slot];
    });
    // Four rows at a time, top first, so the screen fills in order.
    let next = 0;
    const worker = async () => {
      while (next < slots.length && mine === token) {
        const [r, slot] = slots[next++];
        try {
          const res = await get(`/api/v1/rows/${encodeURIComponent(r.key)}?tab=${tab}`);
          if (mine !== token) return;
          const els = res.rows.filter((row) => row.items.length).map((row) => rowEl(row, { moreHref: moreHref(row, tab) }));
          // No rows, for a reason worth saying ("Picked for you" being made, or failed and when it is tried again).
          if (!els.length && res.note) els.push(h("section.row", rowHeader(r.title), h("p.soft.row-note", res.note)));
          slot.replaceWith(...els);
          els.forEach(fillPosters);
        } catch {
          slot.remove();
        }
      }
    };
    await Promise.all([worker(), worker(), worker(), worker()]);
  };
  await load();
  return { onShow: () => setGlow(GLOW[tab]) };
}
