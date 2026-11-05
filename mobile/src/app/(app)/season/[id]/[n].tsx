// One season: every episode with a tick (marks Jellyfin played too), a
// heart (likes that episode) and stars (rates it, 1 to 10 like a title) —
// that episode only, not the show — Play for episodes in the library, and
// Request for a season that isn't there. The `episode` param (from Recently
// watched, Continue watching and Up next) opens that episode and scrolls to it.
import { useEffect, useRef, useState } from "react";
import { FlatList, Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { Image } from "expo-image";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { router, useLocalSearchParams } from "expo-router";
import { SafeAreaView } from "react-native-safe-area-context";

import { Icon } from "@/components/Icon";
import { EpisodeMarks, PlaySheet } from "@/components/TitleActions";
import { Chip, ErrorBox, Loading, tap, useToast } from "@/components/ui";
import { api } from "@/lib/api";
import { send } from "@/lib/outbox";
import { colors, gutter, img } from "@/lib/theme";
import type { Episode, MeOut, SeasonOut, TitleDetail } from "@/lib/types";

export default function SeasonScreen() {
  const { id, n, episode } = useLocalSearchParams<{ id: string; n: string; episode?: string }>();
  const number = Number(n);
  const focus = episode ? Number(episode) : null;
  const path = `/api/v1/title/tv/${encodeURIComponent(id)}`;
  const qc = useQueryClient();
  const toast = useToast();
  const show = useQuery({ queryKey: ["title", "tv", id], queryFn: () => api.get<TitleDetail>(path) });
  // How many episodes one call may carry (BATCH_MAX_ITEMS, from /api/v1/me).
  const me = useQuery({ queryKey: ["me"], queryFn: () => api.get<MeOut>("/api/v1/me") });
  const per = me.data?.limits?.batch_max_items ?? 500;
  const season = useQuery({ queryKey: ["season", id, number], queryFn: () => api.get<SeasonOut>(`${path}/season/${number}`) });
  const [play, setPlay] = useState<number | null>(null);
  const [open, setOpen] = useState<number | null>(focus);
  const list = useRef<FlatList<Episode>>(null);
  const scrolled = useRef(false);
  const [requested, setRequested] = useState(false);

  const mark = async (eps: Episode[], watched: boolean) => {
    const key = ["season", id, number];
    qc.setQueryData<SeasonOut>(key, (s) => s && { ...s, episodes: s.episodes.map((e) => (eps.some((x) => x.number === e.number) ? { ...e, watched: watched ? Math.max(1, e.watched || 0) : 0 } : e)) });
    // BATCH_MAX_ITEMS episodes a call (the server's request size), as many calls as the season needs.
    const calls = [];
    for (let i = 0; i < eps.length; i += per) {
      calls.push(send("POST", `${path}/episodes`, { episodes: eps.slice(i, i + per).map((e) => ({ season: number, episode: e.number })), watched }));
    }
    Promise.all(calls).then(() => {
      qc.invalidateQueries({ queryKey: ["title", "tv", id] });
      qc.invalidateQueries({ queryKey: ["next", id] });
    });
    if (eps.length > 1) toast(watched ? `Marked ${eps.length} episodes as watched` : `Unmarked ${eps.length} episodes`);
  };

  // The heart and the stars (EpisodeMarks send them through the outbox):
  // shown at once, kept in this season's cached page.
  const patch = (e: Episode, p: { favorite?: boolean; rating?: number | null }) => {
    qc.setQueryData<SeasonOut>(["season", id, number], (s) => s && { ...s, episodes: s.episodes.map((x) => (x.number === e.number ? { ...x, ...p } : x)) });
  };

  const s = season.data;
  const eps = s?.episodes || [];
  const aired = eps.filter((e) => e.aired);
  const allSeen = aired.length > 0 && aired.every((e) => e.watched);
  const info = show.data?.seasons.find((x) => x.number === number);
  const anyInLib = eps.some((e) => e.in_library);
  const focusIndex = focus != null ? eps.findIndex((e) => e.number === focus) : -1;

  // The episode asked for: scrolled to once its season has loaded.
  useEffect(() => {
    if (scrolled.current || focusIndex < 0) return;
    scrolled.current = true;
    const t = setTimeout(() => list.current?.scrollToIndex({ index: focusIndex, viewPosition: 0.3, animated: false }), 50);
    return () => clearTimeout(t);
  }, [focusIndex]);

  return (
    <SafeAreaView edges={["top"]} style={{ flex: 1, backgroundColor: colors.bg }}>
      <View style={st.head}>
        <Pressable onPress={() => router.back()} style={st.back}><Icon name="left" /></Pressable>
        <View style={{ flex: 1 }}>
          <Text style={st.h1}>{s?.name || `Season ${number}`}</Text>
          {/* The show's own page (its heart and rating are the whole show's). */}
          <Pressable onPress={() => router.push({ pathname: "/title/[media]/[id]", params: { media: "tv", id: String(id) } })} accessibilityRole="link" testID="season-show-link">
            <Text style={st.sub}>{show.data?.title}</Text>
          </Pressable>
        </View>
      </View>
      <ScrollView horizontal showsHorizontalScrollIndicator={false} style={{ flexGrow: 0, flexShrink: 0 }} contentContainerStyle={{ paddingHorizontal: gutter, gap: 8, paddingVertical: 8 }}>
        {(show.data?.seasons || []).map((x) => (
          <Chip key={x.number} label={x.number === 0 ? "Specials" : `Season ${x.number}`} on={x.number === number}
            onPress={() => router.replace({ pathname: "/season/[id]/[n]", params: { id, n: String(x.number) } })} />
        ))}
      </ScrollView>
      {season.isLoading ? <Loading /> : season.error ? <ErrorBox message={(season.error as Error).message} onRetry={() => season.refetch()} /> : (
        <FlatList
          ref={list}
          data={eps}
          // Episodes not laid out yet: jump near by the average height, then try again.
          onScrollToIndexFailed={(info) => {
            list.current?.scrollToOffset({ offset: info.averageItemLength * info.index, animated: false });
            setTimeout(() => list.current?.scrollToIndex({ index: info.index, viewPosition: 0.3, animated: false }), 120);
          }}
          keyExtractor={(e) => String(e.number)}
          contentContainerStyle={{ padding: gutter, gap: 10, paddingBottom: 80 }}
          ListHeaderComponent={
            <View style={st.tools}>
              <Text style={st.count}>{eps.filter((e) => e.watched).length} of {eps.length} watched</Text>
              {aired.length ? <Chip label={allSeen ? "Season watched" : "Mark season watched"} icon={allSeen ? "tickFill" : "tick"} on={allSeen}
                onPress={() => mark(allSeen ? aired : aired.filter((e) => !e.watched), !allSeen)} /> : null}
              {!anyInLib && info?.status !== "available" ? (
                requested || info?.status ? <Chip label="Requested" icon="clock" on /> : (
                  <Chip label={`Request season ${number}`} icon="download" onPress={async () => {
                    try {
                      const r = await api.post<{ already: boolean }>(`${path}/request`, { seasons: [number] });
                      setRequested(true);
                      toast(r.already ? "Already requested" : `Requested season ${number}`);
                    } catch (e: any) { toast(e.message, { error: true }); }
                  }} />
                )
              ) : null}
            </View>
          }
          renderItem={({ item: e }) => (
            // Not one accessibility element: on iOS a pressable card hides the buttons
            // inside it (Play, the tick, the heart) from VoiceOver and from the device
            // tests. The title carries the card's own action, showing the summary.
            <Pressable accessible={false} onPress={() => setOpen(open === e.number ? null : e.number)} style={[st.ep, !e.aired && { opacity: 0.62 }]}>
              <View style={st.still}>
                {e.still ? <Image source={img(e.still, "w300")} style={StyleSheet.absoluteFill} /> : null}
                {e.in_library ? (
                  <Pressable onPress={() => { tap(); setPlay(e.number); }} style={st.play} accessibilityLabel={`Play episode ${e.number}`}>
                    <Icon name="play" size={18} color="#0d0f14" />
                  </Pressable>
                ) : null}
              </View>
              <View style={{ flex: 1 }} accessible accessibilityRole="button"
                accessibilityHint={e.overview ? (open === e.number ? "Hides the summary" : "Shows the summary") : undefined}
                onAccessibilityTap={() => setOpen(open === e.number ? null : e.number)}>
                <Text style={[st.epTitle, e.watched ? { color: colors.text2 } : null]}>{e.number}. {e.name}</Text>
                <Text style={st.epMeta}>{[e.air_date ? (e.aired ? e.air_date : `Airs ${e.air_date}`) : "Date to come", e.runtime ? `${e.runtime}\u00a0min` : null, e.vote ? `★\u00a0${e.vote}` : null].filter(Boolean).join(" · ")}</Text>
                {open === e.number && e.overview ? <Text style={st.ov}>{e.overview}</Text> : null}
              </View>
              <View>
                <Pressable disabled={!e.aired && !e.watched} onPress={() => { tap(); mark([e], !e.watched); }} style={st.tick} testID={`episode-tick-${e.number}`}
                  accessibilityLabel={e.watched ? `Mark episode ${e.number} unwatched` : `Mark episode ${e.number} watched`}>
                  <Icon name={e.watched ? "tickFill" : "tick"} size={30} color={e.watched ? colors.ok : colors.text3} />
                </Pressable>
                <EpisodeMarks showId={Number(id)} showTitle={show.data?.title} season={number} e={e} onChange={(p) => patch(e, p)} />
              </View>
            </Pressable>
          )}
        />
      )}
      {play != null && show.data ? (
        <PlaySheet t={{ key: show.data.key, media_type: "tv", tmdb_id: show.data.tmdb_id, title: show.data.title }} season={number} episode={play} onClose={() => setPlay(null)} />
      ) : null}
    </SafeAreaView>
  );
}

const st = StyleSheet.create({
  head: { flexDirection: "row", alignItems: "center", gap: 12, paddingHorizontal: gutter, paddingTop: 10 },
  back: { width: 44, height: 44, borderRadius: 22, backgroundColor: "rgba(255,255,255,0.07)", borderWidth: 1, borderColor: colors.line, alignItems: "center", justifyContent: "center" },
  h1: { color: colors.text, fontSize: 24, fontWeight: "800" },
  sub: { color: colors.text3, fontSize: 14 },
  tools: { flexDirection: "row", flexWrap: "wrap", alignItems: "center", gap: 8, marginBottom: 4 },
  count: { color: colors.text3, fontSize: 14, fontWeight: "600", marginRight: "auto" },
  ep: { flexDirection: "row", gap: 12, alignItems: "flex-start", backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line, borderRadius: 15, padding: 10 },
  still: { width: 120, height: 68, borderRadius: 10, overflow: "hidden", backgroundColor: colors.surface2 },
  play: { position: "absolute", right: 6, bottom: 6, width: 34, height: 34, borderRadius: 17, backgroundColor: "#fff", alignItems: "center", justifyContent: "center" },
  epTitle: { color: colors.text, fontSize: 15.5, fontWeight: "700" },
  epMeta: { color: colors.text3, fontSize: 13.5, marginTop: 2 },
  ov: { color: colors.text2, fontSize: 14, lineHeight: 20, marginTop: 6 },
  tick: { width: 44, height: 44, alignItems: "center", justifyContent: "center" },
});
