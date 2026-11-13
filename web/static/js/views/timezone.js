// Profile's "Time zone": the zone this person's days follow (history's days,
// "this year", Home's daily rows, the digest weeks). Automatic: their
// browser or phone reports it (api.js reportTimeZone). Or a zone picked here
// by hand, which the server keeps whatever their devices report until they
// pick Automatic again.
import { deviceTimeZone, get, put } from "../api.js";
import { sheet, toast } from "../ui.js";
import { h, icon } from "../util.js";

// "America/Argentina/Buenos_Aires" -> "America/Argentina/Buenos Aires".
const pretty = (zone) => zone.replace(/_/g, " ");

// The Settings row: the zone now and who sets it; a tap opens the picker.
// onChange runs after a choice is saved (Profile reloads its numbers, which
// may now count a different "this year").
export function timeZoneRow(me, onChange) {
  const zone = me.time_zone;
  const manual = me.time_zone_mode === "manual";
  const sub = manual ? `${pretty(zone)} · chosen by you`
    : `Automatic (this device)${zone ? ` · ${pretty(zone)}` : ""}`;
  return h("button.trow", { onclick: () => picker(me, onChange), "aria-haspopup": "dialog" },
    h("span.ic", icon("globe")), h("span.tx", h("b", "Time zone"), h("span", sub)), icon("right"));
}

function check(on) { return h("span.check" + (on ? ".on" : ""), on ? icon("check") : null); }

function picker(me, onChange) {
  const device = deviceTimeZone();
  const manual = me.time_zone_mode === "manual";

  async function choose(close, mode, zone) {
    close();
    try {
      await put("/api/v1/me/time-zone/choice", { mode, time_zone: zone });
      toast(mode === "manual" ? `Time zone: ${pretty(zone)}` : "Time zone follows this device");
      onChange();
    } catch (e) { toast(e.message, { error: true }); }
  }

  sheet((close) => {
    const list = h("div", h("div.loadmore", h("div.spin")));
    const search = h("input.input", { type: "search", placeholder: "Search a city or UTC+2", "aria-label": "Search time zones",
      autocomplete: "off", enterkeyhint: "search" });
    // Automatic needs the device's zone: it is stored at once, so the days
    // are right now rather than after the next page load's report.
    const auto = h("button.opt", { disabled: !device, onclick: () => choose(close, "automatic", device) },
      h("span.grow", "Automatic (this device)",
        h("small", device ? `This device is on ${pretty(device)}` : "This browser doesn't say its time zone")),
      check(!manual));

    get("/api/v1/time-zones", { ttl: 3_600_000 }).then(({ zones }) => {
      // One button per zone, each with its search text: name words and the
      // offset with and without the leading zero ("utc+05:30", "utc+5:30").
      const rows = zones.map((z) => {
        const on = manual && z.name === me.time_zone;
        const el = h("button.opt", { onclick: () => choose(close, "manual", z.name) },
          h("span.grow", pretty(z.name), h("small", z.offset)), check(on));
        return { el, key: `${pretty(z.name)} ${z.offset} ${z.offset.replace(/([+-])0/, "$1")}`.toLowerCase() };
      });
      const empty = h("div.sub", { hidden: true }, "No time zone matches that.");
      list.replaceChildren(...rows.map((r) => r.el), empty);
      // Filtering hides rows instead of rebuilding them: ~420 buttons, made once.
      search.addEventListener("input", () => {
        const words = search.value.toLowerCase().replace(/_/g, " ").split(/\s+/).filter(Boolean);
        let shown = 0;
        for (const r of rows) {
          const hit = words.every((w) => r.key.includes(w));
          r.el.hidden = !hit;
          shown += hit;
        }
        empty.hidden = shown > 0;
      });
      // The chosen zone is in view when the sheet opens.
      const chosen = rows.find((r) => r.el.querySelector(".check.on"));
      if (chosen) requestAnimationFrame(() => chosen.el.scrollIntoView({ block: "center" }));
    }).catch((e) => list.replaceChildren(h("div.err-box", e.message)));

    return h("div",
      h("h2", "Time zone"),
      h("div.sub", "Your history's days, “this year”, Home's daily rows and the digest weeks follow it."),
      auto,
      h("div.field.tz-search", search),
      list);
  });
}
