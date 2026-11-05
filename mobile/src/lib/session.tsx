// Who is signed in on this device, and where their Movie Night is.
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

import { addSignOutNote, api, configure, serverUrl } from "./api";
import { clear as clearOutbox } from "./outbox";
import { load, remove, save } from "./storage";
import { clearTitleStates } from "./titleState";

const DEFAULT_SERVER = "https://movienight.example.com";

/** "3 changes made on this phone weren't saved: ..." */
export function lostNote(n: number): string {
  return `${n} ${n === 1 ? "change" : "changes"} made on this phone (ratings, ticks, lists, ...) ${n === 1 ? "wasn't" : "weren't"} saved: ${n === 1 ? "it was" : "they were"} still waiting to reach Movie Night when this phone was signed out.`;
}

type Session = {
  ready: boolean;
  server: string;
  token: string | null;
  /** This device's entry in Profile → Signed-in devices. */
  deviceId: number | null;
  setSignedIn: (server: string, token: string, deviceId: number | null) => Promise<void>;
  /** Signs this device out on the server too, then forgets the token. */
  signOut: () => Promise<void>;
};

const Ctx = createContext<Session | null>(null);

/** `onSignedOut` empties what the app keeps of the person (its cache). */
export function SessionProvider({ children, onSignedOut }: { children: ReactNode; onSignedOut: () => void }) {
  const [ready, setReady] = useState(false);
  const [server, setServer] = useState(DEFAULT_SERVER);
  const [token, setToken] = useState<string | null>(null);
  const [deviceId, setDeviceId] = useState<number | null>(null);

  // Forget the token here only: used when the server already refused it.
  // Nothing of the person stays for whoever signs in next: not the token in
  // memory, their changes waiting to go, their screens or title states.
  // Changes still waiting are lost with it, and the sign-in screen says how
  // many (never without a word).
  const forget = useCallback(async () => {
    configure(serverUrl(), "", () => {});
    onSignedOut();
    clearTitleStates();
    const [, , lost] = await Promise.all([remove("token"), remove("device"), clearOutbox()]);
    if (lost) addSignOutNote(lostNote(lost));
    setToken(null);
    setDeviceId(null);
  }, [onSignedOut]);

  useEffect(() => {
    (async () => {
      const [s, t, d] = await Promise.all([load("server"), load("token"), load("device")]);
      if (!t) {
        // Left from someone signed out before (the app closed mid sign-out).
        const lost = await clearOutbox();
        if (lost) addSignOutNote(lostNote(lost));
      }
      const srv = s || DEFAULT_SERVER;
      configure(srv, t || "", () => { forget(); });
      setServer(srv);
      setToken(t);
      setDeviceId(d ? Number(d) || null : null);
      setReady(true);
    })();
  }, [forget]);

  const setSignedIn = useCallback(async (srv: string, tok: string, device: number | null) => {
    await Promise.all([save("server", srv), save("token", tok), device ? save("device", String(device)) : remove("device")]);
    configure(srv, tok, () => { forget(); });
    setServer(srv);
    setToken(tok);
    setDeviceId(device);
  }, [forget]);

  const signOut = useCallback(async () => {
    if (deviceId) {
      try { await api.del(`/api/v1/me/devices/${deviceId}`); } catch { /* offline or already gone: forget it anyway */ }
    }
    await forget();
  }, [deviceId, forget]);

  const value = useMemo(() => ({ ready, server, token, deviceId, setSignedIn, signOut }), [ready, server, token, deviceId, setSignedIn, signOut]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useSession(): Session {
  const s = useContext(Ctx);
  if (!s) throw new Error("useSession outside SessionProvider");
  return s;
}
