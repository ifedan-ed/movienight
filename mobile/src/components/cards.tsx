// Posters, rows and grids: the same cards as the web app, with library,
// download, request, watchlist and seen/rated badges that follow taps made
// anywhere in the app.
import { memo, useEffect, useMemo, type ReactElement } from "react";
import { FlatList, Pressable, StyleSheet, Text, useWindowDimensions, View } from "react-native";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Image } from "expo-image";
import { router } from "expo-router";

import { authHeaders, serverUrl } from "../lib/api";
import { colors, gutter, img, radius } from "../lib/theme";
import { remember, useTitleState } from "../lib/titleState";
import type { Card, PageOut, PosterRow, UpNextItem } from "../lib/types";
import { Icon } from "./Icon";
import { Loading, SectionTitle, tap } from "./ui";

export function openTitle(c: { media_type?: string | null; tmdb_id?: number | null }) {
  if (c.media_type && c.tmdb_id) router.push({ pathname: "/title/[media]/[id]", params: { media: c.media_type, id: String(c.tmdb_id) } });
}

/** One episode opens on its season page, scrolled to it and opened
 * (Recently watched, Continue watching, Up next). */
export function openEpisode(showId: number, season: number, episode: number) {
  router.push({ pathname: "/season/[id]/[n]", params: { id: String(showId), n: String(season), episode: String(episode) } });
}

/** An episode's card (Recently watched): its own heart and rating. */
export type EpisodeMark = { season: number; episode: number; favorite?: boolean; rating?: number | null };

// ep: an episode's card; the corner says the episode's own rating and heart
// (it was watched), not the show's.
function Badges({ k, ep }: { k: string; ep?: EpisodeMark | null }) {
  const t = useTitleState(k);
  const s = ep ? { ...t, rating: ep.rating || null, watched: 1 } : t;
  const heart = ep?.favorite ? <Icon name="heartFill" size={13} color={colors.fav} /> : null;
  let badge = null;
  if (s.in_library) badge = <View style={[st.badge, { backgroundColor: colors.ok }]}><Icon name="check" size={16} color={colors.okInk} /></View>;
  else if (s.request === "processing" || s.request === "partial") badge = <View style={[st.badge, { backgroundColor: colors.accent }]}><Icon name="download" size={16} color={colors.accentInk} /></View>;
  else if (s.request === "pending") badge = <View style={[st.badge, { backgroundColor: colors.info }]}><Icon name="clock" size={16} color="#071630" /></View>;
  else if (s.watchlist) badge = <View style={[st.badge, { backgroundColor: "rgba(10,12,17,0.82)" }]}><Icon name="bookmarkFill" size={16} color={colors.accent} /></View>;
  return (
    <>
      {badge}
      {s.rating ? (
        <View style={st.seen}>{heart}<Icon name="starFill" size={13} color={colors.accent} /><Text style={st.seenText}>{s.rating}</Text></View>
      ) : s.watched ? (
        <View style={st.seen}>{heart}<Icon name="check" size={13} color={colors.ok} /><Text style={st.seenText}>Seen</Text></View>
      ) : null}
    </>
  );
}

export const PosterCard = memo(function PosterCard({ c, width, meta, episode, testID }: { c: Card; width: number; meta?: string; episode?: EpisodeMark | null; testID?: string }) {
  useEffect(() => { remember(c.key, c); }, [c]);
  const pill = [c.year ? String(c.year) : null, c.vote ? `★\u00a0${c.vote}` : null].filter(Boolean).join("  ");
  // An episode's card opens that episode; a title's card its page.
  const open = () => (episode ? openEpisode(c.tmdb_id, episode.season, episode.episode) : openTitle(c));
  return (
    <Pressable onPress={() => { tap(); open(); }} style={{ width }} accessibilityRole="button" testID={testID}
      accessibilityLabel={episode ? `${c.title}, season ${episode.season} episode ${episode.episode}` : c.title}>
      <View style={[st.poster, { width, height: width * 1.5 }]}>
        {c.poster ? (
          <Image source={img(c.poster)} style={StyleSheet.absoluteFill} contentFit="cover" transition={150} recyclingKey={c.key} />
        ) : (
          <Text style={st.noimg}>{c.title}</Text>
        )}
        {pill ? <View style={st.pill}><Text style={st.pillText}>{pill}</Text></View> : null}
        <Badges k={c.key} ep={episode} />
      </View>
      <Text style={st.cardTitle} numberOfLines={2}>{c.title}</Text>
      {meta ? <Text style={st.cardMeta} numberOfLines={1}>{meta}</Text> : null}
    </Pressable>
  );
});

export function usePosterWidth() {
  const { width } = useWindowDimensions();
  return Math.max(112, Math.min(172, width * 0.305));
}

export function LandscapeCard({ e, width }: { e: UpNextItem; width: number }) {
  const source = e.backdrop ? img(e.backdrop, "w780")
    : e.image_item ? { uri: `${serverUrl()}/api/v1/img/jf/${e.series_item || e.image_item}?kind=Backdrop&w=780`, headers: authHeaders() } : undefined;
  return (
    // An episode (Continue watching, Next up) opens that episode; a film its page.
    <Pressable onPress={() => { tap(); if (e.media_type === "tv" && e.tmdb_id && e.season != null && e.episode != null) openEpisode(e.tmdb_id, e.season, e.episode); else openTitle(e); }} style={{ width }}>
      <View style={[st.lposter, { width, height: width * 9 / 16 }]}>
        {source ? <Image source={source} style={StyleSheet.absoluteFill} contentFit="cover" /> : null}
        <View style={st.lshade} />
        <View style={st.lt}>
          <Text style={st.ltTitle} numberOfLines={1}>{e.title}</Text>
          {e.subtitle ? <Text style={st.ltSub} numberOfLines={1}>{e.subtitle}</Text> : null}
        </View>
        <View style={st.lplay}><Icon name="play" size={20} color="#0d0f14" /></View>
        {e.progress ? <View style={st.lbar}><View style={[st.lbarFill, { width: `${Math.max(3, e.progress)}%` }]} /></View> : null}
      </View>
    </Pressable>
  );
}

export function RowView({ row, onMore }: { row: PosterRow | { id: string; title: string; kicker?: string | null; subtitle?: string | null; style: "landscape"; items: UpNextItem[] }; onMore?: () => void }) {
  const w = usePosterWidth();
  const { width: screen } = useWindowDimensions();
  const landscape = row.style === "landscape";
  return (
    <View>
      <SectionTitle
        title={row.title}
        sub={row.subtitle}
        kicker={row.kicker}
        right={onMore ? (
          <Pressable onPress={() => { tap(); onMore(); }} style={st.more} accessibilityLabel={`See all: ${row.title}`}>
            <Icon name="right" size={22} />
          </Pressable>
        ) : null}
      />
      <FlatList
        horizontal
        showsHorizontalScrollIndicator={false}
        data={row.items as any[]}
        keyExtractor={(it: any, i) => (it.key || it.jellyfin_id || String(i))}
        contentContainerStyle={{ paddingHorizontal: gutter, gap: 12 }}
        renderItem={({ item }) => landscape
          ? <LandscapeCard e={item as UpNextItem} width={Math.min(360, screen * 0.8)} />
          : <PosterCard c={item as Card} width={w} />}
      />
    </View>
  );
}

/** An endless grid: load(page) -> {items, more}, cached per `queryKey`
 * (a new key starts over). `header` may read the first page. */
export function InfiniteGrid<P extends Pick<PageOut, "items" | "more"> & { note?: string | null }>({ load, header, empty, queryKey }: {
  load: (page: number) => Promise<P>; queryKey: unknown[]; empty?: string;
  header?: ReactElement | null | ((first: P | undefined) => ReactElement | null);
}) {
  const { width } = useWindowDimensions();
  const cols = width >= 700 ? Math.floor((width - gutter * 2) / 160) : 3;
  const cardW = (width - gutter * 2 - (cols - 1) * 10) / cols;
  const q = useInfiniteQuery({
    queryKey: ["grid", ...queryKey],
    queryFn: ({ pageParam }) => load(pageParam),
    initialPageParam: 1,
    getNextPageParam: (last, all) => (last.more && last.items.length > 0 ? all.length + 1 : undefined),
  });
  const items = useMemo(() => {
    const seen = new Set<string>();
    const out: Card[] = [];
    for (const page of q.data?.pages || []) for (const c of page.items) if (!seen.has(c.key)) { seen.add(c.key); out.push(c); }
    return out;
  }, [q.data]);
  const busy = q.isLoading || q.isFetchingNextPage;
  const error = q.error ? (q.error as Error).message : null;
  // Where the grid ends short of the list (TMDB serves only 500 pages), it says why.
  const last = q.data?.pages[q.data.pages.length - 1];
  const endNote = !q.hasNextPage && last?.note ? last.note : null;

  return (
    <FlatList
      key={cols}
      data={items}
      numColumns={cols}
      keyExtractor={(c) => c.key}
      ListHeaderComponent={typeof header === "function" ? header(q.data?.pages[0]) : header}
      columnWrapperStyle={{ gap: 10, paddingHorizontal: gutter }}
      contentContainerStyle={{ gap: 16, paddingBottom: 120 }}
      renderItem={({ item }) => <PosterCard c={item} width={cardW} />}
      onEndReachedThreshold={1.5}
      onEndReached={() => { if (q.hasNextPage && !q.isFetching && !q.isError) q.fetchNextPage(); }}
      refreshing={q.isRefetching && !q.isFetchingNextPage}
      onRefresh={() => q.refetch()}
      ListFooterComponent={busy ? <Loading /> : error ? <Text style={{ color: colors.danger, padding: gutter }}>{error}</Text>
        : endNote ? <Text style={{ color: colors.text3, padding: gutter }}>{endNote}</Text> : null}
      ListEmptyComponent={!busy && !error ? <Text style={{ color: colors.text3, textAlign: "center", padding: 40 }}>{empty || "Nothing here yet."}</Text> : null}
    />
  );
}

const st = StyleSheet.create({
  poster: { borderRadius: radius, overflow: "hidden", backgroundColor: colors.surface2, borderWidth: 1, borderColor: colors.line, justifyContent: "center" },
  noimg: { color: colors.text3, textAlign: "center", padding: 10, fontWeight: "600" },
  pill: { position: "absolute", bottom: 7, alignSelf: "center", backgroundColor: "rgba(10,12,17,0.8)", borderRadius: 999, paddingHorizontal: 9, paddingVertical: 3 },
  pillText: { color: "#fff", fontSize: 12, fontWeight: "700" },
  badge: { position: "absolute", top: 7, right: 7, width: 30, height: 30, borderRadius: 15, alignItems: "center", justifyContent: "center" },
  seen: { position: "absolute", top: 7, left: 7, height: 24, paddingHorizontal: 8, borderRadius: 999, backgroundColor: "rgba(10,12,17,0.82)", flexDirection: "row", alignItems: "center", gap: 4 },
  seenText: { color: colors.text2, fontSize: 11.5, fontWeight: "700" },
  cardTitle: { color: colors.text, marginTop: 8, fontSize: 14.5, fontWeight: "600", lineHeight: 18 },
  cardMeta: { color: colors.text3, fontSize: 13, marginTop: 2 },
  more: { width: 40, height: 40, borderRadius: 20, backgroundColor: "rgba(255,255,255,0.08)", alignItems: "center", justifyContent: "center" },
  lposter: { borderRadius: radius, overflow: "hidden", backgroundColor: colors.surface2 },
  lshade: { ...StyleSheet.absoluteFill, backgroundColor: "rgba(5,6,9,0.35)" },
  lt: { position: "absolute", left: 14, right: 60, bottom: 14 },
  ltTitle: { color: colors.text, fontSize: 18, fontWeight: "800" },
  ltSub: { color: colors.text2, fontSize: 14 },
  lplay: { position: "absolute", right: 12, bottom: 14, width: 44, height: 44, borderRadius: 22, backgroundColor: "#fff", alignItems: "center", justifyContent: "center" },
  lbar: { position: "absolute", left: 0, right: 0, bottom: 0, height: 4, backgroundColor: "rgba(255,255,255,0.18)" },
  lbarFill: { height: 4, backgroundColor: colors.accent },
});
