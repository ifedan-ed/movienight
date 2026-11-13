// The phone's time zone, reported to the server so this person's days
// (history, "this year", the daily rows, the digest emails' weeks) are the
// phone's days. The zone comes from the phone's own setting (Intl, which
// Hermes answers from the system), never from where the connection seems to
// come from.
import { useEffect, useRef } from "react";
import { AppState } from "react-native";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { api, ApiError } from "@/lib/api";
import type { MeOut } from "@/lib/types";

/** The IANA name the phone is set to ("Europe/Berlin"), or null when the
 * system gives none. */
export function deviceTimeZone(): string | null {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || null;
  } catch {
    return null;
  }
}

/** Sends the phone's zone when it differs from the one /me says is stored:
 * once when the app starts and again whenever it comes back to the front (an
 * iPhone resumes the app rather than starting it, and a trip abroad changes
 * the zone in between). Then everything on screen is fetched again, so the
 * days shown are already the phone's. */
export function useReportTimeZone() {
  const qc = useQueryClient();
  const me = useQuery({ queryKey: ["me"], queryFn: () => api.get<MeOut>("/api/v1/me") });
  // The zone sent (or being sent) in this run: asked once, not on every
  // refresh of /me. A refusal (a zone the server doesn't know) is not asked
  // again until the app restarts; no connection is tried again next time.
  const sent = useRef<string | null>(null);
  const stored = me.data ? (me.data.time_zone ?? null) : undefined;
  useEffect(() => {
    if (stored === undefined) return; // /me not read yet
    const check = () => {
      const zone = deviceTimeZone();
      if (!zone || zone === stored || zone === sent.current) return;
      sent.current = zone;
      api.put("/api/v1/me/time-zone", { time_zone: zone })
        .then(() => qc.invalidateQueries())
        .catch((e) => { if (e instanceof ApiError && e.status === 0) sent.current = null; });
    };
    check();
    const sub = AppState.addEventListener("change", (s) => { if (s === "active") check(); });
    return () => sub.remove();
  }, [stored, qc]);
}
