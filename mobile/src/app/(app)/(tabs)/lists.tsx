// Lists: yours (Watchlist, Favorites, Ratings, your own lists), then Movie Night
// and MDBList lists; filter by Starred / Yours / Movie Night / MDBList.
import { useState } from "react";
import { Pressable, RefreshControl, ScrollView, StyleSheet, Text, View } from "react-native";
import { Image } from "expo-image";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { router } from "expo-router";
import { SafeAreaView } from "react-native-safe-area-context";

import { Icon, type IconName } from "@/components/Icon";
import { Chip, ErrorBox, Loading, SectionTitle, tap, useToast } from "@/components/ui";
import { api } from "@/lib/api";
import { colors, gutter, img } from "@/lib/theme";
import type { ListSummary, ListsOut } from "@/lib/types";

type Filter = "all" | "starred" | "mine" | "shared" | "mdblist";
const FILTERS: [Filter, string][] = [["all", "All"], ["starred", "Starred"], ["mine", "Yours"], ["shared", "Shared"], ["mdblist", "MDBList"]];
const GROUP_LABEL: Record<string, string> = { mine: "Your lists", shared: "Shared lists", mdblist: "MDBList lists" };

function Collage({ posters }: { posters: string[] }) {
  return (
    <View style={{ flexDirection: "row", height: 118 }}>
      {(posters.length ? posters : ["", "", ""]).slice(0, 5).map((p, i) => (
        <View key={i} style={[st.cp, i > 0 && { marginLeft: -28 }]}>{p ? <Image source={img(p, "w185")} style={StyleSheet.absoluteFill} /> : null}</View>
      ))}
    </View>
  );
}

function ListCard({ l, onFlag }: { l: ListSummary; onFlag: (l: ListSummary, field: "starred" | "pinned") => void }) {
  const by = l.kind === "smart" ? "Smart list" : [l.group === "mine" ? null : l.by, l.count != null ? `${l.count} titles` : null].filter(Boolean).join(" · ");
  return (
    <Pressable onPress={() => router.push({ pathname: "/list/[id]", params: { id: String(l.id) } })} style={st.card}>
      <View style={st.top}>
        <View style={{ flex: 1 }}>
          <Text style={st.name} numberOfLines={2}>{l.name}</Text>
          <Text style={st.by}>{by}</Text>
        </View>
        <Pressable onPress={() => { tap(); onFlag(l, "starred"); }} style={[st.pin, l.starred && st.pinOn]} accessibilityLabel={l.starred ? "Unstar" : "Star"}>
          <Icon name={l.starred ? "starFill" : "star"} size={19} color={l.starred ? colors.accent : colors.text2} />
        </Pressable>
        <Pressable onPress={() => { tap(); onFlag(l, "pinned"); }} style={[st.pin, l.pinned && st.pinOn]} accessibilityLabel={l.pinned ? "Remove from Home" : "Show on Home"}>
          <Icon name="home" size={19} color={l.pinned ? colors.accent : colors.text2} />
        </Pressable>
      </View>
      <Collage posters={l.posters || []} />
    </Pressable>
  );
}

function Special({ name, sub, posters, icon, kind }: { name: string; sub: string; posters: string[]; icon: IconName; kind: string }) {
  return (
    <Pressable onPress={() => router.push({ pathname: "/collection/[kind]", params: { kind } })} style={st.card}>
      <View style={st.top}>
        <View style={{ flex: 1 }}><Text style={st.name}>{name}</Text><Text style={st.by}>{sub}</Text></View>
        <View style={st.pin}><Icon name={icon} size={19} color={colors.text2} /></View>
      </View>
      {posters.length ? <Collage posters={posters} /> : null}
    </Pressable>
  );
}

export default function Lists() {
  const [filter, setFilter] = useState<Filter>("all");
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: ["lists"], queryFn: () => api.get<ListsOut>("/api/v1/lists") });

  const flag = async (l: ListSummary, field: "starred" | "pinned") => {
    const next = !l[field];
    qc.setQueryData<ListsOut>(["lists"], (d) => d && { ...d, lists: d.lists.map((x) => (x.id === l.id ? { ...x, [field]: next } : x)) });
    try {
      await api.patch(`/api/v1/lists/${l.id}`, { [field]: next });
      toast(field === "pinned" ? (next ? `“${l.name}” is on Home now` : `Removed “${l.name}” from Home`) : (next ? `Starred “${l.name}”` : `Unstarred “${l.name}”`));
      if (field === "pinned") qc.invalidateQueries({ queryKey: ["home"] });
    } catch (e: any) {
      qc.invalidateQueries({ queryKey: ["lists"] });
      toast(e.message, { error: true });
    }
  };

  const d = q.data;
  const shown = (d?.lists || []).filter((l) => filter === "all" || (filter === "starred" ? l.starred : l.group === filter));
  const group = (g: string) => shown.filter((l) => l.group === g);

  return (
    <SafeAreaView edges={["top"]} style={{ flex: 1, backgroundColor: colors.bg }}>
      <ScrollView refreshControl={<RefreshControl refreshing={q.isRefetching} onRefresh={() => q.refetch()} tintColor={colors.accent} />} contentContainerStyle={{ paddingBottom: 60 }}>
        <View style={st.head}>
          <Text style={st.h1}>Lists</Text>
          <Pressable onPress={() => router.push("/mdblist")} style={st.iconbtn} accessibilityLabel="Find lists on MDBList"><Icon name="search" /></Pressable>
        </View>
        <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ paddingHorizontal: gutter, gap: 8, paddingVertical: 8 }}>
          {FILTERS.map(([id, label]) => <Chip key={id} label={label} icon={id === "starred" ? (filter === id ? "starFill" : "star") : undefined} on={filter === id} onPress={() => setFilter(id)} />)}
        </ScrollView>
        {q.isLoading ? <Loading /> : q.error ? <ErrorBox message={(q.error as Error).message} onRetry={() => q.refetch()} /> : d ? (
          <>
            {filter === "all" || filter === "mine" ? (
              <>
                <SectionTitle title="Your lists" />
                <View style={st.grid}>
                  <Special name="Watchlist" sub={`${d.watchlist.count} titles`} posters={d.watchlist.posters} icon="bookmark" kind="watchlist" />
                  <Special name="Favorites" sub={`${d.favorites.count} titles`} posters={d.favorites.posters} icon="heart" kind="favorites" />
                  <Special name="Ratings" sub={`${d.ratings.count} rated`} posters={d.ratings.posters} icon="starFill" kind="ratings" />
                  {group("mine").map((l) => <ListCard key={l.id} l={l} onFlag={flag} />)}
                </View>
              </>
            ) : group("mine").length ? (
              <><SectionTitle title={GROUP_LABEL.mine} /><View style={st.grid}>{group("mine").map((l) => <ListCard key={l.id} l={l} onFlag={flag} />)}</View></>
            ) : null}
            {["shared", "mdblist"].map((g) => group(g).length ? (
              <View key={g}>
                <SectionTitle title={GROUP_LABEL[g]} />
                <View style={st.grid}>{group(g).map((l) => <ListCard key={l.id} l={l} onFlag={flag} />)}</View>
              </View>
            ) : null)}
            {filter === "starred" && !shown.length ? <Text style={st.empty}>Tap the star on any list to keep it here.</Text> : null}
          </>
        ) : null}
      </ScrollView>
    </SafeAreaView>
  );
}

const st = StyleSheet.create({
  head: { flexDirection: "row", alignItems: "center", paddingHorizontal: gutter, paddingTop: 10 },
  h1: { color: colors.text, fontSize: 32, fontWeight: "800", flex: 1 },
  iconbtn: { width: 44, height: 44, borderRadius: 22, backgroundColor: "rgba(255,255,255,0.07)", borderWidth: 1, borderColor: colors.line, alignItems: "center", justifyContent: "center" },
  grid: { paddingHorizontal: gutter, gap: 12 },
  card: { backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line, borderRadius: 18, padding: 14, gap: 12 },
  top: { flexDirection: "row", alignItems: "flex-start", gap: 8 },
  name: { color: colors.text, fontSize: 18, fontWeight: "800" },
  by: { color: colors.text3, fontSize: 13.5, marginTop: 2 },
  pin: { height: 38, minWidth: 38, paddingHorizontal: 9, borderRadius: 10, backgroundColor: "rgba(255,255,255,0.07)", borderWidth: 1, borderColor: colors.line, alignItems: "center", justifyContent: "center" },
  pinOn: { backgroundColor: colors.accentSoft, borderColor: "rgba(245,183,59,0.45)" },
  cp: { width: 78, height: 117, borderRadius: 10, overflow: "hidden", backgroundColor: colors.surface2, borderWidth: 1, borderColor: colors.line2 },
  empty: { color: colors.text3, textAlign: "center", padding: 30, fontSize: 15 },
});
