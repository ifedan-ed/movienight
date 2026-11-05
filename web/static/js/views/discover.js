// Discover: browse TMDB by genre, streaming service, years, rating and sort.
// Any combination can be saved as a smart list (and pinned to Home).
import { get, post } from "../api.js";
import { go, setQuery } from "../nav.js";
import { infiniteGrid, promptSheet, sheet, toast } from "../ui.js";
import { h, icon, swap } from "../util.js";

const SORTS = [
  ["popularity.desc", "Most popular"],
  ["vote_average.desc", "Highest rated"],
  ["primary_release_date.desc", "Newest"],
  ["revenue.desc", "Box office"],
];
const RATINGS = [["", "Any rating"], ["6", "6+"], ["7", "7+"], ["8", "8+"]];
const DECADES = [["", "", "Any year"], ["2020", "", "2020s"], ["2010", "2019", "2010s"], ["2000", "2009", "2000s"],
  ["1990", "1999", "90s"], ["1980", "1989", "80s"], ["1970", "1979", "70s"], ["1950", "1969", "50s–60s"], ["", "1949", "Before 1950"]];

export default async function render(root, { query }) {
  const f = {
    media: query.get("media") || "movie",
    genre: query.get("genre") || "",
    provider: query.get("provider") || "",
    year_from: query.get("year_from") || "",
    year_to: query.get("year_to") || "",
    min_rating: query.get("min_rating") || "",
    sort: query.get("sort") || "popularity.desc",
  };
  // Genres and services hardly change: reused for an hour (3,600,000 ms) in this tab.
  const [genres, providers] = await Promise.all([
    get(`/api/v1/genres?media=${f.media}`, { ttl: 3_600_000 }).catch(() => ({ genres: [] })),
    get("/api/v1/providers", { ttl: 3_600_000 }).catch(() => ({ providers: [] })),
  ]);
  let genreList = genres.genres;

  const seg = h("div.seg");
  const chips = h("div.chips");
  const results = h("div");
  const saveBtn = h("button.chip", { onclick: saveSmart, hidden: true }, icon("plus"), "Save as list");
  root.append(h("header.head", h("h1", "Discover"), saveBtn), seg, chips, results);

  function pick(title, options, current, onPick) {
    sheet((close) => h("div", h("h2", title),
      options.map(([value, label]) => h("button.opt", { onclick: () => { close(); onPick(value); } },
        h("span", { style: { flex: "1" } }, label),
        h("span.check" + (String(current) === String(value) ? ".on" : ""), String(current) === String(value) ? icon("check") : null)))));
  }

  function chip(label, active, onclick) {
    return h("button.chip" + (active ? ".on" : ""), { onclick }, label, icon("down"));
  }

  async function paint() {
    setQuery({ ...f, sort: f.sort === "popularity.desc" ? null : f.sort, media: f.media === "movie" ? null : f.media });
    swap(seg, ...[["movie", "Movies"], ["tv", "Shows"]].map(([id, label]) =>
      h("button" + (f.media === id ? ".on" : ""), { onclick: async () => {
        if (f.media === id) return;
        f.media = id; f.genre = "";
        genreList = (await get(`/api/v1/genres?media=${id}`, { ttl: 3_600_000 })).genres;
        paint();
      } }, label)));
    const gName = genreList.find((g) => String(g.id) === f.genre)?.name;
    const pName = providers.providers.find((p) => String(p.id) === f.provider)?.name;
    const decade = DECADES.find(([a, b]) => a === f.year_from && b === f.year_to);
    swap(chips,
      chip(gName || "Genre", !!gName, () => pick("Genre", [["", "All genres"], ...genreList.map((g) => [String(g.id), g.name])], f.genre, (v) => { f.genre = v; paint(); })),
      chip(pName || "Streaming", !!pName, () => pick("Streaming on", [["", "Anywhere"], ...providers.providers.map((p) => [String(p.id), p.name])], f.provider, (v) => { f.provider = v; paint(); })),
      chip(decade && decade[2] !== "Any year" ? decade[2] : "Years", !!(f.year_from || f.year_to), () => pick("Released", DECADES.map(([a, b, l]) => [`${a}|${b}`, l]), `${f.year_from}|${f.year_to}`, (v) => { [f.year_from, f.year_to] = v.split("|"); paint(); })),
      chip(f.min_rating ? `${f.min_rating}+ rating` : "Rating", !!f.min_rating, () => pick("TMDB rating", RATINGS, f.min_rating, (v) => { f.min_rating = v; paint(); })),
      chip(SORTS.find(([v]) => v === f.sort)?.[1] || "Sort", f.sort !== "popularity.desc", () => pick("Sort by", SORTS, f.sort, (v) => { f.sort = v; paint(); })));
    saveBtn.hidden = !(f.genre || f.provider || f.year_from || f.year_to || f.min_rating || f.sort !== "popularity.desc");
    const qs = new URLSearchParams(Object.entries(f).filter(([, v]) => v));
    swap(results, infiniteGrid((page) => get(`/api/v1/discover?${qs}&page=${page}`), { empty: "Nothing matches these filters." }));
  }

  async function saveSmart() {
    const name = await promptSheet("Save as a smart list", {
      sub: "It keeps itself up to date with these filters. You can pin it to Home.",
      placeholder: "e.g. 80s thrillers on Netflix", ok: "Save list",
    });
    if (!name) return;
    try {
      const res = await post("/api/v1/lists/smart", { name, filters: f });
      toast("Smart list saved");
      go(`/list/${res.id}`);
    } catch (e) { toast(e.message, { error: true }); }
  }

  paint();
}
