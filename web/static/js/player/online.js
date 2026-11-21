// Subtitles found online, in the player's subtitle menu ("Search online").
// The server asks the subtitle sites the install has set up, ranks what
// they offer for the file playing, and keeps the one picked as a track of
// that file for everyone in the household (internal/subtitles).
//
// Here: the stored tracks join the file's own in the menu (their index is
// "o<id>", so they never reach Jellyfin's reports), the search panel
// (language, results, machine-translated ones on request), picking one
// (it shows at once, the video plays on), removing one, and each stored
// track's delay, which is saved on the server so every device gets it.
import { del, get, post, put } from "../api.js";
import { toast } from "../ui.js";
import { h, icon } from "../util.js";

const DELAY_SAVE_AFTER = 600; // ms after the last delay change before it is saved

export const isOnline = (t) => !!(t && t.online);

export class OnlineSubs {
  constructor(player) {
    this.p = player;
    this.tracks = [];
    this.enabled = false;
    this.lang = null;
    this.languages = [];
    this.found = null;     // the last search's answer
    this.machine = false;  // machine-translated results shown
    this.busy = false;
  }

  base() { return `/api/v1/title/${this.p.media}/${this.p.tmdbId}/subtitles`; }

  // load reads the languages (once) and the file's stored tracks. A failure
  // costs only this menu: the film plays on with its own tracks.
  async load() {
    try {
      const [langs, list] = await Promise.all([
        this.languages.length ? null : get("/api/v1/subtitles/languages", { ttl: 3_600_000 }),
        get(`${this.base()}?ticket=${encodeURIComponent(this.p.info.ticket)}`, { fresh: true }),
      ]);
      if (langs) {
        this.enabled = langs.enabled;
        this.languages = langs.languages;
        this.lang ||= langs.default;
      }
      this.tracks = list.tracks.map((t) => this.asTrack(t));
    } catch {
      this.tracks = [];
    }
    this.merge(this.p.info);
  }

  asTrack(t) {
    return { index: `o${t.id}`, label: t.label, language: t.language, kind: t.kind, url: t.url,
      online: { id: t.id, delay: t.delay_s, canRemove: t.can_remove, release: t.release, hi: t.hearing_impaired } };
  }

  // merge puts the stored tracks after the file's own in a fresh watch
  // answer (another audio track or a new ticket starts a new one).
  merge(info) {
    info.subtitles = [...info.subtitles.filter((s) => !isOnline(s)), ...this.tracks];
  }

  // ------------------------------------------------------------- the delay

  // saveDelay keeps a stored track's delay on the server, once the [ and ]
  // presses stop.
  saveDelay(t, d) {
    t.online.delay = d;
    clearTimeout(this.delayTimer);
    this.delayTimer = setTimeout(() => {
      put(`/api/v1/subtitles/${t.online.id}/delay`, { delay_s: d }).catch((e) => toast(e.message, { error: true }));
    }, DELAY_SAVE_AFTER);
  }

  // ------------------------------------------------------------- the panel

  async search() {
    this.busy = true;
    this.found = null;
    this.repaint();
    try {
      const q = new URLSearchParams({ ticket: this.p.info.ticket, language: this.lang, ...(this.machine ? { machine: "true" } : {}) });
      this.found = await get(`${this.base()}/search?${q}`, { fresh: true });
    } catch (e) {
      this.found = { results: [], hidden: 0, notes: [e.message] };
    }
    this.busy = false;
    this.repaint();
  }

  repaint() { if (this.p.menuName === "online" && !this.p.menuEl.hidden) this.p.paintMenu(); }

  // pick downloads a result, adds it as a track and shows it: the video
  // keeps playing.
  async pick(r) {
    if (this.busy) return;
    this.busy = true;
    this.repaint();
    try {
      const t = this.asTrack(await post(this.base(), { ticket: this.p.info.ticket, source: r.source, id: r.id }));
      this.tracks = [...this.tracks.filter((x) => x.index !== t.index), t];
      this.merge(this.p.info);
      this.busy = false;
      this.p.closeMenu();
      await this.p.chooseSub(t.index);
      this.p.osd(t.label);
    } catch (e) {
      this.busy = false;
      toast(e.message, { error: true });
      this.repaint();
    }
  }

  async remove(t) {
    try {
      await del(`/api/v1/subtitles/${t.online.id}`);
    } catch (e) {
      toast(e.message, { error: true });
      return;
    }
    this.tracks = this.tracks.filter((x) => x.index !== t.index);
    if (this.p.sub === t.index) await this.p.chooseSub(null);
    this.merge(this.p.info);
    if (!this.p.menuEl.hidden) this.p.paintMenu();
  }

  // removeButton is the small bin next to a stored track in the menu.
  removeButton(t) {
    if (!t.online.canRemove) return null;
    return h("button.pl-x", { type: "button", "aria-label": `Remove ${t.label}`, title: "Remove",
      onclick: (e) => { e.stopPropagation(); this.remove(t); } }, icon("trash"));
  }

  // panel is the menu's search page.
  panel() {
    const f = this.found;
    const select = h("select.pl-select", { "aria-label": "Language", onchange: (e) => { this.lang = e.target.value; this.search(); } },
      this.languages.map((l) => h("option", { value: l.code, selected: l.code === this.lang }, l.name)));
    const results = !f ? [] : f.results.map((r) => {
      const meta = [r.source_label, r.downloads ? `${r.downloads.toLocaleString()} downloads` : null].filter(Boolean).join(" · ");
      return h("button.pl-mi.pl-res", { type: "button", disabled: this.busy || !r.available, title: r.release,
        onclick: () => this.pick(r) },
        h("span", h("b", r.release.split("\n")[0] || r.source_label), h("small", meta)),
        r.hash_match ? h("em.pl-tag.match", "Match") : null,
        r.hearing_impaired ? h("em.pl-tag", { title: "Hearing impaired" }, "HI") : null,
        r.machine_translated ? h("em.pl-tag", { title: "Machine translated" }, "MT") : null);
    });
    return [
      h("div.pl-mhrow",
        h("button.pl-back", { type: "button", "aria-label": "Back to subtitles", onclick: () => this.p.menu("subs", this.p.menuAnchor) }, icon("left")),
        h("div.pl-mh", "Search online")),
      h("div.pl-lang", select),
      this.busy ? h("div.pl-searching", h("div.spin")) : null,
      f ? f.notes.map((n) => h("p.pl-note", n)) : null,
      f && !this.busy && !f.results.length ? h("p.pl-note", "Nothing found") : null,
      results,
      f && f.hidden && !this.machine ? h("button.pl-mi.pl-more", { type: "button", onclick: () => { this.machine = true; this.search(); } },
        h("span", `Machine translated (${f.hidden})`)) : null,
    ];
  }
}
