// Where VLC / Infuse send you back (movienight://play/<id>): "Finished it?"
// when the film could have ended; otherwise straight back.
import { useEffect, useRef, useState } from "react";
import { StyleSheet, Text, View } from "react-native";
import { Image } from "expo-image";
import { useQueryClient } from "@tanstack/react-query";
import { router, useLocalSearchParams } from "expo-router";

import { Button, ErrorBox, Loading, useToast } from "@/components/ui";
import { api } from "@/lib/api";
import { send } from "@/lib/outbox";
import { colors, gutter, img } from "@/lib/theme";
import { getTitleState, setTitleState } from "@/lib/titleState";
import type { PlaySessionOut } from "@/lib/types";

// A Play's id as the server makes it: any other link (another app can open
// movienight://play/...) is not sent anywhere.
const SESSION_ID = /^[A-Za-z0-9_-]{16,40}$/;

export default function PlayDone() {
  const { session } = useLocalSearchParams<{ session: string }>();
  const toast = useToast();
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [s, setS] = useState<PlaySessionOut | null>(null);
  const [error, setError] = useState("");
  // Asked only when the film could have ended; otherwise straight back. Sent
  // once: the server says "ask" only the first time, so sending it again (a
  // refresh) would close the question while it is being answered.
  const sent = useRef<string | null>(null);
  useEffect(() => {
    if (sent.current === session) return;
    sent.current = session;
    if (!SESSION_ID.test(session || "")) { router.back(); return; }
    api.post<{ ask: boolean; session: PlaySessionOut }>(`/api/v1/play/${encodeURIComponent(session)}/back`, {})
      .then((r) => { if (r.ask) setS(r.session); else router.back(); })
      .catch((e) => setError(e.message));
  }, [session]);
  const answer = async (outcome: "watched" | "not_finished") => {
    if (!s) return;
    setBusy(true);
    send("POST", `/api/v1/play/${s.id}/finish`, { outcome }).then(() => qc.invalidateQueries());
    if (outcome === "watched" && s.key) {
      setTitleState(s.key, { watched: (getTitleState(s.key).watched || 0) + 1 });
      toast(`Marked ${s.title} as watched`);
    }
    router.back();
  };
  return (
    <View style={st.wrap}>
      {error ? <ErrorBox message={error} /> : s ? (
        <>
          {s.poster ? <Image source={img(s.poster, "w342")} style={st.poster} /> : null}
          <Text style={st.h1}>Finished it?</Text>
          <Text style={st.sub}>{s.title}</Text>
          <Button kind="request" icon="check" label="Yes, mark as watched" busy={busy} onPress={() => answer("watched")} />
          <View style={{ height: 8 }} />
          <Button label="Not yet" onPress={() => answer("not_finished")} />
        </>
      ) : <Loading />}
    </View>
  );
}

const st = StyleSheet.create({
  wrap: { flex: 1, backgroundColor: colors.bg, justifyContent: "center", padding: gutter + 8 },
  poster: { width: 130, height: 195, borderRadius: 12, alignSelf: "center", marginBottom: 16 },
  h1: { color: colors.text, fontSize: 28, fontWeight: "800", textAlign: "center" },
  sub: { color: colors.text3, fontSize: 15.5, textAlign: "center", marginTop: 6, marginBottom: 20 },
});
