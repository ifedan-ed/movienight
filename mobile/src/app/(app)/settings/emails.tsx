// Emails: the weekly digest, month in review and year in review (same
// settings as the website's Profile → Emails). On unless switched off here, on
// the website, or with the "Turn this email off" link in every digest.
import { useState } from "react";
import { Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { router } from "expo-router";
import { SafeAreaView } from "react-native-safe-area-context";

import { Icon } from "@/components/Icon";
import { Loading, tap, useToast } from "@/components/ui";
import { api } from "@/lib/api";
import { colors, gutter } from "@/lib/theme";
import type { EmailSettings } from "@/lib/types";

const KINDS: { flag: "weekly" | "monthly" | "yearly"; title: string; when: string }[] = [
  { flag: "weekly", title: "Weekly digest", when: "Mondays: last week's films and episodes, your ratings, what's new" },
  { flag: "monthly", title: "Month in review", when: "The 1st of each month" },
  { flag: "yearly", title: "Year in review", when: "1 January" },
];

export default function Emails() {
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: ["emails"], queryFn: () => api.get<EmailSettings>("/api/v1/me/emails") });
  const d = q.data;
  const [saving, setSaving] = useState(false);
  const toggle = async (flag: (typeof KINDS)[number]["flag"]) => {
    if (!d || saving) return;
    setSaving(true);
    try {
      qc.setQueryData(["emails"], await api.put<EmailSettings>("/api/v1/me/emails", { [flag]: !d[flag] }));
    } catch (e: any) { toast(e.message, { error: true }); }
    setSaving(false);
  };
  return (
    <SafeAreaView edges={["top"]} style={{ flex: 1, backgroundColor: colors.bg }}>
      <View style={st.head}>
        <Pressable onPress={() => router.back()} style={st.back}><Icon name="left" /></Pressable>
        <Text style={st.h1}>Emails</Text>
      </View>
      <ScrollView contentContainerStyle={{ padding: gutter, paddingBottom: 60 }}>
        <Text style={st.p}>These are on unless you switch them off. One is only sent when you watched, rated or loved something, or something new came into the library.</Text>
        {!d ? <Loading /> : KINDS.map((k) => (
          <Pressable key={k.flag} disabled={saving} onPress={() => { tap(); toggle(k.flag); }} style={st.row}
            accessibilityRole="checkbox" accessibilityState={{ checked: d[k.flag] }}>
            <View style={[st.box, d[k.flag] && st.boxOn]}>{d[k.flag] ? <Icon name="check" size={20} color={colors.accentInk} /> : null}</View>
            <View style={{ flex: 1 }}>
              <Text style={st.name}>{k.title}</Text>
              <Text style={st.sub}>{k.when}</Text>
            </View>
          </Pressable>
        ))}
        {d ? <Text style={[st.p, { marginTop: 16 }]}>Sent to {d.address || "no address yet (set one on the website, Profile → Emails)"}</Text> : null}
      </ScrollView>
    </SafeAreaView>
  );
}

// The same look as Players.
const st = StyleSheet.create({
  head: { flexDirection: "row", alignItems: "center", gap: 12, paddingHorizontal: gutter, paddingTop: 10 },
  back: { width: 44, height: 44, borderRadius: 22, backgroundColor: "rgba(255,255,255,0.07)", borderWidth: 1, borderColor: colors.line, alignItems: "center", justifyContent: "center" },
  h1: { color: colors.text, fontSize: 24, fontWeight: "800" },
  p: { color: colors.text2, fontSize: 15, lineHeight: 21, marginBottom: 8 },
  row: { flexDirection: "row", alignItems: "center", gap: 12, paddingVertical: 12, borderBottomWidth: 1, borderBottomColor: colors.line },
  box: { width: 30, height: 30, borderRadius: 9, borderWidth: 2, borderColor: colors.line2, alignItems: "center", justifyContent: "center" },
  boxOn: { backgroundColor: colors.accent, borderColor: colors.accent },
  name: { color: colors.text, fontSize: 16.5, fontWeight: "700" },
  sub: { color: colors.text3, fontSize: 14 },
});
