// Changes made on this phone, kept until the server has them: watchlist,
// favorites, ratings, lists, watched, episode ticks, hearts and ratings, "Finished it?".
// Each is sent at once when online; offline (or on a dropped connection)
// it waits here, on the phone, and the queue is sent in order as soon as
// the connection is back — before anything is refreshed, so what the
// server says afterwards already includes it.
import AsyncStorage from "@react-native-async-storage/async-storage";

import { api, ApiError } from "./api";
import { holdBusy } from "./busy";
import { isOnline, onOnlineChange } from "./net";

type Item = { id: string; method: "POST" | "PUT" | "DELETE"; path: string; body?: unknown; key?: string; at: number };

const KEY = "mn:outbox";
let items: Item[] = [];
let loaded = false;
let flushing: Promise<void> | null = null;
let retryTimer: ReturnType<typeof setTimeout> | null = null;
let backoff = 2000;
const listeners = new Set<() => void>();
const afterSync = new Set<() => void>();
// Told when the server refuses a change for good, with its words, so the
// person hears of it (SyncBar shows it) instead of it vanishing.
const refusedListeners = new Set<(message: string) => void>();

async function load() {
  if (loaded) return;
  try { items = JSON.parse((await AsyncStorage.getItem(KEY)) || "[]"); } catch { items = []; }
  loaded = true;
}

async function persist() {
  try { await AsyncStorage.setItem(KEY, JSON.stringify(items)); } catch { /* storage full: still in memory */ }
  listeners.forEach((l) => l());
}

let seq = 0;
const newId = () => `${Date.now().toString(36)}-${(seq++).toString(36)}`;

/** Send a change now, or keep it for later. `key` replaces an older change
 * with the same key that hasn't gone yet (where you are in a session). */
export async function send(method: Item["method"], path: string, body?: unknown, opts: { key?: string } = {}) {
  await load();
  if (opts.key) items = items.filter((i) => i.key !== opts.key);
  items.push({ id: newId(), method, path, body, key: opts.key, at: Date.now() });
  await persist();
  return flush();
}

/** Send what's waiting, in order. Stops at the first one that can't go yet
 * (no connection, server trouble) and leaves it and the rest for later; a
 * change the server refuses for good (a list that was deleted) is dropped,
 * and the person is told why (onRefused). */
export function flush(): Promise<void> {
  if (flushing) return flushing;
  // A send in flight is in progress: a restart between the server taking it
  // and its leaving the queue would send it twice.
  const release = holdBusy();
  flushing = (async () => {
    await load();
    let sent = 0;
    let stuck = false;
    while (items.length && isOnline()) {
      const it = items[0];
      try {
        if (it.method === "POST") await api.post(it.path, it.body ?? {});
        else if (it.method === "PUT") await api.put(it.path, it.body ?? {});
        else await api.del(it.path);
      } catch (e) {
        const status = e instanceof ApiError ? e.status : 0;
        if (status === 0 || status === 401 || status === 408 || status === 429 || status >= 500) { stuck = true; break; }
        const why = e instanceof Error && e.message ? e.message : `the server refused it (${status})`;
        refusedListeners.forEach((fn) => fn(`A change couldn't be saved: ${why}`));
      }
      items.shift();
      sent += 1;
      await persist();
    }
    if (sent && !items.length) afterSync.forEach((fn) => fn());
    // Online but the send failed (a connection that has just come back, a
    // busy server): try again shortly, waiting longer each time, up to a minute.
    if (retryTimer) { clearTimeout(retryTimer); retryTimer = null; }
    if (stuck && isOnline()) {
      retryTimer = setTimeout(() => { retryTimer = null; flush(); }, backoff);
      backoff = Math.min(backoff * 2, 60_000);
    } else {
      backoff = 2000;
    }
  })().finally(() => { flushing = null; release(); });
  return flushing;
}

export function pending(): number {
  return items.length;
}

/** Signing out: what's waiting was that person's, so it never goes.
 * Returns how many changes were thrown away, so the person can be told. */
export async function clear(): Promise<number> {
  await load();
  const lost = items.length;
  items = [];
  if (retryTimer) { clearTimeout(retryTimer); retryTimer = null; }
  backoff = 2000;
  await persist();
  return lost;
}

/** How many changes are waiting (loaded from the phone first). */
export async function waiting(): Promise<number> {
  await load();
  return items.length;
}

export function onChange(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

/** Called with a plain message when the server refuses a change for good. */
export function onRefused(fn: (message: string) => void): () => void {
  refusedListeners.add(fn);
  return () => { refusedListeners.delete(fn); };
}

/** Called once everything waiting has reached the server. */
export function onSynced(fn: () => void): () => void {
  afterSync.add(fn);
  return () => afterSync.delete(fn);
}

// Back online: send what's waiting first.
onOnlineChange(() => { if (isOnline()) flush(); });
load().then(() => { listeners.forEach((l) => l()); flush(); });
