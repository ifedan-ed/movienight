// Where VLC / Infuse send you back after playing: "Finished it?" when the
// film could have ended; otherwise straight back to the title.
import { post } from "../api.js";
import { go } from "../nav.js";
import { setState, states, toast } from "../ui.js";
import { episodeHref, h, icon, img, swap } from "../util.js";

// Where to go afterwards: an episode's own place (its season page, scrolled
// to it, where its heart and stars are), a film's page, or Home.
function after(s) {
  if (!s.key) return "/";
  if (s.media_type === "episode" && s.tmdb_id && s.season != null && s.episode != null) return episodeHref(s.tmdb_id, s.season, s.episode);
  return `/title/${s.key.replace(":", "/")}`;
}

export default async function render(root, { params }) {
  const body = h("div.finished.page-center", h("div.loadmore", h("div.spin")));
  root.append(body);
  let s;
  try {
    const back = await post(`/api/v1/play/${params[0]}/back`, {});
    s = back.session;
    if (!back.ask) { go(after(s), { replace: true }); return; }
  } catch (e) { swap(body, h("div.err-box", e.message), h("a.btn.btn-quiet", { href: "/" }, "Home")); return; }
  const answer = async (outcome, btn) => {
    btn.disabled = true;
    try {
      await post(`/api/v1/play/${s.id}/finish`, { outcome });
      if (outcome === "watched" && s.key) {
        const st = states.get(s.key) || {};
        setState(s.key, { watched: (st.watched || 0) + 1 });
        toast(`Marked ${s.title} as watched`);
      }
      go(after(s), { replace: true });
    } catch (e) { btn.disabled = false; toast(e.message, { error: true }); }
  };
  swap(body,
    s.poster ? h("img.fin-poster", { src: img(s.poster, "w342"), alt: "" }) : null,
    h("h1", "Finished it?"),
    h("p.soft", s.title),
    h("button.btn.btn-request", { onclick: (e) => answer("watched", e.currentTarget) }, icon("check"), "Yes, mark as watched"),
    h("div", { style: { height: "8px" } }),
    h("button.btn.btn-quiet", { onclick: (e) => answer("not_finished", e.currentTarget) }, "Not yet"));
}
