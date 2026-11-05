// Profile: your numbers, what you watched, your requests, your connections
// (Jellyfin, players, emails, Kodi) and settings.
import { get, post } from "../api.js";
import { fillPosters, posterCard, rowHeader, toast } from "../ui.js";
import { fmtDate, h, icon, initials, swap } from "../util.js";
import { requestRow } from "./requests.js";

function syncButton(label, what, ic, sub = "") {
  const btn = h("button.trow", { style: { width: "100%", textAlign: "left" } },
    h("span.ic", icon(ic)), h("span.tx", h("b", label), h("span", sub)), icon("right"));
  btn.addEventListener("click", async () => {
    const note = btn.querySelector(".tx span");
    swap(note, h("span.spin.inline"), " working…");
    try {
      const res = await post(`/api/v1/sync/${what}`);
      const c = res.counts || {};
      const parts = Object.entries(c).filter(([, v]) => typeof v === "number").map(([k, v]) => `${v} ${k.replace("_", " ")}`);
      note.textContent = parts.length ? `Done: ${parts.join(", ")}` : "Done";
      toast(`${label}: done`);
    } catch (e) {
      note.textContent = e.message;
      toast(e.message, { error: true });
    }
  });
  return btn;
}

function link(href, ic, title, sub) {
  return h("a.trow", { href }, h("span.ic", icon(ic)), h("span.tx", h("b", title), h("span", sub)), icon("right"));
}

export default async function render(root) {
  const body = h("div", h("div.loadmore", h("div.spin")));
  root.append(body);

  async function load() {
    let me, hist, reqs;
    try {
      [me, hist, reqs] = await Promise.all([
        get("/api/v1/me", { fresh: true }),
        get("/api/v1/history?page=1", { fresh: true }),
        get("/api/v1/requests?mine=true").catch(() => ({ items: [] })),
      ]);
    } catch (e) { swap(body, h("div.err-box", e.message)); return; }
    const s = me.stats;
    const stat = (n, label) => h("div.stat", h("b", Number(n || 0).toLocaleString()), h("span", label));
    const name = me.display_name || me.user;

    const recent = hist.items.filter((x) => x.card).slice(0, 20);
    // An episode opens that episode (its season page), with its own rating
    // and heart in the corner; a film opens its page.
    const recentRow = h("div.scroller", recent.map((x) => posterCard(x.card, {
      meta: x.episode != null ? `S${x.season}·E${x.episode} · ${fmtDate(x.watched_at, { month: "short", day: "numeric" })}` : fmtDate(x.watched_at, { month: "short", day: "numeric", year: "numeric" }),
      episode: x.media_type === "episode" && x.season != null && x.episode != null
        ? { season: x.season, episode: x.episode, favorite: x.episode_favorite, rating: x.episode_rating } : null,
    })));
    const jf = me.jellyfin;

    swap(body,
      h("header.head",
        h("div.avatar", initials(name)),
        h("div", { style: { flex: "1", minWidth: "0" } },
          h("h1", { style: { fontSize: "26px" } }, name),
          h("div.sub", me.display_name ? me.user : "Movie Night"))),
      jf.linked ? null : h("a.note.cta", { href: "/settings/jellyfin" }, icon("plug"),
        h("span", h("b", "Connect your Jellyfin account"), " so your plays come in, Play opens VLC or Infuse, and requests are yours.")),
      // Admins only: what Movie Night couldn't do by itself (a library sync that didn't finish, ...).
      (me.admin_notices || []).map((n) => h("div.note", { role: "status" }, h("b", "For admins: "), n)),
      // Yours: your Jellyfin plays couldn't be read, or some have no date (not added).
      jf.plays_notice ? h("a.note", { href: "/settings/jellyfin", role: "status" }, jf.plays_notice) : null,
      h("div.stats",
        stat(s.movies, "Movies watched"), stat(s.hours, "Hours watched"), stat(s.episodes, "Episodes"),
        stat(s.this_year, `Movies in ${new Date().getFullYear()}`), stat(s.ratings, "Ratings"), stat(s.watchlist, "On watchlist")),
      recent.length ? h("section.row", rowHeader("Recently watched", { moreHref: "/history" }), recentRow) : null,
      reqs.items.length ? h("section.row", rowHeader("Your requests", { sub: "Live from Seerr", moreHref: "/requests" }),
        h("div.rows", reqs.items.slice(0, 5).map(requestRow))) : null,
      h("section.row", rowHeader("Your lists and ratings"),
        h("div.rows",
          link("/collection/ratings", "starFill", "Your ratings", `${s.ratings} rated`),
          link("/collection/watchlist", "bookmark", "Watchlist", `${s.watchlist} titles`),
          link("/history", "clock", "Full history", `${s.movie_plays} movie watches · ${s.episodes} episodes`))),
      h("section.row", rowHeader("Connections"),
        h("div.rows",
          link("/settings/jellyfin", "plug", "Jellyfin", jf.linked ? `Connected${jf.user_name ? " as " + jf.user_name : ""}` : "Not connected"),
          link("/settings/players", "play", "Players and subtitles", "VLC, Infuse, Outplayer or your own"),
          link("/settings/emails", "mail", "Emails", "Weekly digest and month in review"),
          link("/settings/kodi", "key", "Kodi and other players", me.scrobble_token_set ? "Token set" : "Report watches from other apps"))),
      h("section.row", rowHeader("Settings"),
        h("div.rows",
          link("/settings/home", "sliders", "Home rows", "Choose and order what Home shows"),
          h("a.trow", { href: "/account", "data-native": true }, h("span.ic", icon("key")),
            h("span.tx", h("b", "Account"), h("span", "Password and signing out")), icon("right")),
          me.is_admin ? syncButton("Rescan Jellyfin library", "library", "film", "For everyone on this Movie Night") : null,
          me.is_admin && me.features.mdblist ? syncButton("Sync MDBList lists now", "lists", "refresh", "They also sync every night") : null,
          h("a.trow", { href: "/api/v1/export", target: "_blank", rel: "noopener" }, h("span.ic", icon("download")),
            h("span.tx", h("b", "Download my data"), h("span", "Everything Movie Night keeps for you, as one file")), icon("right")))),
      me.features.dry_run ? h("div.note", "Test mode is on: Request buttons don't send anything to Seerr.") : null);
    fillPosters(body);
  }
  await load();
  return { onShow: load };
}
