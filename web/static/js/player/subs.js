// Text subtitles (SRT, WebVTT; Jellyfin sends them all as WebVTT), drawn by
// the player itself rather than by the browser's <track>: so the delay and
// the size apply at once, and the lines move up out of the way of the
// controls. On an iPhone, whose fullscreen is the system's own player, the
// same cues are handed to a native text track while it is open.
import { h } from "../util.js";

function seconds(stamp) {
  const parts = stamp.trim().split(":").map(Number);
  if (parts.some((n) => Number.isNaN(n))) return NaN;
  return parts.reduce((acc, n) => acc * 60 + n, 0);
}

// parseVtt reads WebVTT into [{start, end, text}], sorted by start.
// Comments, styles and regions are skipped; cue settings are ignored (the
// player places every line itself).
export function parseVtt(text) {
  const cues = [];
  const blocks = text.replace(/\r\n?/g, "\n").split(/\n{2,}/);
  for (const block of blocks) {
    const lines = block.split("\n");
    const at = lines.findIndex((l) => l.includes("-->"));
    if (at < 0) continue;
    const [a, rest] = lines[at].split("-->");
    const start = seconds(a), end = seconds(rest.trim().split(/\s+/)[0]);
    const body = lines.slice(at + 1).join("\n").trim();
    if (Number.isNaN(start) || Number.isNaN(end) || end <= start || !body) continue;
    cues.push({ start, end, text: body });
  }
  return cues.sort((x, y) => x.start - y.start);
}

const ENTITIES = { "&amp;": "&", "&lt;": "<", "&gt;": ">", "&nbsp;": " ", "&lrm;": "‎", "&rlm;": "‏" };
const decode = (s) => s.replace(/&(amp|lt|gt|nbsp|lrm|rlm);/g, (m) => ENTITIES[m]);

// cueNodes turns a cue's text into nodes: <i>, <b> and <u> are kept as
// elements, every other tag is dropped, text is never parsed as HTML.
export function cueNodes(text) {
  const root = document.createDocumentFragment();
  const stack = [root];
  const tag = /<(\/?)([a-zA-Z]+)[^>]*>/g;
  let last = 0, m;
  const add = (s) => { if (s) stack[stack.length - 1].append(decode(s)); };
  while ((m = tag.exec(text))) {
    add(text.slice(last, m.index));
    last = tag.lastIndex;
    const name = m[2].toLowerCase();
    if (!["i", "b", "u"].includes(name)) continue;
    if (m[1]) { if (stack.length > 1 && stack[stack.length - 1].localName === name) stack.pop(); continue; }
    const el = document.createElement(name);
    stack[stack.length - 1].append(el);
    stack.push(el);
  }
  add(text.slice(last));
  return root;
}

export class TextSubs {
  constructor(video, layer) {
    this.video = video;
    this.layer = layer;
    this.cues = [];
    this.delay = 0;
    this.shown = "";
    this.native = null;
    this.loading = null;
  }

  // load fetches a track; a newer load wins over one still on its way.
  async load(url, label = "", lang = "") {
    const ticket = Symbol("load");
    this.loading = ticket;
    const res = await fetch(url, { credentials: "same-origin" });
    if (!res.ok) throw new Error(`Subtitles didn't load (${res.status})`);
    const text = await res.text();
    if (this.loading !== ticket) return;
    this.cues = parseVtt(text);
    this.label = label;
    this.lang = lang;
    this.syncNative();
    this.render(true);
  }

  clear() {
    this.loading = null;
    this.cues = [];
    this.syncNative();
    this.render(true);
  }

  setDelay(d) {
    this.delay = d;
    this.syncNative();
    this.render(true);
  }

  // active is every cue on screen at time t: a cue shows from start+delay
  // to end+delay (a positive delay shows subtitles later).
  active(t) {
    const at = t - this.delay;
    return this.cues.filter((c) => c.start <= at && at < c.end);
  }

  render(force = false) {
    const now = this.active(this.video.currentTime || 0);
    const key = now.map((c) => c.start + ":" + c.text).join("|");
    if (!force && key === this.shown) return;
    this.shown = key;
    this.layer.replaceChildren(...now.map((c) => h("div.pl-cue", ...c.text.split("\n").flatMap((line, i) =>
      i ? [h("br"), cueNodes(line)] : [cueNodes(line)]))));
  }

  // An iPhone's fullscreen is the system player, which only shows the
  // video's own text tracks: the cues (with the delay) are copied to one
  // while it is open.
  syncNative() {
    if (!this.native) return;
    const t = this.native;
    while (t.cues && t.cues.length) t.removeCue(t.cues[0]);
    for (const c of this.cues) {
      t.addCue(new VTTCue(c.start + this.delay, c.end + this.delay, c.text.replace(/<[^>]+>/g, "")));
    }
  }

  nativeOn(on) {
    if (on && !this.native && this.video.addTextTrack) {
      this.native = this.video.addTextTrack("subtitles", this.label || "Subtitles", this.lang || "");
      this.syncNative();
    }
    if (this.native) this.native.mode = on && this.cues.length ? "showing" : "hidden";
  }
}
