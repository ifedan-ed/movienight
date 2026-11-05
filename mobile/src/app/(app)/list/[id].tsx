// One list: Movies / Shows, sorting, star, show on Home, copy to your lists.
import { useState } from "react";
import { Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { router, useLocalSearchParams } from "expo-router";
import { SafeAreaView } from "react-native-safe-area-context";

import { InfiniteGrid } from "@/components/cards";
import { Icon } from "@/components/Icon";
import { Sheet } from "@/components/Sheet";
import { Chip, Segmented, tap, useToast } from "@/components/ui";
import { api } from "@/lib/api";
import { colors, gutter } from "@/lib/theme";
import type { ListOut } from "@/lib/types";

const SORTS: [string, string][] = [["list", "List order"], ["added", "Recently added"], ["title", "Title"], ["year", "Newest"], ["rating", "Highest rated"]];

export default function ListScreen() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const [media, setMedia] = useState<"" | "movie" | "tv">("");
  const [sort, setSort] = useState("list");
  const [sortOpen, setSortOpen] = useState(false);
  const qc = useQueryClient();
  const toast = useToast();
  const path = `/api/v1/lists/${encodeURIComponent(id)}`;
  const meta = useQuery({ queryKey: ["listmeta", id], queryFn: () => api.get<ListOut>(`${path}?page=1`) });
  const m = meta.data;

  const load = (page: number) => {
    const qs = new URLSearchParams({ page: String(page), sort });
    if (media) qs.set("media", media);
    return api.get<ListOut>(`${path}?${qs}`);
  };
  const flag = async (field: "starred" | "pinned") => {
    if (!m) return;
    try {
      const r = await api.patch<{ pinned: boolean; starred: boolean }>(path, { [field]: !m[field] });
      qc.setQueryData<ListOut>(["listmeta", id], (d) => d && { ...d, ...r });
      qc.invalidateQueries({ queryKey: ["lists"] });
      toast(field === "pinned" ? (r.pinned ? "It's on your Home now" : "Removed from Home") : (r.starred ? "Starred" : "Unstarred"));
    } catch (e: any) { toast(e.message, { error: true }); }
  };
  const copy = async () => {
    try {
      const r = await api.post<{ id: number }>(`${path}/copy`);
      qc.invalidateQueries({ queryKey: ["lists"] });
      toast("Copied to your lists");
      router.replace({ pathname: "/list/[id]", params: { id: String(r.id) } });
    } catch (e: any) { toast(e.message, { error: true }); }
  };
  const mixed = !!m && m.kind !== "smart" && m.movies > 0 && m.shows > 0;

  return (
    <SafeAreaView edges={["top"]} style={{ flex: 1, backgroundColor: colors.bg }}>
      <InfiniteGrid
        queryKey={["list", id, media, sort]}
        load={load}
        empty={media ? `No ${media === "tv" ? "shows" : "movies"} in this list` : "This list is empty"}
        header={
          <View style={{ marginBottom: 8 }}>
            <View style={st.head}>
              <Pressable onPress={() => router.back()} style={st.back}><Icon name="left" /></Pressable>
              <Text style={st.h1} numberOfLines={3}>{m?.name || "List"}</Text>
            </View>
            {m ? <Text style={st.sub}>{[m.group === "mine" ? (m.kind === "smart" ? "Smart list" : "Your list") : m.by, m.count != null ? `${m.count} titles` : null].filter(Boolean).join(" · ")}</Text> : null}
            {m?.not_shown ? (
              // MDBList items without a TMDB id can't be shown: say how many.
              <Text style={st.sub}>{`${m.not_shown} ${m.not_shown === 1 ? "title" : "titles"} of this MDBList list can't be shown here: MDBList has no TMDB id for ${m.not_shown === 1 ? "it" : "them"}.`}</Text>
            ) : null}
            {mixed ? <Segmented value={media} onChange={setMedia} options={[["", `All ${m!.count}`], ["movie", `Movies ${m!.movies}`], ["tv", `Shows ${m!.shows}`]]} /> : null}
            {m ? (
              <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ paddingHorizontal: gutter, gap: 8, paddingVertical: 4 }}>
                <Chip label={m.starred ? "Starred" : "Star"} icon={m.starred ? "starFill" : "star"} on={m.starred} onPress={() => flag("starred")} />
                <Chip label={m.pinned ? "On Home" : "Show on Home"} icon="home" on={m.pinned} onPress={() => flag("pinned")} />
                {m.kind !== "smart" ? <Chip label={SORTS.find(([v]) => v === sort)?.[1] || "Sort"} icon="sort" on={sort !== "list"} onPress={() => setSortOpen(true)} /> : null}
                {m.kind === "list" && !m.editable ? <Chip label="Copy to my lists" icon="copy" onPress={copy} /> : null}
              </ScrollView>
            ) : null}
          </View>
        }
      />
      <Sheet visible={sortOpen} onClose={() => setSortOpen(false)} title="Sort by">
        {SORTS.map(([v, label]) => (
          <Pressable key={v} onPress={() => { tap(); setSort(v); setSortOpen(false); }} style={st.opt}>
            <Text style={st.optText}>{label}</Text>
            {v === sort ? <Icon name="check" color={colors.accent} /> : null}
          </Pressable>
        ))}
      </Sheet>
    </SafeAreaView>
  );
}

const st = StyleSheet.create({
  head: { flexDirection: "row", alignItems: "center", gap: 12, paddingHorizontal: gutter, paddingTop: 10 },
  back: { width: 44, height: 44, borderRadius: 22, backgroundColor: "rgba(255,255,255,0.07)", borderWidth: 1, borderColor: colors.line, alignItems: "center", justifyContent: "center" },
  h1: { color: colors.text, fontSize: 24, fontWeight: "800", flex: 1 },
  sub: { color: colors.text3, fontSize: 14.5, paddingHorizontal: gutter, marginTop: 6 },
  opt: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", minHeight: 56, paddingHorizontal: 14, borderRadius: 14, backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line, marginBottom: 8 },
  optText: { color: colors.text, fontSize: 16, fontWeight: "600" },
});
