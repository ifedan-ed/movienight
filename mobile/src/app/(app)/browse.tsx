// "See all" behind a Home row: the same row as an endless grid.
import { Pressable, StyleSheet, Text, View } from "react-native";
import { router, useLocalSearchParams } from "expo-router";
import { SafeAreaView } from "react-native-safe-area-context";

import { InfiniteGrid } from "@/components/cards";
import { Icon } from "@/components/Icon";
import { api } from "@/lib/api";
import { colors, gutter } from "@/lib/theme";
import type { PageOut } from "@/lib/types";

export default function Browse() {
  const { key, tab, title } = useLocalSearchParams<{ key: string; tab?: string; title?: string }>();
  return (
    <SafeAreaView edges={["top"]} style={{ flex: 1, backgroundColor: colors.bg }}>
      <InfiniteGrid
        queryKey={["browse", key, tab || "for_you"]}
        load={(page) => api.get<PageOut>(`/api/v1/browse?key=${encodeURIComponent(key)}&tab=${encodeURIComponent(tab || "for_you")}&page=${page}`)}
        header={
          <View style={st.head}>
            <Pressable onPress={() => router.back()} style={st.back}><Icon name="left" /></Pressable>
            <Text style={st.h1} numberOfLines={2}>{title || "Browse"}</Text>
          </View>
        }
      />
    </SafeAreaView>
  );
}

const st = StyleSheet.create({
  head: { flexDirection: "row", alignItems: "center", gap: 12, paddingHorizontal: gutter, paddingTop: 10, marginBottom: 10 },
  back: { width: 44, height: 44, borderRadius: 22, backgroundColor: "rgba(255,255,255,0.07)", borderWidth: 1, borderColor: colors.line, alignItems: "center", justifyContent: "center" },
  h1: { color: colors.text, fontSize: 24, fontWeight: "800", flex: 1 },
});
