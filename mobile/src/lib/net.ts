// Whether the phone is online, wired into React Query: offline, queries
// pause instead of failing and pick up again on reconnect.
import { useSyncExternalStore } from "react";
import { Platform } from "react-native";
import NetInfo from "@react-native-community/netinfo";
import { onlineManager } from "@tanstack/react-query";

let online = true;
const listeners = new Set<() => void>();

function set(now: boolean) {
  if (now === online) return;
  online = now;
  listeners.forEach((l) => l());
}

onlineManager.setEventListener((setOnline) => {
  const off = NetInfo.addEventListener((state) => {
    const now = state.isConnected !== false && state.isInternetReachable !== false;
    set(now);
    setOnline(now);
  });
  // In a browser with navigator.connection, NetInfo only hears that API's
  // "change" event; the browser's own online/offline events are the ones that
  // fire when the connection drops, so the web build listens to them too.
  if (Platform.OS === "web" && typeof window !== "undefined") {
    const fire = () => { set(navigator.onLine); setOnline(navigator.onLine); };
    window.addEventListener("online", fire);
    window.addEventListener("offline", fire);
    return () => { off(); window.removeEventListener("online", fire); window.removeEventListener("offline", fire); };
  }
  return off;
});

export function isOnline() {
  return online;
}

export function onOnlineChange(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function useOnline(): boolean {
  return useSyncExternalStore(onOnlineChange, isOnline, isOnline);
}
