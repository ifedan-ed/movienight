// Phones, tablets and TVs signed in to this account, with the date each one's
// sign-in runs out by itself; sign any of them out.
import { Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { router } from "expo-router";
import { SafeAreaView } from "react-native-safe-area-context";

import { Icon } from "@/components/Icon";
import { Button, Empty, Loading, useToast } from "@/components/ui";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { colors, gutter } from "@/lib/theme";
import type { DeviceOut } from "@/lib/types";

export default function Devices() {
  const qc = useQueryClient();
  const toast = useToast();
  const { deviceId, signOut } = useSession();
  const q = useQuery({ queryKey: ["devices"], queryFn: () => api.get<DeviceOut[]>("/api/v1/me/devices") });
  const out = async (d: DeviceOut) => {
    if (d.id === deviceId) return signOut();
    try {
      await api.del(`/api/v1/me/devices/${d.id}`);
      qc.invalidateQueries({ queryKey: ["devices"] });
      toast(`Signed out ${d.name}`);
    } catch (e: any) { toast(e.message, { error: true }); }
  };
  return (
    <SafeAreaView edges={["top"]} style={{ flex: 1, backgroundColor: colors.bg }}>
      <View style={st.head}>
        <Pressable onPress={() => router.back()} style={st.back}><Icon name="left" /></Pressable>
        <Text style={st.h1}>Signed-in devices</Text>
      </View>
      <ScrollView contentContainerStyle={{ padding: gutter, gap: 10, paddingBottom: 60 }}>
        <Text style={st.p}>Each device signs out by itself on the date shown: after a stretch without being used, or a year after it signed in. That way a lost phone doesn&apos;t keep access. Sign in again any time.</Text>
        {q.isLoading ? <Loading /> : !q.data?.length ? <Empty title="No devices" /> : q.data.map((d) => (
          <View key={d.id} style={st.card}>
            <Icon name="phone" color={colors.accent} />
            <View style={{ flex: 1 }}>
              <Text style={st.name}>{d.name}{d.id === deviceId ? <Text style={st.here}>  · This phone</Text> : null}</Text>
              <Text style={st.sub}>Signed in {new Date(d.created_at).toLocaleDateString()}{d.last_used_at ? ` · last used ${new Date(d.last_used_at).toLocaleDateString()}` : ""}</Text>
              <Text style={st.sub}>{d.expires_because === "unused"
                ? `Signs out ${new Date(d.expires_at).toLocaleDateString()} unless it is used before`
                : `Signs out ${new Date(d.expires_at).toLocaleDateString()}, a year after it signed in`}</Text>
            </View>
            <Button small kind="danger" label="Sign out" onPress={() => out(d)} />
          </View>
        ))}
      </ScrollView>
    </SafeAreaView>
  );
}

const st = StyleSheet.create({
  head: { flexDirection: "row", alignItems: "center", gap: 12, paddingHorizontal: gutter, paddingTop: 10 },
  back: { width: 44, height: 44, borderRadius: 22, backgroundColor: "rgba(255,255,255,0.07)", borderWidth: 1, borderColor: colors.line, alignItems: "center", justifyContent: "center" },
  h1: { color: colors.text, fontSize: 24, fontWeight: "800" },
  card: { flexDirection: "row", alignItems: "center", gap: 12, backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line, borderRadius: 15, padding: 12 },
  name: { color: colors.text, fontSize: 15.5, fontWeight: "700" },
  sub: { color: colors.text3, fontSize: 13 },
  p: { color: colors.text2, fontSize: 14.5, lineHeight: 20 },
  here: { color: colors.accent, fontWeight: "800" },
});
