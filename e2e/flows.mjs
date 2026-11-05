// Browser flows on a phone (WebKit as iPhone, Chromium as Android) against
// the e2e server (tests/e2e_server.py, fake upstreams). Run with e2e/run.sh.
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

async function run(name, browserType, device) {
  console.log(`${name}:`);
  const browser = await browserType.launch();
  const ctx = await browser.newContext({ ...device, extraHTTPHeaders: { "X-Authentik-Username": `e2e-${name}` } });
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
    await page.waitForSelector(`.sres b:has-text('Kodi (${name})')`, { timeout: 5000 });
    const got = await post("/api/v1/scrobble/pair/poll", { poll: tv.poll });
    expect(got.status === "ok" && got.token.startsWith("mn_kodi_"), "the TV didn't get its token");
  });

  await step("no script errors on any page", async () => { expect(errors.length === 0, errors.join(" | ")); });
  await browser.close();
}

async function wide() {
  console.log("desktop:");
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1600, height: 900 }, extraHTTPHeaders: { "X-Authentik-Username": "e2e-desktop" } });
  const page = await ctx.newPage();
  await step("See all keeps loading on a wide screen", async () => {
    await page.goto(`${BASE}/browse?key=popular&tab=movies&title=Popular`, { waitUntil: "networkidle" });
    await page.waitForTimeout(2500);
    const n = await page.locator(".grid .card").count();
    expect(n >= 40, `only ${n} titles loaded`);
    expect(await page.locator(".loadmore .spin").count() === 0, "the spinner is stuck");
  });
  await browser.close();
}

await run("webkit", webkit, devices["iPhone 13"]);
await run("chromium", chromium, devices["Pixel 7"]);
await wide();
console.log(failures ? `${failures} FAILED` : "all passed");
process.exit(failures ? 1 : 0);
