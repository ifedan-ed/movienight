// The sign-in screen: the server's address, then whichever ways that server
// offers. With PASSWORD_SIGNIN on (GET /api/v1/auth/methods), a username and
// password typed here, above the browser sign-in (the SSO provider, or whatever
// OIDC_BUTTON_LABEL says); otherwise the browser sign-in alone, as before.
import { useEffect, useRef, useState } from "react";
import { Image, KeyboardAvoidingView, Platform, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";
import * as Haptics from "expo-haptics";
import { SafeAreaView } from "react-native-safe-area-context";

import { Button, useToast } from "@/components/ui";
import { takeSignOutReason } from "@/lib/api";
import { normalizeServer, signIn, signInMethods, signInWithPassword, type SignedIn } from "@/lib/auth";
import { useSession } from "@/lib/session";
import { caret, colors, gutter } from "@/lib/theme";
import type { SignInMethodsOut } from "@/lib/types";

// Asked once typing in the address has paused this long, not per letter.
const METHODS_DELAY_MS = 400;

export default function SignIn() {
  const { server, setSignedIn } = useSession();
  const [address, setAddress] = useState(server);
  // What the server at an address offers, kept with that address so an
  // answer for the one before an edit never shows.
  const [methods, setMethods] = useState<{ server: string; methods: SignInMethodsOut | null } | null>(null);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState<"password" | "browser" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [why] = useState(takeSignOutReason); // the server's words, when it signed this phone out
  const passwordRef = useRef<TextInput>(null);
  const toast = useToast();

  const srv = normalizeServer(address);
  useEffect(() => {
    if (!srv) return;
    const ctrl = new AbortController();
    const timer = setTimeout(async () => {
      const got = await signInMethods(srv, ctrl.signal);
      if (!ctrl.signal.aborted) setMethods({ server: srv, methods: got });
    }, METHODS_DELAY_MS);
    return () => { clearTimeout(timer); ctrl.abort(); };
  }, [srv]);
  // Unknown (not asked yet, no answer, an older server): the browser
  // sign-in alone, which every Movie Night version has.
  const offered = methods && methods.server === srv ? methods.methods : null;
  const withPassword = !!offered?.password;
  // The browser button is left out only where it could not help: passwords
  // on and no OIDC (its page would ask for the same password).
  const withBrowser = !offered || offered.oidc || !offered.password;
  const browserLabel = offered?.oidc && offered.oidc_label ? offered.oidc_label : "Sign in";

  const done = async (res: SignedIn) => {
    await setSignedIn(srv, res.token, res.deviceId);
    toast(`Signed in as ${res.user}`);
  };
  // Shown under the fields until the next edit (a toast would be gone
  // before a "wait 15 minutes" was read).
  const fail = (e: unknown) => {
    setError((e instanceof Error && e.message) || "Sign-in failed");
    Haptics.notificationAsync(Haptics.NotificationFeedbackType.Error).catch(() => {});
  };

  const goPassword = async () => {
    if (!srv || busy) return;
    if (!username.trim() || !password) {
      setError(!username.trim() ? "Type your username." : "Type your password.");
      return;
    }
    setBusy("password");
    setError(null);
    try {
      await done(await signInWithPassword(srv, username, password));
    } catch (e) {
      fail(e);
      setBusy(null);
    }
  };

  const goBrowser = async () => {
    if (!srv || busy) return;
    setBusy("browser");
    setError(null);
    try {
      const res = await signIn(srv);
      if (res) await done(res);
    } catch (e) {
      fail(e);
    }
    setBusy(null);
  };

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.bg }}>
      <KeyboardAvoidingView behavior={Platform.OS === "ios" ? "padding" : undefined} style={{ flex: 1 }}>
        <ScrollView contentContainerStyle={st.wrap} keyboardShouldPersistTaps="handled">
          {/* One column, phone-wide even on an iPad. */}
          <View style={st.col}>
            <Image source={require("../../assets/icon.png")} style={st.icon} />
            <Text style={st.h1}>Movie Night</Text>
            <Text style={st.p}>Sign in with your Movie Night account.</Text>
            {why ? <Text style={st.why}>{why}</Text> : null}
            <Text style={st.label}>Your Movie Night address</Text>
            <TextInput {...caret}
              style={st.input}
              value={address}
              onChangeText={(v) => { setAddress(v); setError(null); }}
              autoCapitalize="none"
              autoCorrect={false}
              keyboardType="url"
              textContentType="URL"
              returnKeyType={withPassword ? "next" : "go"}
              onSubmitEditing={withPassword ? undefined : goBrowser}
              placeholder="https://movienight.example.com"
              placeholderTextColor={colors.text3}
            />
            {withPassword ? (
              <View style={{ marginTop: 18 }}>
                {/* textContentType and autoComplete let iOS (and Android's
                    autofill) offer the password saved for this site: the
                    server's apple-app-site-association names this app. */}
                <Text style={st.label}>Username</Text>
                <TextInput {...caret}
                  style={st.input}
                  value={username}
                  onChangeText={(v) => { setUsername(v); setError(null); }}
                  autoCapitalize="none"
                  autoCorrect={false}
                  spellCheck={false}
                  textContentType="username"
                  autoComplete="username"
                  returnKeyType="next"
                  submitBehavior="submit"
                  onSubmitEditing={() => passwordRef.current?.focus()}
                  maxLength={200}
                />
                <Text style={[st.label, { marginTop: 12 }]}>Password</Text>
                <TextInput {...caret}
                  ref={passwordRef}
                  style={st.input}
                  value={password}
                  onChangeText={(v) => { setPassword(v); setError(null); }}
                  secureTextEntry
                  autoCapitalize="none"
                  autoCorrect={false}
                  textContentType="password"
                  autoComplete="current-password"
                  returnKeyType="go"
                  onSubmitEditing={goPassword}
                  maxLength={200}
                />
              </View>
            ) : null}
            {error ? <Text style={st.err} accessibilityRole="alert">{error}</Text> : null}
            {withPassword ? (
              <Button kind="request" label="Sign in" icon="user" busy={busy === "password"} disabled={!!busy} onPress={goPassword} style={{ marginTop: 16 }} />
            ) : null}
            {withPassword && withBrowser ? <Text style={st.or}>or</Text> : null}
            {withBrowser ? (
              <Button kind={withPassword ? "quiet" : "request"} label={browserLabel} icon={withPassword ? undefined : "user"}
                busy={busy === "browser"} disabled={!!busy} onPress={goBrowser} style={{ marginTop: withPassword ? 0 : 16 }} />
            ) : null}
            {withPassword ? <Text style={st.help}>No account yet, or forgot your password? Ask the person who runs this Movie Night.</Text> : null}
          </View>
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}

const st = StyleSheet.create({
  wrap: { flexGrow: 1, justifyContent: "center", paddingHorizontal: gutter + 8, paddingVertical: 24 },
  col: { width: "100%", maxWidth: 480, alignSelf: "center" },
  icon: { width: 88, height: 88, borderRadius: 22, alignSelf: "center", marginBottom: 18 },
  h1: { color: colors.text, fontSize: 32, fontWeight: "800", textAlign: "center" },
  p: { color: colors.text2, fontSize: 16, textAlign: "center", marginTop: 8, marginBottom: 28 },
  label: { color: colors.text2, fontSize: 14, fontWeight: "700", marginBottom: 6 },
  input: { height: 52, borderRadius: 13, backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line2, color: colors.text, paddingHorizontal: 14, fontSize: 16 },
  why: { color: "#f3d79a", fontSize: 14.5, textAlign: "center", marginTop: -16, marginBottom: 20 },
  err: { color: "#ffc2c2", fontSize: 15, marginTop: 14, padding: 12, borderRadius: 12, backgroundColor: "rgba(255,100,100,0.1)", borderWidth: 1, borderColor: "rgba(255,100,100,0.3)" },
  or: { color: colors.text3, fontSize: 14, textAlign: "center", marginVertical: 12 },
  help: { color: colors.text3, fontSize: 14, textAlign: "center", marginTop: 18 },
});
