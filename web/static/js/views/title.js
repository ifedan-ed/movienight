// Title page: big Play / Request button (shows: the next episode), the quick
// actions, outside scores, details, people, seasons (each opens its
// episodes), collection and "More like this".
import { get } from "../api.js";
import { episodeHeart, episodeStars, listsSheet, primaryButton, rateSheet, startPolling, stopPolling, toggleFavorite, toggleWatchlist, watchedSheet } from "../actions.js";
import { back } from "../nav.js";
import { fillPosters, remember, rowEl, states } from "../ui.js";
import { episodeHref, fmtDate, h, icon, img, initials, runtime, sourceLabel, swap } from "../util.js";

// Where the person stopped (from the Movie Night plugin for Jellyfin).
function clock(sec) {
  const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60), s = Math.floor(sec % 60);
  return h ? `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}` : `${m}:${String(s).padStart(2, "0")}`;
}

function resumeLine(r, showId) {
  if (!r || !r.position_s) return null;
  const ep = r.episode != null ? `S${r.season}·E${r.episode} · ` : "";
  const left = r.duration_s ? Math.max(0, Math.round((r.duration_s - r.position_s) / 60)) : null;
  const where = r.state === "stopped" ? `Stopped at ${clock(r.position_s)}` : `Watching now · ${clock(r.position_s)}`;
  // A show's episode opens that episode (its season page).
  return h(showId && r.episode != null ? "a.d-resume" : "div.d-resume", showId && r.episode != null ? { href: episodeHref(showId, r.season, r.episode) } : {},
    h("div.bar", h("i", { style: { width: `${Math.max(3, r.percent || 0)}%` } })),
    h("span", ep + where + (left != null ? ` · ${left} min left` : "") + (r.client ? ` · ${r.client}` : "")));
}

function scoreChips(sc, d) {
  const out = [];
  const cm = d.community;
  if (cm && cm.count) out.push(h("a.score", { href: "#ratings" }, h("b.shared", "Shared"), String(cm.average), h("small", `${cm.count} rating${cm.count > 1 ? "s" : ""}`)));
  if (d.vote) out.push(h("span.score", h("span.star", "★"), String(d.vote), h("small", "TMDB")));
  if (!sc) return out;
  const link = (href, ...kids) => (href ? h("a.score", { href, target: "_blank", rel: "noopener" }, ...kids) : h("span.score", ...kids));
  if (sc.imdb_rating) out.push(link(d.imdb_url, h("b.imdb", "IMDb"), String(sc.imdb_rating)));
  if (sc.rt_critics != null) out.push(link(sc.rt_url, h("span.rt" + (sc.rt_critics >= 60 ? ".fresh" : ".rotten")), `${sc.rt_critics}%`, h("small", "critics")));
  if (sc.rt_audience != null) out.push(link(sc.rt_url, h("span.pop"), `${sc.rt_audience}%`, h("small", "audience")));
  if (sc.metacritic != null) out.push(h("span.score", h("span.mc" + (sc.metacritic >= 61 ? ".good" : sc.metacritic >= 40 ? ".mid" : ".bad"), String(sc.metacritic)), h("small", "Metacritic")));
  if (sc.letterboxd) out.push(h("span.score", h("span.lb"), sc.letterboxd.toFixed(1), h("small", "Letterboxd")));
  if (sc.trakt_rating) out.push(h("span.score", h("b.trakt", "Trakt"), String(sc.trakt_rating)));
  return out;
}

export default async function render(root, { params }) {
  const [media, id] = params;
  const key = `${media}:${id}`;
  root.append(
    h("div.d-hero", h("div.d-topbar", h("button.iconbtn", { onclick: () => back("/"), "aria-label": "Back" }, icon("left")))),
    h("div.d-body", { style: { marginTop: "-40px" } }, h("div.sk", { style: { height: "34px", width: "70%", borderRadius: "10px" } }),
      h("div.sk", { style: { height: "54px", marginTop: "18px", borderRadius: "15px" } })));

  let d;
  try {
    d = await get(`/api/v1/title/${media}/${id}`, { fresh: true });
  } catch (e) {
    swap(root, h("header.back-head", h("button.iconbtn", { onclick: () => back("/"), "aria-label": "Back" }, icon("left")), h("h1", "Not available")),
      h("div.err-box", e.message));
    return;
  }
  const c = { key, media_type: media, tmdb_id: Number(id), title: d.title, year: d.year, poster: d.poster, vote: d.vote };
  remember({ key, ...d.state });
  let watches = d.watches || [];

  // --- hero + header
  const heroImg = d.backdrop ? img(d.backdrop, "w1280") : d.poster ? img(d.poster, "w780") : null;
  const metaBits = [
    d.year,
    media === "tv" ? (d.number_of_seasons ? `${d.number_of_seasons} season${d.number_of_seasons > 1 ? "s" : ""}` : "Series") : runtime(d.runtime),
    d.certification ? h("span.cert", d.certification) : null,
    media === "tv" && d.runtime ? `${d.runtime}m episodes` : null,
    d.ongoing ? h("span.ongoing", "Ongoing") : null,
  ].filter(Boolean);

  // --- primary button (polls while requested); shows play their next episode
  const primary = primaryButton(c, states.get(key), { onChange: () => paintActions() });
  primary.dataset.poll = key;
  const nextNote = h("div.btn-note", { hidden: true });
  // Up next (shows): the episode Play starts, with its own heart and stars
  // (that episode's, not the show's); its name opens it on its season page.
  const upNext = h("div.upnext", { hidden: true });
  const paintUpNext = (n) => {
    upNext.hidden = !n;
    if (!n) return swap(upNext);
    const e = { number: n.episode, favorite: n.favorite, rating: n.rating };
    swap(upNext,
      h("a.upnext-ep", { href: episodeHref(id, n.season, n.episode) }, h("span.upnext-k", "Up next"), h("b", `S${n.season}·E${n.episode}`), icon("right")),
      episodeHeart(id, n.season, e, { onChange: (v) => { n.favorite = v; } }),
      episodeStars(id, n.season, e, { showTitle: d.title, onChange: (v) => { n.rating = v; } }));
  };
  const loadNext = async () => {
    if (media !== "tv") return;
    try {
      const res = await get(`/api/v1/title/tv/${id}/next`, { fresh: true });
      primary.next = res.next;
      paintUpNext(res.next);
      paintActions();
      primary.paint(states.get(key) || {});
      nextNote.hidden = !!res.next || !(states.get(key) || {}).in_library || !watches.length;
      nextNote.textContent = "You're up to date with what has aired.";
    } catch { /* keep the plain button */ }
  };

  // --- quick actions
  const actions = h("div.actions");
  const showSub = media === "tv" ? "Your rating out of 10, for the whole show (rate an episode on its season page)" : null;
  function paintActions() {
    const s = states.get(key) || {};
    const nWatched = s.watched || 0;
    swap(actions,
      h("button.act" + (s.watchlist ? ".on" : ""), { onclick: async () => { await toggleWatchlist(c); paintActions(); } },
        icon(s.watchlist ? "bookmarkFill" : "bookmark"), s.watchlist ? "In watchlist" : "Watchlist"),
      media === "tv"
        ? h("a.act" + (nWatched ? ".ok" : ""), { href: primary.next ? episodeHref(id, primary.next.season, primary.next.episode) : `/title/tv/${id}/season/${firstSeason()}` },
          icon(nWatched ? "check" : "eye"), nWatched ? `${nWatched} ep${nWatched > 1 ? "s" : ""} seen` : "Episodes")
        : h("button.act" + (nWatched ? ".ok" : ""), { onclick: () => watchedSheet(c, watches, (w) => { watches = w; paintActions(); paintWatches(); }) },
          icon(nWatched ? "check" : "eye"), nWatched > 1 ? `Watched ×${nWatched}` : "Watched"),
      // A show's rating and heart are the whole show's (an episode has its
      // own on the season page), so for shows they say so.
      h("button.act" + (s.rating ? ".on" : ""), { onclick: () => rateSheet(c, onRated, { review: (d.community && d.community.mine && d.community.mine.review) || "", sub: showSub }) },
        icon(s.rating ? "starFill" : "star"), s.rating ? `${media === "tv" ? "Show " : ""}${s.rating}/10` : media === "tv" ? "Rate show" : "Rate"),
      h("button.act" + (s.favorite ? ".fav" : ""), { "aria-pressed": String(!!s.favorite), onclick: async () => { await toggleFavorite(c); paintActions(); } },
        icon(s.favorite ? "heartFill" : "heart"), media === "tv" ? (s.favorite ? "Favorited show" : "Favorite show") : (s.favorite ? "Favorited" : "Favorite")),
      h("button.act", { onclick: () => listsSheet(c) }, icon("listPlus"), "List"));
  }
  const firstSeason = () => {
    const regular = (d.seasons || []).filter((s) => s.number > 0);
    const next = primary.next;
    if (next) return next.season;
    return (regular[0] || d.seasons[0] || { number: 1 }).number;
  };
  paintActions();
  // Repaint when this title's state changes anywhere; only while the page shows.
  const onState = (e) => { if (e.detail.key === key) { paintActions(); primary.paint(e.detail.state); } };
  document.addEventListener("mn:state", onState);

  // --- your watches (movies; shows have their episode lists)
  const watchesBox = h("div");
  function paintWatches() {
    swap(watchesBox, ...(media === "movie" && watches.length ? [
      h("h3.sec-title", "Your history"),
      h("div.watches", watches.slice(0, 8).map((w) => h("div.watch",
        icon("check"),
        h("div.w-main", h("b", fmtDate(w.watched_at)), h("span", (w.sources || []).map(sourceLabel).join(" + ")))))),
      // The 8 newest here; the rest are counted and in Full history, not left out without a word.
      watches.length > 8 ? h("a.soft", { href: "/history", style: { display: "block", padding: "0 var(--gutter)" } },
        `…and ${watches.length - 8} more in Full history`) : null,
    ] : []));
  }
  paintWatches();

  // --- ratings and comments on this Movie Night (this install)
  const ratingsBox = h("div", { id: "ratings" });
  function paintRatings() {
    const cm = d.community || { count: 0, others: [] };
    const mine = cm.mine;
    const card = (r) => h("div.rcard" + (r.mine ? ".mine" : ""),
      h("div.rcard-head", h("b", r.mine ? "You" : r.name), h("span.rstars", "★".repeat(Math.round(r.rating / 2)) || "★"), h("span.rnum", `${r.rating}/10`),
        h("span.rdate", fmtDate(r.rated_at, { month: "short", day: "numeric", year: "numeric" }))),
      r.review ? h("p.rtext", r.review) : (r.mine ? h("p.rtext.soft", "No comment yet — tap Edit to add one.") : null),
      r.mine ? h("button.btn.btn-small.btn-quiet", { onclick: () => rateSheet(c, onRated, { review: r.review || "", sub: showSub }) }, icon("edit"), "Edit") : null);
    swap(ratingsBox,
      h("h3.sec-title", "Ratings on Movie Night", cm.count ? h("small.sec-sub", ` · ${cm.average}/10 from ${cm.count}`) : null),
      mine ? card(mine) : h("button.btn.btn-quiet", { onclick: () => rateSheet(c, onRated, { sub: showSub }) }, icon("star"), "Rate it and say what you thought"),
      ...(cm.others || []).map(card));
  }
  function onRated(value, review) {
    const cm = d.community || (d.community = { count: 0, others: [] });
    const others = cm.others || [];
    cm.mine = value ? { name: "You", rating: value, review, rated_at: new Date().toISOString(), mine: true } : null;
    const all = [...others.map((o) => o.rating), ...(cm.mine ? [cm.mine.rating] : [])];
    cm.count = all.length;
    cm.average = all.length ? Math.round((all.reduce((a, b) => a + b, 0) / all.length) * 10) / 10 : null;
    paintActions();
    paintRatings();
    swap(scores, ...scoreChips(lastScores, d));
  }
  paintRatings();

  // --- details
  const facts = h("dl.facts");
  const fact = (label, value) => { if (value && (!Array.isArray(value) || value.length)) facts.append(h("dt", label), h("dd", Array.isArray(value) ? value.join(", ") : value)); };
  fact(d.director_label, d.directors);
  fact("Written by", d.writers);
  fact(media === "tv" ? "Network" : "Studio", media === "tv" ? d.networks : d.studios);
  fact(media === "tv" ? "First aired" : "Released", d.release_date ? fmtDate(d.release_date) : null);
  if (media === "tv" && d.number_of_episodes) fact("Episodes", `${d.number_of_episodes} in ${d.number_of_seasons} season${d.number_of_seasons > 1 ? "s" : ""}`);
  if (media === "tv" && d.next_episode && d.next_episode.air_date) {
    fact("Next episode", h("a.soft", { href: episodeHref(id, d.next_episode.season, d.next_episode.episode) },
      `S${d.next_episode.season}·E${d.next_episode.episode} on ${fmtDate(d.next_episode.air_date, { weekday: "short", month: "short", day: "numeric" })}`));
  }
  fact("Language", d.languages);
  if (d.status_text && !["Released"].includes(d.status_text)) fact("Status", d.status_text);

  const links = h("div.links",
    d.trailer ? h("a.btn.btn-small.btn-ghost", { href: d.trailer, target: "_blank", rel: "noopener" }, icon("trailer"), "Trailer") : null,
    d.imdb_url ? h("a.btn.btn-small.btn-quiet", { href: d.imdb_url, target: "_blank", rel: "noopener" }, "IMDb") : null,
    h("a.btn.btn-small.btn-quiet", { href: d.tmdb_url, target: "_blank", rel: "noopener" }, "TMDB"));

  const stream = (d.providers || {}).stream || [];
  const providers = stream.length ? [h("h3.sec-title", "Also streaming on"),
    h("div.providers", stream.map((p) => h("div.provider", p.logo ? h("img", { src: img(p.logo, "w92"), alt: "" }) : null, p.name)))] : [];

  const cast = (d.cast || []).length ? [h("h3.sec-title", { style: { padding: "0 var(--gutter)" } }, "Cast"),
    h("div.people", d.cast.map((p) => h("a.person", { href: `/person/${p.id}` },
      h("div.ph", p.photo ? h("img", { src: img(p.photo, "w185"), alt: "", loading: "lazy" }) : initials(p.name)),
      h("b", p.name), p.character ? h("span", p.character) : null)))] : [];

  const seasons = (d.seasons || []).length ? [h("h3.sec-title", { style: { padding: "0 var(--gutter)" } }, "Seasons"),
    h("div.seasons", d.seasons.map((s) => {
      const done = s.episodes && s.watched >= s.episodes;
      return h("a.season", { href: `/title/tv/${id}/season/${s.number}` },
        h("div.poster", s.poster ? h("img", { src: img(s.poster), alt: "", loading: "lazy" }) : h("div.noimg", s.name),
          s.status === "available" ? h("span.badge.lib", icon("check")) : s.status === "processing" || s.status === "partial" ? h("span.badge.dl", icon("download")) : null,
          s.watched ? h("span.seen" + (done ? ".done" : ""), icon("check"), done ? "Seen" : `${s.watched}/${s.episodes || "?"}`) : null),
        h("b", s.name), h("span", `${s.episodes || "?"} episodes`));
    }))] : [];

  let lastScores = d.scores;
  const scores = h("div.d-scores", scoreChips(d.scores, d));
  const scoresNote = h("div"); // why outside scores couldn't be refreshed (MDBList refused or didn't answer)
  const related = h("div");

  swap(root,
    h("div.d-hero", heroImg ? h("img", { src: heroImg, alt: "" }) : null,
      h("div.d-topbar", h("button.iconbtn", { onclick: () => back("/"), "aria-label": "Back" }, icon("left")))),
    h("div.d-head",
      h("div.poster", d.poster ? h("img", { src: img(d.poster, "w342"), alt: "" }) : h("div.noimg", d.title)),
      h("div", { style: { minWidth: "0", paddingBottom: "4px" } },
        h("h1", d.title),
        h("div.d-meta", metaBits.map((b) => (b instanceof Node ? b : h("span", String(b))))))),
    h("div.d-body",
      (d.genres || []).length ? h("div.d-genres", d.genres.map((g) => h("span", g))) : null,
      scores,
      scoresNote,
      h("div.d-primary", primary, nextNote, upNext, resumeLine(d.resume, media === "tv" ? id : null)),
      actions,
      d.tagline ? h("p.tagline", d.tagline) : null,
      d.overview ? h("p.overview", { style: d.tagline ? {} : { marginTop: "22px" } }, d.overview) : null,
      links,
      ratingsBox,
      facts,
      providers,
      watchesBox),
    ...seasons,
    ...cast,
    related);

  loadNext();
  // Outside scores (IMDb, Rotten Tomatoes, ...) when the page has none yet, or
  // no Rotten Tomatoes score (the library copy may have one from Jellyfin).
  if (!d.scores || d.scores.rt_critics == null) {
    get(`/api/v1/title/${media}/${id}/scores`).then((r) => {
      if (r.scores) { lastScores = r.scores; swap(scores, ...scoreChips(r.scores, d)); }
      if (r.note) swap(scoresNote, h("p.soft", { role: "status" }, r.note));
    }).catch(() => {});
  }
  // related rows load after the page is up
  get(`/api/v1/title/${media}/${id}/related`).then((r) => {
    const els = [];
    if (r.collection && r.collection.items.length > 1) els.push(rowEl({ title: r.collection.name, items: r.collection.items, style: "poster" }));
    // TMDB's first page of recommendations; "See all" goes on through the rest.
    if (r.recommendations.length) {
      els.push(rowEl({ title: "More like this", items: r.recommendations, style: "poster" },
        { moreHref: `/browse?key=${encodeURIComponent(`recs:${media}:${id}`)}&title=${encodeURIComponent(`More like ${d.title}`)}` }));
    }
    swap(related, ...els);
    fillPosters(related);
  }).catch(() => {});

  const s0 = states.get(key) || {};
  if (["pending", "processing", "partial"].includes(s0.request)) startPolling(c, (next) => primary.paint(next));

  return {
    onShow: () => {
      document.addEventListener("mn:state", onState);
      const s = states.get(key) || {};
      primary.paint(s);
      paintActions();
      loadNext();
      if (["pending", "processing", "partial"].includes(s.request)) startPolling(c, (next) => primary.paint(next));
    },
    onHide: () => {
      document.removeEventListener("mn:state", onState);
      stopPolling(key);
    },
  };
}
