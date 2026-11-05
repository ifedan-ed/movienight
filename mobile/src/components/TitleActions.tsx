// The big button (Play / Request / Requested / Downloading) and the five
// action tiles (Watchlist, Watched, Rate, Favorite, List), with their sheets;
// and one episode's own heart and stars (season page, Up next), which act on
// that episode only, never on the show.
import { useEffect, useRef, useState } from "react";
import { Linking, Platform, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";
import { useQuery } from "@tanstack/react-query";
import { router } from "expo-router";

import { api, titlePath } from "../lib/api";
import { isIntentLink, openIntentLink } from "../lib/intent";
import { send } from "../lib/outbox";
import { caret, colors } from "../lib/theme";
import { getTitleState, setTitleState, useTitleState } from "../lib/titleState";
import type { MeOut, PlayInfo, PlayOut, TitleState, Watch } from "../lib/types";
import { Icon, type IconName } from "./Icon";
import { Sheet } from "./Sheet";
import { RatingLabel, Stars } from "./Stars";
import { Button, tap, useToast } from "./ui";

export type T = { key: string; media_type: "movie" | "tv"; tmdb_id: number; title: string };

const REQUEST_FIELDS = ["in_library", "jellyfin_id", "jellyfin_url", "request", "progress", "requested_at"] as const;
const requestPart = (st: Partial<TitleState>) => Object.fromEntries(REQUEST_FIELDS.filter((f) => f in st).map((f) => [f, (st as any)[f]]));

function until(iso?: string | null) {
  if (!iso) return "";
  const s = (new Date(iso).getTime() - Date.now()) / 1000;
  if (s <= 60) return "almost done";
  if (s < 3600) return `${Math.round(s / 60)} min left`;
  if (s < 86400) return `${Math.round(s / 3600)} h left`;
  return `${Math.round(s / 86400)} d left`;
}

// ---------------------------------------------------------------------------
// Play / Request
// ---------------------------------------------------------------------------

export function PrimaryButton({ t, next }: { t: T; next?: { season: number; episode: number; label: string } | null }) {
  const s = useTitleState(t.key);
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const [playOpen, setPlayOpen] = useState(false);
  const poll = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    // Live status while requested or downloading.
    const waiting = s.request === "pending" || s.request === "processing" || s.request === "partial";
    if (!waiting) return;
    poll.current = setTimeout(async () => {
      try { setTitleState(t.key, requestPart(await api.get<TitleState>(`${titlePath(t)}/status`))); } catch { /* next time */ }
    }, 8000);
    return () => { if (poll.current) clearTimeout(poll.current); };
  }, [s, t]);

  if (s.in_library) {
    return (
      <>
        <Button kind="play" icon="play" label={next ? next.label : "Play"} onPress={() => setPlayOpen(true)} />
        {playOpen ? <PlaySheet t={t} season={next?.season} episode={next?.episode} onClose={() => setPlayOpen(false)} /> : null}
      </>
    );
  }
  if (s.request === "processing" || s.request === "partial") {
    const p = s.progress;
    return <Button kind="status" icon={p ? "download" : "clock"} label={p ? `Downloading ${p.percent}%${p.eta ? " · " + until(p.eta) : ""}` : "Requested · waiting for a download"} disabled />;
  }
  if (s.request === "pending") return <Button kind="status" icon="clock" label="Requested · waiting for approval" disabled />;
  if (s.request === "dry_run") return <Button kind="status" icon="info" label="Test mode: request not sent" disabled />;
  return (
    <Button kind="request" icon="download" label="Request" busy={busy} onPress={async () => {
      setBusy(true);
      try {
        const res = await api.post<{ already: boolean; state: TitleState }>(`${titlePath(t)}/request`);
        setTitleState(t.key, requestPart(res.state));
        toast(res.already ? "Already requested — showing its status" : `Requested ${t.title}`);
      } catch (e: any) { toast(e.message, { error: true }); }
      setBusy(false);
    }} />
  );
}

export function PlaySheet({ t, season, episode, onClose }: { t: T; season?: number; episode?: number; onClose: () => void }) {
  const toast = useToast();
  const [info, setInfo] = useState<PlayInfo | null>(null);
  const [player, setPlayer] = useState<string>("");
  const [sub, setSub] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);

  // Keyed on the title, not the object: the page makes a new one each time it
  // redraws, which would put back the default player and subtitle.
  useEffect(() => {
    const qs = season != null && episode != null ? `?season=${season}&episode=${episode}` : "";
    api.get<PlayInfo>(`${titlePath(t)}/play${qs}`).then((i) => {
      setInfo(i);
      setPlayer(i.default_player);
      setSub(i.chosen_subtitle ?? null);
    }).catch((e) => { toast(e.message, { error: true }); onClose(); });
  }, [t.media_type, t.tmdb_id, season, episode]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!info) return null;
  if (!info.available) {
    return <Sheet visible onClose={onClose} title={t.title} sub="This isn't in the library yet."><Button label="Close" onPress={onClose} /></Sheet>;
  }
  if (!info.linked) {
    return (
      <Sheet visible onClose={onClose} title={info.title || t.title}>
        <Button kind="request" icon="plug" label="Connect Jellyfin" onPress={() => { onClose(); router.push("/settings/jellyfin"); }} />
        <View style={{ height: 8 }} />
        {info.web_url ? <Button icon="play" label="Open in Jellyfin" onPress={() => { onClose(); Linking.openURL(info.web_url!); }} /> : null}
      </Sheet>
    );
  }
  const textSubs = (info.subtitles || []).filter((s) => s.text);
  const start = async () => {
    if (player === "jellyfin" && info.web_url) { onClose(); Linking.openURL(info.web_url); return; }
    setBusy(true);
    let out: PlayOut;
    try {
      out = await api.post<PlayOut>(`${titlePath(t)}/play`, {
        player, season: info.season ?? season, episode: info.episode ?? episode, subtitle: sub, return_to: "app",
      });
    } catch (e: any) {
      toast(e.message, { error: true });
      setBusy(false);
      return;
    }
    onClose();
    try {
      if (Platform.OS === "android" && isIntentLink(out.url)) await openIntentLink(out.url);  // VLC (Android)
      else await Linking.openURL(out.url);
    } catch {
      // Never the phone's own message: it repeats the link, Jellyfin token
      // and all. The Play just started is closed, so nobody is asked about it.
      toast("That player isn't installed on this phone", { error: true });
      send("POST", `/api/v1/play/${encodeURIComponent(out.session_id)}/finish`, { outcome: "dismissed" });
    }
  };
  return (
    <Sheet visible onClose={onClose} title={info.title || t.title}>
      <Text style={st.mini}>PLAY IN</Text>
      <View style={st.players}>
        {info.players.map((p) => (
          <Pressable key={p.id} onPress={() => { tap(); setPlayer(p.id); }} style={[st.player, p.id === player && st.playerOn]}>
            <Text style={[st.playerText, p.id === player && { color: colors.accent }]}>{p.name}</Text>
          </Pressable>
        ))}
      </View>
      {textSubs.length && player !== "jellyfin" ? (
        <>
          <Text style={st.mini}>SUBTITLES</Text>
          <View style={st.players}>
            <Pressable onPress={() => setSub(null)} style={[st.subChip, sub == null && st.playerOn]}><Text style={[st.playerText, sub == null && { color: colors.accent }]}>Off</Text></Pressable>
            {textSubs.map((s) => (
              <Pressable key={s.index} onPress={() => { tap(); setSub(s.index); }} style={[st.subChip, sub === s.index && st.playerOn]}>
                <Text style={[st.playerText, sub === s.index && { color: colors.accent }]}>{s.label.replace(/ - (Default|SUBRIP|SRT|ASS|SSA|WEBVTT|PGSSUB)\b/gi, "")}{s.external ? " · file" : ""}</Text>
              </Pressable>
            ))}
          </View>
        </>
      ) : null}
      <View style={{ height: 14 }} />
      <Button kind="play" icon="play" label={player === "jellyfin" ? "Open in Jellyfin" : "Play"} busy={busy} onPress={start} />
    </Sheet>
  );
}

// ---------------------------------------------------------------------------
// The five tiles
// ---------------------------------------------------------------------------

function Tile({ icon, label, look, onPress, testID }: { icon: IconName; label: string; look?: "on" | "ok" | "fav"; onPress: () => void; testID?: string }) {
  const c = look === "on" ? colors.accent : look === "ok" ? colors.ok : look === "fav" ? colors.fav : colors.text2;
  return (
    <Pressable onPress={() => { tap(); onPress(); }} testID={testID} accessibilityRole="button" accessibilityLabel={label}
      style={({ pressed }) => [st.tile, look && { borderColor: c, backgroundColor: `${c}1f` }, pressed && { transform: [{ scale: 0.96 }] }]}>
      <Icon name={icon} size={25} color={c} />
      {/* Two lines: "Favorited show" doesn't fit one on a phone. */}
      <Text style={[st.tileText, { color: c }]} numberOfLines={2}>{label}</Text>
    </Pressable>
  );
}

export function ActionTiles({ t, onRate, onWatched, onLists }: { t: T; onRate: () => void; onWatched: () => void; onLists: () => void }) {
  const s = useTitleState(t.key);
  const toast = useToast();
  const toggle = async (field: "watchlist" | "favorite") => {
    const on = !getTitleState(t.key)[field];
    setTitleState(t.key, { [field]: on });
    // Kept on the phone until the server has it (offline included).
    send(on ? "POST" : "DELETE", `${titlePath(t)}/${field}`);
    toast(`${on ? "Added" : "Removed"} ${t.title} ${on ? "to" : "from"} your ${field === "watchlist" ? "watchlist" : "favorites"}`);
  };
  const watched = s.watched || 0;
  // A show's rating and heart are the whole show's (an episode has its own on
  // the season page), so for shows they say so.
  const tv = t.media_type === "tv";
  return (
    <View style={st.tiles}>
      <Tile icon={s.watchlist ? "bookmarkFill" : "bookmark"} label={s.watchlist ? "In watchlist" : "Watchlist"} look={s.watchlist ? "on" : undefined} onPress={() => toggle("watchlist")} />
      <Tile icon={watched ? "check" : "eye"} label={t.media_type === "tv" ? (watched ? `${watched} eps` : "Episodes") : watched > 1 ? `Watched ×${watched}` : "Watched"} look={watched ? "ok" : undefined} onPress={onWatched} />
      <Tile icon={s.rating ? "starFill" : "star"} label={s.rating ? `${tv ? "Show " : ""}${s.rating}/10` : tv ? "Rate show" : "Rate"} look={s.rating ? "on" : undefined} onPress={onRate} testID="title-rate" />
      <Tile icon={s.favorite ? "heartFill" : "heart"} label={tv ? (s.favorite ? "Favorited show" : "Favorite show") : s.favorite ? "Favorited" : "Favorite"} look={s.favorite ? "fav" : undefined} onPress={() => toggle("favorite")} testID="title-favorite" />
      <Tile icon="listPlus" label="List" onPress={onLists} />
    </View>
  );
}

export function RateSheet({ t, review, onClose, onSaved }: { t: T; review?: string | null; onClose: () => void; onSaved: (v: number | null, review: string | null) => void }) {
  const sub = t.media_type === "tv" ? "Your rating out of 10, for the whole show (rate an episode on its season page)" : "Your rating out of 10";
  const current = getTitleState(t.key).rating || 0;
  const [value, setValue] = useState(current);
  const [note, setNote] = useState(review || "");
  const toast = useToast();
  // The install's comment length (TEXT_MAX_CHARS, a technical limit): the
  // box counts down to it and says by how much a longer one is over, and
  // Save waits until it fits. No maxLength: that would cut pasted text
  // without a word.
  const me = useQuery({ queryKey: ["me"], queryFn: () => api.get<MeOut>("/api/v1/me") });
  const maxChars = me.data?.limits?.text_max_chars ?? 5000;
  const over = note.trim().length > maxChars;
  const save = () => {
    send("POST", `${titlePath(t)}/rating`, { rating: value, review: note.trim() });
    setTitleState(t.key, { rating: value });
    toast(`Rated ${t.title}: ${value}/10`);
    onSaved(value, note.trim() || null);
    onClose();
  };
  const remove = () => {
    send("DELETE", `${titlePath(t)}/rating`);
    setTitleState(t.key, { rating: null });
    toast("Rating removed");
    onSaved(null, null);
    onClose();
  };
  return (
    <Sheet visible onClose={onClose} title={`Rate ${t.title}`} sub={sub}>
      <Stars value={value} onChange={setValue} />
      <RatingLabel value={value} />
      <TextInput {...caret}
        style={st.note}
        value={note}
        onChangeText={setNote}
        multiline
        placeholder="What did you think? (optional)"
        placeholderTextColor={colors.text3}
      />
      {note.length > maxChars * 0.9 ? (
        <Text style={[st.count, over && { color: colors.danger }]}>
          {over ? `${(note.trim().length - maxChars).toLocaleString()} characters too long: a comment is at most ${maxChars.toLocaleString()}` : `${Math.max(0, maxChars - note.trim().length).toLocaleString()} characters left`}
        </Text>
      ) : null}
      <View style={{ height: 12 }} />
      <Button kind="request" label={current ? "Save" : "Save rating"} disabled={!value || over} onPress={save} />
      {current ? <Button icon="trash" label="Remove rating" onPress={remove} style={{ marginTop: 8 }} /> : null}
    </Sheet>
  );
}

// ---------------------------------------------------------------------------
// One episode: its own heart and stars
// ---------------------------------------------------------------------------

export type EpisodeOwn = { number: number; name?: string; favorite?: boolean; rating?: number | null };

/** The title's rating sheet for one episode: the same stars and scale, no
 * comment. Sent through the outbox, like every rating. */
export function EpisodeRateSheet({ showId, showTitle, season, e, onClose, onSaved }: {
  showId: number; showTitle?: string; season: number; e: EpisodeOwn; onClose: () => void; onSaved: (v: number | null) => void;
}) {
  const current = e.rating || 0;
  const [value, setValue] = useState(current);
  const toast = useToast();
  const url = `/api/v1/title/tv/${showId}/episode/${season}/${e.number}/rating`;
  const save = () => {
    send("PUT", url, { rating: value });
    toast(`Rated episode ${e.number}: ${value}/10`);
    onSaved(value);
    onClose();
  };
  const remove = () => {
    send("DELETE", url);
    toast("Rating removed");
    onSaved(null);
    onClose();
  };
  const where = [showTitle, `S${season}·E${e.number}`, e.name].filter(Boolean).join(" · ");
  return (
    <Sheet visible onClose={onClose} title={`Rate episode ${e.number}`} sub={`${where}. Your rating out of 10, for this episode only`}>
      <Stars value={value} onChange={setValue} />
      <RatingLabel value={value} />
      <View style={{ height: 12 }} />
      <Button kind="request" label={current ? "Save" : "Save rating"} disabled={!value} onPress={save} testID="episode-rate-save" />
      {current ? <Button icon="trash" label="Remove rating" onPress={remove} style={{ marginTop: 8 }} testID="episode-rate-remove" /> : null}
    </Sheet>
  );
}

/** One episode's heart and stars, shown at once and sent through the outbox
 * (so they also wait for a connection), in the order tapped. `onChange`
 * puts the new value where the screen keeps the episode. Test IDs:
 * `${idPrefix}-like-N`, `${idPrefix}-rate-N`. */
export function EpisodeMarks({ showId, showTitle, season, e, onChange, idPrefix = "episode", size = 44 }: {
  showId: number; showTitle?: string; season: number; e: EpisodeOwn; idPrefix?: string; size?: number;
  onChange: (patch: { favorite?: boolean; rating?: number | null }) => void;
}) {
  const [rating, setRating] = useState(false);
  const like = () => {
    const on = !e.favorite;
    onChange({ favorite: on });
    send(on ? "POST" : "DELETE", `/api/v1/title/tv/${showId}/episode/${season}/${e.number}/favorite`);
  };
  const box = { width: size, height: size, alignItems: "center" as const, justifyContent: "center" as const };
  return (
    <>
      <Pressable onPress={() => { tap(); like(); }} style={box} accessibilityRole="button" testID={`${idPrefix}-like-${e.number}`}
        accessibilityState={{ selected: !!e.favorite }}
        accessibilityLabel={e.favorite ? `Unlike episode ${e.number}` : `Like episode ${e.number}`}>
        <Icon name={e.favorite ? "heartFill" : "heart"} size={24} color={e.favorite ? colors.fav : colors.text3} />
      </Pressable>
      <Pressable onPress={() => { tap(); setRating(true); }} style={box} accessibilityRole="button" testID={`${idPrefix}-rate-${e.number}`}
        accessibilityState={{ selected: !!e.rating }}
        accessibilityLabel={e.rating ? `Episode ${e.number} rated ${e.rating} out of 10; change the rating` : `Rate episode ${e.number}`}>
        <Icon name={e.rating ? "starFill" : "star"} size={e.rating ? 20 : 24} color={e.rating ? colors.accent : colors.text3} />
        {e.rating ? <Text style={st.epRateN}>{e.rating}</Text> : null}
      </Pressable>
      {rating ? (
        <EpisodeRateSheet showId={showId} showTitle={showTitle} season={season} e={e} onClose={() => setRating(false)}
          onSaved={(v) => onChange({ rating: v })} />
      ) : null}
    </>
  );
}

export function ListsSheet({ t, onClose }: { t: T; onClose: () => void }) {
  const toast = useToast();
  const [lists, setLists] = useState<{ id: number; name: string; in_list: boolean }[] | null>(null);
  const [name, setName] = useState("");
  useEffect(() => {
    api.get<{ lists: { id: number; name: string; in_list: boolean }[] }>(`${titlePath(t)}/lists`).then((r) => setLists(r.lists)).catch((e) => toast(e.message, { error: true }));
  }, [t.media_type, t.tmdb_id]); // eslint-disable-line react-hooks/exhaustive-deps
  const flip = async (l: { id: number; name: string; in_list: boolean }) => {
    setLists((ls) => ls!.map((x) => (x.id === l.id ? { ...x, in_list: !l.in_list } : x)));
    if (l.in_list) send("DELETE", `/api/v1/lists/${l.id}/items/${t.media_type}/${t.tmdb_id}`);
    else send("POST", `/api/v1/lists/${l.id}/items`, { media_type: t.media_type, tmdb_id: t.tmdb_id });
    toast(l.in_list ? `Removed from ${l.name}` : `Added to ${l.name}`);
  };
  const create = async () => {
    if (!name.trim()) return;
    try {
      const res = await api.post<{ id: number }>("/api/v1/lists", { name: name.trim() });
      await api.post(`/api/v1/lists/${res.id}/items`, { media_type: t.media_type, tmdb_id: t.tmdb_id });
      toast(`Added to ${name.trim()}`);
      onClose();
    } catch (e: any) { toast(e.message, { error: true }); }
  };
  return (
    <Sheet visible onClose={onClose} title="Add to a list" sub={t.title}>
      {(lists || []).map((l) => (
        <Pressable key={l.id} onPress={() => { tap(); flip(l); }} style={st.opt}>
          <Icon name="lists" size={22} color={colors.text2} />
          <Text style={[st.optText, { flex: 1 }]}>{l.name}</Text>
          <View style={[st.check, l.in_list && st.checkOn]}>{l.in_list ? <Icon name="check" size={18} color={colors.accentInk} /> : null}</View>
        </Pressable>
      ))}
      <View style={[st.opt, { gap: 8 }]}>
        <TextInput {...caret} style={[st.optText, { flex: 1, color: colors.text, paddingVertical: 8 }]} value={name} onChangeText={setName} placeholder="New list name" placeholderTextColor={colors.text3} onSubmitEditing={create} returnKeyType="done" />
        <Button small kind="request" label="Create" onPress={create} disabled={!name.trim()} />
      </View>
    </Sheet>
  );
}

// ---------------------------------------------------------------------------
// Watched (a film): just now, yesterday or a day on the calendar, as the
// website asks; with the watches already recorded, each one removable.
// ---------------------------------------------------------------------------

const SOURCE_LABEL: Record<string, string> = {
  manual: "Marked here", jellyfin: "Jellyfin", trakt: "Shared", api: "Other app", player: "Played from here", kodi: "Kodi",
};
// That day's evening here, with this phone's offset. A bare date would be
// read as the evening in the server's time zone (New York), which in Paris is
// already the next day.
const pad = (n: number) => String(n).padStart(2, "0");
const evening = (d: Date) => {
  const e = new Date(d.getFullYear(), d.getMonth(), d.getDate(), 20);
  const off = -e.getTimezoneOffset();
  return `${e.getFullYear()}-${pad(e.getMonth() + 1)}-${pad(e.getDate())}T20:00:00${off < 0 ? "-" : "+"}${pad(Math.floor(Math.abs(off) / 60))}:${pad(Math.abs(off) % 60)}`;
};
const longDay = (d: Date) => d.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
const watchDay = (iso: string) => {
  const d = new Date(iso.length <= 10 ? `${iso}T12:00:00` : iso);
  return isNaN(d.getTime()) ? "" : longDay(d);
};

export function WatchedSheet({ t, watches, onClose, onChanged }: { t: T; watches: Watch[]; onClose: () => void; onChanged: () => void }) {
  const toast = useToast();
  const mark = (when: string | null) => {
    setTitleState(t.key, { watched: (getTitleState(t.key).watched || 0) + 1, watchlist: false });
    toast(`Marked ${t.title} as watched`);
    send("POST", `${titlePath(t)}/watched`, { watched_at: when }).then(onChanged);
    onClose();
  };
  const remove = (w: Watch) => {
    setTitleState(t.key, { watched: Math.max(0, (getTitleState(t.key).watched || 0) - 1) });
    toast("Removed from history");
    send("POST", `${titlePath(t)}/unwatch`, { ids: w.ids }).then(onChanged);
    onClose();
  };
  return (
    <Sheet visible onClose={onClose} title={watches.length ? "Watched again?" : "Mark as watched"} sub={t.title}>
      <Pressable onPress={() => { tap(); mark(null); }} style={st.opt} accessibilityRole="button">
        <Icon name="check" size={22} color={colors.text2} /><Text style={st.optText}>Just now</Text>
      </Pressable>
      <Pressable onPress={() => { tap(); mark(evening(new Date(Date.now() - 86_400_000))); }} style={st.opt} accessibilityRole="button">
        <Icon name="calendar" size={22} color={colors.text2} /><Text style={st.optText}>Yesterday</Text>
      </Pressable>
      <Text style={[st.mini, { marginTop: 18 }]}>ANOTHER DAY</Text>
      <DayPicker onPick={(d) => mark(evening(d))} />
      {watches.length ? (
        <>
          <Text style={[st.mini, { marginTop: 18 }]}>YOUR WATCHES</Text>
          {watches.map((w) => (
            <View key={w.ids.join(",")} style={st.opt}>
              <View style={{ flex: 1 }}>
                <Text style={st.optText}>{watchDay(w.watched_at)}</Text>
                <Text style={st.optSub}>{w.sources.map((s) => SOURCE_LABEL[s] || s).join(" + ")}</Text>
              </View>
              <Pressable onPress={() => { tap(); remove(w); }} hitSlop={10} accessibilityRole="button" accessibilityLabel="Remove this watch">
                <Icon name="trash" size={21} color={colors.text3} />
              </Pressable>
            </View>
          ))}
        </>
      ) : null}
    </Sheet>
  );
}

const WEEKDAYS = ["S", "M", "T", "W", "T", "F", "S"];
const monthName = (i: number) => new Date(2000, i, 1).toLocaleDateString(undefined, { month: "short" });

// A month of days, Sunday first, nothing after today. The month and the year
// can also be picked from a grid, like the website's two lists.
function DayPicker({ onPick }: { onPick: (d: Date) => void }) {
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  const thisYear = today.getFullYear();
  const [month, setMonth] = useState(() => new Date(thisYear, today.getMonth(), 1));
  const [picked, setPicked] = useState(today);
  const [choose, setChoose] = useState<null | "month" | "year">(null);
  const y = month.getFullYear(), m = month.getMonth();
  const go = (yy: number, mm: number) => {
    const d = new Date(yy, mm, 1);
    setMonth(d > today ? new Date(thisYear, today.getMonth(), 1) : d);
    setChoose(null);
  };
  const lastMonth = new Date(y, m + 1, 1) > today;
  const days: (Date | null)[] = [...Array(month.getDay()).fill(null)];
  for (let d = 1, n = new Date(y, m + 1, 0).getDate(); d <= n; d++) days.push(new Date(y, m, d));
  return (
    <View style={st.cal}>
      <View style={st.calHead}>
        <Pressable onPress={() => go(y, m - 1)} hitSlop={8} style={st.calNav} accessibilityRole="button" accessibilityLabel="Previous month">
          <Icon name="left" size={22} color={colors.text2} />
        </Pressable>
        <View style={st.calTitle}>
          <Pressable onPress={() => setChoose(choose === "month" ? null : "month")} style={[st.calSel, choose === "month" && st.playerOn]} accessibilityRole="button" accessibilityLabel="Month">
            <Text style={st.calSelText}>{monthName(m)}</Text><Icon name="down" size={15} color={colors.text3} />
          </Pressable>
          <Pressable onPress={() => setChoose(choose === "year" ? null : "year")} style={[st.calSel, choose === "year" && st.playerOn]} accessibilityRole="button" accessibilityLabel="Year">
            <Text style={st.calSelText}>{y}</Text><Icon name="down" size={15} color={colors.text3} />
          </Pressable>
        </View>
        <Pressable onPress={() => go(y, m + 1)} disabled={lastMonth} hitSlop={8} style={[st.calNav, lastMonth && { opacity: 0.3 }]} accessibilityRole="button" accessibilityLabel="Next month">
          <Icon name="right" size={22} color={colors.text2} />
        </Pressable>
      </View>
      {choose === "month" ? (
        <View style={st.grid}>
          {Array.from({ length: 12 }, (_, i) => {
            const off = y === thisYear && i > today.getMonth();
            return (
              <Pressable key={i} disabled={off} onPress={() => { tap(); go(y, i); }} style={[st.pick, off && { opacity: 0.3 }]} accessibilityRole="button">
                <Text style={[st.pickText, i === m && { color: colors.accent }]}>{monthName(i)}</Text>
              </Pressable>
            );
          })}
        </View>
      ) : choose === "year" ? (
        <ScrollView style={{ maxHeight: 232 }} nestedScrollEnabled>
          <View style={st.grid}>
            {Array.from({ length: thisYear - 1949 }, (_, i) => thisYear - i).map((yr) => (
              <Pressable key={yr} onPress={() => { tap(); go(yr, yr === thisYear ? Math.min(m, today.getMonth()) : m); }} style={st.pick} accessibilityRole="button">
                <Text style={[st.pickText, yr === y && { color: colors.accent }]}>{yr}</Text>
              </Pressable>
            ))}
          </View>
        </ScrollView>
      ) : (
        <>
          <View style={st.week}>{WEEKDAYS.map((w, i) => <Text key={i} style={st.weekText}>{w}</Text>)}</View>
          <View style={st.days}>
            {days.map((day, i) => {
              if (!day) return <View key={`b${i}`} style={st.dayCell} />;
              const on = day.getTime() === picked.getTime();
              const future = day > today;
              return (
                <Pressable key={i} disabled={future} onPress={() => { tap(); setPicked(day); }} style={st.dayCell}
                  accessibilityRole="button" accessibilityState={{ selected: on, disabled: future }}
                  accessibilityLabel={day.toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long", year: "numeric" })}>
                  <View style={[st.day, day.getTime() === today.getTime() && st.dayToday, on && st.dayOn]}>
                    <Text style={[st.dayText, future && { color: colors.text3, opacity: 0.45 }, on && { color: colors.accentInk }]}>{day.getDate()}</Text>
                  </View>
                </Pressable>
              );
            })}
          </View>
        </>
      )}
      <Button kind="request" label={`Save ${longDay(picked)}`} onPress={() => onPick(picked)} style={{ marginTop: 12 }} />
    </View>
  );
}

const st = StyleSheet.create({
  tiles: { flexDirection: "row", gap: 7, marginTop: 10 },
  tile: { flex: 1, minHeight: 70, borderRadius: 15, backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line, alignItems: "center", justifyContent: "center", gap: 6, paddingHorizontal: 2 },
  tileText: { fontSize: 12, fontWeight: "700", textAlign: "center" },
  epRateN: { color: colors.accent, fontSize: 12, fontWeight: "800", lineHeight: 13, marginTop: 1 },
  mini: { color: colors.text3, fontSize: 12.5, fontWeight: "800", letterSpacing: 1, marginTop: 12, marginBottom: 8 },
  players: { flexDirection: "row", flexWrap: "wrap", gap: 8 },
  player: { minWidth: 110, height: 48, borderRadius: 13, backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line, alignItems: "center", justifyContent: "center", paddingHorizontal: 12, flexGrow: 1 },
  playerOn: { backgroundColor: colors.accentSoft, borderColor: "rgba(245,183,59,0.5)" },
  playerText: { color: colors.text2, fontWeight: "700", fontSize: 15 },
  subChip: { height: 40, borderRadius: 999, paddingHorizontal: 14, backgroundColor: "rgba(255,255,255,0.07)", borderWidth: 1, borderColor: colors.line, justifyContent: "center" },
  opt: { flexDirection: "row", alignItems: "center", gap: 12, minHeight: 56, paddingHorizontal: 14, borderRadius: 14, backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line, marginTop: 8 },
  optText: { color: colors.text, fontSize: 16, fontWeight: "600" },
  optSub: { color: colors.text3, fontSize: 13, marginTop: 2 },
  check: { width: 26, height: 26, borderRadius: 8, borderWidth: 2, borderColor: colors.line2, alignItems: "center", justifyContent: "center" },
  checkOn: { backgroundColor: colors.accent, borderColor: colors.accent },
  cal: { marginTop: 2 },
  calHead: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", marginBottom: 8 },
  calNav: { width: 44, height: 44, borderRadius: 12, alignItems: "center", justifyContent: "center", backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line },
  calTitle: { flexDirection: "row", gap: 8 },
  calSel: { flexDirection: "row", alignItems: "center", gap: 6, height: 44, paddingHorizontal: 14, borderRadius: 12, backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line },
  calSelText: { color: colors.text, fontSize: 16, fontWeight: "700" },
  grid: { flexDirection: "row", flexWrap: "wrap", gap: 8 },
  pick: { width: "23%", flexGrow: 1, height: 46, borderRadius: 12, alignItems: "center", justifyContent: "center", backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line },
  pickText: { color: colors.text, fontSize: 15.5, fontWeight: "700" },
  week: { flexDirection: "row" },
  weekText: { width: `${100 / 7}%`, textAlign: "center", color: colors.text3, fontSize: 12.5, fontWeight: "800", paddingVertical: 4 },
  days: { flexDirection: "row", flexWrap: "wrap" },
  dayCell: { width: `${100 / 7}%`, height: 46, alignItems: "center", justifyContent: "center" },
  day: { width: 40, height: 40, borderRadius: 20, alignItems: "center", justifyContent: "center" },
  dayToday: { borderWidth: 1.5, borderColor: colors.accent },
  dayOn: { backgroundColor: colors.accent, borderColor: colors.accent },
  dayText: { color: colors.text, fontSize: 16, fontWeight: "600" },
  note: { marginTop: 14, minHeight: 90, borderRadius: 13, backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line2, color: colors.text, padding: 12, fontSize: 15.5, textAlignVertical: "top" },
  count: { color: colors.text3, fontSize: 13, marginTop: 6, textAlign: "right" },
});
