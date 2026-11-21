// Crash reports. The server puts this file (and the Sentry SDK before it,
// /static/vendor/sentry.min.js) into the page only when ERROR_REPORTS_WEB_DSN
// is set, so a site without it loads neither.
//
// Reports never go to the collector directly: the SDK's `tunnel` sends them
// to this site (POST /api/v1/errors/web), which checks, limits and forwards
// them with the real DSN. The DSN below is a placeholder the SDK needs to
// start; the server replaces it. Only uncaught errors and unhandled promise
// rejections are sent: no sessions, no tracing, no replay, no breadcrumbs,
// and nothing about the person.
const Sentry = window.Sentry;

// The address without its query or fragment (a search's words live there).
function bare(url) {
  if (typeof url !== "string") return url;
  const cut = url.search(/[?#]/);
  return cut < 0 ? url : url.slice(0, cut);
}

function scrub(event) {
  delete event.user;
  if (event.request) {
    const ua = event.request.headers && event.request.headers["User-Agent"];
    event.request = { url: bare(event.request.url), headers: ua ? { "User-Agent": ua } : {} };
  }
  for (const ex of event.exception?.values || []) {
    for (const f of ex.stacktrace?.frames || []) {
      f.filename = bare(f.filename);
      if (f.abs_path) f.abs_path = bare(f.abs_path);
    }
  }
  return event;
}

if (Sentry) {
  Sentry.init({
    dsn: `https://public@${location.host}/0`,
    tunnel: "/api/v1/errors/web",
    // Every /api write must carry it (the server's CSRF guard).
    transportOptions: { headers: { "X-Requested-With": "movienight" } },
    release: `movienight-web@${document.documentElement.dataset.v || "unknown"}`,
    sendDefaultPii: false,
    sampleRate: 1.0,
    sendClientReports: false,
    // Only what catches errors: no session per page load, no breadcrumbs of
    // what was clicked or fetched.
    defaultIntegrations: false,
    integrations: [
      Sentry.globalHandlersIntegration({ onerror: true, onunhandledrejection: true }),
      Sentry.linkedErrorsIntegration(),
      Sentry.dedupeIntegration(),
      Sentry.httpContextIntegration(),
    ],
    beforeSend: scrub,
  });
}
