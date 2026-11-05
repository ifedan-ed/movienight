// "See all" behind a Home row: the same row as an endless grid.
import { get } from "../api.js";
import { back } from "../nav.js";
import { infiniteGrid } from "../ui.js";
import { h, icon } from "../util.js";

export default async function render(root, { query }) {
  const key = query.get("key") || "trending";
  const tab = query.get("tab") || "for_you";
  const title = h("h1", query.get("title") || "Browse");
  root.append(
    h("header.back-head", h("button.iconbtn", { onclick: () => back("/"), "aria-label": "Back" }, icon("left")), title),
    infiniteGrid(async (page) => {
      const res = await get(`/api/v1/browse?key=${encodeURIComponent(key)}&tab=${tab}&page=${page}`);
      if (page === 1 && !query.get("title")) title.textContent = res.title;
      return res;
    }));
}
