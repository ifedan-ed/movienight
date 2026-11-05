// Players: tick the ones the Play sheet offers (same settings as the web).
import { useState } from "react";
import { Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { router } from "expo-router";
import { SafeAreaView } from "react-native-safe-area-context";

import { Icon } from "@/components/Icon";
import { Loading, tap, useToast } from "@/components/ui";
import { api } from "@/lib/api";
import { colors, gutter } from "@/lib/theme";
import type { PlayerSettingsOut } from "@/lib/types";

const NOTES: Record<string, string> = {
  vlc: "iPhone and iPad", infuse: "iPhone, iPad and Apple TV",
  outplayer: "iPhone and iPad", vlc_android: "Android", jellyfin: "Jellyfin",
  swiftfin: "iPhone, iPad and Apple TV", streamyfin: "iPhone and Android",
};

export default function Players() {
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: ["players"], queryFn: () => api.get<PlayerSettingsOut>("/api/v1/me/players") });
  const d = q.data;
  // One change at a time: each sends every player as the screen shows them,
  // so a second tap before the first reply would switch the first one back.
  const [saving, setSaving] = useState(false);
  const toggle = async (id: string) => {
    if (!d || saving) return;
    const enabled = d.players.filter((p) => (p.id === id ? !p.enabled : p.enabled)).map((p) => p.id);
    if (!enabled.length) return toast("Keep at least one player", { error: true });
    setSaving(true);
    try {
      const custom = d.players.filter((p) => !p.builtin).map((p) => ({ name: p.name, template: p.template }));
      const res = await api.put<PlayerSettingsOut>("/api/v1/me/players", { custom, enabled, default_player: d.default_player, subtitle_language: d.subtitle_language });
      qc.setQueryData(["players"], res);
    } catch (e: any) { toast(e.message, { error: true }); }
    setSaving(false);
  };
  const askToggle = async () => {
    if (!d || saving) return;
    setSaving(true);
    try {
      const custom = d.players.filter((p) => !p.builtin).map((p) => ({ name: p.name, template: p.template }));
      const res = await api.put<PlayerSettingsOut>("/api/v1/me/players", {
        custom, enabled: d.players.filter((p) => p.enabled).map((p) => p.id), default_player: d.default_player,
        subtitle_language: d.subtitle_language, ask_finished: d.ask_finished === false,
      });
      qc.setQueryData(["players"], res);
    } catch (e: any) { toast(e.message, { error: true }); }
    setSaving(false);
  };
  return (
    <SafeAreaView edges={["top"]} style={{ flex: 1, backgroundColor: colors.bg }}>
      <View style={st.head}>
        <Pressable onPress={() => router.back()} style={st.back}><Icon name="left" /></Pressable>
        <Text style={st.h1}>Players</Text>
      </View>
      <ScrollView contentContainerStyle={{ padding: gutter, paddingBottom: 60 }}>
        {!d ? <Loading /> : d.players.map((p) => (
          <Pressable key={p.id} disabled={saving} onPress={() => { tap(); toggle(p.id); }} style={st.row}>
            <View style={[st.box, p.enabled && st.boxOn]}>{p.enabled ? <Icon name="check" size={20} color={colors.accentInk} /> : null}</View>
            <View style={{ flex: 1 }}>
              <Text style={st.name}>{p.name}</Text>
              <Text style={st.sub}>{p.builtin ? NOTES[p.id] || "" : p.template}</Text>
            </View>
          </Pressable>
        ))}
        {d ? <Text style={[st.p, { marginTop: 16 }]}>Subtitle language: {d.subtitle_language || "none"}</Text> : null}
        {d ? (
          <Pressable disabled={saving} onPress={() => { tap(); askToggle(); }} style={[st.row, { marginTop: 8 }]} accessibilityRole="checkbox" accessibilityState={{ checked: d.ask_finished !== false }}>
            <View style={[st.box, d.ask_finished !== false && st.boxOn]}>{d.ask_finished !== false ? <Icon name="check" size={20} color={colors.accentInk} /> : null}</View>
            <Text style={[st.name, { flex: 1 }]}>Ask if I finished it</Text>
          </Pressable>
        ) : null}
      </ScrollView>
    </SafeAreaView>
  );
}

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
