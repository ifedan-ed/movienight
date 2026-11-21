// Profile: your numbers, what you watched, your requests, your connections
// (Jellyfin, players, emails, Kodi) and settings (Home rows, time zone, ...).
import { watchedSheet } from "../actions.js";
import { get, post } from "../api.js";
import { fillPosters, posterCard, rowHeader, toast } from "../ui.js";
import { episodeHref, fmtDate, h, icon, initials, swap, titleHref } from "../util.js";
import { requestRow } from "./requests.js";
import { timeZoneRow } from "./timezone.js";

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

// Plays Jellyfin has without a date: they can't go on the timeline, so
// each is listed for the person to add here with a date, or to say it
// wasn't watched. Either answer hides it for good (on the server), so the
// notice ends once each is dealt with.
function undatedNote(items, more, reload) {
  const box = h("div.note.undated", { role: "status" });
  const hide = (it) => post(`/api/v1/me/jellyfin/undated/${encodeURIComponent(it.id)}/hide`);
  const row = (it) => {
    const ep = it.media_type === "tv" && it.season != null && it.episode != null;
    const name = ep ? `${it.title} · S${it.season}·E${it.episode}` : it.title;
    const href = it.tmdb_id == null ? null : ep ? episodeHref(it.tmdb_id, it.season, it.episode) : titleHref(it);
    const done = async (btn) => {
      btn.disabled = true;
      try {
        await hide(it);
        li.remove();
        if (!box.querySelector(".un-row")) box.remove();
        return true;
      } catch (e) {
        btn.disabled = false;
        toast(e.message, { error: true });
        return false;
      }
    };
    const add = h("button.btn.btn-small.btn-ghost", { type: "button", onclick: () => {
      const c = { media_type: it.media_type, tmdb_id: it.tmdb_id, title: name, key: `${it.media_type}:${it.tmdb_id}` };
      // An episode is ticked on its show (which also marks it in Jellyfin);
      // a film takes the usual watched sheet as it is.
      const save = ep ? (when) => post(`/api/v1/title/tv/${it.tmdb_id}/episodes`,
        { episodes: [{ season: it.season, episode: it.episode }], watched: true, watched_at: when }) : null;
      // The numbers above change with the watch: the page is read again.
      watchedSheet(c, [], async () => { if (await done(add)) reload(); }, { save });
    } }, "Add to history");
    const not = h("button.btn.btn-small.btn-quiet", { type: "button", onclick: () => done(not) }, "Not watched");
    const li = h("li.un-row",
      href ? h("a.un-name", { href }, name) : h("span.un-name", name),
      h("span.un-acts", it.tmdb_id == null ? null : add, not));
    return li;
  };
  swap(box,
    h("b", "No date in Jellyfin, so not in your history"),
    h("ul.un-list", items.map(row)),
    more ? h("p.un-more", `And ${more.toLocaleString()} more`) : null);
  return box;
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
      jf.linked || !me.features.jellyfin ? null : h("a.note.cta", { href: "/settings/jellyfin" }, icon("plug"),
        h("span", h("b", "Connect your Jellyfin account"), " so your plays come in, Play opens VLC or Infuse, and requests are yours.")),
      // Admins only: what Movie Night couldn't do by itself (a library sync that didn't finish, ...).
      (me.admin_notices || []).map((n) => h("div.note", { role: "status" }, h("b", "For admins: "), n)),
      // Yours: your Jellyfin plays couldn't be read.
      jf.plays_notice ? h("a.note", { href: "/settings/jellyfin", role: "status" }, jf.plays_notice) : null,
      // And the ones Jellyfin has without a date, by name.
      (jf.plays_undated || []).length ? undatedNote(jf.plays_undated, jf.plays_undated_more || 0, load) : null,
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
          me.features.jellyfin ? link("/settings/jellyfin", "plug", "Jellyfin", jf.linked ? `Connected${jf.user_name ? " as " + jf.user_name : ""}` : "Not connected") : null,
          link("/settings/players", "play", "Players and subtitles", "VLC, Infuse, Outplayer or your own"),
          link("/settings/emails", "mail", "Emails", "Weekly digest and month in review"),
          link("/settings/kodi", "key", "Kodi and other players", me.scrobble_token_set ? "Token set" : "Report watches from other apps"))),
      h("section.row", rowHeader("Settings"),
        h("div.rows",
          link("/settings/home", "sliders", "Home rows", "Choose and order what Home shows"),
          timeZoneRow(me, load),
          h("a.trow", { href: "/account", "data-native": true }, h("span.ic", icon("key")),
            h("span.tx", h("b", "Account"), h("span", "Password and signing out")), icon("right")),
          me.is_admin && me.features.libraries ? link("/settings/libraries", "film", "Libraries", "Your film and show folders") : null,
          me.is_admin && me.features.jellyfin ? syncButton("Rescan Jellyfin library", "library", "film", "For everyone on this Movie Night") : null,
          me.is_admin && me.features.mdblist ? syncButton("Sync MDBList lists now", "lists", "refresh", "They also sync every night") : null,
          h("a.trow", { href: "/api/v1/export", target: "_blank", rel: "noopener" }, h("span.ic", icon("download")),
            h("span.tx", h("b", "Download my data"), h("span", "Everything Movie Night keeps for you, as one file")), icon("right")))),
      me.features.dry_run ? h("div.note", "Test mode is on: Request buttons don't send anything to Seerr.") : null);
    fillPosters(body);
  }
  await load();
  return { onShow: load };
}
