// Connect your Jellyfin account (Quick Connect or password, once). Only the
// sign-in token is kept, on the server, encrypted.
import { useEffect, useRef, useState } from "react";
import { Pressable, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { router } from "expo-router";
import { SafeAreaView } from "react-native-safe-area-context";

import { Icon } from "@/components/Icon";
import { Button, Loading, useToast } from "@/components/ui";
import { api } from "@/lib/api";
import { caret, colors, gutter } from "@/lib/theme";
import type { JellyfinLink } from "@/lib/types";

export default function JellyfinSettings() {
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: ["jellyfin"], queryFn: () => api.get<JellyfinLink>("/api/v1/me/jellyfin") });
  const [code, setCode] = useState<string | null>(null);
  const [note, setNote] = useState("");
  const [user, setUser] = useState("");
  const [pass, setPass] = useState("");
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => { if (timer.current) clearTimeout(timer.current); }, []);

  const done = (name?: string | null) => {
    toast(`Connected${name ? ` as ${name}` : ""}`);
    setCode(null);
    qc.invalidateQueries();
  };
  const quickConnect = async () => {
    if (timer.current) clearTimeout(timer.current);
    try {
      const got = await api.post<{ code: string; secret: string }>("/api/v1/me/jellyfin/quick-connect");
      setCode(got.code);
      setNote("Waiting for you to enter it…");
      const started = Date.now();
      const poll = async () => {
        try {
          const r = await api.post<{ linked: boolean; user_name?: string | null }>("/api/v1/me/jellyfin/quick-connect/check", { secret: got.secret });
          if (r.linked) return done(r.user_name);
        } catch (e: any) { setNote(e.message); return; }
        if (Date.now() - started > 5 * 60_000) { setNote("That code expired. Get a new one."); return; }
        timer.current = setTimeout(poll, 2500);
      };
      timer.current = setTimeout(poll, 2500);
    } catch (e: any) { toast(e.message, { error: true }); }
  };
  const password = async () => {
    try {
      const r = await api.post<{ user_name?: string | null }>("/api/v1/me/jellyfin/password", { username: user.trim(), password: pass });
      setPass("");
      done(r.user_name);
    } catch (e: any) { toast(e.message, { error: true }); }
  };
  const j = q.data;

  return (
    <SafeAreaView edges={["top"]} style={{ flex: 1, backgroundColor: colors.bg }}>
      <View style={st.head}>
        <Pressable onPress={() => router.back()} style={st.back}><Icon name="left" /></Pressable>
        <Text style={st.h1}>Jellyfin</Text>
      </View>
      <ScrollView contentContainerStyle={{ padding: gutter, gap: 12, paddingBottom: 60 }} keyboardShouldPersistTaps="handled">
        {q.isLoading ? <Loading /> : j?.can_play ? (
          <View style={st.panel}>
            <Icon name="tickFill" size={44} color={colors.ok} />
            <Text style={st.h2}>Connected{j.user_name ? ` as ${j.user_name}` : ""}</Text>
            <Text style={st.p}>To {j.server}. Your Jellyfin plays come into your history and Play opens VLC, Infuse or your own player.</Text>
            {/* The last plays read failed, or some plays have no date (not added). */}
            {j.plays_notice ? <Text style={[st.p, { color: "#f3d79a" }]}>{j.plays_notice}</Text> : null}
            <Button kind="danger" label="Disconnect" onPress={async () => {
              try {
                await api.del("/api/v1/me/jellyfin");
                qc.invalidateQueries();
              } catch (e: any) { toast(e.message, { error: true }); }
            }} style={{ marginTop: 8 }} />
          </View>
        ) : (
          <>
            <Text style={st.p}>{j?.linked ? "Your Jellyfin plays already come in. One more step: sign in once so Play can open VLC, Infuse or your own player." : `Connect your account on ${j?.server || "your Jellyfin"} so your plays come in and Play can open VLC or Infuse.`}</Text>
            <View style={st.panel}>
              <Text style={st.h2}>Quick Connect</Text>
              <Text style={st.p}>In a Jellyfin app where you’re signed in, open Settings → Quick Connect and type this code:</Text>
              <Text style={st.code}>{code || "······"}</Text>
              {note ? <Text style={st.small}>{note}</Text> : null}
              <Button kind="request" icon="key" label={code ? "New code" : "Get a code"} onPress={quickConnect} />
            </View>
            <View style={st.panel}>
              <Text style={st.h2}>Or sign in once</Text>
              <Text style={st.small}>Only the sign-in token is kept (encrypted). Your password isn’t stored.</Text>
              <TextInput {...caret} style={st.input} value={user} onChangeText={setUser} placeholder="Jellyfin username" placeholderTextColor={colors.text3} autoCapitalize="none" autoCorrect={false} />
              <TextInput {...caret} style={st.input} value={pass} onChangeText={setPass} placeholder="Password" placeholderTextColor={colors.text3} secureTextEntry />
              <Button label="Connect" onPress={password} disabled={!user.trim()} />
            </View>
          </>
        )}
      </ScrollView>
    </SafeAreaView>
  );
}

const st = StyleSheet.create({
  head: { flexDirection: "row", alignItems: "center", gap: 12, paddingHorizontal: gutter, paddingTop: 10 },
  back: { width: 44, height: 44, borderRadius: 22, backgroundColor: "rgba(255,255,255,0.07)", borderWidth: 1, borderColor: colors.line, alignItems: "center", justifyContent: "center" },
  h1: { color: colors.text, fontSize: 24, fontWeight: "800" },
  panel: { backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line, borderRadius: 18, padding: 16, gap: 10 },
  h2: { color: colors.text, fontSize: 19, fontWeight: "800" },
  p: { color: colors.text2, fontSize: 15, lineHeight: 21 },
  small: { color: colors.text3, fontSize: 13.5 },
  code: { color: colors.accent, fontSize: 40, fontWeight: "800", letterSpacing: 6, textAlign: "center", paddingVertical: 10 },
  input: { height: 50, borderRadius: 13, backgroundColor: colors.bg2, borderWidth: 1, borderColor: colors.line2, color: colors.text, paddingHorizontal: 14, fontSize: 16 },
});
