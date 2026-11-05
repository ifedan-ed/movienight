// Search: your library and every rated title on TMDB (by meaning and
// keywords, typo-tolerant), whichever holds the best match first; then people.
import { useEffect, useState } from "react";
import { Pressable, ScrollView, StyleSheet, Text, TextInput, useWindowDimensions, View } from "react-native";
import { Image } from "expo-image";
import { useQuery } from "@tanstack/react-query";
import { router } from "expo-router";
import { SafeAreaView } from "react-native-safe-area-context";

import { openTitle, PosterCard } from "@/components/cards";
import { Icon } from "@/components/Icon";
import { Button, Empty, ErrorBox, Loading, SectionTitle, Segmented } from "@/components/ui";
import { api } from "@/lib/api";
import { caret, colors, gutter, img } from "@/lib/theme";
import type { AiGuessOut, Card, MeOut, SearchOut } from "@/lib/types";

const IDEAS = ["a heist that goes wrong", "80s comedies", "Tom Hanks", "space survival", "courtroom drama"];

export default function Search() {
  const [text, setText] = useState("");
  const [q, setQ] = useState("");
  const [media, setMedia] = useState<"" | "movie" | "tv">("");
  const { width } = useWindowDimensions();
  // The install's search length (SEARCH_MAX_CHARS, a technical limit): the
  // box counts down to it, and a longer search is refused in the server's
  // own words (shown below), never cut.
  const me = useQuery({ queryKey: ["me"], queryFn: () => api.get<MeOut>("/api/v1/me") });
  const maxChars = me.data?.limits?.search_max_chars ?? 200;
  const cols = width >= 700 ? 5 : 3;
  const cardW = (width - gutter * 2 - (cols - 1) * 10) / cols;
  // "Show more" asks for the next page of the same search (each page holds
  // the matches before it too); everything the server sends is shown.
  // A new search (other words, or Movies/Shows) starts again at page 1.
  const [paged, setPaged] = useState({ key: "", page: 1 });
  const searchKey = `${media}|${q}`;
  const page = paged.key === searchKey ? paged.page : 1;
  useEffect(() => { const t = setTimeout(() => setQ(text.trim()), 380); return () => clearTimeout(t); }, [text]);
  const res = useQuery({
    queryKey: ["search", q, media, page],
    queryFn: () => api.get<SearchOut>(`/api/v1/search?q=${encodeURIComponent(q)}${media ? `&media=${media}` : ""}${page > 1 ? `&page=${page}` : ""}`),
    enabled: q.length > 0,
    // While the next page loads, the one before stays on screen.
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[1] === q && prevQuery?.queryKey[2] === media ? prev : undefined),
  });
  const more = (what: "library" | "tmdb" | "people") => res.data?.more?.[what] ? (
    <Button small kind="quiet" label="Show more" busy={res.isPlaceholderData} onPress={() => setPaged({ key: searchKey, page: page + 1 })} style={{ marginHorizontal: gutter, marginTop: 10, alignSelf: "flex-start" }} />
  ) : null;
  // A described plot: the AI's best guess, shown on top when it answers
  // (the model may think for minutes, as long as the server waits for it).
  // A real answer is kept a day; a failed or empty one isn't, so the next
  // search asks again (the server keeps those a minute only).
  const ai = useQuery({ queryKey: ["search-ai", q, media], queryFn: () => api.get<AiGuessOut>(`/api/v1/search/ai?q=${encodeURIComponent(q)}${media ? `&media=${media}` : ""}`, 190_000),
    enabled: !!res.data?.ai, retry: false, staleTime: (query) => (query.state.data?.matches.length ? 86_400_000 : 0) });
  const row = (c: Card, why?: string | null) => (
    <Pressable key={c.key} onPress={() => openTitle(c)} style={st.row}>
      <View style={st.thumb}>{c.poster ? <Image source={img(c.poster, "w154")} style={StyleSheet.absoluteFill} /> : null}</View>
      <View style={{ flex: 1 }}>
        <Text style={st.rowTitle}>{c.title}</Text>
        <Text style={st.rowMeta}>{[c.year, c.media_type === "tv" ? "Show" : "Movie", c.vote ? `★\u00a0${c.vote}` : null].filter(Boolean).join(" · ")}</Text>
        {why ? <Text style={st.why}>{why}</Text> : null}
      </View>
      <Icon name="right" color={colors.text3} />
    </Pressable>
  );

  return (
    <SafeAreaView edges={["top"]} style={{ flex: 1, backgroundColor: colors.bg }}>
      <Text style={st.h1}>Search</Text>
      <View style={st.box}>
        <Icon name="search" color={colors.text3} />
        <TextInput {...caret} testID="search-box" style={st.input} value={text} onChangeText={setText} placeholder="Titles, people, or describe a movie" placeholderTextColor={colors.text3}
          autoCapitalize="none" autoCorrect={false} returnKeyType="search" clearButtonMode="while-editing" />
      </View>
      {text.length > maxChars * 0.9 ? (
        <Text style={[st.count, text.length > maxChars && { color: colors.danger }]}>
          {text.length > maxChars ? `${(text.length - maxChars).toLocaleString()} characters too long: a search is at most ${maxChars.toLocaleString()}` : `${(maxChars - text.length).toLocaleString()} characters left`}
        </Text>
      ) : null}
      <Segmented value={media} onChange={setMedia} options={[["", "All"], ["movie", "Movies"], ["tv", "Shows"]]} />
      <ScrollView contentContainerStyle={{ paddingBottom: 80 }} keyboardShouldPersistTaps="handled">
        {!q ? (
          <View style={{ padding: gutter, gap: 8 }}>
            <Text style={{ color: colors.text3, fontWeight: "700" }}>TRY DESCRIBING IT</Text>
            {IDEAS.map((idea) => <Pressable key={idea} onPress={() => setText(idea)}><Text style={st.idea}>{idea}</Text></Pressable>)}
          </View>
        ) : res.isLoading ? <Loading /> : res.error && !res.data ? (
          // Refused or failed (a search over SEARCH_MAX_CHARS, no connection): say why.
          <ErrorBox message={(res.error as Error).message} onRetry={() => res.refetch()} />
        ) : res.data ? (
          <>
            {res.data.ai ? (
              <View>
                <SectionTitle title="Best guess" />
                {ai.isLoading ? <Loading /> : ai.data?.matches.length ? ai.data.matches.map((m) => row(m.card, m.why)) : (
                  // No guess: say why rather than hide it.
                  <Text style={st.guessNote}>{ai.isError ? "The AI didn't answer this time. Search again to ask it again." : ai.data?.note || "The AI had no guess for this description."}</Text>
                )}
              </View>
            ) : null}
            {res.data.corrected ? (
              <Text style={st.fixed}>Showing results for <Text style={{ color: colors.text, fontWeight: "700" }}>{res.data.corrected}</Text></Text>
            ) : null}
            {(res.data.first === "tmdb" ? ["tmdb", "library"] : ["library", "tmdb"]).map((section) => section === "library" ? (
              res.data!.library.length ? (
                <View key="library">
                  <SectionTitle title="In your library" />
                  <View style={st.grid}>{res.data!.library.map((c) => <PosterCard key={c.key} c={c} width={cardW} />)}</View>
                  {more("library")}
                </View>
              ) : null
            ) : res.data!.tmdb.length ? (
              <View key="tmdb">
                <SectionTitle title={res.data!.library.length ? "Not in your library" : "Results"} />
                {res.data!.tmdb.map((c) => row(c))}
                {more("tmdb")}
              </View>
            ) : null)}
            {res.data.people.length ? (
              <>
                <SectionTitle title="People" />
                <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ paddingHorizontal: gutter, gap: 14 }}>
                  {res.data.people.map((p) => (
                    <Pressable key={p.id} style={{ width: 84, alignItems: "center" }} onPress={() => p.id && router.push({ pathname: "/person/[id]", params: { id: String(p.id) } })}>
                      <View style={st.photo}>{p.photo ? <Image source={img(p.photo, "w185")} style={StyleSheet.absoluteFill} /> : null}</View>
                      <Text style={{ color: colors.text, fontSize: 13.5, fontWeight: "700", textAlign: "center" }} numberOfLines={2}>{p.name}</Text>
                    </Pressable>
                  ))}
                </ScrollView>
                {more("people")}
              </>
            ) : null}
            {res.data.note ? <Text style={st.guessNote}>{res.data.note}</Text> : null}
            {!res.data.library.length && !res.data.tmdb.length && !res.data.people.length ? <Empty title="Nothing found" text="Try other words, or describe the story." /> : null}
          </>
        ) : null}
      </ScrollView>
    </SafeAreaView>
  );
}

const st = StyleSheet.create({
  h1: { color: colors.text, fontSize: 32, fontWeight: "800", paddingHorizontal: gutter, paddingTop: 10 },
  box: { flexDirection: "row", alignItems: "center", gap: 10, marginHorizontal: gutter, marginTop: 8, height: 54, borderRadius: 16, backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line2, paddingHorizontal: 14 },
  input: { flex: 1, color: colors.text, fontSize: 17, height: 52 },
  idea: { color: colors.accent, fontSize: 16, paddingVertical: 6 },
  fixed: { color: colors.text3, fontSize: 14.5, paddingHorizontal: gutter, paddingTop: 10 },
  why: { color: colors.text2, fontSize: 13.5, fontStyle: "italic", marginTop: 2 },
  count: { color: colors.text3, fontSize: 13.5, paddingHorizontal: gutter, paddingTop: 6 },
  guessNote: { color: colors.text3, fontSize: 14.5, paddingHorizontal: gutter, paddingBottom: 6 },
  grid: { flexDirection: "row", flexWrap: "wrap", gap: 10, paddingHorizontal: gutter, rowGap: 16 },
  row: { flexDirection: "row", alignItems: "center", gap: 12, paddingVertical: 10, marginHorizontal: gutter, borderBottomWidth: 1, borderBottomColor: colors.line },
  thumb: { width: 54, height: 81, borderRadius: 8, overflow: "hidden", backgroundColor: colors.surface2 },
  rowTitle: { color: colors.text, fontSize: 16.5, fontWeight: "700" },
  rowMeta: { color: colors.text3, fontSize: 14 },
  photo: { width: 76, height: 76, borderRadius: 38, overflow: "hidden", backgroundColor: colors.surface2, marginBottom: 6 },
});
