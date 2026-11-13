// Profile's "Time zone" picker: the zone this person's days follow
// (history's days, "this year", Home's daily rows, the digest weeks).
// Automatic: the phone reports its own (lib/timeZone.ts). Or a zone picked
// here by hand, which the server keeps whatever the phone or browser reports
// until Automatic is picked again.
import { memo, useMemo, useState } from "react";
import { Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { Icon } from "@/components/Icon";
import { Sheet } from "@/components/Sheet";
import { ErrorBox, Loading, tap, useToast } from "@/components/ui";
import { api } from "@/lib/api";
import { caret, colors } from "@/lib/theme";
import { deviceTimeZone } from "@/lib/timeZone";
import type { MeOut, TimeZoneChoiceIn, TimeZonesOut } from "@/lib/types";

/** "America/Argentina/Buenos_Aires" -> "America/Argentina/Buenos Aires". */
export const prettyZone = (zone: string) => zone.replace(/_/g, " ");

/** The Profile row's second line: the zone now and who sets it. */
export function timeZoneSummary(me: MeOut): string {
  if (me.time_zone_mode === "manual" && me.time_zone) return `${prettyZone(me.time_zone)} · chosen by you`;
  return `Automatic (this device)${me.time_zone ? ` · ${prettyZone(me.time_zone)}` : ""}`;
}

const Option = memo(function Option({ title, sub, on, disabled, onPress }: {
  title: string; sub: string; on: boolean; disabled?: boolean; onPress: () => void;
}) {
  return (
    <Pressable onPress={() => { tap(); onPress(); }} disabled={disabled} style={[st.opt, disabled && { opacity: 0.5 }]}
      accessibilityRole="radio" accessibilityState={{ checked: on, disabled }}>
      <View style={{ flex: 1 }}>
        <Text style={st.optText}>{title}</Text>
        <Text style={st.optSub}>{sub}</Text>
      </View>
      <View style={[st.check, on && st.checkOn]}>{on ? <Icon name="check" size={18} color={colors.accentInk} /> : null}</View>
    </Pressable>
  );
});

export function TimeZoneSheet({ me, onClose }: { me: MeOut; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [q, setQ] = useState("");
  const device = deviceTimeZone();
  const manual = me.time_zone_mode === "manual";
  // The list changes only with the server's zone database: kept for the
  // app's run (offsets are "now"; a stale one an hour after a DST change is
  // harmless, the name is what is stored).
  const zones = useQuery({ queryKey: ["time-zones"], queryFn: () => api.get<TimeZonesOut>("/api/v1/time-zones"), staleTime: 3_600_000 });

  // Each zone's search text: name words and the offset with and without the
  // leading zero ("utc+05:30", "utc+5:30"), built once per list.
  const keyed = useMemo(() => (zones.data?.zones ?? []).map((z) => ({
    ...z, key: `${prettyZone(z.name)} ${z.offset} ${z.offset.replace(/([+-])0/, "$1")}`.toLowerCase(),
  })), [zones.data]);
  const words = q.toLowerCase().replace(/_/g, " ").split(/\s+/).filter(Boolean);
  const shown = words.length ? keyed.filter((z) => words.every((w) => z.key.includes(w))) : keyed;

  const choose = async (body: TimeZoneChoiceIn) => {
    onClose();
    try {
      await api.put("/api/v1/me/time-zone/choice", body);
      toast(body.mode === "manual" ? `Time zone: ${prettyZone(body.time_zone)}` : "Time zone follows this phone");
      // Every day-based thing on screen (history, "this year", Home's
      // rows) may have moved: fetch it all again.
      qc.invalidateQueries();
    } catch (e: any) { toast(e.message, { error: true }); }
  };

  return (
    <Sheet visible onClose={onClose} title="Time zone" sub="Your history's days, “this year”, Home's daily rows and the digest weeks follow it.">
      {/* Automatic needs the phone's zone: stored at once, so the days are
          right now rather than after the next report. */}
      <Option title="Automatic (this device)" on={!manual} disabled={!device}
        sub={device ? `This phone is on ${prettyZone(device)}` : "This phone doesn't say its time zone"}
        onPress={() => device && choose({ mode: "automatic", time_zone: device })} />
      <TextInput {...caret} style={st.input} value={q} onChangeText={setQ} placeholder="Search a city or UTC+2"
        placeholderTextColor={colors.text3} autoCapitalize="none" autoCorrect={false} clearButtonMode="while-editing"
        accessibilityLabel="Search time zones" returnKeyType="search" />
      {zones.isLoading ? <Loading /> : zones.error ? <ErrorBox message={(zones.error as Error).message} onRetry={() => zones.refetch()} />
        : shown.length ? shown.map((z) => (
          <Option key={z.name} title={prettyZone(z.name)} sub={z.offset} on={manual && z.name === me.time_zone}
            onPress={() => choose({ mode: "manual", time_zone: z.name })} />
        )) : <Text style={st.none}>No time zone matches that.</Text>}
    </Sheet>
  );
}

// The same option rows as the other sheets (TitleActions: lists, watched).
const st = StyleSheet.create({
  opt: { flexDirection: "row", alignItems: "center", gap: 12, minHeight: 56, paddingHorizontal: 14, paddingVertical: 8, borderRadius: 14, backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line, marginTop: 8 },
  optText: { color: colors.text, fontSize: 16, fontWeight: "600" },
  optSub: { color: colors.text3, fontSize: 13, marginTop: 2 },
  check: { width: 26, height: 26, borderRadius: 8, borderWidth: 2, borderColor: colors.line2, alignItems: "center", justifyContent: "center" },
  checkOn: { backgroundColor: colors.accent, borderColor: colors.accent },
  input: { height: 50, borderRadius: 13, backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line2, color: colors.text, paddingHorizontal: 14, fontSize: 16, marginTop: 14, marginBottom: 6 },
  none: { color: colors.text3, fontSize: 14.5, marginTop: 12 },
});
