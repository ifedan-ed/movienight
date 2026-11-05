import { useEffect, useRef, useState } from "react";
import { AppState, Image, Text, View } from "react-native";
import AsyncStorage from "@react-native-async-storage/async-storage";
import { createAsyncStoragePersister } from "@tanstack/query-async-storage-persister";
import { QueryClient, useQueryClient } from "@tanstack/react-query";
import { PersistQueryClientProvider } from "@tanstack/react-query-persist-client";
import { DarkTheme, SplashScreen, Stack, ThemeProvider, usePathname } from "expo-router";
import { StatusBar } from "expo-status-bar";
import { SafeAreaProvider } from "react-native-safe-area-context";

import { Sheet } from "@/components/Sheet";
import { SyncBar } from "@/components/SyncBar";
import { Button, ToastHost, useToast } from "@/components/ui";
import { api } from "@/lib/api";
import { holdBusy, isBusy } from "@/lib/busy";
import { isOnline, onOnlineChange } from "@/lib/net";
import { flush, onSynced, send } from "@/lib/outbox";
import { SessionProvider, useSession } from "@/lib/session";
import { colors, img } from "@/lib/theme";
import { getTitleState, setTitleState } from "@/lib/titleState";
import type { PlaySessionOut } from "@/lib/types";
import { startOnLatestUpdate, updateOnReturn } from "@/lib/updates";

const WEEK = 7 * 24 * 3600_000;

// What the server said last time stays on the phone (a week), so the app
// opens at once and works without a connection; whenever there is one, the
// screen is refreshed from the server. Offline, requests wait instead of failing.
const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 60_000, gcTime: WEEK, retry: 1, networkMode: "offlineFirst", refetchOnReconnect: false, refetchOnWindowFocus: false },
    mutations: { networkMode: "offlineFirst" },
  },
});

const persister = createAsyncStoragePersister({ storage: AsyncStorage, key: "mn:cache", throttleTime: 1000 });

// Signed out: the next person to sign in on this phone sees none of it.
function clearCache() {
  queryClient.clear();
  persister.removeClient();
}

// Staying in step with the server: send what's waiting on the phone first,
// then refresh what's on screen — when the app comes to the front, when the
// connection comes back, and every minute while it's open.
function resync() {
  if (!isOnline()) return;
  flush().then(() => queryClient.invalidateQueries({ refetchType: "active" }));
}
AppState.addEventListener("change", (s) => { if (s === "active") resync(); });
onOnlineChange(resync);
onSynced(() => queryClient.invalidateQueries({ refetchType: "active" }));
setInterval(() => { if (AppState.currentState === "active") resync(); }, 60_000);

const theme = {
  ...DarkTheme,
  colors: { ...DarkTheme.colors, background: colors.bg, card: colors.bg2, text: colors.text, border: colors.line, primary: colors.accent },
};

// Keep the splash up until the stored sign-in is read, so no screen asks the
// server anything before it has this device's token.
SplashScreen.preventAutoHideAsync().catch(() => {});

const TAB_PAGES = new Set(["/", "/discover", "/search", "/lists", "/profile"]);

function RootNavigator() {
  const { ready, token } = useSession();
  // The splash also waits, briefly, for an update sent over the air (lib/updates).
  const [current, setCurrent] = useState(false);
  useEffect(() => { startOnLatestUpdate().finally(() => setCurrent(true)); }, []);
  // A waiting update goes in on the way back to the app, from a tab's own
  // page only, and never while something is in progress (lib/busy).
  const pathname = usePathname();
  const here = useRef(pathname);
  useEffect(() => { here.current = pathname; }, [pathname]);
  useEffect(() => updateOnReturn(() => TAB_PAGES.has(here.current) && !isBusy()), []);
  useEffect(() => { if (ready && current) SplashScreen.hide(); }, [ready, current]);
  // Not until then: a link the app was opened with (a title, a play
  // session) must meet the real sign-in state, not a passing "signed out".
  if (!ready || !current) return null;
  const signedIn = !!token;
  return (
    <Stack screenOptions={{ headerShown: false, contentStyle: { backgroundColor: colors.bg } }}>
      <Stack.Protected guard={signedIn}>
        <Stack.Screen name="(app)" />
      </Stack.Protected>
      <Stack.Protected guard={!signedIn}>
        <Stack.Screen name="signin" options={{ animation: "fade" }} />
      </Stack.Protected>
    </Stack>
  );
}

/** Back from VLC / Infuse: "Finished it?" for the latest Play. */
function FinishedPrompt() {
  const { token } = useSession();
  const toast = useToast();
  const qc = useQueryClient();
  const [s, setS] = useState<PlaySessionOut | null>(null);
  const asked = useRef(new Set<string>());
  // The server asks once, so from its answer until the person's answer is
  // queued no update may restart the app (lib/busy).
  const held = useRef<(() => void) | null>(null);
  const letGo = () => { held.current?.(); held.current = null; };

  useEffect(() => {
    if (!token) return;
    const check = async () => {
      const release = holdBusy();
      let kept = false;
      try {
        const res = await api.get<{ session: PlaySessionOut | null }>("/api/v1/play/open");
        const open = res.session;
        if (open && !asked.current.has(open.id)) {
          asked.current.add(open.id);
          held.current?.();
          held.current = release;
          kept = true;
          setS(open);
        }
      } catch { /* ignore */ } finally {
        if (!kept) release();
      }
    };
    const sub = AppState.addEventListener("change", (state) => { if (state === "active") check(); });
    check();
    return () => sub.remove();
  }, [token]);

  if (!s) return null;
  const answer = async (outcome: "watched" | "not_finished") => {
    const current = s;
    // This answer's own hold, let go once the answer is queued: by then a
    // newer "Finished it?" may hold one of its own.
    const mine = held.current;
    held.current = null;
    setS(null);
    send("POST", `/api/v1/play/${current.id}/finish`, { outcome }).then(() => qc.invalidateQueries()).finally(() => mine?.());
    if (outcome === "watched" && current.key) {
      setTitleState(current.key, { watched: (getTitleState(current.key).watched || 0) + 1 });
      toast(`Marked ${current.title} as watched`);
    }
  };
  return (
    <Sheet visible onClose={() => { setS(null); letGo(); }}>
      <View style={{ alignItems: "center" }}>
        {s.poster ? <Image source={{ uri: img(s.poster, "w185") }} style={{ width: 110, height: 165, borderRadius: 12, marginBottom: 14 }} /> : null}
        <Text style={{ color: colors.text, fontSize: 22, fontWeight: "800" }}>Finished it?</Text>
        <Text style={{ color: colors.text3, fontSize: 15, marginTop: 4, marginBottom: 16, textAlign: "center" }}>{s.title}</Text>
      </View>
      <Button kind="request" icon="check" label="Yes, mark as watched" onPress={() => answer("watched")} />
      <View style={{ height: 8 }} />
      <Button label="Not yet" onPress={() => answer("not_finished")} />
    </Sheet>
  );
}

export default function RootLayout() {
  return (
    <SafeAreaProvider>
      <PersistQueryClientProvider client={queryClient}
        persistOptions={{ persister, maxAge: WEEK, buster: "1", dehydrateOptions: { shouldDehydrateQuery: (q) => q.state.status === "success" } }}>
        <SessionProvider onSignedOut={clearCache}>
          <ThemeProvider value={theme}>
            <ToastHost>
              <StatusBar style="light" />
              <RootNavigator />
              <FinishedPrompt />
              <SyncBar />
            </ToastHost>
          </ThemeProvider>
        </SessionProvider>
      </PersistQueryClientProvider>
    </SafeAreaProvider>
  );
}
