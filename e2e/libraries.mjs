// Movie Night's own media libraries on a fresh install with no Jellyfin
// (E2E_SUITE=libraries e2e/run.sh): an admin adds a library on the clips
// run.sh made, the scan finds them, the titles show on Home, and they play
// as the file itself, remuxed and transcoded, with another audio track,
// subtitles, a seek, resume and the watch recorded at the end. Chromium on
// a desktop and as an Android phone.
import { createRequire } from "module";

const require = createRequire(import.meta.url);
const { chromium, devices } = require("playwright");
const BASE = process.argv[2] || "http://127.0.0.1:8139";
const FOLDER = process.argv[3] || "/media/lib";
const SHOTS = process.env.E2E_SHOTS || "";

let failures = 0;
let current = null;

async function step(name, fn) {
  try {
    await fn();
    console.log(`  ok   ${name}`);
  } catch (e) {
    failures += 1;
    console.log(`  FAIL ${name}: ${e.message.split("\n")[0]}`);
    if (SHOTS && current) await current.screenshot({ path: `${SHOTS}/libraries-fail-${failures}.png` }).catch(() => {});
  }
}

function expect(cond, message) {
  if (!cond) throw new Error(message);
}

async function until(page, what, test, ms = 20000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    const v = await test();
    if (v) return v;
    await page.waitForTimeout(150);
  }
  throw new Error(`timed out: ${what}`);
}

const video = (page, js, arg) => page.locator("video.pl-video").evaluate(js, arg);
const at = (page) => video(page, (v) => v.currentTime);

async function pick(page, control, label) {
  if (await page.locator(".pl-menu").isVisible()) await page.keyboard.press("Escape");
  await page.locator(`.pl-btn[aria-label="${control}"]`).click();
  const esc = label.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  await page.locator(".pl-mi").filter({ has: page.locator("span", { hasText: new RegExp(`^${esc}$`) }) }).click();
}

// play opens a title, presses Play (a library-only title goes straight to
// the player) and returns the watch answer.
async function play(page, id) {
  await page.goto(`${BASE}/title/movie/${id}`, { waitUntil: "networkidle" });
  const answer = page.waitForResponse((r) => r.url().endsWith(`/title/movie/${id}/watch`) && r.request().method() === "POST");
  await page.locator(".d-primary .btn-play").click();
  const w = await (await answer).json();
  await page.waitForSelector("video.pl-video", { timeout: 10000 });
  const resume = page.locator(".pl-resume button", { hasText: "Start over" });
  if (await resume.isVisible().catch(() => false)) await resume.click();
  if (await video(page, (v) => v.paused)) await page.locator(".pl-big").click().catch(() => {});
  await until(page, "playing", async () => (await at(page)) > 1, 30000);
  return w;
}

async function admin(page, ctx, name) {
  await step("an admin adds two libraries", async () => {
    await page.goto(`${BASE}/profile`, { waitUntil: "networkidle" });
    expect(await page.locator("a.trow[href='/settings/jellyfin']").count() === 0, "Jellyfin is offered on an install without it");
    await page.locator("a.trow[href='/settings/libraries']").click();
    await page.waitForSelector("button:has-text('Add library')");
    const existing = await page.locator("section.lib").count();
    if (existing === 0) {
      if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-libraries-empty.png` });
      for (const [title, kind, dir] of [["Films", null, "Films"], ["Shows", "Shows", "Shows"]]) {
        await page.locator("button", { hasText: "Add library" }).click();
        await page.locator(".sheet input[aria-label='Name']").fill(title);
        if (kind) await page.locator(".sheet .chip", { hasText: kind }).click();
        await page.locator(".sheet textarea").fill(`${FOLDER}/${dir}`);
        if (SHOTS && kind) await page.screenshot({ path: `${SHOTS}/${name}-libraries-add.png` });
        await page.locator(".sheet button", { hasText: /^Add$/ }).click();
        await page.waitForSelector(`section.lib h2:has-text('${title}')`);
      }
    }
  });

  await step("the scan finds the files, and the one that isn't a video", async () => {
    await until(page, "scanned", async () => {
      const r = await (await ctx.request.get(`${BASE}/api/v1/libraries`)).json();
      return r.libraries.length === 2 && r.libraries.every((l) => !l.scan && l.scanned_at) && r.libraries;
    }, 60000);
    await page.reload({ waitUntil: "networkidle" });
    const films = page.locator("section.lib", { hasText: "Films" });
    await films.locator(".soft", { hasText: "3 films" }).waitFor({ timeout: 5000 });
    await films.locator(".lib-toggle").click();
    await films.locator(".lib-file", { hasText: "Broken.mkv" }).waitFor({ timeout: 5000 });
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-libraries.png`, fullPage: true });
  });

  await step("Scan now shows its progress and finishes", async () => {
    const films = page.locator("section.lib", { hasText: "Films" });
    await films.locator("button", { hasText: "Scan now" }).click();
    await until(page, "scan done", async () => {
      const r = await (await ctx.request.get(`${BASE}/api/v1/libraries`)).json();
      return r.libraries.every((l) => !l.scan);
    }, 30000);
  });
}

async function member(page, name) {
  const reqs = [];
  page.on("request", (r) => reqs.push(r.url()));
  const wake = () => page.locator(".pl").hover({ position: { x: 40, y: 40 } }).catch(() => {});

  await step("Home shows the library's titles", async () => {
    await page.goto(`${BASE}/`, { waitUntil: "networkidle" });
    await page.waitForSelector("section.row .card", { timeout: 15000 });
    const text = await page.locator("main, #view").first().innerText();
    expect(/Movie 605/.test(text) || /Movie 603/.test(text), "no library title on Home");
  });

  await step("an MP4 plays as the file itself", async () => {
    const w = await play(page, 603);
    expect(w.mode === "direct" && w.source === "library", `mode ${w.mode} source ${w.source}`);
    expect(reqs.some((u) => /\/api\/v1\/stream\/[^/]+\/stream\.mp4$/.test(u)), "the file was not asked for");
  });

  await step("Matroska with H.264 is remuxed to HLS", async () => {
    const w = await play(page, 604);
    expect(w.mode === "hls", `mode ${w.mode}`);
    expect(reqs.some((u) => /\/hls\/\d+\.m4s$/.test(u)), "no HLS segment");
  });

  let w605;
  await step("HEVC is transcoded, with chapters as Skip buttons", async () => {
    w605 = await play(page, 605);
    expect(w605.mode === "hls" && w605.segments.some((s) => s.kind === "intro"), `mode ${w605.mode} segments ${JSON.stringify(w605.segments)}`);
    await page.keyboard.press("Escape");
  });

  await step("subtitles show, embedded and the file beside it", async () => {
    await pick(page, "Subtitles", "English");
    await page.waitForSelector(".pl-cue:has-text('Hello there')", { timeout: 8000 });
    await pick(page, "Subtitles", "German");
    await page.waitForSelector(".pl-cue:has-text('Guten Tag')", { timeout: 8000 });
    await page.keyboard.press("Escape");
  });

  // Search online for a library file: its moviehash is read from the media
  // service, so the fake site's hash-matched subtitle comes first. Picking
  // it shows its lines; it is removed again so the next browser starts
  // from none.
  await step("Search online finds the library file's subtitle and shows it", async () => {
    await wake();
    await page.locator(`.pl-btn[aria-label="Subtitles"]`).click();
    await page.locator(".pl-find").click();
    await page.waitForSelector(".pl-res", { timeout: 15000 });
    expect(await page.locator(".pl-res").first().locator(".pl-tag.match").count() === 1, "the file's own subtitle is not first");
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-library-search-online.png` });
    await page.locator(".pl-res").first().click();
    await page.waitForSelector(".pl-menu", { state: "hidden" });
    await video(page, (v) => { v.currentTime = 2; });
    await page.waitForSelector(".pl-cue:has-text('Online hello')", { timeout: 10000 });
    await wake();
    await page.locator(`.pl-btn[aria-label="Subtitles"]`).click();
    await page.locator(`.pl-x[aria-label="Remove English · OpenSubtitles"]`).click();
    await until(page, "the track removed", async () => (await page.locator(".pl-mi", { hasText: "OpenSubtitles" }).count()) === 0, 5000);
    await page.keyboard.press("Escape");
    if (await video(page, (v) => v.paused)) await video(page, (v) => v.play());
  });

  await step("another audio track carries on from the same second", async () => {
    const from = await at(page);
    const answer = page.waitForResponse((r) => r.url().endsWith("/title/movie/605/watch"));
    await pick(page, "Audio", "German · Surround · 5.1");
    const w = await (await answer).json();
    expect(w.audio_index === 2 && w.play_id === w605.play_id, `audio ${w.audio_index}`);
    await until(page, "playing again", async () => !(await video(page, (v) => v.paused)) && (await at(page)) > from + 0.3, 30000);
    await wake();
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-library-player.png` });
  });

  await step("a seek far ahead plays from there", async () => {
    await video(page, (v) => { v.currentTime = 40; });
    await until(page, "at 40 s", async () => (await at(page)) >= 40 && !(await video(page, (v) => v.paused)), 30000);
  });

  await step("leaving keeps the place; the next play offers to resume", async () => {
    await video(page, (v) => v.pause());
    const stopped = page.waitForResponse((r) => r.url().endsWith("/api/v1/watch/report") && r.request().postData().includes('"stop"'));
    await wake();
    await page.locator(".pl-top .pl-btn[aria-label='Back']").click();
    await stopped;
    await page.waitForSelector(".d-primary .btn-play", { timeout: 10000 });
    await page.locator(".d-primary .btn-play").click();
    const resume = page.locator(".pl-resume button", { hasText: /Resume from 0:4/ });
    await resume.waitFor({ timeout: 10000 });
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-library-resume.png` });
    await resume.click();
    await until(page, "resumed", async () => (await at(page)) >= 39, 30000);
  });

  await step("playing into the credits records the watch", async () => {
    if (await video(page, (v) => v.paused)) await video(page, (v) => v.play());
    await video(page, (v) => { v.currentTime = 56; });
    await page.waitForSelector(".toast:has-text('Marked Movie 605 as watched')", { timeout: 30000 });
  });
}

async function browserRun(name, device) {
  console.log(`${name}:`);
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ ...device, extraHTTPHeaders: { "X-Dev-User": "owner" } });
  const page = await ctx.newPage();
  current = page;
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  // A failing stream request is printed: it is what explains a play that
  // doesn't start.
  page.on("response", (r) => {
    if (r.status() >= 400 && r.url().includes("/api/v1/")) console.log(`       ${r.status()} ${r.request().method()} ${r.url().replace(/stream\/[^/]+/, "stream/…")}`);
  });
  page.on("console", (m) => { if (m.type() === "error") console.log(`       console: ${m.text().slice(0, 200)}`); });
  await admin(page, ctx, name);
  await member(page, name);
  await step("no script errors", async () => { expect(errors.length === 0, errors.join(" | ")); });
  await browser.close();
}

await browserRun("desktop", { viewport: { width: 1280, height: 800 } });
await browserRun("phone", devices["Pixel 7"]);
console.log(failures ? `${failures} FAILED` : "all passed");
process.exit(failures ? 1 : 0);
