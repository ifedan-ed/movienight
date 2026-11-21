// Libraries (admins): Movie Night's own media libraries. Each is a name,
// films or shows, and folders as the media service sees them. Scans run on
// a worker (every 30 minutes, or Scan now); this page shows their progress
// and the files that can't be read or matched, with a fix by TMDB id.
import { del, get, post, put } from "../api.js";
import { back } from "../nav.js";
import { confirmSheet, sheet, toast } from "../ui.js";
import { fmtDate, h, icon, swap } from "../util.js";

const POLL_MS = 2000; // while a scan is queued or running

export default async function render(root) {
  const body = h("div", h("div.loadmore", h("div.spin")));
  root.append(h("header.back-head", h("button.iconbtn", { onclick: () => back("/profile"), "aria-label": "Back" }, icon("left")),
    h("h1", "Libraries")), body);
  let data = null;
  let timer = null;
  const open = new Set(); // libraries whose problems are shown

  async function load() {
    clearTimeout(timer);
    try { data = await get("/api/v1/libraries", { fresh: true }); }
    catch (e) { swap(body, h("div.err-box", e.message)); return; }
    paint();
    if (root.isConnected && data.libraries.some((l) => l.scan)) timer = setTimeout(load, POLL_MS);
  }

  function paint() {
    if (!data.enabled) {
      swap(body, h("div.note", "Media libraries are off on this install (MEDIA_URL)."));
      return;
    }
    swap(body,
      data.media_error ? h("div.err-box", { role: "status" }, data.media_error) : null,
      data.libraries.map(card),
      h("div.lib-add", h("button.btn.btn-request", { onclick: () => edit(null) }, icon("plus"), "Add library")));
  }

  function card(l) {
    const scan = l.scan;
    const counts = `${l.titles} ${l.kind === "shows" ? (l.titles === 1 ? "show" : "shows") : (l.titles === 1 ? "film" : "films")} · ${l.files} files`;
    let state;
    if (scan && scan.state === "queued") state = h("span.soft", "Waiting to scan…");
    else if (scan) {
      const pct = scan.to_probe ? Math.round((scan.probed / scan.to_probe) * 100) : 0;
      state = h("div.lib-progress", { role: "progressbar", "aria-valuenow": pct, "aria-valuemin": 0, "aria-valuemax": 100 },
        h("div.lib-bar", h("i", { style: { width: `${scan.to_probe ? pct : 8}%` } })),
        h("span.soft", scan.to_probe ? `Reading ${scan.probed} of ${scan.to_probe} new files` : `Looking through ${scan.seen} files`));
    } else if (l.scan_error) state = h("span.lib-error", l.scan_error);
    else state = h("span.soft", l.scanned_at ? `Scanned ${fmtDate(l.scanned_at, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" })}` : "Not scanned yet");

    const problems = h("div.lib-problems");
    if (open.has(l.id)) showProblems(l, problems);
    return h("section.panel.lib", { dataset: { library: l.id } },
      h("div.lib-head",
        h("span.ic", icon(l.kind === "shows" ? "tv" : "film")),
        h("div.tx", h("h2", l.name), h("span.soft", counts))),
      h("div.lib-paths", l.paths.map((p) => h("code", p))),
      state,
      l.folder_problems.map((f) => h("div.lib-error", `${f.path}: ${f.error}`)),
      h("div.lib-actions",
        h("button.btn.btn-small.btn-request", { disabled: !!scan, onclick: async () => {
          try { await post(`/api/v1/libraries/${l.id}/scan`); await load(); }
          catch (e) { toast(e.message, { error: true }); }
        } }, icon("refresh"), "Scan now"),
        h("button.btn.btn-small.btn-quiet", { onclick: () => edit(l) }, icon("edit"), "Edit"),
        h("button.btn.btn-small.btn-danger", { "aria-label": `Remove ${l.name}`, onclick: () => confirmSheet(`Remove ${l.name}?`,
          "Its titles leave Movie Night. The files stay where they are.", "Remove", async () => {
            try { await del(`/api/v1/libraries/${l.id}`); toast("Removed"); await load(); }
            catch (e) { toast(e.message, { error: true }); }
          }) }, icon("trash"))),
      l.problems ? h("button.lib-toggle", { "aria-expanded": String(open.has(l.id)), onclick: () => {
        if (open.has(l.id)) open.delete(l.id); else open.add(l.id);
        paint();
      } }, icon(open.has(l.id) ? "up" : "down"), `${l.problems} ${l.problems === 1 ? "file needs" : "files need"} a look`) : null,
      problems);
  }

  async function showProblems(l, box) {
    swap(box, h("div.spin.inline"));
    let res;
    try { res = await get(`/api/v1/libraries/${l.id}/problems`, { fresh: true }); }
    catch (e) { swap(box, h("div.lib-error", e.message)); return; }
    swap(box, res.items.map((p) => h("div.sres.lib-file",
      h("div.tx", h("b", p.path), h("span", p.problem)),
      p.readable ? h("button.btn.btn-small.btn-quiet", { onclick: () => match(l, p) }, "Match") : null)));
  }

  function match(l, p) {
    sheet((close) => {
      const input = h("input.input", { type: "text", inputmode: "numeric", pattern: "[0-9]*", placeholder: "TMDB id",
        "aria-label": "TMDB id", autocomplete: "off" });
      const save = async (btn) => {
        const id = Number(input.value.trim().replace(/\D/g, ""));
        if (!id) { input.focus(); return; }
        btn.disabled = true;
        try {
          const r = await put(`/api/v1/libraries/files/${p.file_id}/match`, { tmdb_id: id });
          close();
          toast(r.files === 1 ? "Matched" : `Matched ${r.files} files`);
          await load();
        } catch (e) { btn.disabled = false; toast(e.message, { error: true }); }
      };
      const ok = h("button.btn.btn-request", { onclick: () => save(ok) }, icon("check"), "Match");
      input.addEventListener("keydown", (e) => { if (e.key === "Enter") save(ok); });
      const query = [p.title, p.year].filter(Boolean).join(" ");
      return h("div",
        h("h2", p.title || p.path),
        h("p.soft", p.path),
        h("div.field", input),
        h("a.btn-note", { href: `https://www.themoviedb.org/search/${l.kind === "shows" ? "tv" : "movie"}?query=${encodeURIComponent(query)}`,
          target: "_blank", rel: "noopener" }, "Find it on TMDB"),
        h("div", { style: { height: "12px" } }),
        ok);
    });
  }

  function edit(l) {
    sheet((close) => {
      let kind = l ? l.kind : "movies";
      const name = h("input.input", { type: "text", value: l ? l.name : "", maxlength: 100, placeholder: "Name", "aria-label": "Name" });
      const paths = h("textarea.input.lib-paths-in", { rows: 3, placeholder: data.folders[0] ? `${data.folders[0]}/Films` : "/media/Films",
        "aria-label": "Folders, one a line", spellcheck: "false", autocapitalize: "off" });
      paths.value = l ? l.paths.join("\n") : "";
      const kinds = h("div.chips.lib-kinds");
      const paintKinds = () => swap(kinds, [["movies", "Films", "film"], ["shows", "Shows", "tv"]].map(([k, label, ic]) =>
        h("button.chip" + (kind === k ? ".on" : ""), { type: "button", disabled: !!l && kind !== k, "aria-pressed": String(kind === k),
          onclick: () => { kind = k; paintKinds(); } }, icon(ic), label)));
      paintKinds();
      // The media service's folders: a tap adds one as a line.
      const roots = data.folders.length ? h("div.chips.lib-roots", data.folders.map((f) => h("button.chip", { type: "button", onclick: () => {
        paths.value = (paths.value.trim() ? paths.value.trim() + "\n" : "") + f + "/";
        paths.focus();
      } }, f))) : null;
      const save = async (btn) => {
        const body = { name: name.value.trim(), paths: paths.value.split("\n").map((s) => s.trim()).filter(Boolean) };
        if (!body.name) { name.focus(); return; }
        if (!body.paths.length) { paths.focus(); return; }
        btn.disabled = true;
        try {
          if (l) await put(`/api/v1/libraries/${l.id}`, body);
          else await post("/api/v1/libraries", { ...body, kind });
          close();
          toast(l ? "Saved" : "Added: scanning");
          await load();
        } catch (e) { btn.disabled = false; toast(e.message, { error: true }); }
      };
      return h("div.lib-form",
        h("h2", l ? l.name : "Add library"),
        h("div.field", name),
        kinds,
        h("div.field", { style: { marginTop: "10px" } }, h("label", "Folders"), paths, roots),
        h("button.btn.btn-request", { onclick: (e) => save(e.currentTarget) }, icon("check"), l ? "Save" : "Add"));
    });
  }

  await load();
  return { onShow: load };
}
