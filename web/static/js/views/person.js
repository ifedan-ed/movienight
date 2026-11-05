// A person: photo, short bio, and everything they're known for.
import { get } from "../api.js";
import { back } from "../nav.js";
import { fillPosters, posterCard } from "../ui.js";
import { fmtDate, h, icon, img, initials, swap } from "../util.js";

export default async function render(root, { params }) {
  const head = h("header.back-head", h("button.iconbtn", { onclick: () => back("/search"), "aria-label": "Back" }, icon("left")), h("h1", ""));
  const body = h("div", h("div.loadmore", h("div.spin")));
  root.append(head, body);
  let p;
  try { p = await get(`/api/v1/person/${params[0]}`); }
  catch (e) { swap(body, h("div.err-box", e.message)); return; }
  head.querySelector("h1").textContent = p.name;
  const bio = p.biography || "";
  const bioEl = h("p.overview", { style: { fontSize: "15px", color: "var(--text-2)" } }, bio.length > 360 ? bio.slice(0, 360) + "…" : bio);
  const moreBtn = bio.length > 360 ? h("button.btn.btn-small.btn-quiet", { onclick: () => { bioEl.textContent = bio; moreBtn.remove(); } }, "Read more") : null;
  const grid = h("div.grid", p.items.map((c) => posterCard(c)));
  swap(body,
    h("div", { style: { display: "flex", gap: "16px", alignItems: "center", padding: "6px var(--gutter) 0" } },
      h("div.person", { style: { flex: "0 0 104px" } }, h("div.ph", { style: { width: "104px", height: "104px", fontSize: "30px" } },
        p.photo ? h("img", { src: img(p.photo, "h632"), alt: "" }) : initials(p.name))),
      h("div", h("div", { style: { fontWeight: "750", fontSize: "16px" } }, p.known_for || ""),
        h("div", { style: { color: "var(--text-3)", fontSize: "14.5px" } }, [p.birthday ? `Born ${fmtDate(p.birthday)}` : null, p.place_of_birth].filter(Boolean).join(" · ")))),
    bio ? h("div", { style: { padding: "0 var(--gutter)" } }, bioEl, moreBtn) : null,
    h("div.section-label", "Known for"),
    grid);
  fillPosters(grid);
}
