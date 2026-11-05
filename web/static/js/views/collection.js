// Watchlist, favourites and ratings as grids, filterable by movies/shows,
// with an Edit mode to take titles off.
import { del, get } from "../api.js";
import { back, setQuery } from "../nav.js";
import { infiniteGrid, setState, toast } from "../ui.js";
import { h, icon, swap } from "../util.js";

const NAMES = { watchlist: "Watchlist", favorites: "Favorites", rated: "Your top rated", ratings: "Your ratings" };
// What "remove" means for each collection, and the state it clears.
const REMOVE = {
  watchlist: ["watchlist", { watchlist: false }, "Removed from your watchlist"],
  favorites: ["favorite", { favorite: false }, "Removed from your favorites"],
  rated: ["rating", { rating: null }, "Rating removed"],
  ratings: ["rating", { rating: null }, "Rating removed"],
};

export default async function render(root, { params, query }) {
  const kind = params[0];
  let media = query.get("media") || "";
  let editing = false;
  const sub = h("div.sub");
  const seg = h("div.seg");
  const editBtn = h("button.chip", { onclick: () => { editing = !editing; load(); } });
  const box = h("div");
  root.append(
    h("header.back-head", h("button.iconbtn", { onclick: () => back("/lists"), "aria-label": "Back" }, icon("left")),
      h("div", { style: { flex: "1", minWidth: "0" } }, h("h1", NAMES[kind]), sub), editBtn),
    seg, box);

  const removeButton = (el, c) => {
    const [path, state, message] = REMOVE[kind];
    el.querySelector(".poster").append(h("button.badge.remove", {
      "aria-label": `Remove ${c.title}`,
      onclick: async (e) => {
        e.preventDefault();
        e.stopPropagation();
        try {
          await del(`/api/v1/title/${c.media_type}/${c.tmdb_id}/${path}`);
          setState(c.key, state);
          el.remove();
          toast(`${message}: ${c.title}`);
        } catch (err) { toast(err.message, { error: true }); }
      },
    }, icon("x")));
  };

  const load = () => {
    setQuery({ media });
    swap(editBtn, icon(editing ? "check" : "edit"), editing ? "Done" : "Edit");
    editBtn.classList.toggle("on", editing);
    swap(seg, ...[["", "All"], ["movie", "Movies"], ["tv", "Shows"]].map(([id, label]) =>
      h("button" + (id === media ? ".on" : ""), { onclick: () => { if (id !== media) { media = id; load(); } } }, label)));
    box.classList.toggle("editing", editing);
    swap(box, infiniteGrid(async (page) => {
      const res = await get(`/api/v1/collection/${kind}?page=${page}${media ? `&media=${media}` : ""}`, { fresh: page === 1 });
      sub.textContent = `${res.count} title${res.count === 1 ? "" : "s"}`;
      return res;
    }, {
      empty: kind === "watchlist" ? "Your watchlist is empty. Tap the bookmark on any title to add it." : "Nothing here yet.",
      decorate: editing ? removeButton : null,
    }));
  };
  load();
}
