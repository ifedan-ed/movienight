// A fix sent over the air goes in at the next start instead of the one after:
// while the splash is up the app asks whether there is one, and if so fetches
// it and restarts on it at once. A slow answer never holds the start for
// long; an update found late is put in at the following start, as before.
import { AppState } from "react-native";
import * as Updates from "expo-updates";

const ASK_MS = 1500; // the splash waits this long for "is there an update?"
const FETCH_MS = 10_000; // and this long for the download, then starts on what is installed

function within<T>(work: Promise<T>, ms: number): Promise<T | null> {
  return Promise.race([work, new Promise<null>((done) => setTimeout(() => done(null), ms))]);
}

export async function startOnLatestUpdate(): Promise<void> {
  if (__DEV__ || !Updates.isEnabled) return;
  try {
    const found = await within(Updates.checkForUpdateAsync(), ASK_MS);
    if (!found?.isAvailable) return;
    const fetched = await within(Updates.fetchUpdateAsync(), FETCH_MS);
    if (fetched?.isNew) await Updates.reloadAsync();
  } catch {
    // Start on what is installed.
  }
}

// Back after a while away. An iPhone resumes an app rather than starting it,
// so a start is rarer than it sounds; an update waiting when the app comes
// back is put in at once, but only on a screen where a restart loses nothing
// (a tab's own page, with nothing in progress: an open sheet, "Finished it?",
// a change still sending; see lib/busy). Away means 5 minutes or more.
// These timings are listed in docs/limits.md (Phone app).
const AWAY_MS = 5 * 60_000;

export function updateOnReturn(safeHere: () => boolean): () => void {
  let hiddenAt = 0;
  const sub = AppState.addEventListener("change", async (state) => {
    if (state !== "active") {
      hiddenAt ||= Date.now();
      return;
    }
    const away = hiddenAt ? Date.now() - hiddenAt : 0;
    hiddenAt = 0;
    if (away < AWAY_MS || __DEV__ || !Updates.isEnabled) return;
    try {
      const found = await Updates.checkForUpdateAsync();
      if (!found.isAvailable) return;
      await Updates.fetchUpdateAsync();
      if (safeHere()) await Updates.reloadAsync();
    } catch {
      // The next start puts it in.
    }
  });
  return () => sub.remove();
}
