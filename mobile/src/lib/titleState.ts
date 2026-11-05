// One place that knows each title's state (library, request, watchlist,
// watched, rating, favourite), so a tap on the title page updates every card
// showing it. A field changed here in the last two minutes beats an older
// server reply that arrives late (the "tap twice to add" bug, fixed once).
import { useSyncExternalStore } from "react";

import type { TitleState } from "./types";

const FIELDS = ["in_library", "jellyfin_id", "jellyfin_url", "request", "progress", "watchlist", "watched", "rating", "favorite"] as const;
const LOCAL_WINS_MS = 120_000;

type State = Partial<TitleState>;
const states = new Map<string, State>();
const localEdits = new Map<string, Record<string, number>>();
const listeners = new Map<string, Set<() => void>>();
const EMPTY: State = Object.freeze({});

function emit(key: string) {
  listeners.get(key)?.forEach((fn) => fn());
}

/** Take a server copy (a card, a page) without undoing recent taps. */
export function remember(key: string, s: State | undefined | null) {
  if (!key || !s) return;
  const next: State = { ...(states.get(key) || {}) };
  const edits = localEdits.get(key);
  const now = Date.now();
  let changed = false;
  for (const f of FIELDS) {
    if (!(f in s)) continue;
    if (edits && edits[f] && now - edits[f] < LOCAL_WINS_MS) continue;
    if ((next as any)[f] !== (s as any)[f]) {
      (next as any)[f] = (s as any)[f];
      changed = true;
    }
  }
  states.set(key, next);
  if (changed) emit(key);
}

/** A change made here (a tap). */
export function setTitleState(key: string, patch: State): State {
  const edits = localEdits.get(key) || {};
  const now = Date.now();
  Object.keys(patch).forEach((f) => { edits[f] = now; });
  localEdits.set(key, edits);
  const next = { ...(states.get(key) || {}), ...patch };
  states.set(key, next);
  emit(key);
  return next;
}

export function getTitleState(key: string): State {
  return states.get(key) || {};
}

/** Signing out: nothing of that person's stays for the next one. */
export function clearTitleStates() {
  states.clear();
  localEdits.clear();
  listeners.forEach((_, key) => emit(key));
}

export function useTitleState(key: string, initial?: State | null): State {
  if (initial && !states.has(key)) states.set(key, { ...initial });
  return useSyncExternalStore(
    (fn) => {
      const set = listeners.get(key) || new Set();
      set.add(fn);
      listeners.set(key, set);
      return () => set.delete(fn);
    },
    () => states.get(key) || EMPTY,
  );
}
