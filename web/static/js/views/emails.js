// Emails: the weekly digest (Mondays), the month in review (the 1st) and the
// year in review (1 January), written by the install's AI model from what you
// watched. On by default; switch each off here. Preview the last one or have
// it sent now. The "Turn this email off" link in every digest opens this page
// with ?off=week|month|year and switches that one off at once.
import { get, post, put } from "../api.js";
import { back, setQuery } from "../nav.js";
import { sheet, toast } from "../ui.js";
import { fmtDate, h, icon, swap } from "../util.js";

export default async function render(root, { query } = {}) {
  const body = h("div", h("div.loadmore", h("div.spin")));
  root.append(h("header.back-head", h("button.iconbtn", { onclick: () => back("/profile"), "aria-label": "Back" }, icon("left")), h("h1", "Emails")), body);
  let st, me;
  try { [st, me] = await Promise.all([get("/api/v1/me/emails", { fresh: true }), get("/api/v1/me")]); }
  catch (e) { swap(body, h("div.err-box", e.message)); return; }

  async function save(patch) {
    try { st = await put("/api/v1/me/emails", patch); paint(); toast("Saved"); }
    catch (e) { toast(e.message, { error: true }); }
  }

  async function make(kind, btn, send) {
    btn.disabled = true;
    const label = btn.textContent;
    swap(btn, h("span.spin"), send ? "Sending…" : "Writing it…");
    try {
      const d = await post(`/api/v1/me/digests/${kind}`);
      if (send) {
        const r = await post(`/api/v1/me/digests/${d.id}/send`);
        toast(`Sent to ${r.sent_to}`);
      } else {
        sheet(() => h("div", h("h2", d.subject), h("div.sub", "Preview"),
          h("iframe.digest-frame", { src: `/api/v1/me/digests/${d.id}/html`, title: d.subject })));
      }
    } catch (e) { toast(e.message, { error: true }); }
    btn.disabled = false;
    btn.textContent = label;
  }

  function sw(on, onToggle, label) {
    return h("button.switch" + (on ? ".on" : ""), { role: "switch", "aria-checked": String(on), "aria-label": label, onclick: onToggle });
  }

  const FLAG = { week: "weekly", month: "monthly", year: "yearly" };
  const NAME = { week: "weekly digest", month: "month in review", year: "year in review" };
  let turnedOff = null;
  const off = query?.get("off");
  if (FLAG[off]) {
    // From the link in a digest: switch that email off now, and say so.
    try { st = await put("/api/v1/me/emails", { [FLAG[off]]: false }); turnedOff = off; }
    catch (e) { toast(e.message, { error: true }); }
    setQuery({ off: null }); // a reload doesn't switch it off again
  }

  function kindBlock(kind, title, when) {
    const on = st[FLAG[kind]];
    return h("div.panel",
      h("div.panel-head", h("div.grow", h("h2", title), h("p.soft", when)), sw(on, () => save({ [FLAG[kind]]: !on }), title)),
      h("div.two",
        h("button.btn.btn-small.btn-quiet", { onclick: (e) => make(kind, e.currentTarget, false) }, "Preview the last one"),
        me.features.email ? h("button.btn.btn-small.btn-quiet", { onclick: (e) => make(kind, e.currentTarget, true) }, "Send it to me now") : null));
  }

  function paint() {
    const address = h("input.input", { type: "email", value: st.address || "", placeholder: "you@example.com", autocomplete: "email" });
    swap(body,
      turnedOff ? h("div.note", { role: "status" }, h("b", `The ${NAME[turnedOff]} is off.`),
        " You won't get it any more. Switch it back on below whenever you like.") : null,
      h("p.intro", "These emails are on unless you switch them off, and one is only sent when you watched, rated or loved something, or something new came into the library."),
      me.features.email ? null : h("div.note", "Email isn't set up on this install yet, so these are previews only."),
      me.features.ai ? null : h("div.note", "No AI model is set up, so the emails use plain wording."),
      kindBlock("week", "Weekly digest", "Mondays: last week's films and episodes, your ratings, what's new and what's next."),
      kindBlock("month", "Month in review", `The 1st of each month: the month in film, your film of the month and favourite genres. Next: ${fmtDate(nextFirst(), { month: "long", day: "numeric" })}.`),
      kindBlock("year", "Year in review", "1 January: your year in film — the numbers, month by month, your best films and most-watched shows."),
      h("div.section-label", "Send to"),
      h("div", { style: { padding: "0 var(--gutter)" } },
        h("div.field", address),
        h("button.btn.btn-small.btn-quiet", { onclick: () => save({ address: address.value.trim() }) }, "Save address")));
  }

  function nextFirst() {
    const d = new Date();
    return new Date(d.getFullYear(), d.getMonth() + 1, 1).toISOString().slice(0, 10);
  }

  paint();
}
