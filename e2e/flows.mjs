// Browser flows on a phone (WebKit as iPhone, Chromium as Android) and a
// desktop against the Go server with fake upstreams
// (server/internal/server/e2e_serve_test.go). Run with e2e/run.sh.
import { createRequire } from "module";

const require = createRequire(import.meta.url);
const { webkit, chromium, devices } = require("playwright");
const BASE = process.argv[2] || "http://127.0.0.1:8139";

let failures = 0;

const SHOTS = process.env.E2E_SHOTS || "";
let current = null; // the page being tested, for failure screenshots

async function step(name, fn) {
  try {
    await fn();
    console.log(`  ok   ${name}`);
  } catch (e) {
    failures += 1;
    console.log(`  FAIL ${name}: ${e.message.split("\n")[0]}`);
    if (SHOTS && current) await current.screenshot({ path: `${SHOTS}/fail-${failures}.png` }).catch(() => {});
  }
}

function expect(cond, message) {
  if (!cond) throw new Error(message);
}

// zone: the time zone the browser runs in (each person's days follow it).
async function run(name, browserType, device, zone) {
  console.log(`${name}:`);
  const browser = await browserType.launch();
  const ctx = await browser.newContext({ ...device, timezoneId: zone, extraHTTPHeaders: { "X-Dev-User": `e2e-${name}` } });
  const page = await ctx.newPage();
  current = page;
  const errors = [];
  // WebKit cancels in-flight requests when a test hands off to vlc:// and
  // reports it as an error; that one is expected.
  page.on("pageerror", (e) => { if (!/access control checks/.test(e.message)) errors.push(e.message); });
  const go = async (path) => { await page.goto(BASE + path, { waitUntil: "networkidle" }); await page.waitForTimeout(400); };

  await step("Home draws its rows", async () => {
    await go("/");
    await page.waitForSelector("section.row .card", { timeout: 15000 });
    expect(await page.locator(".err-box").count() === 0, "an error box is showing");
  });

  await step("the browser's time zone is reported once", async () => {
    let stored = null;
    for (let i = 0; i < 25 && stored !== zone; i++) {
      stored = (await (await ctx.request.get(`${BASE}/api/v1/me`)).json()).time_zone;
      if (stored !== zone) await page.waitForTimeout(200);
    }
    expect(stored === zone, `the server has ${stored}, the browser runs in ${zone}`);
    // Stored now: the next page load asks /me and sends nothing.
    const puts = [];
    const watch = (r) => { if (r.url().endsWith("/api/v1/me/time-zone")) puts.push(r); };
    page.on("request", watch);
    await go("/");
    page.off("request", watch);
    expect(puts.length === 0, "sent again though the server has it");
  });

  // Profile's time-zone picker: a zone picked by hand is kept (this
  // browser's own zone is no longer reported), Automatic follows it again.
  await step("a time zone picked in Profile sticks; Automatic follows the browser again", async () => {
    const meNow = async () => (await (await ctx.request.get(`${BASE}/api/v1/me`)).json());
    const until = async (want) => {
      let m = null;
      for (let i = 0; i < 25; i++) {
        m = await meNow();
        if (m.time_zone === want.zone && m.time_zone_mode === want.mode) return;
        await page.waitForTimeout(200);
      }
      throw new Error(`the server has ${m.time_zone} (${m.time_zone_mode}), not ${want.zone} (${want.mode})`);
    };
    const row = page.locator("button.trow", { hasText: "Time zone" });
    await go("/profile");
    const shown = zone.replace(/_/g, " ");
    expect((await row.innerText()).includes(`Automatic (this device) · ${shown}`), `the row says ${await row.innerText()}`);
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-profile.png`, fullPage: true });
    await row.click();
    const search = page.locator(".sheet input[type='search']");
    await search.waitFor({ timeout: 5000 });
    await page.waitForSelector(".sheet .opt:has-text('Asia/Tokyo')", { timeout: 5000 });
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-timezone-picker.png` });
    await search.fill("tokyo");
    // Automatic stays on top; of the zones only Tokyo is left.
    const visible = await page.locator(".sheet .opt:visible").allInnerTexts();
    expect(visible.length === 2 && visible[1].includes("Asia/Tokyo") && visible[1].includes("UTC+09:00"), `shown: ${visible.join(" | ")}`);
    await search.fill("utc+5:30");
    expect(await page.locator(".sheet .opt:visible", { hasText: "Asia/Kolkata" }).count() === 1, "searching an offset doesn't find Kolkata");
    await search.fill("tokyo");
    await page.locator(".sheet .opt", { hasText: "Asia/Tokyo" }).click();
    await until({ zone: "Asia/Tokyo", mode: "manual" });
    await page.waitForSelector("button.trow:has-text('Asia/Tokyo · chosen by you')", { timeout: 5000 });
    await page.waitForTimeout(400); // the sheet has slid away
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-timezone-chosen.png` });
    // A new page load reports nothing: the choice is kept.
    const puts = [];
    const watch = (r) => { if (r.url().endsWith("/api/v1/me/time-zone")) puts.push(r); };
    page.on("request", watch);
    await go("/");
    page.off("request", watch);
    expect(puts.length === 0, "this browser's zone was reported over the choice");
    await until({ zone: "Asia/Tokyo", mode: "manual" });
    // Back to Automatic: this browser's zone at once.
    await go("/profile");
    await row.click();
    await page.locator(".sheet .opt", { hasText: "Automatic (this device)" }).click();
    await until({ zone, mode: "automatic" });
    await page.waitForSelector(`button.trow:has-text('Automatic (this device) · ${shown}')`, { timeout: 5000 });
  });

  await step("rate with a comment; it shows under Ratings on Movie Night", async () => {
    await go("/title/movie/603");
    await page.locator(".actions .act", { hasText: "Rate" }).click();
    // The right half of the 4th star is 8/10 (clicked once the sheet has settled).
    const star = page.locator(".sheet .stars.big button").nth(3);
    const box = await star.boundingBox();
    await star.click({ position: { x: box.width * 0.75, y: box.height / 2 } });
    await page.locator(".sheet textarea").fill("Better the second time.");
    await page.locator(".sheet .btn-request").click();
    await page.waitForSelector("#ratings .rcard.mine .rtext:has-text('Better the second time.')", { timeout: 5000 });
    expect((await page.locator(".actions .act", { hasText: "8/10" }).count()) === 1, "the Rate tile doesn't say 8/10");
  });

  await step("watchlist tile turns on", async () => {
    await page.locator(".actions .act", { hasText: "Watchlist" }).click();
    await page.waitForSelector(".actions .act.on:has-text('In watchlist')", { timeout: 5000 });
  });

  await step("mark as watched on a day picked from the calendar", async () => {
    await page.locator(".actions .act", { hasText: "Watched" }).click();
    await page.waitForSelector(".sheet .cal .cal-day.on", { timeout: 5000 });
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-calendar-open.png` });
    await page.locator(".sheet .cal button[aria-label='Previous month']").click();
    await page.locator(".sheet .cal .cal-day", { hasText: /^3$/ }).first().click();
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-calendar-picked.png` });
    const sheet = await page.locator(".sheet").boundingBox();
    const cal = await page.locator(".sheet .cal").boundingBox();
    expect(cal.x >= sheet.x - 1 && cal.x + cal.width <= sheet.x + sheet.width + 1, "the calendar is wider than the sheet");
    const sent = page.waitForRequest((r) => r.url().endsWith("/api/v1/title/movie/603/watched") && r.method() === "POST");
    await page.locator(".sheet .cal-save").click();
    const body = JSON.parse((await sent).postData() || "{}");
    expect(/^\d{4}-\d{2}-03T20:00:00[+-]\d{2}:\d{2}$/.test(body.watched_at || ""), `sent ${body.watched_at}`);
    await page.waitForSelector(".actions .act.ok", { timeout: 5000 });
  });

  await step("make a list from a title, open it, star it", async () => {
    await page.locator(".actions .act").nth(4).click(); // the List tile
    await page.locator(".sheet .opt", { hasText: "New list" }).click();
    await page.waitForTimeout(400);
    await page.locator(".sheet input").fill("E2E list");
    await page.locator(".sheet .btn-request").click();
    await page.waitForTimeout(800);
    await go("/lists?filter=mine");
    await page.locator(".lcard2", { hasText: "E2E list" }).click();
    await page.waitForSelector(".grid .card", { timeout: 5000 });
    await page.locator(".chips .chip", { hasText: "Star" }).click();
    await page.waitForSelector(".chips .chip.on:has-text('Starred')", { timeout: 5000 });
    await go("/lists?filter=starred");
    expect(await page.locator(".lcard2", { hasText: "E2E list" }).count() === 1, "the starred filter doesn't show it");
  });

  await step("tick an episode and the season count follows", async () => {
    await go("/title/tv/1399/season/1");
    // Waits for the server's answer: the count changes at the tap, and leaving
    // the page at once can cancel the save (Chromium does), which the steps
    // below would then miss.
    await Promise.all([
      page.waitForResponse((r) => r.url().endsWith("/api/v1/title/tv/1399/episodes") && r.request().method() === "POST" && r.ok(), { timeout: 5000 }),
      page.locator(".ep .tick").first().click(),
    ]);
    await page.waitForSelector(".season-count:has-text('1 of 10 watched')", { timeout: 5000 });
    await go("/title/tv/1399");
    await page.waitForSelector(".d-primary .btn", { timeout: 5000 });
  });

  await step("like an episode; its heart is still on after a reload", async () => {
    await go("/title/tv/1399/season/1");
    // Together, so a missing heart fails this step instead of leaving the
    // wait for its answer to end the whole run.
    const [sent] = await Promise.all([
      page.waitForResponse((r) => r.url().endsWith("/api/v1/title/tv/1399/episode/1/2/favorite") && r.request().method() === "POST", { timeout: 5000 }),
      page.locator(".ep .ep-like").nth(1).click({ timeout: 5000 }),
    ]);
    expect(sent.ok(), "the like wasn't saved");
    expect(await page.locator(".ep").nth(1).evaluate((el) => !el.classList.contains("open")), "the heart also opened the episode");
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-episode-liked.png` });
    await go("/title/tv/1399/season/1");
    await page.waitForSelector(".ep .ep-like.on", { timeout: 5000 });
    const on = await page.locator(".ep .ep-like").evaluateAll((els) => els.map((el) => el.classList.contains("on")));
    expect(on.filter(Boolean).length === 1 && on[1], `hearts on: ${on.map((x, i) => (x ? i + 1 : null)).filter(Boolean)}`);
    const wide = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(wide <= 0, `the season page is ${wide}px wider than the screen`);
  });

  // One episode's own rating (2026-10-12): the stars next to its heart, the
  // title's sheet without a comment; the show stays unrated.
  await step("rate an episode; its stars keep the rating after a reload, the show isn't rated", async () => {
    await go("/title/tv/1399/season/1");
    await page.locator(".ep .ep-rate").first().click({ timeout: 5000 });
    await page.waitForSelector(".sheet .stars.big button", { timeout: 5000 });
    await page.waitForTimeout(400); // the sheet settles
    expect(await page.locator(".sheet textarea").count() === 0, "an episode's sheet asks for a comment");
    expect(await page.locator(".ep").first().evaluate((el) => !el.classList.contains("open")), "the stars also opened the episode");
    const star = page.locator(".sheet .stars.big button").nth(3);
    const box = await star.boundingBox();
    await star.click({ position: { x: box.width * 0.75, y: box.height / 2 } });
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-episode-rate-sheet.png` });
    const [sent] = await Promise.all([
      page.waitForResponse((r) => r.url().endsWith("/api/v1/title/tv/1399/episode/1/1/rating") && r.request().method() === "PUT", { timeout: 5000 }),
      page.locator(".sheet .btn-request").click(),
    ]);
    expect(sent.ok(), "the rating wasn't saved");
    expect((await sent.json()).rating === 8, "not saved as 8/10");
    await page.waitForSelector(".ep#e1 .ep-rate.on:has-text('8')", { timeout: 5000 });
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-episode-rated.png` });
    await go("/title/tv/1399/season/1");
    await page.waitForSelector(".ep#e1 .ep-rate.on:has-text('8')", { timeout: 5000 });
    expect(await page.locator(".ep .ep-rate.on").count() === 1, "more than one episode shows a rating");
    const wide = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(wide <= 0, `the season page is ${wide}px wider than the screen`);
    await go("/title/tv/1399");
    await page.waitForSelector(".actions .act:has-text('Rate show')", { timeout: 5000 });
    expect(await page.locator(".actions .act", { hasText: "Favorite show" }).count() === 1, "the heart doesn't say Favorite show");
  });

  // Each place that shows an episode on its own opens that episode and shows
  // its own heart and rating: Up next on the show page, Recently watched, History.
  await step("Up next on the show page shows the episode's own heart and stars and opens it", async () => {
    // The show rated 3/10 as a whole: nowhere below may that be shown as an episode's.
    await page.evaluate(() => fetch("/api/v1/title/tv/1399/rating", { method: "POST", headers: { "Content-Type": "application/json", "X-Requested-With": "movienight" }, body: JSON.stringify({ rating: 3 }) }));
    await go("/title/tv/1399");
    await page.waitForSelector(".upnext:not([hidden]) .upnext-ep:has-text('S1·E2')", { timeout: 5000 });
    expect(await page.locator(".actions .act", { hasText: "Show 3/10" }).count() === 1, "the show's tile doesn't say Show 3/10");
    // Episode 2 was liked above and isn't rated: its own, not the show's 3.
    expect(await page.locator(".upnext .ep-like.on").count() === 1, "Up next's heart isn't the episode's");
    expect(await page.locator(".upnext .ep-rate.on").count() === 0, "Up next's stars show the show's rating");
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-show-up-next.png` });
    await page.locator(".upnext .upnext-ep").click();
    await page.waitForURL(/\/title\/tv\/1399\/season\/1\?episode=2$/, { timeout: 5000 });
    await page.waitForSelector(".ep#e2.open", { timeout: 5000 });
  });

  const opened = async (n) => {
    await page.waitForSelector(`.ep#e${n}.open`, { timeout: 5000 });
    await page.waitForTimeout(300);
    const r = await page.locator(`.ep#e${n}`).boundingBox();
    const vh = await page.evaluate(() => window.innerHeight);
    expect(r && r.y >= 0 && r.y < vh, `episode ${n} isn't on screen (y ${r && Math.round(r.y)}, screen ${vh})`);
    expect(await page.locator(".back-head a.show-link[href='/title/tv/1399']").count() === 1, "no way back to the show");
  };

  await step("Recently watched opens the episode, with its own rating in the corner", async () => {
    await go("/profile");
    const card = page.locator("section.row a.card[href='/title/tv/1399/season/1?episode=1']");
    await card.waitFor({ timeout: 5000 });
    expect((await card.locator(".seen.rated").innerText()).trim() === "8", `the corner says ${await card.locator(".seen").innerText()}, not the episode's 8`);
    expect(await page.locator("section.row a.card[href='/title/movie/603']").count() >= 1, "a film in Recently watched doesn't open its page");
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-recently-watched.png` });
    await card.click();
    await page.waitForURL(/\/title\/tv\/1399\/season\/1\?episode=1$/, { timeout: 5000 });
    await opened(1);
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-recently-watched-opened.png` });
  });

  await step("History opens the episode and shows its own rating", async () => {
    await go("/history");
    const row = page.locator(".trow a.tx[href='/title/tv/1399/season/1?episode=1']");
    await row.waitFor({ timeout: 5000 });
    expect((await row.locator(".h-rated").innerText()).includes("8/10"), "History doesn't show the episode's 8/10");
    await row.click();
    await page.waitForURL(/\/title\/tv\/1399\/season\/1\?episode=1$/, { timeout: 5000 });
    await opened(1);
    // Changed there to 6/10 (the 3rd star's right half), it shows in History after Back.
    await page.locator(".ep#e1 .ep-rate").click();
    await page.waitForSelector(".sheet .stars.big button", { timeout: 5000 });
    await page.waitForTimeout(400);
    const star = page.locator(".sheet .stars.big button").nth(2);
    const box = await star.boundingBox();
    await star.click({ position: { x: box.width * 0.75, y: box.height / 2 } });
    await Promise.all([
      page.waitForResponse((r) => r.url().endsWith("/api/v1/title/tv/1399/episode/1/1/rating") && r.request().method() === "PUT" && r.ok(), { timeout: 5000 }),
      page.locator(".sheet .btn-request").click(),
    ]);
    await page.goBack();
    await page.waitForSelector(".trow a.tx[href='/title/tv/1399/season/1?episode=1'] .h-rated:has-text('6/10')", { timeout: 5000 });
  });

  await step("connect Jellyfin with Quick Connect", async () => {
    await go("/settings/jellyfin");
    await page.locator("button", { hasText: "Get a code" }).click();
    await page.waitForSelector(".qc-code:has-text('123456')", { timeout: 5000 });
    await page.waitForSelector("h2:has-text('Connected')", { timeout: 10000 });
  });

  await step("Play offers the players and builds a VLC link with subtitles", async () => {
    await go("/title/movie/603");
    await page.locator(".d-primary .btn-play").click();
    await page.waitForSelector(".sheet .player", { timeout: 5000 });
    expect(await page.locator(".sheet .player").count() >= 2, "fewer than two players offered");
    await page.locator(".sheet .player", { hasText: "VLC" }).first().click();
    const resp = page.waitForResponse((r) => r.url().endsWith("/api/v1/title/movie/603/play") && r.request().method() === "POST");
    await page.locator(".sheet .btn-play").click().catch(() => {});
    const body = await (await resp).json();
    expect(body.url.startsWith("vlc-x-callback://x-callback-url/stream?url="), "not a VLC link");
    expect(body.url.includes("Stream.srt"), "no subtitle file in the link");
  });

  await playerFlow(page, name);

  await step("Back keeps a row's sideways scroll", async () => {
    await go("/");
    await page.waitForSelector("section.row .scroller .card", { timeout: 15000 });
    await page.waitForTimeout(1500); // let every row arrive
    // A row long enough to scroll sideways (Trending has 20 in the fakes).
    const index = await page.locator("section.row .scroller").evaluateAll((els) => els.findIndex((el) => el.children.length >= 10));
    expect(index >= 0, "no long row on Home");
    const sc = page.locator("section.row .scroller").nth(index);
    await sc.evaluate((el) => { el.scrollLeft = 300; });
    await page.waitForTimeout(400);
    const before = await sc.evaluate((el) => el.scrollLeft); // after the row snaps to a card
    const card = sc.locator(".card").nth(3);
    await card.evaluate((el) => el.click()); // a tap where it is, without scrolling the row first
    await page.waitForSelector(".d-head h1", { timeout: 10000 });
    await page.goBack();
    await page.waitForTimeout(800);
    const left = await page.locator("section.row .scroller").nth(index).evaluate((el) => el.scrollLeft);
    expect(before > 100 && Math.abs(left - before) < 4, `scrollLeft is ${left}, was ${before}`);
  });

  await step("connect Kodi with the code the TV shows", async () => {
    const post = (path, body) => fetch(BASE + path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }).then((r) => r.json());
    const tv = await post("/api/v1/scrobble/pair", { device: `Kodi (${name})` });  // what the add-on does
    await go("/settings/kodi");
    await page.locator("input[aria-label='Code shown on the TV']").fill(tv.code.toLowerCase());
    await page.locator("button", { hasText: "Connect" }).first().click();
    // Confirmed, the TV is shown as finishing until it collects its sign-in.
    await page.waitForSelector(`.sres.waiting b:has-text('Kodi (${name})')`, { timeout: 5000 });
    const got = await post("/api/v1/scrobble/pair/poll", { poll: tv.poll });
    expect(got.status === "ok" && got.token.startsWith("mn_kodi_"), "the TV didn't get its token");
    // Then the page lists it as signed in, by itself.
    await page.waitForSelector(`.sres:not(.waiting) b:has-text('Kodi (${name})')`, { timeout: 10000 });
  });

  await step("no script errors on any page", async () => { expect(errors.length === 0, errors.join(" | ")); });
  await browser.close();
}

// ---------------------------------------------------------------------------
// Play here: Movie Night's own player, against the fake Jellyfin's clip
// (60 s, two audio tracks, two text subtitles, an ASS one, an intro).
// ---------------------------------------------------------------------------

const video = (page, js, arg) => page.locator("video.pl-video").evaluate(js, arg);
async function until(page, what, test, ms = 15000) {
  const end = Date.now() + ms;
  let last;
  while (Date.now() < end) {
    last = await test();
    if (last) return last;
    await page.waitForTimeout(150);
  }
  throw new Error(`timed out: ${what}`);
}
const at = (page) => video(page, (v) => v.currentTime);
async function seekTo(page, t, play = false) {
  // To seek and play, start first and seek once it is under way: Linux
  // WebKit's GStreamer jumps to the end of a file when play() follows a
  // seek made while paused (a plain <video> does it too; Safari and
  // Chromium do not).
  if (play && await video(page, (v) => v.paused)) {
    const from = await at(page);
    await video(page, (v) => v.play());
    await until(page, "under way", async () => (await at(page)) > from + 0.4);
  }
  await video(page, (v, t) => { v.currentTime = t; }, t);
  await until(page, `at ${t} s`, async () => Math.abs((await at(page)) - t) < 0.6);
}

// pick opens a control's menu and picks an entry by its exact label.
async function pick(page, control, label) {
  if (await page.locator(".pl-menu").isVisible()) await page.keyboard.press("Escape");
  await page.locator(`.pl-btn[aria-label="${control}"]`).click();
  await page.locator(".pl-mi").filter({ has: page.locator("span", { hasText: new RegExp(`^${label}$`) }) }).click();
}

async function playerFlow(page, name) {
  const reqs = [];
  page.on("request", (r) => reqs.push(r.url()));
  const wake = () => page.locator(".pl").hover({ position: { x: 40, y: 40 } }).catch(() => {});

  await step("Play here opens the player and plays the file as it is", async () => {
    await page.goto(`${BASE}/title/movie/603`, { waitUntil: "networkidle" });
    await page.locator(".d-primary .btn-play").click();
    await page.locator(".sheet button", { hasText: "Play here" }).click();
    await page.waitForURL(/\/watch\/movie\/603$/);
    await page.waitForSelector("video.pl-video", { timeout: 10000 });
    await until(page, "playing", async () => (await at(page)) > 1);
    expect(reqs.some((u) => /\/api\/v1\/stream\/[^/]+\/stream\.mp4$/.test(u)), "not the file itself");
    expect(!reqs.some((u) => /api_key|ApiKey/i.test(u)), "a key in an address");
    expect(await page.locator("#tabbar").isHidden(), "the tab bar shows over the player");
  });

  await step("Skip intro jumps past the intro", async () => {
    await seekTo(page, 2, true);
    await wake();
    await page.locator(".pl-skip", { hasText: "Skip intro" }).click({ timeout: 5000 });
    await until(page, "past the intro", async () => (await at(page)) >= 9.5);
  });

  await step("subtitles show, and the delay moves them", async () => {
    await pick(page, "Subtitles", "English");
    await page.keyboard.press("Escape");
    await video(page, (v) => v.pause());
    await seekTo(page, 3.5);
    await page.waitForTimeout(300);
    expect(await page.locator(".pl-cue").count() === 0, "a line shows between lines");
    await page.keyboard.press("]");
    await page.keyboard.press("]"); // +1.0 s: "Hello there" (1-3 s) now shows 2-4 s
    await page.waitForSelector(".pl-cue:has-text('Hello there')", { timeout: 3000 });
    const kept = await page.evaluate(() => localStorage.getItem("mn:subdelay:movie:603"));
    expect(kept === "1", `delay kept as ${kept}`);
    await seekTo(page, 4.1); // 1 s later the next line ("A long line", 4-30 s) is not up yet
    await page.waitForTimeout(300);
    expect(await page.locator(".pl-cue:has-text('long')").count() === 0, "the delay did not move the next line");
    await seekTo(page, 5.2);
    await page.waitForSelector(".pl-cue i:has-text('long')", { timeout: 3000 });
    await page.keyboard.press("[");
    await page.keyboard.press("[");
    expect(await page.evaluate(() => localStorage.getItem("mn:subdelay:movie:603")) === null, "the delay did not reset");
    await pick(page, "Subtitles", "Français");
    await page.waitForSelector(".pl-cue:has-text('Bonjour')", { timeout: 5000 });
    await page.waitForTimeout(300);
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-player-subtitles.png` });
    await page.keyboard.press("Escape");
  });

  // Search online against the fake subtitle sites (fakes/subtitles.go):
  // the result made for this file comes first, picking it shows its lines
  // at once, its delay is kept on the server, and it is a track of the
  // file after a reload. It is removed at the end, so each browser starts
  // from none.
  await step("Search online finds the file's subtitle, shows it, and keeps it with its delay", async () => {
    await wake();
    await page.locator(`.pl-btn[aria-label="Subtitles"]`).click();
    await page.locator(".pl-find").click();
    await page.waitForSelector(".pl-res", { timeout: 10000 });
    expect(await page.locator(".pl-select").inputValue() === "en", "the language is not English");
    expect(await page.locator(".pl-res").first().locator(".pl-tag.match").count() === 1, "the file's own subtitle is not first");
    expect(await page.locator(".pl-more", { hasText: "Machine translated (1)" }).count() === 1, "machine translations are not hidden");
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-player-search-online.png` });
    await video(page, (v) => v.pause());
    await page.locator(".pl-res").first().click();
    await page.waitForSelector(".pl-menu", { state: "hidden" });
    await seekTo(page, 2);
    await page.waitForSelector(".pl-cue:has-text('Online hello')", { timeout: 5000 });
    await seekTo(page, 3.5);
    await page.waitForTimeout(300);
    expect(await page.locator(".pl-cue").count() === 0, "a line shows after the first one ends");
    const saved = page.waitForRequest((r) => /\/api\/v1\/subtitles\/\d+\/delay$/.test(r.url()) && r.method() === "PUT");
    await page.keyboard.press("]");
    await page.keyboard.press("]"); // +1.0 s: "Online hello" (1-3 s) now shows 2-4 s
    await page.waitForSelector(".pl-cue:has-text('Online hello')", { timeout: 3000 });
    expect(JSON.parse((await saved).postData()).delay_s === 1, "the delay was not saved on the server");
    expect(await page.evaluate(() => localStorage.getItem("mn:subdelay:movie:603")) === null, "an online track's delay went to this browser");
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-player-online-cue.png` });

    await page.reload({ waitUntil: "networkidle" });
    await page.waitForSelector("video.pl-video");
    await video(page, (v) => v.pause());
    await pick(page, "Subtitles", "English · OpenSubtitles");
    await page.keyboard.press("Escape");
    await seekTo(page, 3.5);
    await page.waitForSelector(".pl-cue:has-text('Online hello')", { timeout: 5000 }); // the delay came back
    await wake();
    await page.locator(`.pl-btn[aria-label="Subtitles"]`).click();
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-player-online-track.png` });
    await page.locator(`.pl-x[aria-label="Remove English · OpenSubtitles"]`).click();
    await until(page, "the track removed", async () => (await page.locator(".pl-mi", { hasText: "OpenSubtitles" }).count()) === 0, 5000);
    expect(await page.locator(".pl-cue").count() === 0, "the removed track still shows");
    await page.keyboard.press("Escape");
  });

  // A fresh play after the paused seeks above: Linux WebKit's GStreamer
  // does not start again after them (see seekTo).
  await step("the player starts again on a reload", async () => {
    await page.reload({ waitUntil: "networkidle" });
    await page.waitForSelector("video.pl-video");
    if (await video(page, (v) => v.paused)) await page.locator(".pl-big").click();
    await until(page, "playing", async () => (await at(page)) > 1);
  });

  await step("speed goes up and down with < and >", async () => {
    await page.keyboard.press(">");
    expect(await video(page, (v) => v.playbackRate) === 1.25, "not 1.25×");
    await page.keyboard.press("<");
    await page.keyboard.press("<");
    expect(await video(page, (v) => v.playbackRate) === 0.75, "not 0.75×");
    await page.keyboard.press(">");
  });

  await step("another audio track carries on from the same second on Jellyfin's HLS", async () => {
    const from = await at(page);
    await pick(page, "Audio", "Deutsch");
    await until(page, "the HLS playlist with track 2", async () => reqs.some((u) => /\/master\.m3u8\?.*AudioStreamIndex=2/.test(u)));
    await until(page, "playing again", async () => !(await video(page, (v) => v.paused)) && (await at(page)) > from + 0.5, 20000);
    const t = await at(page);
    expect(t >= from - 1 && t < from + 12, `carried on at ${t}, was at ${from}`);
    expect(!reqs.some((u) => /api_key|ApiKey/i.test(u)), "a key in an address");
    await wake();
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-player-playing.png` });
  });

  await step("styled subtitles draw with JASSUB", async () => {
    await pick(page, "Subtitles", "English \\(styled\\)");
    await page.waitForSelector(".JASSUB canvas", { timeout: 15000, state: "attached" });
    await pick(page, "Subtitles", "Off");
    await until(page, "JASSUB gone", async () => (await page.locator(".JASSUB").count()) === 0, 5000);
  });

  await step("the settings menu lists chapters, speed and picture", async () => {
    await wake();
    await page.locator(`.pl-btn[aria-label="Settings"]`).click();
    await page.waitForSelector(".pl-menu .pl-mi:has-text('Middle')");
    await page.locator(".pl-chip", { hasText: "Fill" }).click();
    expect(await page.locator(".pl").getAttribute("data-picture") === "fill", "the picture did not change");
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-player-settings.png` });
    await page.locator(".pl-chip", { hasText: "Fit" }).click();
    await page.locator(".pl-mi", { hasText: "Ending" }).click();
    await until(page, "at the chapter", async () => Math.abs((await at(page)) - 40) < 2);
  });

  await step("leaving keeps the place, and the next play offers to resume", async () => {
    await video(page, (v) => v.pause());
    await seekTo(page, 41);
    const stopped = page.waitForResponse((r) => r.url().endsWith("/api/v1/watch/report") && r.request().postData().includes('"stop"'));
    // Failing a step, not the whole run, when the stop never comes.
    stopped.catch(() => {});
    await wake();
    await page.locator(`.pl-top .pl-btn[aria-label="Back"]`).click();
    await stopped;
    await page.waitForSelector(".d-primary .btn-play", { timeout: 10000 });
    await page.locator(".d-primary .btn-play").click();
    await page.locator(".sheet button", { hasText: "Play here" }).click();
    const resume = page.locator(".pl-resume button", { hasText: /Resume from 0:4[01]/ });
    await resume.waitFor({ timeout: 10000 });
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-player-resume.png` });
    await resume.click();
    await until(page, "resumed", async () => (await at(page)) >= 40);
  });

  await step("the keyboard works the player", async () => {
    await page.locator(".pl").focus();
    await page.keyboard.press("m");
    expect(await video(page, (v) => v.muted), "m did not mute");
    await page.keyboard.press("m");
    const t = await at(page);
    await page.keyboard.press("ArrowLeft");
    await until(page, "10 s back", async () => (await at(page)) < t - 8);
    await page.keyboard.press("Space");
    await until(page, "paused", async () => video(page, (v) => v.paused));
    await page.keyboard.press("Space");
    await until(page, "playing", async () => !(await video(page, (v) => v.paused)));
  });

  await step("playing to the end records the watch", async () => {
    await seekTo(page, 57, true);
    await page.waitForSelector(".toast:has-text('Marked Movie 603 as watched')", { timeout: 15000 });
  });
}

async function wide() {
  console.log("desktop:");
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1600, height: 900 }, extraHTTPHeaders: { "X-Dev-User": "e2e-desktop" } });
  const page = await ctx.newPage();
  await step("See all keeps loading on a wide screen", async () => {
    await page.goto(`${BASE}/browse?key=popular&tab=movies&title=Popular`, { waitUntil: "networkidle" });
    await page.waitForTimeout(2500);
    const n = await page.locator(".grid .card").count();
    expect(n >= 40, `only ${n} titles loaded`);
    expect(await page.locator(".loadmore .spin").count() === 0, "the spinner is stuck");
  });
  // Not connected to Jellyfin: the player streams with the install's key.
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.setViewportSize({ width: 1280, height: 720 });
  await playerFlow(page, "desktop");
  await step("no script errors in the player", async () => { expect(errors.length === 0, errors.join(" | ")); });

  await step("the time-zone picker on a wide screen", async () => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(`${BASE}/profile`, { waitUntil: "networkidle" });
    await page.locator("button.trow", { hasText: "Time zone" }).click();
    await page.waitForSelector(".sheet .opt:has-text('Europe/London')", { timeout: 5000 });
    await page.waitForTimeout(400); // the sheet has slid up
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/desktop-timezone-picker.png` });
    await page.goto(`${BASE}/profile`, { waitUntil: "networkidle" });
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/desktop-profile.png`, fullPage: true });
  });
  await browser.close();
}

await run("webkit", webkit, devices["iPhone 13"], "Pacific/Auckland");
await run("chromium", chromium, devices["Pixel 7"], "Europe/Berlin");
await wide();
console.log(failures ? `${failures} FAILED` : "all passed");
process.exit(failures ? 1 : 0);
