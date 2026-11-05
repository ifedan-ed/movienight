// JSON API helpers for /api/v1. GETs are cached briefly so Back is instant;
// any write clears the cache because it may change what pages show.
// The website's timers and caches are listed in docs/limits.md (Website).

const cache = new Map(); // url -> {at, promise}
const TTL = 60_000; // ms: a GET answer is reused for 60 s in this tab (lost on reload)

export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

async function call(method, url, body) {
  let res;
  const headers = {};
  if (body) headers["Content-Type"] = "application/json";
  // Every write carries this header; the server refuses writes without it
  // (a page on another site can't add it without asking first).
  if (method !== "GET") headers["X-Requested-With"] = "movienight";
  try {
    res = await fetch(url, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
      credentials: "same-origin",
    });
  } catch {
    throw new ApiError(0, "No connection — check your network");
  }
  if (res.status === 401 || res.redirected && !res.url.includes(location.host)) {
    location.reload(); // the session ran out: the app sends us to the provider to sign in
    throw new ApiError(401, "Signing in again…");
  }
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  if (!res.ok) throw new ApiError(res.status, (data && data.detail) || `Something went wrong (${res.status})`);
  return data;
}

export function get(url, { fresh = false, ttl = TTL } = {}) {
  const hit = cache.get(url);
  if (!fresh && hit && Date.now() - hit.at < ttl) return hit.promise;
  const promise = call("GET", url).catch((e) => { cache.delete(url); throw e; });
  cache.set(url, { at: Date.now(), promise });
  return promise;
}

export function post(url, body = {}) { cache.clear(); return call("POST", url, body); }
// A POST that only reads (poster back-fill): keeps the page cache.
export function postRead(url, body = {}) { return call("POST", url, body); }
export function put(url, body = {}) { cache.clear(); return call("PUT", url, body); }
export function patch(url, body = {}) { cache.clear(); return call("PATCH", url, body); }
export function del(url) { cache.clear(); return call("DELETE", url); }
// The install's request shapes (/api/v1/me `limits`: BATCH_MAX_ITEMS, ...),
// read once per page load, so calls are split to fit them.
let limitsAsked = null;
export function limits() {
  limitsAsked ||= call("GET", "/api/v1/me").then((me) => me.limits || {}).catch(() => { limitsAsked = null; return {}; });
  return limitsAsked;
}
export function forget(prefix = "") { for (const k of [...cache.keys()]) if (k.startsWith(prefix)) cache.delete(k); }
