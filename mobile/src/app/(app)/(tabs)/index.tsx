// Home: For You / Movies / Shows, the hero, then the rows you chose on the
// web's Home rows screen (same settings).
import { useEffect, useState } from "react";
import { FlatList, Pressable, RefreshControl, ScrollView, StyleSheet, Text, useWindowDimensions, View } from "react-native";
import { Image } from "expo-image";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { router } from "expo-router";
import { SafeAreaView } from "react-native-safe-area-context";

import { openTitle, RowView } from "@/components/cards";
import { PrimaryButton } from "@/components/TitleActions";
import { Chip, ErrorBox, Loading, SectionTitle, tap } from "@/components/ui";
import { api } from "@/lib/api";
import { colors, gutter, img } from "@/lib/theme";
import { remember } from "@/lib/titleState";
import type { Card, CardsOut, HomeOut, PosterRow, RowsOut } from "@/lib/types";

type Tab = "for_you" | "movies" | "shows";
const TABS: [Tab, string, "sparkles" | "film" | "tv"][] = [["for_you", "For You", "sparkles"], ["movies", "Movies", "film"], ["shows", "Shows", "tv"]];

function moreHref(row: PosterRow, tab: Tab) {
  if (row.list_id) return () => router.push({ pathname: "/list/[id]", params: { id: String(row.list_id) } });
  if (row.id === "watchlist" || row.id === "favorites" || row.id === "rated") return () => router.push({ pathname: "/collection/[kind]", params: { kind: row.id } });
  if (row.more) { const m = row.more; return () => router.push({ pathname: "/browse", params: { key: m.key, tab, title: m.title } }); }
  return undefined;
}

function Hero({ items }: { items: Card[] }) {
  const { width } = useWindowDimensions();
  const w = width - gutter * 2;
  const [index, setIndex] = useState(0);
  useEffect(() => { items.forEach((c) => remember(c.key, c)); }, [items]);
  if (!items.length) return null;
  return (
    <View style={{ marginTop: 8 }}>
      <FlatList
        horizontal
        pagingEnabled
        showsHorizontalScrollIndicator={false}
        data={items}
        keyExtractor={(c) => c.key}
        onMomentumScrollEnd={(e) => setIndex(Math.round(e.nativeEvent.contentOffset.x / (w + 12)))}
        snapToInterval={w + 12}
        decelerationRate="fast"
        contentContainerStyle={{ paddingHorizontal: gutter, gap: 12 }}
        renderItem={({ item: c }) => {
          return (
            <Pressable onPress={() => openTitle(c)} style={[st.slide, { width: w, height: Math.min(520, w * 1.2) }]}>
              <Image source={img(c.backdrop, "w1280")} style={StyleSheet.absoluteFill} contentFit="cover" />
              <View style={st.slideShade} />
              <View style={st.slideInfo}>
                <Text style={st.kicker}>{(c.hero_label || "Featured").toUpperCase()}</Text>
                <Text style={st.slideTitle} numberOfLines={2}>{c.title}</Text>
                <Text style={st.slideMeta}>{[c.year, c.vote ? `★\u00a0${c.vote}` : null, (c.genres || []).slice(0, 2).join(", ")].filter(Boolean).join(" · ")}</Text>
                {c.overview ? <Text style={st.slideOv} numberOfLines={2}>{c.overview}</Text> : null}
                <PrimaryButton t={{ key: c.key, media_type: c.media_type as "movie" | "tv", tmdb_id: c.tmdb_id, title: c.title }} />
              </View>
            </Pressable>
          );
        }}
      />
      {items.length > 1 ? (
        <View style={st.dots}>{items.map((c, i) => <View key={c.key} style={[st.dot, i === index && st.dotOn]} />)}</View>
      ) : null}
    </View>
  );
}

function HomeRow({ tab, rowKey, title }: { tab: Tab; rowKey: string; title: string }) {
  const q = useQuery({ queryKey: ["row", tab, rowKey], queryFn: () => api.get<RowsOut>(`/api/v1/rows/${encodeURIComponent(rowKey)}?tab=${tab}`) });
  if (!q.data) return null;
  const shown = q.data.rows.filter((r) => r.items.length);
  // No rows, for a reason worth saying ("Picked for you" being made, or failed and when it is tried again).
  if (!shown.length && q.data.note) {
    return (
      <View>
        <SectionTitle title={title} />
        <Text style={st.rowNote}>{q.data.note}</Text>
      </View>
    );
  }
  return (
    <>
      {shown.map((r) => (
        <RowView key={r.id} row={r as any} onMore={r.style === "poster" ? moreHref(r as PosterRow, tab) : undefined} />
      ))}
    </>
  );
}

export default function Home() {
  const [tab, setTab] = useState<Tab>("for_you");
  const qc = useQueryClient();
  const spec = useQuery({ queryKey: ["home", tab], queryFn: () => api.get<HomeOut>(`/api/v1/home?tab=${tab}`) });
  const hero = useQuery({ queryKey: ["hero", tab], queryFn: () => api.get<CardsOut>(`/api/v1/hero?tab=${tab}`) });
  const [refreshing, setRefreshing] = useState(false);
  const refresh = async () => {
    setRefreshing(true);
    await qc.invalidateQueries();
    setRefreshing(false);
  };
  return (
    <SafeAreaView edges={["top"]} style={{ flex: 1, backgroundColor: colors.bg }}>
      <ScrollView refreshControl={<RefreshControl refreshing={refreshing} onRefresh={refresh} tintColor={colors.accent} />} contentContainerStyle={{ paddingBottom: 40 }}>
        <Text style={st.h1}>Movie Night</Text>
        <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ paddingHorizontal: gutter, gap: 8, paddingVertical: 8 }}>
          {TABS.map(([id, label, icon]) => <Chip key={id} label={label} icon={icon} on={tab === id} onPress={() => { tap(); setTab(id); }} />)}
        </ScrollView>
        {hero.data ? <Hero items={hero.data.items} /> : <View style={[st.slide, { marginHorizontal: gutter, height: 300, marginTop: 8 }]} />}
        {spec.isLoading ? <Loading /> : spec.error ? <ErrorBox message={(spec.error as Error).message} onRetry={() => spec.refetch()} /> : null}
        {spec.data?.rows.map((r) => <HomeRow key={`${tab}:${r.key}`} tab={tab} rowKey={r.key} title={r.title} />)}
      </ScrollView>
    </SafeAreaView>
  );
}

const st = StyleSheet.create({
  h1: { color: colors.text, fontSize: 32, fontWeight: "800", paddingHorizontal: gutter, paddingTop: 10 },
  rowNote: { color: colors.text3, fontSize: 14.5, paddingHorizontal: gutter, paddingBottom: 6 },
  slide: { borderRadius: 20, overflow: "hidden", backgroundColor: colors.surface, justifyContent: "flex-end" },
  slideShade: { ...StyleSheet.absoluteFill, backgroundColor: "rgba(8,9,13,0.45)" },
  slideInfo: { padding: 16, gap: 4 },
  kicker: { color: colors.accent, fontSize: 12.5, fontWeight: "800", letterSpacing: 1 },
  slideTitle: { color: colors.text, fontSize: 27, fontWeight: "800" },
  slideMeta: { color: colors.text2, fontSize: 14.5, fontWeight: "600" },
  slideOv: { color: colors.text2, fontSize: 14.5, marginBottom: 10 },
  dots: { flexDirection: "row", justifyContent: "center", gap: 6, marginTop: 10 },
  dot: { width: 7, height: 7, borderRadius: 4, backgroundColor: "rgba(255,255,255,0.25)" },
  dotOn: { width: 20, backgroundColor: colors.accent },
});
