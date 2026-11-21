// Crash reports, sent to the Movie Night server the phone talks to (POST
// /api/v1/errors/app), which checks and limits them and forwards them with
// its own collector's DSN (ERROR_REPORTS_APP_DSN). The app never holds a
// collector key: its code is public, and so would the key be.
//
// Off until that server says it takes them (GET /api/v1/auth/methods,
// error_reports.app). Only errors go: no sessions, tracing, replays,
// screenshots or the person. Native crashes are not caught: the native SDK
// would send them straight to the DSN, which here is a placeholder, so it
// is never started (enableNative: false) and only JavaScript errors, which
// are most of an Expo app's crashes, are reported.
import AsyncStorage from "@react-native-async-storage/async-storage";
import * as Sentry from "@sentry/react-native";
import Constants from "expo-constants";
import * as Updates from "expo-updates";
import { Platform } from "react-native";

// The server whose reports were on last time, so the next start reports
// from its first line instead of after a round trip.
const REMEMBERED = "mn:error-reports";

let current: string | null = null; // the server reports go to now, or none

/** The address without its query or fragment (search words, codes). */
export function bare(url: string): string {
  const cut = url.search(/[?#]/);
  return cut < 0 ? url : url.slice(0, cut);
}

/** What goes with an error: nothing about the person, no request detail. */
export function scrubEvent<E extends Sentry.ErrorEvent>(event: E): E {
  delete event.user;
  delete event.server_name;
  if (event.contexts?.device) delete event.contexts.device.name;
  if (event.request) event.request = { url: event.request.url ? bare(event.request.url) : undefined };
  return event;
}

/** A breadcrumb keeps its facts (where, method, status), never what was
 * logged to the console, a header, a body or a query string. */
export function scrubBreadcrumb(crumb: Sentry.Breadcrumb): Sentry.Breadcrumb | null {
  if (crumb.category === "console") return null;
  if (crumb.message) crumb.message = bare(crumb.message);
  if (crumb.data) {
    const kept: Record<string, unknown> = {};
    for (const k of ["method", "status_code", "url", "from", "to"]) {
      const v = crumb.data[k];
      if (v !== undefined) kept[k] = typeof v === "string" ? bare(v) : v;
    }
    crumb.data = kept;
  }
  return crumb;
}

function start(server: string) {
  // RN's URL lacks .host; the address is ours (normalizeServer), so a split does.
  const [, scheme, host] = /^(https?):\/\/([^/]+)/i.exec(server) || [];
  if (!host) return;
  Sentry.init({
    // A placeholder: the SDK needs a DSN to start, and the server swaps in
    // the real one.
    dsn: `${scheme}://public@${host}/0`,
    tunnel: `${server}/api/v1/errors/app`,
    // Every /api write must carry it (the server's CSRF guard).
    transportOptions: { headers: { "X-Requested-With": "movienight" } },
    enableNative: false,
    autoInitializeNativeSdk: false,
    enableAutoSessionTracking: false,
    sendClientReports: false,
    sendDefaultPii: false,
    enableCaptureFailedRequests: false,
    attachScreenshot: false,
    attachViewHierarchy: false,
    sampleRate: 1.0,
    release: `movienight-app@${Constants.expoConfig?.version ?? "unknown"}`,
    initialScope: {
      tags: { os: Platform.OS, os_version: String(Platform.Version), update: Updates.updateId ?? "embedded" },
    },
    beforeSend: scrubEvent,
    beforeBreadcrumb: scrubBreadcrumb,
  });
  current = server;
}

function stop() {
  if (current) Sentry.close();
  current = null;
}

/** Reports for server on or off, as its /auth/methods said. Idempotent: the
 * same answer again changes nothing. */
export function setErrorReports(server: string, on: boolean) {
  // Development builds show errors on screen; the web build is the website's.
  if (__DEV__ || Platform.OS === "web") return;
  const target = on ? server : null;
  if (target === current) return;
  stop();
  if (target) start(target);
  (target ? AsyncStorage.setItem(REMEMBERED, target) : AsyncStorage.removeItem(REMEMBERED)).catch(() => {});
}

/** At start: reports on at once if they were on for this same server last
 * time (its answer then refreshes it). */
export async function resumeErrorReports(server: string) {
  try {
    if ((await AsyncStorage.getItem(REMEMBERED)) === server) setErrorReports(server, true);
  } catch { /* off until the server answers */ }
}
