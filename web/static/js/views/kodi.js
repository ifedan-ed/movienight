// Kodi: install the Movie Night add-on, connect it with the code the TV
// shows, see and sign out the connected ones. Plus a personal token for any
// other app that wants to report what you watched.
import { del, get, post } from "../api.js";
import { back } from "../nav.js";
import { confirmSheet, toast } from "../ui.js";
import { h, icon, swap } from "../util.js";

export default async function render(root) {
  const body = h("div", h("div.loadmore", h("div.spin")));
  root.append(h("header.back-head", h("button.iconbtn", { onclick: () => back("/profile"), "aria-label": "Back" }, icon("left")), h("h1", "Kodi and other players")), body);
  let me, devices;
  try { [me, devices] = await Promise.all([get("/api/v1/me", { fresh: true }), get("/api/v1/me/devices", { fresh: true })]); }
  catch (e) { swap(body, h("div.err-box", e.message)); return; }
  const source = `${location.origin}/kodi/`;
  const endpoint = `${location.origin}/api/v1/scrobble`;

  function copy(text) {
    navigator.clipboard?.writeText(text).then(() => toast("Copied"), () => toast("Copy it by hand", { error: true }));
  }

  const codeInput = h("input.input", {
    type: "text", placeholder: "ABCD-2345", maxlength: 12, autocapitalize: "characters", autocomplete: "off", spellcheck: "false",
    "aria-label": "Code shown on the TV", style: { letterSpacing: "0.12em", fontSize: "20px", textTransform: "uppercase" },
  });
  // A confirmed code is not a signed-in TV yet: the TV gets its sign-in
  // when it next asks (every few seconds), and only then is it listed. So
  // the page shows it as waiting and looks again until it is there, instead
  // of a list that stays empty after "Connect" until a reload.
  let waiting = null; // the device name just confirmed, until the TV collects its sign-in
  async function watchForTv(known) {
    const until = Date.now() + 10 * 60 * 1000; // a code's lifetime
    while (waiting && root.isConnected && Date.now() < until) {
      await new Promise((r) => setTimeout(r, 3000)); // the TV's own polling interval
      try { devices = await get("/api/v1/me/devices", { fresh: true }); } catch { continue; }
      if (devices.some((d) => d.kind === "scrobble" && !known.has(d.id))) { waiting = null; paint(); return; }
    }
    if (waiting) { waiting = null; if (root.isConnected) paint(); }
  }
  async function connect() {
    const code = codeInput.value.trim();
    if (!code) { codeInput.focus(); return; }
    try {
      const r = await post("/api/v1/me/devices/pair", { code });
      codeInput.value = "";
      toast(`Confirmed ${r.device}`);
      waiting = r.device;
      paint();
      watchForTv(new Set(devices.map((d) => d.id)));
    } catch (e) { toast(e.message, { error: true }); }
  }
  codeInput.addEventListener("keydown", (e) => { if (e.key === "Enter") connect(); });

  function paint(token = null) {
    const kodis = devices.filter((d) => d.kind === "scrobble");
    swap(body,
      h("p.intro", "The Movie Night add-on for Kodi puts what you finish in Kodi into your history here, movies and episodes alike."),
      h("div.panel",
        h("h2", "Connect a Kodi"),
        h("p.soft", "Open the Movie Night add-on in Kodi (Add-ons, Program add-ons). It shows a code; type it here."),
        h("div.two", { style: { alignItems: "center", gridTemplateColumns: "1fr auto" } }, codeInput,
          h("button.btn.btn-request", { onclick: connect }, icon("plug"), "Connect"))),
      kodis.length || waiting ? h("div.panel",
        h("h2", "Connected"),
        waiting ? h("div.sres.waiting", h("div.tx", h("b", waiting), h("span", "Finishing on the TV…"))) : null,
        kodis.map((d) => h("div.sres",
          h("div.tx", h("b", d.name), h("span", d.last_used_at ? `Last report ${new Date(d.last_used_at).toLocaleDateString()}` : `Connected ${new Date(d.created_at).toLocaleDateString()}`),
            // Its sign-in runs out by itself: after a stretch unused, or a year after it connected.
            h("span", d.expires_because === "unused"
              ? `Signs out ${new Date(d.expires_at).toLocaleDateString()} unless it reports before`
              : `Signs out ${new Date(d.expires_at).toLocaleDateString()}, a year after it connected`)),
          h("button.btn.btn-small.btn-danger", { onclick: () => confirmSheet(`Sign out ${d.name}?`, "It stops reporting what you watch until you connect it again.", "Sign out", async () => {
            try { await del(`/api/v1/me/devices/${d.id}`); devices = devices.filter((x) => x.id !== d.id); paint(); toast("Signed out"); }
            catch (e) { toast(e.message, { error: true }); }
          }) }, "Sign out")))) : null,
      h("div.panel",
        h("h2", "Install the add-on"),
        h("ol.steps",
          h("li", "In Kodi: Settings, System, Add-ons: turn on ", h("b", "Unknown sources"), "."),
          h("li", "Settings, File manager, Add source: enter this address and name it Movie Night:", h("div.token", source)),
          h("li", "Settings, Add-ons, ", h("b", "Install from zip file"), ", Movie Night, then the zip in it."),
          h("li", "Open the add-on: it shows the code for the box above.")),
        h("button.btn.btn-small.btn-quiet", { onclick: () => copy(source) }, icon("copy"), "Copy the address")),
      h("details.panel",
        h("summary", h("b", "Other apps"), h("span.soft", " — a personal token")),
        h("p.soft", "Any other app can report what you watched with a POST and your token as Authorization: Bearer …"),
        token
          ? [h("p.soft", "Shown only now."), h("div.token", token), h("button.btn.btn-small.btn-quiet", { onclick: () => copy(token) }, icon("copy"), "Copy")]
          : h("p.soft", me.scrobble_token_set ? "A token is set. Making a new one turns the old one off." : "No token yet."),
        h("div.two", { style: { marginTop: "12px" } },
          h("button.btn.btn-small.btn-request", { onclick: async () => {
            try { const r = await post("/api/v1/me/scrobble-token"); me.scrobble_token_set = true; paint(r.token); }
            catch (e) { toast(e.message, { error: true }); }
          } }, icon("key"), me.scrobble_token_set ? "New token" : "Make a token"),
          me.scrobble_token_set ? h("button.btn.btn-small.btn-danger", { onclick: () => confirmSheet("Turn the token off?", "Apps using it stop reporting watches.", "Turn off", async () => {
            try { await del("/api/v1/me/scrobble-token"); me.scrobble_token_set = false; paint(); toast("Token turned off"); }
            catch (e) { toast(e.message, { error: true }); }
          }) }, "Turn off") : null),
        h("div.token.small", endpoint),
        h("pre.code", '{"media_type": "movie", "tmdb_id": 603, "source": "kodi"}\n{"media_type": "episode", "tmdb_id": 1399, "season": 1, "episode": 2}')));
  }
  paint();
}
