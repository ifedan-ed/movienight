// Movie Night service worker: makes the app installable and lets it open
// without a connection. Always asks the network first, so a deploy shows up
// on the next load; the copy kept here is only for when you're offline.
const CACHE = "movienight-v2";

self.addEventListener("install", () => self.skipWaiting());

self.addEventListener("activate", (event) => {
  event.waitUntil((async () => {
    for (const key of await caches.keys()) if (key !== CACHE) await caches.delete(key);
    await self.clients.claim();
  })());
});

function keep(request, response) {
  // Only plain successful answers from here (never a sign-in redirect).
  if (response.ok && response.type === "basic") {
    const copy = response.clone();
    caches.open(CACHE).then((c) => c.put(request, copy)).catch(() => {});
  }
  return response;
}

self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return; // posters: the browser's own cache
  if (url.pathname.startsWith("/api/")) return;     // always live
  if (req.mode === "navigate") {
    event.respondWith(fetch(req).then((r) => keep(new Request("/"), r)).catch(async () => (await caches.match("/")) || Response.error()));
    return;
  }
  if (url.pathname.startsWith("/static/")) {
    event.respondWith(fetch(req).then((r) => keep(req, r)).catch(async () => (await caches.match(req)) || Response.error()));
  }
});
