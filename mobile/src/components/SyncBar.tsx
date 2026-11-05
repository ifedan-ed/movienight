// A thin bar under the status bar: "Offline · 3 changes waiting" or
// "Syncing 3 changes…"; nothing while no change is waiting. A change the
// server refuses for good is said in a toast, never dropped without a word.
import { useEffect, useState, useSyncExternalStore } from "react";
import { StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { useToast } from "@/components/ui";
import { useOnline } from "@/lib/net";
import { onChange, onRefused, pending } from "@/lib/outbox";
import { colors } from "@/lib/theme";

export function SyncBar() {
  const online = useOnline();
  const toast = useToast();
  useEffect(() => onRefused((message) => toast(message, { error: true })), [toast]);
  const waiting = useSyncExternalStore(onChange, pending, pending);
  const insets = useSafeAreaInsets();
  const [late, setLate] = useState(false);
  useEffect(() => {
    const t = setTimeout(() => setLate(waiting > 0), waiting > 0 ? 1500 : 0);
    return () => clearTimeout(t);
  }, [waiting]);
  if (!(waiting && late)) return null;
  const changes = `${waiting} change${waiting === 1 ? "" : "s"}`;
  const text = online ? `Syncing ${changes}…` : `Offline · ${changes} waiting to sync`;
  return (
    <View pointerEvents="none" style={[st.bar, { top: insets.top + 4 }]}>
      <Text style={[st.text, { color: online ? colors.info : colors.accent }]}>{text}</Text>
    </View>
  );
}

const st = StyleSheet.create({
  bar: { position: "absolute", alignSelf: "center", paddingHorizontal: 12, paddingVertical: 5, borderRadius: 999, borderWidth: 1, borderColor: colors.line2, backgroundColor: "rgba(13,15,20,0.94)", zIndex: 100 },
  text: { fontSize: 12.5, fontWeight: "700" },
});
