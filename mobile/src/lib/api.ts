// The Movie Night API (/api/v1) with this device's token. Every write
// carries X-Requested-With, as the server requires.

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

let server = "";
let token = "";
let onSignedOut: (() => void) | null = null;
// Why the server signed this phone out (its sign-in ran out after 90 days
// unused or a year), shown once on the sign-in screen.
let signOutReason: string | null = null;

export function takeSignOutReason(): string | null {
  const why = signOutReason;
  signOutReason = null;
  return why;
}

/** Adds a sentence to what the sign-in screen says (changes that were
 * waiting on this phone and are lost with the sign-out). */
export function addSignOutNote(note: string) {
  signOutReason = signOutReason ? `${signOutReason} ${note}` : note;
}

export function configure(nextServer: string, nextToken: string, signedOut: () => void) {
  server = nextServer.replace(/\/+$/, "");
  token = nextToken;
  onSignedOut = signedOut;
}

export function serverUrl() {
  return server;
}

export function authHeaders(): Record<string, string> {
  return token ? { Authorization: `Bearer ${token}` } : {};
}

// No answer in this long (a half-dead mobile connection) counts as no
// connection, so the outbox and the refreshes behind it retry later.
const TIMEOUT_MS = 20_000;

async function call<T>(method: string, path: string, body?: unknown, timeoutMs = TIMEOUT_MS): Promise<T> {
  if (!token) throw new ApiError(401, "Signed out");  // nothing goes out once signed out
  const headers: Record<string, string> = { ...authHeaders(), "X-Requested-With": "movienight" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    let res: Response;
    try {
      res = await fetch(`${server}${path}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: ctrl.signal });
    } catch {
      throw new ApiError(0, "No connection — check your network");
    }
    if (res.status === 401) {
      try { const data = await res.json(); if (data?.detail) signOutReason = String(data.detail); } catch { /* no body */ }
      onSignedOut?.();
      throw new ApiError(401, "Signed out");
    }
    let data: any = null;
    try { data = await res.json(); } catch { /* empty body */ }
    if (ctrl.signal.aborted) throw new ApiError(0, "No connection — check your network");
    if (!res.ok) throw new ApiError(res.status, (data && data.detail && String(data.detail)) || `Something went wrong (${res.status})`);
    return data as T;
  } finally {
    clearTimeout(timer);
  }
}

export const api = {
  get: <T>(path: string, timeoutMs?: number) => call<T>("GET", path, undefined, timeoutMs),
  post: <T>(path: string, body: unknown = {}) => call<T>("POST", path, body),
  put: <T>(path: string, body: unknown = {}) => call<T>("PUT", path, body),
  patch: <T>(path: string, body: unknown = {}) => call<T>("PATCH", path, body),
  del: <T>(path: string) => call<T>("DELETE", path),
};

export const titlePath = (c: { media_type: string; tmdb_id: number }) => `/api/v1/title/${c.media_type}/${c.tmdb_id}`;
