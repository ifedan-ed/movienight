// Players: which app Play opens by default, your subtitle language, and
// players you add yourself (any app that opens a video link).
import { get, put } from "../api.js";
import { back } from "../nav.js";
import { sheet, toast } from "../ui.js";
import { h, icon, swap } from "../util.js";

const LANGS = [["eng", "English"], ["spa", "Spanish"], ["fre", "French"], ["ger", "German"], ["por", "Portuguese"], ["ita", "Italian"],
  ["dut", "Dutch"], ["swe", "Swedish"], ["dan", "Danish"], ["nor", "Norwegian"], ["fin", "Finnish"], ["pol", "Polish"],
  ["tur", "Turkish"], ["gre", "Greek"], ["ara", "Arabic"], ["heb", "Hebrew"], ["hin", "Hindi"], ["jpn", "Japanese"],
  ["kor", "Korean"], ["chi", "Chinese"], ["", "None (off)"]];
const EXAMPLES = [
  ["nPlayer", "nplayer-{url_raw}"],
  ["VidHub", "open-vidhub://x-callback-url/open?url={url}"],
  ["SenPlayer", "SenPlayer://x-callback-url/play?url={url}"],
  ["MX Player (Android)", "intent://{url_noscheme}#Intent;scheme={url_scheme};type=video/*;package=com.mxtech.videoplayer.ad;S.title={title};end"],
];

export default async function render(root) {
  const body = h("div", h("div.loadmore", h("div.spin")));
  root.append(h("header.back-head", h("button.iconbtn", { onclick: () => back("/profile"), "aria-label": "Back" }, icon("left")), h("h1", "Players")), body);
  let data;
  try { data = await get("/api/v1/me/players", { fresh: true }); }
  catch (e) { swap(body, h("div.err-box", e.message)); return; }
  let custom = data.players.filter((p) => !p.builtin).map(({ name, template }) => ({ name, template }));
  const enabledIds = () => data.players.filter((p) => p.enabled).map((p) => p.id);

  async function save(patch = {}) {
    try {
      data = await put("/api/v1/me/players", { custom, default_player: data.default_player, subtitle_language: data.subtitle_language, enabled: enabledIds(), ...patch });
      custom = data.players.filter((p) => !p.builtin).map(({ name, template }) => ({ name, template }));
      paint();
      return true;
    } catch (e) { toast(e.message, { error: true }); return false; }
  }

  function editPlayer(index) {
    const p = index == null ? { name: "", template: "" } : custom[index];
    sheet((close) => {
      const name = h("input.input", { value: p.name, placeholder: "Name, e.g. nPlayer" }); // over 40 is refused in plain words
      const tpl = h("textarea.input.tpl", { placeholder: "nplayer-{url_raw}", rows: "3" }, p.template);
      return h("div",
        h("h2", index == null ? "Add a player" : "Edit player"),
        h("div.field", name), h("div.field", tpl),
        h("div.chips.wrap", EXAMPLES.map(([n, t]) => h("button.chip", { onclick: () => { name.value = name.value || n; tpl.value = t; } }, n))),
        h("div", { style: { height: "12px" } }),
        h("button.btn.btn-request", { onclick: async () => {
          const next = { name: name.value.trim(), template: tpl.value.trim() };
          const before = custom.slice();
          if (index == null) custom.push(next); else custom[index] = next;
          if (await save()) { close(); toast("Saved"); } else custom = before;
        } }, "Save"),
        index != null ? h("button.btn.btn-danger", { style: { marginTop: "8px" }, onclick: async () => {
          custom.splice(index, 1);
          if (await save({ default_player: data.default_player.startsWith("custom") ? "vlc" : data.default_player })) { close(); toast("Removed"); }
        } }, "Remove") : null);
    });
  }

  function paint() {
    const langName = (LANGS.find(([c]) => c === (data.subtitle_language || "")) || [null, data.subtitle_language])[1];
    swap(body,
      h("div.section-label", "Players to offer"),
      h("div.rows", data.players.map((p) => h("div.trow",
        h("button.tickbox" + (p.enabled ? ".on" : ""), {
          role: "checkbox", "aria-checked": String(!!p.enabled), "aria-label": `Offer ${p.name}`,
          onclick: () => {
            const on = !p.enabled;
            if (!on && enabledIds().length === 1) return toast("Keep at least one player", { error: true });
            p.enabled = on;
            save();
          },
        }, p.enabled ? icon("check") : null),
        h("span.tx", h("b", p.name), h("span", p.builtin ? builtinNote(p.id) : p.template)),
        p.builtin ? null : h("button.iconbtn", { "aria-label": `Edit ${p.name}`, onclick: () => editPlayer(custom.findIndex((x) => x.name === p.name)) }, icon("edit"))))),
      h("div", { style: { padding: "12px var(--gutter)" } }, h("button.btn.btn-quiet", { onclick: () => editPlayer(null) }, icon("plus"), "Add a player")),
      h("div.section-label", "Subtitles"),
      h("div.rows", h("button.trow", { onclick: () => sheet((close) => h("div", h("h2", "Subtitle language"),
        LANGS.map(([code, label]) => h("button.opt", { onclick: async () => { close(); await save({ subtitle_language: code }); } },
          h("span.grow", label), h("span.check" + ((data.subtitle_language || "") === code ? ".on" : ""), (data.subtitle_language || "") === code ? icon("check") : null))))) },
        h("span.ic", icon("subtitles")), h("span.tx", h("b", "Subtitle language"), h("span", langName || "None")), icon("right"))),
      h("div.section-label", "After playing"),
      h("div.rows", h("div.trow",
        h("button.tickbox" + (data.ask_finished ? ".on" : ""), {
          role: "checkbox", "aria-checked": String(!!data.ask_finished), "aria-label": "Ask if I finished it",
          onclick: () => save({ ask_finished: !data.ask_finished }),
        }, data.ask_finished ? icon("check") : null),
        h("span.tx", h("b", "Ask if I finished it")))));
  }

  function builtinNote(id) {
    return {
      vlc: "iPhone and iPad",
      infuse: "iPhone, iPad and Apple TV",
      outplayer: "iPhone and iPad",
      vlc_android: "Android",
      jellyfin: "Jellyfin",
      swiftfin: "iPhone, iPad and Apple TV",
      streamyfin: "iPhone and Android",
    }[id] || "";
  }

  paint();
}
