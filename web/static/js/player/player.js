// Movie Night's own player ("Play here"). The server hands it a stream
// ticket and the file's tracks, chapters and segments
// (POST /api/v1/title/{media}/{id}/watch); every byte comes through Movie
// Night's stream proxy, so the browser never holds a Jellyfin key.
//
// Playback: the file itself in the <video> element when the browser can
// play it; Jellyfin's HLS otherwise, through hls.js, or Safari's own HLS.
// Another audio track, or a picture subtitle burnt into the video, asks the
// server for a fresh stream and carries on from the same second. Text
// subtitles are drawn here (subs.js); styled ASS/SSA by JASSUB, loaded only
// when such a track is chosen.
//
// Reports: start, every 10 s, pause and stop (POST /api/v1/watch/report).
// The server keeps Jellyfin's resume point and Movie Night's own, and a
// stop near the end records the watch.
import { post, report } from "../api.js";
import { back, go } from "../nav.js";
import { setState, states, toast } from "../ui.js";
import { h, icon, store, swap, unstore } from "../util.js";
import { caps, nativeHls } from "./caps.js";
import { TextSubs } from "./subs.js";

const HLS_JS = "/static/vendor/hls-1.7.4/hls.light.min.mjs";
const HLS_WORKER = "/static/vendor/hls-1.7.4/hls.worker.js";
const JASSUB = "/static/vendor/jassub-1.8.8/";

const REPORT_EVERY = 10_000; // ms between progress reports while playing
const IDLE_AFTER = 3_000;    // ms without a touch or a move before the controls hide
const RESUME_MIN = 30;       // s: a resume point this early is not worth asking about
const RESUME_MAX_SHARE = 0.95;
const NEXT_BEFORE_END = 30;  // s before the end of an episode the Next button shows
const SPEEDS = [0.5, 0.75, 1, 1.25, 1.5, 1.75, 2];
const SIZES = [0.75, 0.9, 1, 1.15, 1.3, 1.5, 1.75, 2];
const DELAY_STEP = 0.5;
const PICTURES = [
  ["fit", "Fit"], ["fill", "Fill"], ["stretch", "Stretch"],
  ["zoom-110", "Zoom 110%"], ["zoom-125", "Zoom 125%"], ["zoom-150", "Zoom 150%"],
];
const SKIP_LABEL = { intro: "Skip intro", credits: "Skip credits", recap: "Skip recap", preview: "Skip preview", commercial: "Skip" };

export function clock(s) {
  s = Math.max(0, Math.floor(s || 0));
  const hr = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
  const two = (n) => String(n).padStart(2, "0");
  return hr ? `${hr}:${two(m)}:${two(sec)}` : `${m}:${two(sec)}`;
}

const signed = (d) => (d > 0 ? "+" : d < 0 ? "−" : "") + Math.abs(d).toFixed(1) + " s";

export class Player {
  constructor(root, { media, tmdbId, season, episode }) {
    this.root = root;
    this.media = media;
    this.tmdbId = tmdbId;
    this.season = season;
    this.episode = episode;
    this.destroyed = false;
    this.listeners = [];
    this.delayKey = `subdelay:${media}:${tmdbId}`;
  }

  // ---------------------------------------------------------------- start

  async start() {
    this.destroyed = false;
    swap(this.root, h("div.pl-wait", h("div.spin")));
    try {
      this.info = await this.ask({});
    } catch (e) {
      if (this.destroyed) return;
      this.fail(e.message);
      return;
    }
    if (this.destroyed) return;
    this.build();
    const r = this.info.resume_s;
    if (r >= RESUME_MIN && (!this.info.duration_s || r < this.info.duration_s * RESUME_MAX_SHARE)) this.askResume(r);
    else this.load(0, true);
  }

  // ask is POST .../watch: a new play, or (replace) the same play on a
  // fresh stream with other tracks.
  ask({ replace = null, audio = null, burn = null }) {
    return post(`/api/v1/title/${this.media}/${this.tmdbId}/watch`, {
      season: this.season, episode: this.episode, caps: caps(),
      ...(audio != null ? { audio } : {}), ...(burn != null ? { burn_subtitle: burn } : {}),
      ...(replace ? { replace } : {}),
    });
  }

  fail(message) {
    swap(this.root, h("div.pl-fail",
      h("p", message),
      h("button.btn.btn-quiet.btn-small", { onclick: () => this.exit() }, icon("left"), "Back")));
  }

  exit() {
    back(`/title/${this.media}/${this.tmdbId}`);
  }

  // ---------------------------------------------------------------- the page

  build() {
    const info = this.info;
    this.v = h("video.pl-video", { playsinline: true, preload: "auto", poster: info.backdrop, "aria-label": info.title });
    this.subLayer = h("div.pl-subs", { "aria-live": "off" });
    this.subs = new TextSubs(this.v, this.subLayer);
    this.subs.delay = store(this.delayKey) || 0;
    this.size = store("subsize") || 1;
    this.speed = 1;
    this.picture = "fit";
    this.audio = info.audio_index;
    this.sub = null;

    this.seek = h("input.pl-seek", { type: "range", min: 0, max: info.duration_s || 0, step: 0.1, value: 0, "aria-label": "Seek" });
    this.buf = h("div.pl-buf");
    this.ticks = h("div.pl-ticks");
    this.timeEl = h("span.pl-time", "0:00");
    this.playBtn = this.button("play", "Play", () => this.toggle());
    this.bigPlay = this.button("play", "Play", () => this.toggle(), "pl-big");
    this.muteBtn = this.button("volume", "Mute", () => this.mute());
    this.vol = h("input.pl-vol", { type: "range", min: 0, max: 1, step: 0.05, value: store("volume") ?? 1, "aria-label": "Volume" });
    this.fsBtn = this.button("fullscreen", "Fullscreen", () => this.fullscreen());
    this.pipBtn = this.pipSupported() ? this.button("pip", "Picture in picture", () => this.pip()) : null;
    this.ccBtn = this.button("subtitles", "Subtitles", (e) => this.menu("subs", e.currentTarget));
    this.audioBtn = info.audio.length > 1 ? this.button("audio", "Audio", (e) => this.menu("audio", e.currentTarget)) : null;
    this.skipBtn = h("button.pl-skip", { type: "button", hidden: true, onclick: () => this.skip() });
    this.nextBox = info.next ? h("div.pl-next", { hidden: true },
      h("button.pl-next-btn", { type: "button", onclick: () => this.next() }, icon("skipNext"),
        h("span", h("b", "Next episode"), h("small", `S${info.next.season}E${info.next.episode} · ${info.next.title}`)))) : null;
    this.menuEl = h("div.pl-menu", { hidden: true, role: "menu" });
    this.spinner = h("div.spin.pl-spin", { hidden: true });
    this.osdEl = h("div.pl-osd", { hidden: true, role: "status" });

    const bar = h("div.pl-bar",
      h("div.pl-seekwrap", this.buf, this.ticks, this.seek),
      h("div.pl-row",
        h("div.pl-desk", this.playBtn, this.button("back10", "Back 10 seconds", () => this.jump(-10)),
          this.button("fwd10", "Forward 10 seconds", () => this.jump(10))),
        h("div.pl-volbox", this.muteBtn, this.vol),
        this.timeEl,
        h("span.pl-gap"),
        this.ccBtn, this.audioBtn,
        this.button("gear", "Settings", (e) => this.menu("settings", e.currentTarget)),
        this.pipBtn, this.fsBtn));
    this.el = h("div.pl", { tabindex: "-1" },
      this.v, this.subLayer,
      h("div.pl-shade"),
      h("div.pl-top",
        this.button("left", "Back", () => this.exit()),
        h("div.pl-title", info.title)),
      h("div.pl-center",
        this.button("back10", "Back 10 seconds", () => this.jump(-10), "pl-mid"),
        this.bigPlay, this.spinner,
        this.button("fwd10", "Forward 10 seconds", () => this.jump(10), "pl-mid")),
      this.skipBtn, this.nextBox, this.osdEl, bar, this.menuEl);
    swap(this.root, this.el);
    this.applySize();
    this.v.volume = Number(this.vol.value);
    this.paintTicks();
    this.wire();
    this.el.focus({ preventScroll: true });
  }

  // osd shows a short word over the picture (speed, delay, a subtitle
  // track): one at a time, replaced by the next press, gone after a moment.
  osd(text) {
    this.osdEl.textContent = text;
    this.osdEl.hidden = false;
    clearTimeout(this.osdTimer);
    this.osdTimer = setTimeout(() => { this.osdEl.hidden = true; }, 1200);
  }

  button(name, label, onclick, cls = "") {
    return h("button.pl-btn" + (cls ? "." + cls : ""), { type: "button", "aria-label": label, title: label, onclick }, icon(name));
  }

  setIcon(btn, name, label) {
    btn.replaceChildren(icon(name));
    btn.setAttribute("aria-label", label);
    btn.title = label;
  }

  duration() {
    return this.info.duration_s || (Number.isFinite(this.v.duration) ? this.v.duration : 0);
  }

  paintTicks() {
    const d = this.duration();
    this.ticks.replaceChildren(...(d ? this.info.chapters.filter((c) => c.start_s > 1 && c.start_s < d - 1)
      .map((c) => h("i", { style: { left: `${(c.start_s / d) * 100}%` }, title: c.name })) : []));
  }

  // ---------------------------------------------------------------- loading

  // load plays the current stream from `at` seconds. A restart (another
  // audio track) passes play = whether it was playing.
  async load(at, play) {
    this.teardownEngine();
    const info = this.info;
    const v = this.v;
    this.loadedAt = at;
    if (info.mode === "hls" && !nativeHls(v)) {
      let Hls;
      try { ({ default: Hls } = await import(HLS_JS)); } catch { this.fail("The player didn't load"); return; }
      if (this.destroyed) return;
      if (!Hls.isSupported()) { this.fail("This browser can't play this file"); return; }
      const hls = new Hls({ workerPath: HLS_WORKER, startPosition: at, maxBufferLength: 30, backBufferLength: 90 });
      this.hls = hls;
      let recovered = false;
      hls.on(Hls.Events.ERROR, (_, d) => {
        if (!d.fatal || this.hls !== hls) return;
        if (d.type === Hls.ErrorTypes.MEDIA_ERROR && !recovered) { recovered = true; hls.recoverMediaError(); return; }
        this.streamFailed(d.response && d.response.code);
      });
      hls.loadSource(info.url);
      hls.attachMedia(v);
    } else {
      v.src = info.url;
      if (at > 0) {
        const seekOnce = () => { v.currentTime = at; };
        v.addEventListener("loadedmetadata", seekOnce, { once: true });
      }
    }
    if (play) this.play();
  }

  teardownEngine() {
    if (this.hls) { this.hls.destroy(); this.hls = null; }
    if (this.v.getAttribute("src")) { this.v.removeAttribute("src"); this.v.load(); }
  }

  // streamFailed: an expired ticket (403) or a dropped connection gets one
  // fresh stream from the same second; a second failure is shown.
  async streamFailed(status) {
    if (this.destroyed) return;
    if (this.retried) { this.fail(status === 403 ? "This stream link has expired" : "The stream stopped"); return; }
    this.retried = true;
    await this.restart({});
  }

  // restart asks for a fresh stream of the same play (other audio, a burnt-in
  // subtitle or none) and carries on from the same second.
  async restart({ audio = this.audio, burn = this.info.burnt_subtitle }) {
    const at = this.v.currentTime || this.loadedAt || 0;
    const playing = !this.v.paused;
    this.spinner.hidden = false;
    let info;
    try {
      info = await this.ask({ replace: this.info.ticket, audio, burn });
    } catch (e) {
      this.spinner.hidden = true;
      toast(e.message, { error: true });
      return false;
    }
    if (this.destroyed) return false;
    this.info = info;
    this.audio = info.audio_index;
    this.startedTicket = null; // the new stream is a new Jellyfin play session
    await this.load(at, playing);
    // Subtitle addresses carry the ticket: reload the text track from the
    // new stream.
    const chosen = this.info.subtitles.find((s) => s.index === this.sub);
    if (chosen && chosen.kind !== "image") await this.showSub(chosen);
    return true;
  }

  // ---------------------------------------------------------------- resume

  askResume(at) {
    const box = h("div.pl-resume", { role: "dialog", "aria-label": "Resume" },
      h("button.btn.btn-play", { onclick: () => { box.remove(); this.load(at, true); } }, icon("play"), `Resume from ${clock(at)}`),
      h("button.btn.btn-ghost", { onclick: () => { box.remove(); this.load(0, true); } }, "Start over"));
    this.el.append(box);
    this.resumeBox = box;
    box.querySelector("button").focus();
  }

  // ---------------------------------------------------------------- events

  on(target, type, fn, opts) {
    target.addEventListener(type, fn, opts);
    this.listeners.push(() => target.removeEventListener(type, fn, opts));
  }

  wire() {
    const v = this.v;
    this.on(v, "play", () => this.paintPlay());
    this.on(v, "pause", () => { this.paintPlay(); if (!v.ended && !v.seeking) this.send("pause"); this.wake(); });
    this.on(v, "playing", () => {
      this.spinner.hidden = true;
      this.send(this.startedTicket === this.info.ticket ? "progress" : "start");
      this.startedTicket = this.info.ticket;
      this.stopped = false;
      this.loop();
    });
    this.on(v, "waiting", () => { this.spinner.hidden = false; });
    this.on(v, "canplay", () => { this.spinner.hidden = true; });
    this.on(v, "timeupdate", () => this.tick());
    this.on(v, "seeked", () => this.tick());
    this.on(v, "durationchange", () => { if (!this.info.duration_s) { this.seek.max = this.duration(); this.paintTicks(); } });
    this.on(v, "progress", () => this.paintBuffer());
    this.on(v, "volumechange", () => this.paintVolume());
    this.on(v, "ended", () => this.ended());
    this.on(v, "error", () => { if (!this.hls && v.error) this.streamFailed(); });
    this.on(v, "click", () => this.toggle());
    // An iPhone's fullscreen is the system player: subtitles go to a native track.
    this.on(v, "webkitbeginfullscreen", () => this.subs.nativeOn(true));
    this.on(v, "webkitendfullscreen", () => this.subs.nativeOn(false));

    this.on(this.seek, "input", () => { this.dragging = true; this.paintTime(Number(this.seek.value)); });
    this.on(this.seek, "change", () => { this.dragging = false; v.currentTime = Number(this.seek.value); });
    this.on(this.vol, "input", () => { v.muted = false; v.volume = Number(this.vol.value); store("volume", v.volume); });

    for (const type of ["pointermove", "pointerdown", "keydown"]) this.on(this.el, type, () => this.wake());
    this.on(document, "keydown", (e) => this.key(e));
    this.on(document, "fullscreenchange", () => this.paintFullscreen());
    this.on(document, "webkitfullscreenchange", () => this.paintFullscreen());
    this.on(document, "pointerdown", (e) => { if (!this.menuEl.hidden && !this.menuEl.contains(e.target) && !e.target.closest(".pl-btn")) this.closeMenu(); });
    // A closing tab still sends its last report (keepalive).
    this.on(window, "pagehide", () => this.stop());

    this.timer = setInterval(() => { if (!v.paused && !v.ended) this.send("progress"); }, REPORT_EVERY);
    this.wake();
  }

  // loop redraws subtitles every frame while playing: timeupdate comes only
  // four times a second, too coarse for a line to appear on its word.
  loop() {
    if (this.frame) return;
    const step = () => {
      this.frame = null;
      if (this.destroyed || this.v.paused) return;
      this.subs.render();
      this.frame = requestAnimationFrame(step);
    };
    this.frame = requestAnimationFrame(step);
  }

  tick() {
    const t = this.v.currentTime;
    if (!this.dragging) { this.seek.value = t; this.paintTime(t); }
    this.subs.render();
    this.paintSkip(t);
  }

  paintTime(t) {
    const d = this.duration();
    this.timeEl.textContent = d ? `${clock(t)} / ${clock(d)}` : clock(t);
    this.seek.style.setProperty("--p", d ? `${(t / d) * 100}%` : "0%");
  }

  paintBuffer() {
    const v = this.v, d = this.duration();
    let end = 0;
    for (let i = 0; i < v.buffered.length; i++) {
      if (v.buffered.start(i) <= v.currentTime + 1) end = Math.max(end, v.buffered.end(i));
    }
    this.buf.style.width = d ? `${Math.min(100, (end / d) * 100)}%` : "0";
  }

  paintPlay() {
    const paused = this.v.paused;
    for (const b of [this.playBtn, this.bigPlay]) this.setIcon(b, paused ? "play" : "pause", paused ? "Play" : "Pause");
    this.el.classList.toggle("paused", paused);
  }

  paintVolume() {
    const v = this.v;
    const silent = v.muted || v.volume === 0;
    this.setIcon(this.muteBtn, silent ? "mute" : "volume", silent ? "Unmute" : "Mute");
    this.vol.value = v.muted ? 0 : v.volume;
  }

  paintFullscreen() {
    const on = !!(document.fullscreenElement || document.webkitFullscreenElement);
    this.setIcon(this.fsBtn, on ? "fullscreenExit" : "fullscreen", on ? "Exit fullscreen" : "Fullscreen");
  }

  // paintSkip shows "Skip intro" (and the like) inside a segment, and the
  // Next episode button in the credits or the last seconds.
  paintSkip(t) {
    const seg = this.info.segments.find((s) => t >= s.start_s && t < s.end_s - 1);
    this.skipBtn.hidden = !seg;
    if (seg) {
      this.skipBtn.textContent = SKIP_LABEL[seg.kind];
      this.skipSeg = seg;
    }
    if (this.nextBox) {
      const d = this.duration();
      const credits = seg && seg.kind === "credits";
      this.nextBox.hidden = !(this.v.ended || credits || (d && t >= d - NEXT_BEFORE_END));
      if (!this.nextBox.hidden && credits) this.skipBtn.hidden = true;
    }
  }

  wake() {
    this.el.classList.remove("idle");
    clearTimeout(this.idleTimer);
    this.idleTimer = setTimeout(() => {
      if (!this.v.paused && this.menuEl.hidden) this.el.classList.add("idle");
    }, IDLE_AFTER);
  }

  // ---------------------------------------------------------------- actions

  play() {
    const p = this.v.play();
    // A browser may refuse to start with sound until it is touched: the
    // big Play button stays for that.
    if (p && p.catch) p.catch(() => this.paintPlay());
  }

  toggle() {
    if (this.resumeBox && this.resumeBox.isConnected) return;
    if (this.v.paused) this.play(); else this.v.pause();
  }

  jump(s) {
    const d = this.duration();
    this.v.currentTime = Math.max(0, Math.min(d || Infinity, this.v.currentTime + s));
    this.wake();
  }

  mute() { this.v.muted = !this.v.muted; }

  volumeBy(step) {
    this.v.muted = false;
    this.v.volume = Math.max(0, Math.min(1, Math.round((this.v.volume + step) * 20) / 20));
    store("volume", this.v.volume);
  }

  fullscreen() {
    if (document.fullscreenElement || document.webkitFullscreenElement) {
      (document.exitFullscreen || document.webkitExitFullscreen).call(document);
      return;
    }
    const el = this.el;
    if (el.requestFullscreen) el.requestFullscreen().catch(() => {});
    else if (el.webkitRequestFullscreen) el.webkitRequestFullscreen();
    else if (this.v.webkitEnterFullscreen) this.v.webkitEnterFullscreen(); // iPhone
  }

  pipSupported() {
    return (document.pictureInPictureEnabled && !this.v.disablePictureInPicture) || !!this.v.webkitSupportsPresentationMode;
  }

  pip() {
    if (document.pictureInPictureElement) { document.exitPictureInPicture().catch(() => {}); return; }
    if (this.v.requestPictureInPicture) { this.v.requestPictureInPicture().catch(() => {}); return; }
    if (this.v.webkitSetPresentationMode) {
      this.v.webkitSetPresentationMode(this.v.webkitPresentationMode === "picture-in-picture" ? "inline" : "picture-in-picture");
    }
  }

  setSpeed(s) {
    this.speed = s;
    this.v.playbackRate = s;
    if (this.ass) this.ass.setRate?.(s);
  }

  speedBy(dir) {
    const i = SPEEDS.indexOf(this.speed);
    const next = SPEEDS[Math.max(0, Math.min(SPEEDS.length - 1, (i < 0 ? 2 : i) + dir))];
    this.setSpeed(next);
    this.osd(`Speed ${next}×`);
  }

  setPicture(p) {
    this.picture = p;
    this.el.dataset.picture = p;
  }

  skip() {
    const seg = this.skipSeg;
    if (!seg) return;
    if (seg.kind === "credits" && this.info.next && seg.end_s >= this.duration() - 2) { this.next(); return; }
    this.v.currentTime = seg.end_s;
  }

  next() {
    const n = this.info.next;
    if (!n) return;
    go(`/watch/tv/${this.tmdbId}?season=${n.season}&episode=${n.episode}`, { replace: true });
  }

  ended() {
    this.paintPlay();
    this.paintSkip(this.v.currentTime);
    this.stop(this.duration());
    this.el.classList.remove("idle");
    if (this.nextBox) this.nextBox.querySelector("button").focus();
  }

  // ---------------------------------------------------------------- subtitles

  async chooseSub(index) {
    const t = this.info.subtitles.find((s) => s.index === index) || null;
    const burnt = this.info.burnt_subtitle;
    this.sub = t ? t.index : null;
    if (t && t.kind === "image") {
      // A picture subtitle can only be burnt into the video by Jellyfin.
      this.clearSubs();
      if (burnt !== t.index) await this.restart({ burn: t.index });
      return;
    }
    if (burnt != null) {
      // Leaving a burnt-in subtitle: a clean stream first.
      if (!(await this.restart({ burn: null }))) return;
    }
    this.clearSubs();
    if (t) await this.showSub(t);
  }

  async showSub(t) {
    try {
      if (t.kind === "ass") await this.showAss(t);
      else await this.subs.load(t.url, t.label, t.language || "");
    } catch (e) {
      toast(e.message || "Subtitles didn't load", { error: true });
    }
  }

  clearSubs() {
    this.subs.clear();
    if (this.ass) { this.ass.destroy(); this.ass = null; }
  }

  // JASSUB draws styled subtitles on a canvas over the video, in a worker,
  // with the fonts embedded in the file.
  async showAss(t) {
    const { default: Jassub } = await import(JASSUB + "jassub.es.js");
    if (this.destroyed || this.sub !== t.index) return;
    if (this.ass) this.ass.destroy();
    this.ass = new Jassub({
      video: this.v, subUrl: t.url, workerUrl: JASSUB + "jassub-worker.js", wasmUrl: JASSUB + "jassub-worker.wasm",
      availableFonts: { "liberation sans": JASSUB + "default.woff2" }, fallbackFont: "liberation sans",
      fonts: this.info.fonts,
      // Never ask for the computer's own fonts: a permission prompt
      // in the middle of a film.
      useLocalFonts: false,
      // JASSUB's offset runs the other way: a positive delay shows lines later.
      timeOffset: -this.subs.delay,
    });
  }

  cycleSub() {
    const list = [null, ...this.info.subtitles.map((s) => s.index)];
    const i = list.indexOf(this.sub);
    const next = list[(i + 1) % list.length];
    this.chooseSub(next);
    const t = this.info.subtitles.find((s) => s.index === next);
    this.osd(t ? t.label : "Subtitles off");
  }

  setDelay(d) {
    d = Math.round(d * 10) / 10;
    this.subs.setDelay(d);
    if (this.ass) this.ass.timeOffset = -d;
    if (d) store(this.delayKey, d); else unstore(this.delayKey);
    if (this.delayEl) this.delayEl.textContent = signed(d);
  }

  delayBy(step) {
    this.setDelay(this.subs.delay + step);
    this.osd(`Subtitle delay ${signed(this.subs.delay)}`);
  }

  applySize() {
    this.el.style.setProperty("--sub-scale", String(this.size));
    if (this.sizeEl) this.sizeEl.textContent = `${Math.round(this.size * 100)}%`;
  }

  sizeBy(dir) {
    const i = SIZES.indexOf(this.size);
    this.size = SIZES[Math.max(0, Math.min(SIZES.length - 1, (i < 0 ? 2 : i) + dir))];
    store("subsize", this.size);
    this.applySize();
  }

  // ---------------------------------------------------------------- menus

  item(label, on, onclick, extra = null) {
    return h("button.pl-mi", { type: "button", role: "menuitemradio", "aria-checked": on ? "true" : "false", onclick },
      h("span", label), extra ? h("small", extra) : null);
  }

  menu(name, anchor) {
    if (!this.menuEl.hidden && this.menuName === name) { this.closeMenu(); return; }
    this.menuName = name;
    this.menuAnchor = anchor;
    this.paintMenu();
    this.menuEl.hidden = false;
    this.el.classList.add("menu-open");
    (this.menuEl.querySelector('[aria-checked="true"]') || this.menuEl.querySelector("button"))?.focus();
  }

  paintMenu() {
    const name = this.menuName;
    const redo = (fn) => async () => { await fn(); if (!this.menuEl.hidden) this.paintMenu(); };
    let body;
    if (name === "subs") {
      const subs = this.info.subtitles;
      body = [
        h("div.pl-mh", "Subtitles"),
        this.item("Off", this.sub == null, redo(() => this.chooseSub(null))),
        subs.map((s) => this.item(s.label.replace(/ - (Default|SUBRIP|SRT|ASS|SSA|WEBVTT|PGSSUB)\b/gi, ""), this.sub === s.index,
          redo(() => this.chooseSub(s.index)), s.kind === "image" ? "burnt in" : s.external ? "file" : null)),
        h("div.pl-mh", "Delay"),
        h("div.pl-step",
          h("button.pl-sb", { type: "button", "aria-label": "Subtitles earlier", onclick: () => this.setDelay(this.subs.delay - DELAY_STEP) }, "−"),
          this.delayEl = h("output.pl-val", signed(this.subs.delay)),
          h("button.pl-sb", { type: "button", "aria-label": "Subtitles later", onclick: () => this.setDelay(this.subs.delay + DELAY_STEP) }, "+"),
          h("button.pl-reset", { type: "button", onclick: () => this.setDelay(0) }, "Reset")),
        h("div.pl-mh", "Size"),
        h("div.pl-step",
          h("button.pl-sb", { type: "button", "aria-label": "Smaller subtitles", onclick: () => this.sizeBy(-1) }, "−"),
          this.sizeEl = h("output.pl-val", `${Math.round(this.size * 100)}%`),
          h("button.pl-sb", { type: "button", "aria-label": "Larger subtitles", onclick: () => this.sizeBy(1) }, "+")),
      ];
    } else if (name === "audio") {
      body = [h("div.pl-mh", "Audio"), this.info.audio.map((a) => this.item(a.label, this.audio === a.index,
        redo(() => (this.audio === a.index ? null : this.restart({ audio: a.index })))))];
    } else {
      const t = this.v.currentTime;
      const ch = this.info.chapters;
      const at = ch.reduce((cur, c, i) => (c.start_s <= t ? i : cur), -1);
      body = [
        ch.length ? [h("div.pl-mh", "Chapters"), ch.map((c, i) => this.item(c.name || `Chapter ${i + 1}`, i === at,
          () => { this.v.currentTime = c.start_s; this.closeMenu(); }, clock(c.start_s)))] : null,
        h("div.pl-mh", "Speed"),
        h("div.pl-chips", SPEEDS.map((s) => h("button.pl-chip" + (s === this.speed ? ".on" : ""),
          { type: "button", "aria-pressed": s === this.speed ? "true" : "false", onclick: redo(() => this.setSpeed(s)) }, `${s}×`))),
        h("div.pl-mh", "Picture"),
        h("div.pl-chips", PICTURES.map(([id, label]) => h("button.pl-chip" + (id === this.picture ? ".on" : ""),
          { type: "button", "aria-pressed": id === this.picture ? "true" : "false", onclick: redo(() => this.setPicture(id)) }, label))),
      ];
    }
    swap(this.menuEl, body);
  }

  closeMenu() {
    this.menuEl.hidden = true;
    this.el.classList.remove("menu-open");
    this.delayEl = this.sizeEl = null;
    if (this.menuAnchor && this.menuAnchor.isConnected) this.menuAnchor.focus({ preventScroll: true });
    this.wake();
  }

  // ---------------------------------------------------------------- keys

  key(e) {
    if (this.destroyed || !this.el.isConnected || e.metaKey || e.ctrlKey || e.altKey) return;
    if (document.querySelector(".sheet.open")) return;
    const t = e.target;
    const onButton = t instanceof HTMLButtonElement;
    const onRange = t instanceof HTMLInputElement && t.type === "range";
    if (!this.menuEl.hidden) {
      if (e.key === "Escape") { e.preventDefault(); this.closeMenu(); return; }
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        const items = [...this.menuEl.querySelectorAll("button")];
        const i = items.indexOf(document.activeElement);
        items[(i + (e.key === "ArrowDown" ? 1 : items.length - 1)) % items.length]?.focus();
        return;
      }
      if (this.menuEl.contains(t)) return; // Enter and Space press the menu's own buttons
    }
    // One map for every layout: letters by their lower case (Shift+F is
    // still f), the rest by their key name.
    const k = e.key.length === 1 ? e.key.toLowerCase() : e.key;
    const act = {
      " ": () => (onButton ? false : this.toggle()),
      k: () => this.toggle(),
      ArrowLeft: () => this.jump(-10),
      ArrowRight: () => this.jump(10),
      ArrowUp: () => this.volumeBy(0.05),
      ArrowDown: () => this.volumeBy(-0.05),
      f: () => this.fullscreen(),
      m: () => this.mute(),
      c: () => this.cycleSub(),
      "[": () => this.delayBy(-DELAY_STEP),
      "]": () => this.delayBy(DELAY_STEP),
      "<": () => this.speedBy(-1),
      ">": () => this.speedBy(1),
    }[k];
    if (!act) return;
    // The volume slider keeps its own arrows; the seek bar's are ±10 s.
    if (onRange && (t === this.vol || !e.key.startsWith("Arrow"))) return;
    if (act() === false) return;
    e.preventDefault();
    this.wake();
  }

  // ---------------------------------------------------------------- reports

  body(event, position) {
    return {
      ticket: this.info.ticket, event, position_s: Math.max(0, position ?? this.v.currentTime ?? 0),
      duration_s: this.duration() || 0, paused: this.v.paused, transcoded: this.info.mode === "hls",
      ...(this.audio != null ? { audio: this.audio } : {}), ...(this.sub != null ? { subtitle: this.sub } : {}),
    };
  }

  send(event) {
    if (!this.info || !this.startedTicket && event !== "start") return;
    report("/api/v1/watch/report", this.body(event)).catch(() => { /* the next report carries on */ });
  }

  // stop is the last report of a play: at the end (the watch is recorded
  // when it is past the end rule), or as the page goes away.
  stop(position) {
    if (!this.info || !this.startedTicket || this.stopped) return;
    this.stopped = true;
    const title = this.info.title, key = this.info.key, movie = this.media === "movie";
    report("/api/v1/watch/report", this.body("stop", position), { keepalive: true }).then((res) => {
      if (!res || !res.watched) return;
      const st = states.get(key) || {};
      setState(key, { watched: (st.watched || 0) + 1, ...(movie ? { watchlist: false } : {}) });
      toast(`Marked ${title} as watched`);
    }).catch(() => { /* nothing to undo */ });
  }

  destroy() {
    if (this.destroyed) return;
    this.stop();
    this.destroyed = true;
    clearInterval(this.timer);
    clearTimeout(this.idleTimer);
    clearTimeout(this.osdTimer);
    if (this.frame) cancelAnimationFrame(this.frame);
    this.frame = null;
    for (const off of this.listeners) off();
    this.listeners = [];
    if (this.v) {
      this.v.pause();
      this.clearSubs();
      this.teardownEngine();
    }
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    this.root.replaceChildren();
  }
}
