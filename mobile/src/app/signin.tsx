import { useState } from "react";
import { Image, KeyboardAvoidingView, Platform, StyleSheet, Text, TextInput } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";

import { Button, useToast } from "@/components/ui";
import { takeSignOutReason } from "@/lib/api";
import { normalizeServer, signIn } from "@/lib/auth";
import { useSession } from "@/lib/session";
import { caret, colors, gutter } from "@/lib/theme";

export default function SignIn() {
  const { server, setSignedIn } = useSession();
  const [address, setAddress] = useState(server);
  const [busy, setBusy] = useState(false);
  const [why] = useState(takeSignOutReason); // the server's words, when it signed this phone out
  const toast = useToast();

  const go = async () => {
    const srv = normalizeServer(address);
    if (!srv) return;
    setBusy(true);
    try {
      const res = await signIn(srv);
      if (res) {
        await setSignedIn(srv, res.token, res.deviceId);
        toast(`Signed in as ${res.user}`);
      }
    } catch (e: any) {
      toast(e.message || "Sign-in failed", { error: true });
    }
    setBusy(false);
  };

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.bg }}>
      <KeyboardAvoidingView behavior={Platform.OS === "ios" ? "padding" : undefined} style={st.wrap}>
        <Image source={require("../../assets/icon.png")} style={st.icon} />
        <Text style={st.h1}>Movie Night</Text>
        <Text style={st.p}>Sign in with your Movie Night account.</Text>
        {why ? <Text style={st.why}>{why}</Text> : null}
        <Text style={st.label}>Your Movie Night address</Text>
        <TextInput {...caret}
          style={st.input}
          value={address}
          onChangeText={setAddress}
          autoCapitalize="none"
          autoCorrect={false}
          keyboardType="url"
          returnKeyType="go"
          onSubmitEditing={go}
          placeholder="https://movienight.example.com"
          placeholderTextColor={colors.text3}
        />
        <Button kind="request" label="Sign in" icon="user" busy={busy} onPress={go} style={{ marginTop: 14 }} />
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}

const st = StyleSheet.create({
  wrap: { flex: 1, justifyContent: "center", paddingHorizontal: gutter + 8 },
  icon: { width: 88, height: 88, borderRadius: 22, alignSelf: "center", marginBottom: 18 },
  h1: { color: colors.text, fontSize: 32, fontWeight: "800", textAlign: "center" },
  p: { color: colors.text2, fontSize: 16, textAlign: "center", marginTop: 8, marginBottom: 28 },
  label: { color: colors.text2, fontSize: 14, fontWeight: "700", marginBottom: 6 },
  input: { height: 52, borderRadius: 13, backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line2, color: colors.text, paddingHorizontal: 14, fontSize: 16 },
  why: { color: "#f3d79a", fontSize: 14.5, textAlign: "center", marginTop: -16, marginBottom: 20 },
});
