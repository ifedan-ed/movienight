// Find lists on MDBList: top lists, search, or a pasted link; add one to
// the install's MDBList lists (kept in step every night).
import { useEffect, useState } from "react";
import { Pressable, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { router } from "expo-router";
import { SafeAreaView } from "react-native-safe-area-context";

import { Icon } from "@/components/Icon";
import { Button, Empty, Loading, useToast } from "@/components/ui";
import { api } from "@/lib/api";
import { caret, colors, gutter } from "@/lib/theme";
import type { MDBListExploreOut } from "@/lib/types";

export default function MDBList() {
  const [text, setText] = useState("");
  const [q, setQ] = useState("");
  const qc = useQueryClient();
  const toast = useToast();
  useEffect(() => { const t = setTimeout(() => setQ(text.trim()), 450); return () => clearTimeout(t); }, [text]);
  const res = useQuery({ queryKey: ["mdblist", q], queryFn: () => api.get<MDBListExploreOut>(`/api/v1/mdblist/lists?q=${encodeURIComponent(q)}`) });
  const follow = async (id: number, name: string) => {
    try {
      const r = await api.post<{ id: number; count: number }>("/api/v1/mdblist/follow", { ref: String(id) });
      toast(`Added “${name}” (${r.count} titles)`);
      qc.invalidateQueries({ queryKey: ["lists"] });
      qc.invalidateQueries({ queryKey: ["mdblist"] });
    } catch (e: any) { toast(e.message, { error: true }); }
  };
  return (
    <SafeAreaView edges={["top"]} style={{ flex: 1, backgroundColor: colors.bg }}>
      <View style={st.head}>
        <Pressable onPress={() => router.back()} style={st.back}><Icon name="left" /></Pressable>
        <Text style={st.h1}>MDBList lists</Text>
      </View>
      <View style={st.box}>
        <Icon name="search" color={colors.text3} />
        <TextInput {...caret} style={st.input} value={text} onChangeText={setText} placeholder="Search lists, or paste an mdblist.com link" placeholderTextColor={colors.text3} autoCapitalize="none" autoCorrect={false} />
      </View>
      <ScrollView contentContainerStyle={{ padding: gutter, paddingBottom: 60 }} keyboardShouldPersistTaps="handled">
        {res.isLoading ? <Loading /> : res.data?.error ? <Empty title="Nothing found" text={res.data.error} /> : (res.data?.results || []).map((l) => (
          <View key={l.mdblist_id ?? l.name} style={st.row}>
            <View style={{ flex: 1 }}>
              <Text style={st.name}>{l.name}</Text>
              <Text style={st.sub}>{[l.user ? `by ${l.user}` : null, l.item_count != null ? `${l.item_count} titles` : null, l.media === "movie" ? "Movies" : l.media === "tv" ? "Shows" : null].filter(Boolean).join(" · ")}</Text>
            </View>
            {l.followed_id ? (
              <Button small label="Open" onPress={() => router.push({ pathname: "/list/[id]", params: { id: String(l.followed_id) } })} />
            ) : (
              <Button small kind="request" icon="plus" label="Add" onPress={() => l.mdblist_id && follow(l.mdblist_id, l.name)} />
            )}
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
  box: { flexDirection: "row", alignItems: "center", gap: 10, marginHorizontal: gutter, marginTop: 10, height: 52, borderRadius: 16, backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line2, paddingHorizontal: 14 },
  input: { flex: 1, color: colors.text, fontSize: 16, height: 50 },
  row: { flexDirection: "row", alignItems: "center", gap: 12, paddingVertical: 12, borderBottomWidth: 1, borderBottomColor: colors.line },
  name: { color: colors.text, fontSize: 16, fontWeight: "700" },
  sub: { color: colors.text3, fontSize: 13.5 },
});
