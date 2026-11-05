// Boot: router, the "Rate what you watched" bar, "Finished it?" after a
// Play in another player, and the service worker (installable app).
import { get, post } from "./api.js";
import { saveRating } from "./actions.js";
import { currentName, onNavigate, start } from "./nav.js";
import { checkOpenPlay } from "./play.js";
import { stars, toast } from "./ui.js";
import { h, icon, img, swap } from "./util.js";

const RATE_ON = new Set(["home", "lists", "profile", "discover"]);
const bar = document.getElementById("rate-bar");

function hideBar() {
  bar.hidden = true;
  document.body.classList.remove("has-rate");
}

async function refreshRateBar(name) {
  if (!RATE_ON.has(name)) return hideBar();
  let item = null;
  // Asked again at most every 5 minutes (300,000 ms) as you move around.
  try { item = (await get("/api/v1/rate-prompt", { ttl: 300_000 })).item; } catch { /* no bar */ }
  if (!item || !RATE_ON.has(currentName())) return hideBar();
  const c = { ...item };
  swap(bar,
    h("a.thumb", { href: `/title/movie/${c.tmdb_id}` }, c.poster ? h("img", { src: img(c.poster, "w154"), alt: "" }) : null),
    h("div.tx", h("span", "Rate it"), h("b", c.title)),
    stars(0, async (v) => {
      try {
        await saveRating(c, v);
        toast(`Rated ${c.title}: ${v}/10`);
        hideBar();
        setTimeout(() => refreshRateBar(currentName()), 700);
      } catch { /* toast shown */ }
    }),
    h("button.x", { "aria-label": "Not now", onclick: async () => {
      hideBar();
      try { await post("/api/v1/rate-prompt/dismiss", { key: c.key }); } catch { /* ignore */ }
    } }, icon("x")));
  bar.hidden = false;
  document.body.classList.add("has-rate");
}

onNavigate(refreshRateBar);
start();

// Coming back from VLC / Infuse: ask whether it was watched to the end
// (checked 1.5 s after the page loads and each time the tab comes back).
document.addEventListener("visibilitychange", () => { if (!document.hidden && currentName() !== "playdone") checkOpenPlay(); });
setTimeout(() => { if (currentName() !== "playdone") checkOpenPlay(); }, 1500);

if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => navigator.serviceWorker.register("/sw.js").catch(() => {}));
}
