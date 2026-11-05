// A person: photo, short bio, and what they're known for.
import { useState } from "react";
import { Pressable, ScrollView, StyleSheet, Text, useWindowDimensions, View } from "react-native";
import { Image } from "expo-image";
import { useQuery } from "@tanstack/react-query";
import { router, useLocalSearchParams } from "expo-router";
import { SafeAreaView } from "react-native-safe-area-context";

import { PosterCard } from "@/components/cards";
import { Icon } from "@/components/Icon";
import { ErrorBox, Loading, SectionTitle } from "@/components/ui";
import { api } from "@/lib/api";
import { colors, gutter, img } from "@/lib/theme";
import type { PersonOut } from "@/lib/types";

export default function Person() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const { width } = useWindowDimensions();
  const [more, setMore] = useState(false);
  const q = useQuery({ queryKey: ["person", id], queryFn: () => api.get<PersonOut>(`/api/v1/person/${encodeURIComponent(id)}`) });
  const cols = width >= 700 ? 5 : 3;
  const w = (width - gutter * 2 - (cols - 1) * 10) / cols;
  const p = q.data;
  return (
    <SafeAreaView edges={["top"]} style={{ flex: 1, backgroundColor: colors.bg }}>
      <ScrollView contentContainerStyle={{ paddingBottom: 60 }}>
        <View style={st.head}>
          <Pressable onPress={() => router.back()} style={st.back}><Icon name="left" /></Pressable>
          <Text style={st.h1} numberOfLines={2}>{p?.name || ""}</Text>
        </View>
        {q.isLoading ? <Loading /> : q.error ? <ErrorBox message={(q.error as Error).message} /> : p ? (
          <>
            <View style={st.row}>
              <View style={st.photo}>{p.photo ? <Image source={img(p.photo, "h632")} style={StyleSheet.absoluteFill} /> : null}</View>
              <View style={{ flex: 1 }}>
                <Text style={{ color: colors.text, fontWeight: "700", fontSize: 16 }}>{p.known_for}</Text>
                <Text style={{ color: colors.text3, fontSize: 14.5 }}>{[p.birthday ? `Born ${p.birthday}` : null, p.place_of_birth].filter(Boolean).join(" · ")}</Text>
              </View>
            </View>
            {p.biography ? (
              <Pressable onPress={() => setMore(!more)} style={{ paddingHorizontal: gutter }}>
                <Text style={st.bio} numberOfLines={more ? undefined : 6}>{p.biography}</Text>
              </Pressable>
            ) : null}
            <SectionTitle title="Known for" />
            <View style={st.grid}>{p.items.map((c) => <PosterCard key={c.key} c={c} width={w} />)}</View>
          </>
        ) : null}
      </ScrollView>
    </SafeAreaView>
  );
}

const st = StyleSheet.create({
  head: { flexDirection: "row", alignItems: "center", gap: 12, paddingHorizontal: gutter, paddingTop: 10 },
  back: { width: 44, height: 44, borderRadius: 22, backgroundColor: "rgba(255,255,255,0.07)", borderWidth: 1, borderColor: colors.line, alignItems: "center", justifyContent: "center" },
  h1: { color: colors.text, fontSize: 24, fontWeight: "800", flex: 1 },
  row: { flexDirection: "row", alignItems: "center", gap: 16, padding: gutter },
  photo: { width: 104, height: 104, borderRadius: 52, overflow: "hidden", backgroundColor: colors.surface2 },
  bio: { color: colors.text2, fontSize: 15, lineHeight: 22 },
  grid: { flexDirection: "row", flexWrap: "wrap", gap: 10, rowGap: 16, paddingHorizontal: gutter },
});
