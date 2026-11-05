// Home rows: turn rows on/off and move them, per tab. Lists you pinned
// appear here too (For You), and can be moved or taken off Home.
import { get, post, put } from "../api.js";
import { back, setQuery } from "../nav.js";
import { confirmSheet, toast } from "../ui.js";
import { debounce, h, icon, swap } from "../util.js";

const TABS = [["for_you", "For You", "sparkles"], ["movies", "Movies", "film"], ["shows", "Shows", "tv"]];
const GROUP_NAME = { mine: "Your list", shared: "Shared list", mdblist: "MDBList list" };

// Which kind of list a Home row is (the server sends group; older replies
// only say who made it).
function rowGroup(r) {
  if (r.group) return r.group;
  if (r.subtitle && r.subtitle.startsWith("by ")) return "shared";
  return "mine";
}

export default async function render(root, { query }) {
  let tab = query.get("tab") || "for_you";
  const chips = h("div.chips");
  const box = h("div");
  root.append(
    h("header.back-head", h("button.iconbtn", { onclick: () => back("/"), "aria-label": "Back" }, icon("left")), h("h1", "Home rows")),
    chips,
    box);

  let rows = [];
  const save = debounce(async () => {
    try { await put(`/api/v1/home/config?tab=${tab}`, { rows: rows.map(({ key, on }) => ({ key, on })) }); }
    catch (e) { toast(e.message, { error: true }); }
  }, 400);

  function paint() {
    swap(chips, ...TABS.map(([id, label, ic]) => h("button.chip" + (id === tab ? ".on" : ""), {
      onclick: () => { if (id !== tab) { tab = id; setQuery({ tab }); load(); } },
    }, icon(ic), label)));
    const on = rows.filter((r) => r.on);
    const offRows = rows.filter((r) => !r.on && !r.list);
    const offLists = (g) => rows.filter((r) => !r.on && r.list && rowGroup(r) === g);
    const item = (r) => {
      const i = rows.indexOf(r);
      const prevOn = rows.slice(0, i).some((x) => x.on), nextOn = rows.slice(i + 1).some((x) => x.on);
      const listId = r.list ? r.key.split(":")[1] : null;
      return h("div.setrow",
        r.list ? h("span", { style: { color: "var(--accent)", display: "grid" } }, icon("lists")) : null,
        listId
          ? h("a.tx", { href: `/list/${listId}`, "aria-label": `Open ${r.title}` },
            h("b", r.title), h("span", `${GROUP_NAME[rowGroup(r)]} · tap to open`))
          : h("div.tx", h("b", r.title), r.subtitle ? h("span", r.subtitle) : null),
        r.on ? h("button.mv", { "aria-label": `Move ${r.title} up`, disabled: !prevOn, onclick: () => move(r, -1) }, icon("up")) : null,
        r.on ? h("button.mv", { "aria-label": `Move ${r.title} down`, disabled: !nextOn, onclick: () => move(r, 1) }, icon("down")) : null,
        h("button.switch" + (r.on ? ".on" : ""), { role: "switch", "aria-checked": String(r.on), "aria-label": r.title, onclick: () => {
          r.on = !r.on;
          if (r.on) { rows.splice(rows.indexOf(r), 1); const lastOn = rows.map((x) => x.on).lastIndexOf(true); rows.splice(lastOn + 1, 0, r); }
          paint(); save();
        } }));
    };
    swap(box,
      h("div.section-label", "On Home, in this order"),
      ...(on.length ? on.map(item) : [h("div.empty", "Nothing on this tab yet.")]),
      ...[["mine", "Your lists"], ["shared", "Shared lists"], ["mdblist", "MDBList lists"]].flatMap(([g, label]) =>
        offLists(g).length ? [h("div.section-label", label), ...offLists(g).map(item)] : []),
      offRows.length ? h("div.section-label", "More rows you can add") : null,
      ...offRows.map(item),
      h("div", { style: { display: "grid", gap: "10px", padding: "18px var(--gutter)" } },
        h("button.btn.btn-quiet", { onclick: () => reset(tab) }, icon("refresh"), "Reset this tab"),
        h("button.btn.btn-danger", { onclick: () => confirmSheet("Reset all tabs?", "Every tab goes back to the default rows. Lists stay on Home.", "Reset all", () => reset("all")) }, icon("refresh"), "Reset all")));
  }

  function move(r, dir) {
    const onRows = rows.filter((x) => x.on);
    const j = onRows.indexOf(r) + dir;
    if (j < 0 || j >= onRows.length) return;
    const other = onRows[j];
    const a = rows.indexOf(r), b = rows.indexOf(other);
    [rows[a], rows[b]] = [rows[b], rows[a]];
    paint(); save();
  }

  async function reset(which) {
    try {
      const res = await post(`/api/v1/home/config/reset?tab=${which}`);
      if (which !== "all") rows = res.rows; else await load();
      paint();
      toast("Back to the default rows");
    } catch (e) { toast(e.message, { error: true }); }
  }

  async function load() {
    swap(box, h("div.loadmore", h("div.spin")));
    try { rows = (await get(`/api/v1/home/config?tab=${tab}`, { fresh: true })).rows; paint(); }
    catch (e) { swap(box, h("div.err-box", e.message)); }
  }
  await load();
}
