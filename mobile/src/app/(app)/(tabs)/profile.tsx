// Profile: your numbers, recently watched (an episode opens that episode),
// connections (Jellyfin, players, emails, signed-in devices) and signing
// this phone out.
import { Alert, Pressable, RefreshControl, ScrollView, StyleSheet, Text, View } from "react-native";
import { useQuery } from "@tanstack/react-query";
import { router } from "expo-router";
import { SafeAreaView } from "react-native-safe-area-context";

import { PosterCard, usePosterWidth } from "@/components/cards";
import { Icon, type IconName } from "@/components/Icon";
import { Button, ErrorBox, Loading, SectionTitle } from "@/components/ui";
import { api } from "@/lib/api";
import { flush, waiting } from "@/lib/outbox";
import { useSession } from "@/lib/session";
import { colors, gutter } from "@/lib/theme";
import type { HistoryOut, MeOut } from "@/lib/types";

function Row({ icon, title, sub, onPress }: { icon: IconName; title: string; sub?: string; onPress: () => void }) {
  return (
    <Pressable onPress={onPress} style={st.row}>
      <View style={st.ic}><Icon name={icon} color={colors.accent} /></View>
      <View style={{ flex: 1 }}>
        <Text style={st.rowTitle}>{title}</Text>
        {sub ? <Text style={st.rowSub}>{sub}</Text> : null}
      </View>
      <Icon name="right" color={colors.text3} />
    </Pressable>
  );
}

export default function Profile() {
  const { signOut, server } = useSession();
  // Changes still waiting on this phone go first; any that can't (no
  // connection) would be lost with the sign-out, so ask before.
  const askSignOut = async () => {
    await flush();
    const n = await waiting();
    if (!n) return signOut();
    Alert.alert("Sign out of this phone?",
      `${n} ${n === 1 ? "change" : "changes"} made on this phone (ratings, ticks, lists, ...) ${n === 1 ? "hasn't" : "haven't"} reached Movie Night yet. Signing out now loses ${n === 1 ? "it" : "them"}; stay signed in and ${n === 1 ? "it goes" : "they go"} as soon as the phone is online.`,
      [{ text: "Stay signed in", style: "cancel" }, { text: "Sign out anyway", style: "destructive", onPress: () => { signOut(); } }]);
  };
  const me = useQuery({ queryKey: ["me"], queryFn: () => api.get<MeOut>("/api/v1/me") });
  const hist = useQuery({ queryKey: ["history", 1], queryFn: () => api.get<HistoryOut>("/api/v1/history?page=1") });
  const w = usePosterWidth();
  const m = me.data;
  const s = m?.stats;
  const stat = (n: number | undefined, label: string) => (
    <View style={st.stat}><Text style={st.statNum}>{(n || 0).toLocaleString()}</Text><Text style={st.statLabel}>{label}</Text></View>
  );
  return (
    <SafeAreaView edges={["top"]} style={{ flex: 1, backgroundColor: colors.bg }}>
      <ScrollView refreshControl={<RefreshControl refreshing={me.isRefetching} onRefresh={() => { me.refetch(); hist.refetch(); }} tintColor={colors.accent} />} contentContainerStyle={{ paddingBottom: 60 }}>
        {me.isLoading ? <Loading /> : me.error ? <ErrorBox message={(me.error as Error).message} onRetry={() => me.refetch()} /> : m && s ? (
          <>
            <View style={st.head}>
              <View style={st.avatar}><Text style={st.avatarText}>{(m.display_name || m.user).slice(0, 1).toUpperCase()}</Text></View>
              <View style={{ flex: 1 }}>
                <Text style={st.h1} numberOfLines={1}>{m.display_name || m.user}</Text>
                <Text style={st.sub} numberOfLines={1}>{server.replace(/^https?:\/\//, "")}</Text>
              </View>
            </View>
            {!m.jellyfin.can_play ? (
              <Pressable onPress={() => router.push("/settings/jellyfin")} style={st.cta}>
                <Icon name="plug" color={colors.accent} />
                <Text style={st.ctaText}><Text style={{ color: colors.accent, fontWeight: "800" }}>Connect Jellyfin</Text> so Play opens VLC or Infuse with subtitles.</Text>
              </Pressable>
            ) : null}
            {(m.admin_notices ?? []).map((n) => (
              // Admins only: what Movie Night couldn't do by itself (a library sync that didn't finish, ...).
              <View key={n} style={st.notice}><Text style={st.noticeText}><Text style={{ fontWeight: "800" }}>For admins: </Text>{n}</Text></View>
            ))}
            {m.jellyfin.plays_notice ? (
              // Yours: your Jellyfin plays couldn't be read, or some have no date (not added).
              <Pressable onPress={() => router.push("/settings/jellyfin")} style={st.notice}><Text style={st.noticeText}>{m.jellyfin.plays_notice}</Text></Pressable>
            ) : null}
            <View style={st.stats}>
              {stat(s.movies, "Movies watched")}{stat(s.hours, "Hours watched")}{stat(s.episodes, "Episodes")}
              {stat(s.this_year, `Movies in ${new Date().getFullYear()}`)}{stat(s.ratings, "Ratings")}{stat(s.watchlist, "On watchlist")}
            </View>
            {hist.data?.items.length ? (
              <>
                <SectionTitle title="Recently watched" />
                <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ paddingHorizontal: gutter, gap: 12 }}>
                  {hist.data.items.filter((x) => x.card).slice(0, 20).map((x) => {
                    // An episode opens that episode (its season page), with its own
                    // rating and heart in the corner; a film opens its page.
                    const ep = x.media_type === "episode" && x.season != null && x.episode != null
                      ? { season: x.season, episode: x.episode, favorite: x.episode_favorite, rating: x.episode_rating } : null;
                    return (
                      <PosterCard key={`${x.ids[0]}`} c={x.card!} width={w} episode={ep}
                        testID={ep ? `recent-episode-${x.card!.tmdb_id}-${ep.season}-${ep.episode}` : undefined}
                        meta={x.episode != null ? `S${x.season}·E${x.episode}` : new Date(x.watched_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })} />
                    );
                  })}
                </ScrollView>
              </>
            ) : null}
            <SectionTitle title="Connections" />
            <View style={{ paddingHorizontal: gutter }}>
              <Row icon="plug" title="Jellyfin" sub={m.jellyfin.can_play ? `Connected${m.jellyfin.user_name ? ` as ${m.jellyfin.user_name}` : ""}` : m.jellyfin.linked ? "Plays come in · sign in to play" : "Not connected"} onPress={() => router.push("/settings/jellyfin")} />
              <Row icon="play" title="Players and subtitles" sub="VLC, Infuse, Outplayer or your own" onPress={() => router.push("/settings/players")} />
              <Row icon="mail" title="Emails" sub="Weekly digest, month and year in review: on or off" onPress={() => router.push("/settings/emails")} />
              <Row icon="phone" title="Signed-in devices" sub="Phones and tablets using this account" onPress={() => router.push("/settings/devices")} />
            </View>
            <View style={{ padding: gutter, gap: 10 }}>
              <Button kind="danger" label="Sign out of this phone" onPress={askSignOut} />
            </View>
          </>
        ) : null}
      </ScrollView>
    </SafeAreaView>
  );
}

const st = StyleSheet.create({
  head: { flexDirection: "row", alignItems: "center", gap: 12, padding: gutter, paddingTop: 14 },
  avatar: { width: 64, height: 64, borderRadius: 32, backgroundColor: colors.accent, alignItems: "center", justifyContent: "center" },
  avatarText: { color: colors.accentInk, fontSize: 28, fontWeight: "800" },
  h1: { color: colors.text, fontSize: 26, fontWeight: "800" },
  sub: { color: colors.text3, fontSize: 14 },
  cta: { flexDirection: "row", gap: 12, alignItems: "center", marginHorizontal: gutter, marginBottom: 8, padding: 14, borderRadius: 14, backgroundColor: "rgba(245,183,59,0.08)", borderWidth: 1, borderColor: "rgba(245,183,59,0.25)" },
  ctaText: { color: "#f3d79a", fontSize: 14.5, flex: 1 },
  notice: { marginHorizontal: gutter, marginBottom: 8, padding: 14, borderRadius: 14, backgroundColor: "rgba(245,183,59,0.08)", borderWidth: 1, borderColor: "rgba(245,183,59,0.25)" },
  noticeText: { color: "#f3d79a", fontSize: 14.5 },
  stats: { flexDirection: "row", flexWrap: "wrap", gap: 10, paddingHorizontal: gutter },
  stat: { width: "31%", flexGrow: 1, backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line, borderRadius: 16, padding: 12 },
  statNum: { color: colors.text, fontSize: 25, fontWeight: "800" },
  statLabel: { color: colors.text3, fontSize: 13, fontWeight: "600" },
  row: { flexDirection: "row", alignItems: "center", gap: 12, paddingVertical: 12, borderBottomWidth: 1, borderBottomColor: colors.line, minHeight: 68 },
  ic: { width: 46, height: 46, borderRadius: 12, backgroundColor: colors.surface2, alignItems: "center", justifyContent: "center" },
  rowTitle: { color: colors.text, fontSize: 16.5, fontWeight: "700" },
  rowSub: { color: colors.text3, fontSize: 14 },
});
