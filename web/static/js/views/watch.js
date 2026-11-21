// /watch/{movie|tv}/{id}[?season=&episode=]: the title in Movie Night's own
// player (player/player.js), over the whole window. Leaving the page stops
// the play (its last report records how far it got); coming back to it
// starts again, with the resume prompt.
import { Player } from "../player/player.js";
import { h } from "../util.js";

const CSS = "/static/css/player.css";

// The player's styles load with the first play, not with every page.
function styles() {
  if (document.querySelector(`link[data-player]`)) return;
  const v = document.documentElement.dataset.v || "";
  document.head.append(h("link", { rel: "stylesheet", href: `${CSS}?v=${v}`, dataset: { player: "1" } }));
}

export default async function watch(root, { params, query }) {
  const [media, id] = params;
  const num = (k) => (query.has(k) && /^\d+$/.test(query.get(k)) ? Number(query.get(k)) : null);
  styles();
  document.body.classList.add("watching");
  const player = new Player(root, { media, tmdbId: Number(id), season: num("season"), episode: num("episode") });
  player.start();
  return {
    onHide: () => { player.destroy(); document.body.classList.remove("watching"); },
    onShow: () => { document.body.classList.add("watching"); if (player.destroyed) player.start(); },
  };
}
