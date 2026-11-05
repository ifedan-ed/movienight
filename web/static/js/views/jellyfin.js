// Connect your Jellyfin account: Quick Connect (type a code into a Jellyfin
// app where you're signed in) or your Jellyfin username and password, once.
// Movie Night keeps only the resulting token, encrypted; never the password.
import { del, get, post } from "../api.js";
import { back } from "../nav.js";
import { confirmSheet, toast } from "../ui.js";
import { ago, h, icon, swap } from "../util.js";

export default async function render(root) {
  const body = h("div", h("div.loadmore", h("div.spin")));
  root.append(h("header.back-head", h("button.iconbtn", { onclick: () => back("/profile"), "aria-label": "Back" }, icon("left")), h("h1", "Jellyfin")), body);
  let timer = null;
  const stop = () => { clearTimeout(timer); timer = null; };

  async function load() {
    stop();
    let st;
    try { st = await get("/api/v1/me/jellyfin", { fresh: true }); }
    catch (e) { swap(body, h("div.err-box", e.message)); return; }
    if (st.linked && st.can_play) return paintLinked(st);
    paintConnect(st);
  }

  function paintLinked(st) {
    swap(body,
      h("div.panel",
        h("div.big-ok", icon("tickFill")),
        h("h2", st.user_name ? `Connected as ${st.user_name}` : "Connected"),
        h("p", `To ${st.server}${st.linked_at ? ` · since ${ago(st.linked_at)}` : ""}.`),
        h("p.soft", "Your plays in Jellyfin come into your history, Play opens VLC, Infuse or your own player, and your requests reach Seerr as you.")),
      // The last plays read failed, or some plays have no date (not added).
      st.plays_notice ? h("div.note", { role: "status" }, st.plays_notice) : null,
      h("div.rows",
        h("a.trow", { href: "/settings/players" }, h("span.ic", icon("play")), h("span.tx", h("b", "Players and subtitles"), h("span", "Which app Play opens, and your subtitle language")), icon("right")),
        h("button.trow", { onclick: async (e) => {
          const note = e.currentTarget.querySelector(".tx span");
          note.textContent = "working…";
          try { const r = await post("/api/v1/sync/plays"); note.textContent = `Done: ${r.counts.plays} new${r.note ? `. ${r.note}` : ""}`; }
          catch (err) { note.textContent = err.message; }
        } }, h("span.ic", icon("refresh")), h("span.tx", h("b", "Get my Jellyfin plays now"), h("span", "It also happens by itself every few minutes")), icon("right"))),
      h("div", { style: { padding: "18px var(--gutter)" } },
        h("button.btn.btn-danger", { onclick: () => confirmSheet("Disconnect Jellyfin?", "Your history here stays. New Jellyfin plays stop coming in until you connect again.", "Disconnect", async () => {
          try { await del("/api/v1/me/jellyfin"); toast("Disconnected"); load(); } catch (e) { toast(e.message, { error: true }); }
        }) }, "Disconnect")));
  }

  function paintConnect(st) {
    const code = h("div.qc-code", "······");
    const qcNote = h("p.soft", "");
    const qcBox = h("div.panel",
      h("h2", "Quick Connect"),
      h("p", "In a Jellyfin app where you're signed in, open Settings → Quick Connect and type this code:"),
      code, qcNote,
      h("button.btn.btn-request", { onclick: startQC }, icon("key"), "Get a code"));
    const user = h("input.input", { placeholder: "Jellyfin username", autocapitalize: "off", autocorrect: "off", autocomplete: "username" });
    const pass = h("input.input", { type: "password", placeholder: "Password", autocomplete: "current-password" });
    const pwBox = h("div.panel",
      h("h2", "Or sign in once"),
      h("p.soft", "Only the sign-in token is kept (encrypted). Your password isn't stored."),
      h("div.field", user), h("div.field", pass),
      h("button.btn.btn-quiet", { onclick: async (e) => {
        const btn = e.currentTarget;
        if (!user.value.trim()) return toast("Type your Jellyfin username", { error: true });
        btn.disabled = true;
        try {
          const r = await post("/api/v1/me/jellyfin/password", { username: user.value.trim(), password: pass.value });
          pass.value = "";
          toast(`Connected as ${r.user_name || user.value}`);
          load();
        } catch (err) { btn.disabled = false; toast(err.message, { error: true }); }
      } }, "Connect"));
    swap(body,
      st.linked
        ? h("p.intro", `Your Jellyfin plays already come into your history. One more step: sign in once so Play can open VLC, Infuse or your own player.`)
        : h("p.intro", `Connect your account on ${st.server} so your plays come into your history, Play can open VLC or Infuse, and requests are yours.`),
      st.quick_connect === false ? null : qcBox,
      pwBox);

    async function startQC(e) {
      const btn = e.currentTarget;
      btn.disabled = true;
      stop();
      let got;
      try { got = await post("/api/v1/me/jellyfin/quick-connect"); }
      catch (err) { btn.disabled = false; toast(err.message, { error: true }); return; }
      code.textContent = got.code;
      qcNote.textContent = "Waiting for you to enter it…";
      btn.disabled = false;
      swap(btn, icon("refresh"), "New code");
      const started = Date.now();
      const poll = async () => {
        if (!code.isConnected) return stop();
        try {
          const r = await post("/api/v1/me/jellyfin/quick-connect/check", { secret: got.secret });
          if (r.linked) { toast(`Connected as ${r.user_name || "you"}`); load(); return; }
        } catch (err) { qcNote.textContent = err.message; return; }
        if (Date.now() - started > 5 * 60_000) { qcNote.textContent = "That code expired. Get a new one."; return; }
        timer = setTimeout(poll, 2500);
      };
      timer = setTimeout(poll, 2500);
    }
  }

  await load();
  return { onHide: stop, onShow: load };
}
