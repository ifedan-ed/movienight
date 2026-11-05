// Watchlist, Favorites and Ratings as grids, Movies / Shows.
import { useState } from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";
import { router, useLocalSearchParams } from "expo-router";
import { SafeAreaView } from "react-native-safe-area-context";

import { InfiniteGrid } from "@/components/cards";
import { Icon } from "@/components/Icon";
import { Segmented } from "@/components/ui";
import { api } from "@/lib/api";
import { colors, gutter } from "@/lib/theme";
import type { CollectionOut } from "@/lib/types";

const NAMES: Record<string, string> = { watchlist: "Watchlist", favorites: "Favorites", rated: "Your top rated", ratings: "Your ratings" };

export default function Collection() {
  const { kind } = useLocalSearchParams<{ kind: string }>();
  const [media, setMedia] = useState<"" | "movie" | "tv">("");
  return (
    <SafeAreaView edges={["top"]} style={{ flex: 1, backgroundColor: colors.bg }}>
      <InfiniteGrid
        queryKey={["collection", kind, media]}
        load={(page) => api.get<CollectionOut>(`/api/v1/collection/${encodeURIComponent(kind)}?page=${page}${media ? `&media=${media}` : ""}`)}
        empty={kind === "watchlist" ? "Your watchlist is empty. Tap the bookmark on any title to add it." : "Nothing here yet."}
        header={(first) => (
          <View style={{ marginBottom: 6 }}>
            <View style={st.head}>
              <Pressable onPress={() => router.back()} style={st.back}><Icon name="left" /></Pressable>
              <View style={{ flex: 1 }}>
                <Text style={st.h1}>{NAMES[kind] || "Collection"}</Text>
                {first?.count != null ? <Text style={st.sub}>{first.count} title{first.count === 1 ? "" : "s"}</Text> : null}
              </View>
            </View>
            <Segmented value={media} onChange={setMedia} options={[["", "All"], ["movie", "Movies"], ["tv", "Shows"]]} />
          </View>
        )}
      />
    </SafeAreaView>
  );
}

const st = StyleSheet.create({
  head: { flexDirection: "row", alignItems: "center", gap: 12, paddingHorizontal: gutter, paddingTop: 10 },
  back: { width: 44, height: 44, borderRadius: 22, backgroundColor: "rgba(255,255,255,0.07)", borderWidth: 1, borderColor: colors.line, alignItems: "center", justifyContent: "center" },
  h1: { color: colors.text, fontSize: 24, fontWeight: "800" },
  sub: { color: colors.text3, fontSize: 14 },
});
