// A title: hero, scores (Movie Night, TMDB, IMDb, Rotten Tomatoes, ...), Play /
// Request, the five actions, ratings and comments, details, seasons, cast,
// collection and "More like this".
import { useEffect, useState } from "react";
import { FlatList, Linking, Pressable, ScrollView, StyleSheet, Text, useWindowDimensions, View } from "react-native";
import { Image } from "expo-image";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { router, useLocalSearchParams } from "expo-router";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { openEpisode, RowView } from "@/components/cards";
import { Icon } from "@/components/Icon";
import { ActionTiles, EpisodeMarks, ListsSheet, PrimaryButton, RateSheet, type T, WatchedSheet } from "@/components/TitleActions";
import { Button, ErrorBox, Loading, SectionTitle, tap } from "@/components/ui";
import { api } from "@/lib/api";
import { colors, gutter, img } from "@/lib/theme";
import { remember } from "@/lib/titleState";
import type { NextEpisodeOut, RelatedOut, Scores, TitleDetail } from "@/lib/types";

function fmtDate(iso?: string | null) {
  if (!iso) return "";
  const d = new Date(iso.length <= 10 ? `${iso}T12:00:00` : iso);
  return isNaN(d.getTime()) ? "" : d.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });
}

// Where the person stopped (from the Movie Night plugin for Jellyfin).
function clock(sec: number) {
  const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60), s = Math.floor(sec % 60);
  return h ? `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}` : `${m}:${String(s).padStart(2, "0")}`;
}

function ResumeLine({ r, showId }: { r: NonNullable<TitleDetail["resume"]>; showId?: number }) {
  const ep = r.episode != null ? `S${r.season}·E${r.episode} · ` : "";
  const left = r.duration_s ? Math.max(0, Math.round((r.duration_s - r.position_s) / 60)) : null;
  const where = r.state === "stopped" ? `Stopped at ${clock(r.position_s)}` : `Watching now · ${clock(r.position_s)}`;
  // A show's episode opens that episode (its season page).
  const episode = showId && r.season != null && r.episode != null ? { s: r.season, e: r.episode } : null;
  return (
    <Pressable disabled={!episode} onPress={() => episode && openEpisode(showId!, episode.s, episode.e)} style={st.resume}>
      <View style={st.resumeBar}><View style={[st.resumeFill, { width: `${Math.max(3, r.percent || 0)}%` }]} /></View>
      <Text style={st.resumeText}>{ep + where + (left != null ? ` · ${left} min left` : "") + (r.client ? ` · ${r.client}` : "")}</Text>
    </Pressable>
  );
}

function ScoreChips({ d, scores }: { d: TitleDetail; scores: Scores | null | undefined }) {
  const chip = (key: string, lead: React.ReactNode, value: string, label?: string, url?: string | null) => (
    <Pressable key={key} disabled={!url} onPress={() => url && Linking.openURL(url)} style={st.score}>
      {lead}<Text style={st.scoreValue}>{value}</Text>{label ? <Text style={st.scoreLabel}>{label}</Text> : null}
    </Pressable>
  );
  const tag = (text: string, bg: string, fg = "#000") => <Text style={[st.tag, { backgroundColor: bg, color: fg }]}>{text}</Text>;
  const out = [];
  const cm = d.community;
  if (cm && cm.count) out.push(chip("shared", tag("Shared", colors.accent, colors.accentInk), String(cm.average), `${cm.count}`));
  if (d.vote) out.push(chip("tmdb", <Text style={{ color: colors.accent }}>★</Text>, String(d.vote), "TMDB"));
  if (scores?.imdb_rating) out.push(chip("imdb", tag("IMDb", "#f5c518"), String(scores.imdb_rating), undefined, d.imdb_url));
  if (scores?.rt_critics != null) out.push(chip("rt", <View style={[st.dot, { backgroundColor: scores.rt_critics >= 60 ? "#fa320a" : "#0ac855" }]} />, `${scores.rt_critics}%`, "critics", scores.rt_url));
  if (scores?.rt_audience != null) out.push(chip("pop", <View style={[st.dot, { backgroundColor: "#fbb03b" }]} />, `${scores.rt_audience}%`, "audience", scores.rt_url));
  if (scores?.metacritic != null) out.push(chip("mc", tag(String(scores.metacritic), scores.metacritic >= 61 ? "#00ce7a" : scores.metacritic >= 40 ? "#ffbd3f" : "#ff6874", "#fff"), "", "Metacritic"));
  if (scores?.letterboxd) out.push(chip("lb", <View style={[st.dot, { backgroundColor: "#00e054" }]} />, scores.letterboxd.toFixed(1), "Letterboxd"));
  if (scores?.trakt_rating) out.push(chip("trakt", tag("Trakt", "#ed1c24", "#fff"), String(scores.trakt_rating)));
  return <View style={st.scores}>{out}</View>;
}

export default function Title() {
  const { media, id } = useLocalSearchParams<{ media: "movie" | "tv"; id: string }>();
  const insets = useSafeAreaInsets();
  const { width } = useWindowDimensions();
  const qc = useQueryClient();
  const path = `/api/v1/title/${encodeURIComponent(media)}/${encodeURIComponent(id)}`;
  const q = useQuery({ queryKey: ["title", media, id], queryFn: () => api.get<TitleDetail>(path) });
  const related = useQuery({ queryKey: ["related", media, id], queryFn: () => api.get<RelatedOut>(`${path}/related`), enabled: !!q.data });
  const next = useQuery({ queryKey: ["next", id], queryFn: () => api.get<NextEpisodeOut>(`/api/v1/title/tv/${encodeURIComponent(id)}/next`), enabled: media === "tv" });
  const [fetched, setFetched] = useState<{ key: string; scores: Scores | null; note: string | null } | null>(null);
  const [sheet, setSheet] = useState<null | "rate" | "lists" | "watched">(null);
  const d = q.data;

  useEffect(() => {
    if (!d) return;
    remember(d.key, d.state);
    if (!d.scores || d.scores.rt_critics == null) {
      // note: why outside scores couldn't be refreshed (MDBList refused or didn't answer)
      api.get<{ scores: Scores | null; note?: string | null }>(`${path}/scores`).then((r) => {
        if (r.scores || r.note) setFetched({ key: d.key, scores: r.scores ?? null, note: r.note ?? null });
      }).catch(() => {});
    }
  }, [d, path]);
  const scores = d && fetched?.key === d.key ? fetched.scores ?? d.scores : d?.scores;
  const scoresNote = d && fetched?.key === d.key ? fetched.note : null;

  if (q.isLoading) return <View style={{ flex: 1, backgroundColor: colors.bg, paddingTop: insets.top + 60 }}><Loading /></View>;
  if (q.error || !d) return <View style={{ flex: 1, backgroundColor: colors.bg, paddingTop: insets.top + 60 }}><ErrorBox message={(q.error as Error)?.message || "Not found"} onRetry={() => q.refetch()} /></View>;

  const t: T = { key: d.key, media_type: d.media_type as "movie" | "tv", tmdb_id: d.tmdb_id, title: d.title };
  const mine = d.community?.mine;
  const meta = [d.year, media === "tv" ? (d.number_of_seasons ? `${d.number_of_seasons} season${d.number_of_seasons > 1 ? "s" : ""}` : "Series") : d.runtime ? `${Math.floor(d.runtime / 60)}h ${d.runtime % 60}m` : null, d.certification].filter(Boolean).join("  ·  ");
  const firstSeason = next.data?.next?.season ?? (d.seasons.find((s) => s.number > 0) || d.seasons[0])?.number ?? 1;
  const onRated = () => qc.invalidateQueries({ queryKey: ["title", media, id] });

  return (
    <View style={{ flex: 1, backgroundColor: colors.bg }}>
      <ScrollView contentContainerStyle={{ paddingBottom: 60 }}>
        <View style={{ height: Math.min(460, width * 0.62) }}>
          <Image source={img(d.backdrop || d.poster, d.backdrop ? "w1280" : "w780")} style={StyleSheet.absoluteFill} contentFit="cover" />
          <View style={st.heroShade} />
        </View>
        <View style={st.head}>
          <View style={st.poster}>{d.poster ? <Image source={img(d.poster)} style={StyleSheet.absoluteFill} /> : null}</View>
          <View style={{ flex: 1, paddingBottom: 4 }}>
            <Text style={st.h1}>{d.title}</Text>
            <Text style={st.meta}>{meta}{d.ongoing ? "  ·  Ongoing" : ""}</Text>
          </View>
        </View>
        <View style={{ paddingHorizontal: gutter }}>
          {d.genres.length ? <Text style={st.genres}>{d.genres.join("  ·  ")}</Text> : null}
          <ScoreChips d={d} scores={scores} />
          {scoresNote ? <Text style={st.scoresNote}>{scoresNote}</Text> : null}
          <View style={{ marginTop: 16 }}><PrimaryButton t={t} next={next.data?.next} /></View>
          {media === "tv" && next.data?.next ? (() => {
            // Up next: the episode Play starts, with its own heart and stars (that
            // episode's, not the show's); its name opens it on its season page.
            const n = next.data.next;
            return (
              <View style={st.upnext}>
                <Pressable style={st.upnextEp} onPress={() => { tap(); openEpisode(d.tmdb_id, n.season, n.episode); }} accessibilityRole="button"
                  testID="upnext-open" accessibilityLabel={`Up next: season ${n.season} episode ${n.episode}`}>
                  <Text style={st.upnextK}>UP NEXT</Text>
                  <Text style={st.upnextT}>S{n.season}·E{n.episode}</Text>
                  <Icon name="right" size={18} color={colors.text3} />
                </Pressable>
                <EpisodeMarks showId={d.tmdb_id} showTitle={d.title} season={n.season} idPrefix="upnext"
                  e={{ number: n.episode, favorite: n.favorite, rating: n.rating }}
                  onChange={(p) => qc.setQueryData<NextEpisodeOut>(["next", id], (o) => (o?.next ? { next: { ...o.next, ...p } } : o))} />
              </View>
            );
          })() : null}
          {d.resume?.position_s ? <ResumeLine r={d.resume} showId={media === "tv" ? d.tmdb_id : undefined} /> : null}
          <ActionTiles
            t={t}
            onRate={() => setSheet("rate")}
            onLists={() => setSheet("lists")}
            onWatched={() => media === "tv"
              ? (next.data?.next ? openEpisode(d.tmdb_id, next.data.next.season, next.data.next.episode)
                : router.push({ pathname: "/season/[id]/[n]", params: { id: String(d.tmdb_id), n: String(firstSeason) } }))
              : setSheet("watched")}
          />
          {d.tagline ? <Text style={st.tagline}>{d.tagline}</Text> : null}
          {d.overview ? <Text style={st.overview}>{d.overview}</Text> : null}

          <SectionTitle title="Ratings on Movie Night" sub={d.community?.count ? `${d.community.average}/10 from ${d.community.count}` : null} />
          {mine ? (
            <View style={[st.rcard, { borderColor: "rgba(245,183,59,0.4)" }]}>
              <Text style={st.rhead}>You  <Text style={{ color: colors.accent }}>{"★".repeat(Math.round(mine.rating / 2))}</Text>  {mine.rating}/10</Text>
              {mine.review ? <Text style={st.rtext}>{mine.review}</Text> : <Text style={[st.rtext, { color: colors.text3 }]}>No comment yet.</Text>}
              <Button small icon="edit" label="Edit" onPress={() => setSheet("rate")} style={{ alignSelf: "flex-start", marginTop: 8 }} />
            </View>
          ) : <Button icon="star" label="Rate it and say what you thought" onPress={() => setSheet("rate")} />}
          {(d.community?.others || []).map((r) => (
            <View key={`${r.name}${r.rated_at}`} style={st.rcard}>
              <Text style={st.rhead}>{r.name}  <Text style={{ color: colors.accent }}>{"★".repeat(Math.round(r.rating / 2))}</Text>  {r.rating}/10</Text>
              {r.review ? <Text style={st.rtext}>{r.review}</Text> : null}
            </View>
          ))}

          <View style={st.facts}>
            {[[d.director_label, d.directors.join(", ")], ["Written by", d.writers.join(", ")],
              [media === "tv" ? "Network" : "Studio", (media === "tv" ? d.networks : d.studios).join(", ")],
              [media === "tv" ? "First aired" : "Released", fmtDate(d.release_date)],
              ["Next episode", d.next_episode?.air_date ? `S${d.next_episode.season}·E${d.next_episode.episode} on ${fmtDate(d.next_episode.air_date)}` : ""],
              ["Language", d.languages.join(", ")]].filter(([, v]) => v).map(([k, v]) => (
              // The next episode to air opens that episode (its season page), as on the website.
              k === "Next episode" && d.next_episode?.season != null && d.next_episode.episode != null ? (
                <Pressable key={k} style={st.fact} accessibilityRole="link" testID="next-episode-fact"
                  onPress={() => { tap(); openEpisode(d.tmdb_id, d.next_episode!.season!, d.next_episode!.episode!); }}>
                  <Text style={st.factK}>{k}</Text><Text style={[st.factV, { color: colors.accent }]}>{v}</Text>
                </Pressable>
              ) : <View key={k} style={st.fact}><Text style={st.factK}>{k}</Text><Text style={st.factV}>{v}</Text></View>
            ))}
          </View>
          <View style={st.links}>
            {d.trailer ? <Button small kind="ghost" icon="play" label="Trailer" onPress={() => Linking.openURL(d.trailer!)} /> : null}
            {d.imdb_url ? <Button small label="IMDb" onPress={() => Linking.openURL(d.imdb_url!)} /> : null}
            <Button small label="TMDB" onPress={() => Linking.openURL(d.tmdb_url)} />
          </View>
        </View>

        {d.seasons.length ? (
          <>
            <SectionTitle title="Seasons" />
            <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ paddingHorizontal: gutter, gap: 12 }}>
              {d.seasons.map((s) => (
                <Pressable key={s.number} style={{ width: 112 }} onPress={() => { tap(); router.push({ pathname: "/season/[id]/[n]", params: { id: String(d.tmdb_id), n: String(s.number) } }); }}>
                  <View style={[st.poster, { width: 112, height: 168, marginBottom: 6 }]}>
                    {s.poster ? <Image source={img(s.poster)} style={StyleSheet.absoluteFill} /> : <Text style={st.noimg}>{s.name}</Text>}
                    {s.watched ? <View style={st.seen}><Icon name="check" size={13} color={colors.ok} /><Text style={st.seenText}>{s.episodes && s.watched >= s.episodes ? "Seen" : `${s.watched}/${s.episodes ?? "?"}`}</Text></View> : null}
                  </View>
                  <Text style={{ color: colors.text, fontWeight: "700" }}>{s.name}</Text>
                  <Text style={{ color: colors.text3, fontSize: 13 }}>{s.episodes ?? "?"} episodes</Text>
                </Pressable>
              ))}
            </ScrollView>
          </>
        ) : null}

        {d.cast.length ? (
          <>
            <SectionTitle title="Cast" />
            {/* Everyone TMDB lists, scrolled sideways (drawn as they come into view). */}
            <FlatList
              horizontal
              showsHorizontalScrollIndicator={false}
              data={d.cast}
              keyExtractor={(p, i) => `${p.id}${p.character}${i}`}
              contentContainerStyle={{ paddingHorizontal: gutter, gap: 14 }}
              renderItem={({ item: p }) => (
                <Pressable style={{ width: 84, alignItems: "center" }} onPress={() => p.id && router.push({ pathname: "/person/[id]", params: { id: String(p.id) } })}>
                  <View style={st.photo}>{p.photo ? <Image source={img(p.photo, "w185")} style={StyleSheet.absoluteFill} /> : null}</View>
                  <Text style={st.personName} numberOfLines={2}>{p.name}</Text>
                  {p.character ? <Text style={st.personRole} numberOfLines={2}>{p.character}</Text> : null}
                </Pressable>
              )}
            />
          </>
        ) : null}

        {related.data?.collection && related.data.collection.items.length > 1 ? (
          <RowView row={{ id: "collection", title: related.data.collection.name || "Collection", style: "poster", items: related.data.collection.items } as any} />
        ) : null}
        {related.data?.recommendations.length ? (
          <RowView row={{ id: "recs", title: "More like this", style: "poster", items: related.data.recommendations } as any}
            onMore={() => router.push({ pathname: "/browse", params: { key: `recs:${media}:${id}`, title: `More like ${d.title}` } })} />
        ) : null}
      </ScrollView>

      <Pressable onPress={() => router.back()} style={[st.back, { top: insets.top + 10 }]} accessibilityLabel="Back"><Icon name="left" /></Pressable>
      {sheet === "rate" ? <RateSheet t={t} review={mine?.review} onClose={() => setSheet(null)} onSaved={onRated} /> : null}
      {sheet === "lists" ? <ListsSheet t={t} onClose={() => setSheet(null)} /> : null}
      {sheet === "watched" ? <WatchedSheet t={t} watches={d.watches || []} onClose={() => setSheet(null)} onChanged={onRated} /> : null}
    </View>
  );
}

const st = StyleSheet.create({
  upnext: { flexDirection: "row", alignItems: "center", gap: 2, marginTop: 10, paddingLeft: 14, paddingRight: 2, paddingVertical: 2, borderRadius: 15, backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line },
  upnextEp: { flex: 1, flexDirection: "row", alignItems: "center", gap: 8, minHeight: 44 },
  upnextK: { color: colors.text3, fontSize: 12, fontWeight: "800", letterSpacing: 0.7 },
  upnextT: { color: colors.text, fontSize: 15.5, fontWeight: "800" },
  heroShade: { ...StyleSheet.absoluteFill, backgroundColor: "rgba(13,15,20,0.35)" },
  head: { flexDirection: "row", gap: 14, alignItems: "flex-end", marginTop: -86, paddingHorizontal: gutter },
  poster: { width: 112, height: 168, borderRadius: 14, overflow: "hidden", backgroundColor: colors.surface2, borderWidth: 1, borderColor: colors.line, justifyContent: "center" },
  noimg: { color: colors.text3, textAlign: "center", padding: 8 },
  h1: { color: colors.text, fontSize: 26, fontWeight: "800", lineHeight: 30 },
  meta: { color: colors.text2, fontSize: 14.5, fontWeight: "600", marginTop: 6 },
  genres: { color: colors.text2, fontSize: 14, marginTop: 14 },
  scores: { flexDirection: "row", flexWrap: "wrap", gap: 8, marginTop: 12 },
  scoresNote: { color: colors.text3, fontSize: 13.5, marginTop: 8 },
  resume: { marginTop: 10, gap: 6 },
  resumeBar: { height: 4, borderRadius: 2, backgroundColor: colors.line2, overflow: "hidden" },
  resumeFill: { height: "100%", backgroundColor: colors.accent },
  resumeText: { color: colors.text2, fontSize: 13 },
  score: { flexDirection: "row", alignItems: "center", gap: 6, height: 34, paddingHorizontal: 10, borderRadius: 10, backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line },
  scoreValue: { color: colors.text, fontWeight: "700", fontSize: 14.5 },
  scoreLabel: { color: colors.text3, fontSize: 12.5, fontWeight: "600" },
  tag: { fontSize: 12, fontWeight: "800", paddingHorizontal: 5, borderRadius: 4, overflow: "hidden" },
  dot: { width: 14, height: 14, borderRadius: 7 },
  tagline: { color: colors.text2, fontStyle: "italic", marginTop: 22, fontSize: 15 },
  overview: { color: "#dfe2e8", fontSize: 16, lineHeight: 24, marginTop: 8 },
  rcard: { backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line, borderRadius: 15, padding: 12, marginBottom: 10 },
  rhead: { color: colors.text, fontWeight: "700", fontSize: 15.5 },
  rtext: { color: colors.text, fontSize: 15, lineHeight: 22, marginTop: 6 },
  facts: { marginTop: 18, gap: 8 },
  fact: { flexDirection: "row", gap: 14 },
  factK: { color: colors.text3, fontWeight: "600", width: 110, fontSize: 15 },
  factV: { color: colors.text, flex: 1, fontSize: 15 },
  links: { flexDirection: "row", gap: 8, flexWrap: "wrap", marginTop: 16 },
  seen: { position: "absolute", top: 7, left: 7, height: 24, paddingHorizontal: 8, borderRadius: 999, backgroundColor: "rgba(10,12,17,0.82)", flexDirection: "row", alignItems: "center", gap: 4 },
  seenText: { color: colors.text2, fontSize: 11.5, fontWeight: "700" },
  photo: { width: 76, height: 76, borderRadius: 38, overflow: "hidden", backgroundColor: colors.surface2, marginBottom: 6 },
  personName: { color: colors.text, fontSize: 13.5, fontWeight: "700", textAlign: "center" },
  personRole: { color: colors.text3, fontSize: 12.5, textAlign: "center" },
  back: { position: "absolute", left: 12, width: 44, height: 44, borderRadius: 22, backgroundColor: "rgba(10,12,17,0.85)", borderWidth: 1, borderColor: "rgba(255,255,255,0.14)", alignItems: "center", justifyContent: "center" },
});
