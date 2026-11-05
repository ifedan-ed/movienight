// Play in VLC, Infuse, Outplayer, Jellyfin or a player you added, with the
// subtitle you want as a normal track; then, when you come back, "Finished
// it?" records the watch (and marks it played in Jellyfin).
import { get, post } from "./api.js";
import { go } from "./nav.js";
import { setState, sheet, states, toast } from "./ui.js";
import { h, icon, img, store, swap } from "./util.js";

const base = (c) => `/api/v1/title/${c.media_type}/${c.tmdb_id}`;

export async function playSheet(c, { season = null, episode = null } = {}) {
  const qs = season != null && episode != null ? `?season=${season}&episode=${episode}` : "";
  let info;
  try { info = await get(`${base(c)}/play${qs}`, { fresh: true }); }
  catch (e) { toast(e.message, { error: true }); return; }
  if (!info.available) {
    toast(c.media_type === "tv" ? "That episode isn't in the library yet" : "This isn't in the library yet");
    return;
  }
  const title = info.title || c.title;
  if (!info.linked) {
    sheet((close) => h("div",
      h("h2", title),
      h("button.btn.btn-request", { onclick: () => { close(); go("/settings/jellyfin"); } }, icon("plug"), "Connect Jellyfin"),
      h("div", { style: { height: "8px" } }),
      h("a.btn.btn-quiet", { href: info.web_url, target: "_blank", rel: "noopener", onclick: close }, icon("play"), "Open in Jellyfin")));
    return;
  }
  let player = store("player") || info.default_player;
  if (!info.players.some((p) => p.id === player)) player = info.default_player;
  const textSubs = info.subtitles.filter((s) => s.text);
  let sub = info.chosen_subtitle;

  sheet((close) => {
    const box = h("div");
    const start = async (btn) => {
      btn.disabled = true;
      try {
        const res = await post(`${base(c)}/play`, {
          player, season: info.season ?? season, episode: info.episode ?? episode,
          subtitle: sub,
        });
        store("player", player);
        close();
        window.location.href = res.url;
      } catch (e) {
        btn.disabled = false;
        toast(e.message, { error: true });
      }
    };
    const paint = () => {
      const subBits = textSubs.length && player !== "jellyfin" ? [
        h("div.mini-label", "Subtitles"),
        h("div.chips.wrap",
          h("button.chip" + (sub == null ? ".on" : ""), { onclick: () => { sub = null; paint(); } }, "Off"),
          textSubs.map((s) => h("button.chip" + (sub === s.index ? ".on" : ""), { onclick: () => { sub = s.index; paint(); } },
            s.label.replace(/ - (Default|SUBRIP|SRT|ASS|SSA|WEBVTT|PGSSUB)\b/gi, ""), s.external ? h("small", "file") : null))),
      ] : [];
      const playBtn = player === "jellyfin"
        ? h("a.btn.btn-play", { href: info.web_url, target: "_blank", rel: "noopener", onclick: close }, icon("play"), "Open in Jellyfin")
        : h("button.btn.btn-play", { onclick: (e) => start(e.currentTarget) }, icon("play"), "Play");
      swap(box,
        h("div.mini-label", "Play in"),
        h("div.players", info.players.map((p) => h("button.player" + (p.id === player ? ".on" : ""), {
          onclick: () => { player = p.id; paint(); },
        }, p.name))),
        subBits,
        h("div", { style: { height: "14px" } }),
        playBtn,
        h("a.btn-note", { href: "/settings/players", onclick: close }, "Players"));
    };
    paint();
    return h("div", h("h2", title), box);
  });
}

// ---------------------------------------------------------------------------
// "Finished it?"
// ---------------------------------------------------------------------------

const asked = new Set();

export function finishedSheet(s) {
  asked.add(s.id);
  const key = s.key;
  sheet((close) => {
    const answer = async (outcome, btn) => {
      if (btn) btn.disabled = true;
      try {
        await post(`/api/v1/play/${s.id}/finish`, { outcome });
        if (outcome === "watched" && key) {
          const st = states.get(key) || {};
          setState(key, { watched: (st.watched || 0) + 1, ...(s.media_type === "movie" ? { watchlist: false } : {}) });
          toast(`Marked ${s.title} as watched`);
        }
        close();
      } catch (e) {
        if (btn) btn.disabled = false;
        toast(e.message, { error: true });
      }
    };
    return h("div.finished",
      s.poster ? h("img.fin-poster", { src: img(s.poster, "w185"), alt: "" }) : null,
      h("h2", "Finished it?"),
      h("div.sub", s.title),
      h("button.btn.btn-request", { onclick: (e) => answer("watched", e.currentTarget) }, icon("check"), "Yes, mark as watched"),
      h("div", { style: { height: "8px" } }),
      h("button.btn.btn-quiet", { onclick: (e) => answer("not_finished", e.currentTarget) }, "Not yet"));
  });
}

// Back in Movie Night: ask about the latest Play when the server says it's time.
export async function checkOpenPlay() {
  if (document.hidden) return;
  let res;
  try { res = await get("/api/v1/play/open", { fresh: true }); } catch { return; }
  const s = res.session;
  if (!s || asked.has(s.id)) return;
  finishedSheet(s);
}
