// Discover: browse TMDB by movies/shows, genre and sort.
import { useState } from "react";
import { ScrollView, Text, View } from "react-native";
import { useQuery } from "@tanstack/react-query";
import { SafeAreaView } from "react-native-safe-area-context";

import { InfiniteGrid } from "@/components/cards";
import { Chip, Segmented } from "@/components/ui";
import { api } from "@/lib/api";
import { colors, gutter } from "@/lib/theme";
import type { PageOut } from "@/lib/types";

const SORTS: [string, string][] = [["popularity.desc", "Popular"], ["vote_average.desc", "Top rated"], ["primary_release_date.desc", "Newest"]];

export default function Discover() {
  const [media, setMedia] = useState<"movie" | "tv">("movie");
  const [genre, setGenre] = useState<string>("");
  const [sort, setSort] = useState("popularity.desc");
  const genres = useQuery({ queryKey: ["genres", media], queryFn: () => api.get<{ genres: { id: number; name: string }[] }>(`/api/v1/genres?media=${media}`), staleTime: 3_600_000 });
  const load = (page: number) => {
    const qs = new URLSearchParams({ media, page: String(page), sort });
    if (genre) qs.set("genre", genre);
    return api.get<PageOut>(`/api/v1/discover?${qs}`);
  };
  return (
    <SafeAreaView edges={["top"]} style={{ flex: 1, backgroundColor: colors.bg }}>
      <InfiniteGrid
        queryKey={["discover", media, genre, sort]}
        load={load}
        empty="Nothing matches these filters."
        header={
          <View style={{ marginBottom: 6 }}>
            <Text style={{ color: colors.text, fontSize: 32, fontWeight: "800", paddingHorizontal: gutter, paddingTop: 10 }}>Discover</Text>
            <Segmented value={media} onChange={(m) => { setMedia(m); setGenre(""); }} options={[["movie", "Movies"], ["tv", "Shows"]]} />
            <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ paddingHorizontal: gutter, gap: 8, paddingBottom: 6 }}>
              {SORTS.map(([v, label]) => <Chip key={v} label={label} on={sort === v} onPress={() => setSort(v)} />)}
            </ScrollView>
            <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ paddingHorizontal: gutter, gap: 8, paddingBottom: 8 }}>
              <Chip label="All genres" on={!genre} onPress={() => setGenre("")} />
              {(genres.data?.genres || []).map((g) => <Chip key={g.id} label={g.name} on={genre === String(g.id)} onPress={() => setGenre(String(g.id))} />)}
            </ScrollView>
          </View>
        }
      />
    </SafeAreaView>
  );
}
