// Everything requested through Seerr, with live status and download progress.
import { get } from "../api.js";
import { back, setQuery } from "../nav.js";
import { ago, h, icon, img, swap, until } from "../util.js";

export function requestLine(c) {
  if (c.in_library || c.media_status === "available") return [h("strong", { style: { color: "var(--ok)" } }, "In your library"), ""];
  if (c.progress) return [h("strong", { style: { color: "var(--accent)" } }, `Downloading ${c.progress.percent}%`), c.progress.eta ? until(c.progress.eta) : ""];
  if (c.request_status === "declined") return [h("strong", { style: { color: "var(--danger)" } }, "Declined"), ""];
  if (c.request_status === "failed") return [h("strong", { style: { color: "var(--danger)" } }, "Failed in Seerr"), ""];
  if (c.media_status === "partial") return [h("strong", { style: { color: "var(--info)" } }, "Partly in your library"), ""];
  if (c.request_status === "pending") return [h("strong", { style: { color: "var(--info)" } }, "Waiting for approval"), ""];
  return [h("strong", { style: { color: "var(--info)" } }, "Requested"), "waiting for a release to download"];
}

export function requestRow(c) {
  const [status, extra] = requestLine(c);
  return h("a.trow", { href: `/title/${c.media_type}/${c.tmdb_id}` },
    h("span.thumb", c.poster ? h("img", { src: img(c.poster, "w154"), alt: "", loading: "lazy" }) : null),
    h("span.tx", h("b", c.title), h("span", status, extra ? ` · ${extra}` : "", ` · asked ${ago(c.requested_at)}`)),
    icon("right"));
}

export default async function render(root, { query }) {
  let mine = query.get("all") !== "1";
  const seg = h("div.seg");
  const body = h("div", h("div.loadmore", h("div.spin")));
  root.append(h("header.back-head", h("button.iconbtn", { onclick: () => back("/profile"), "aria-label": "Back" }, icon("left")), h("h1", "Requests")), seg, body);
  const paintSeg = () => swap(seg, ...[[true, "Yours"], [false, "Everyone's"]].map(([m, label]) =>
    h("button" + (m === mine ? ".on" : ""), { onclick: () => { if (m !== mine) { mine = m; pages = 1; setQuery({ all: mine ? null : "1" }); paintSeg(); load(); } } }, label)));
  // 40 a page, newest first; "Show more" goes on to the last one, and
  // requests that can't be shown as a title (no TMDB id) are counted.
  let pages = 1;
  const load = async () => {
    try {
      const got = [];
      let res = null;
      for (let p = 1; p <= pages; p++) {
        res = await get(`/api/v1/requests?page=${p}${mine ? "&mine=true" : ""}`, { fresh: true });
        got.push(res);
        if (!res.more) break;
      }
      const items = got.flatMap((r) => r.items);
      const hidden = got.reduce((n, r) => n + (r.not_shown || 0), 0);
      const more = res && res.more ? h("button.btn.btn-quiet", { onclick: () => { pages += 1; load(); } }, "Show more") : null;
      swap(body, items.length
        ? [h("div.rows", items.map(requestRow)),
          h("p.soft.row-note", `${items.length.toLocaleString()} of ${(res.total || items.length).toLocaleString()} requests shown`
            + (hidden ? ` · ${hidden} can't be shown here (Seerr has no TMDB id for them)` : "")),
          more]
        : h("div.empty", h("b", "No requests yet"), "Tap Request on anything that isn't in the library."));
    } catch (e) { swap(body, h("div.err-box", e.message)); }
  };
  paintSeg();
  await load();
  let timer = null;
  const tick = () => { clearInterval(timer); timer = setInterval(() => { if (!document.hidden) load(); }, 15000); };
  tick();
  return { onShow: () => { load(); tick(); }, onHide: () => clearInterval(timer) };
}
