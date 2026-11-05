// Small shared pieces: buttons, chips, segmented control, section titles,
// empty and error states, and the toast that confirms every tap.
import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from "react";
import { ActivityIndicator, Animated, Pressable, StyleSheet, Text, View, type StyleProp, type ViewStyle } from "react-native";
import * as Haptics from "expo-haptics";

import { colors, gutter } from "../lib/theme";
import { Icon, type IconName } from "./Icon";

export function tap() {
  Haptics.selectionAsync().catch(() => {});
}

type Kind = "play" | "request" | "status" | "ghost" | "quiet" | "danger";

export function Button({ label, icon, kind = "quiet", onPress, disabled, busy, small, style, testID }: {
  label: string; icon?: IconName; kind?: Kind; onPress?: () => void; disabled?: boolean; busy?: boolean; small?: boolean;
  style?: StyleProp<ViewStyle>; testID?: string;
}) {
  const look = BUTTON[kind];
  return (
    <Pressable
      accessibilityRole="button"
      testID={testID}
      disabled={disabled || busy}
      onPress={() => { tap(); onPress?.(); }}
      style={({ pressed }) => [styles.btn, small && styles.btnSmall, look.box, (disabled || busy) && { opacity: 0.6 }, pressed && { transform: [{ scale: 0.985 }] }, style]}
    >
      {busy ? <ActivityIndicator color={look.text.color as string} /> : icon ? <Icon name={icon} size={small ? 18 : 22} color={look.text.color as string} /> : null}
      <Text style={[styles.btnText, small && { fontSize: 15 }, look.text]} numberOfLines={1}>{label}</Text>
    </Pressable>
  );
}

const BUTTON: Record<Kind, { box: ViewStyle; text: { color: string } }> = {
  play: { box: { backgroundColor: "#fff" }, text: { color: "#0b0d12" } },
  request: { box: { backgroundColor: colors.accent }, text: { color: colors.accentInk } },
  status: { box: { backgroundColor: colors.infoSoft, borderColor: "rgba(121,168,255,0.4)" }, text: { color: "#d6e4ff" } },
  ghost: { box: { backgroundColor: "rgba(255,255,255,0.1)", borderColor: colors.line2 }, text: { color: colors.text } },
  quiet: { box: { backgroundColor: colors.surface, borderColor: colors.line }, text: { color: colors.text } },
  danger: { box: { backgroundColor: "rgba(255,100,100,0.12)", borderColor: "rgba(255,100,100,0.35)" }, text: { color: colors.danger } },
};

export function Chip({ label, on, icon, onPress }: { label: string; on?: boolean; icon?: IconName; onPress?: () => void }) {
  return (
    <Pressable onPress={() => { tap(); onPress?.(); }} style={[styles.chip, on && styles.chipOn]} accessibilityRole="button" accessibilityState={{ selected: !!on }}>
      {icon ? <Icon name={icon} size={18} color={on ? colors.accent : colors.text2} /> : null}
      <Text style={[styles.chipText, on && { color: colors.accent }]}>{label}</Text>
    </Pressable>
  );
}

export function Segmented<T extends string>({ value, options, onChange }: { value: T; options: [T, string][]; onChange: (v: T) => void }) {
  return (
    <View style={styles.seg}>
      {options.map(([v, label]) => (
        <Pressable key={v} onPress={() => { if (v !== value) { tap(); onChange(v); } }} style={[styles.segBtn, v === value && styles.segOn]}>
          <Text style={[styles.segText, v === value && { color: colors.text }]}>{label}</Text>
        </Pressable>
      ))}
    </View>
  );
}

export function SectionTitle({ title, sub, kicker, right }: { title: string; sub?: string | null; kicker?: string | null; right?: ReactNode }) {
  return (
    <View style={styles.sec}>
      <View style={styles.secBar} />
      <View style={{ flex: 1 }}>
        {kicker ? <Text style={styles.kicker}>{kicker.toUpperCase()}</Text> : null}
        <Text style={styles.secTitle} numberOfLines={2}>{title}</Text>
        {sub ? <Text style={styles.secSub}>{sub}</Text> : null}
      </View>
      {right}
    </View>
  );
}

export function Empty({ title, text }: { title: string; text?: string }) {
  return (
    <View style={styles.empty}>
      <Text style={styles.emptyTitle}>{title}</Text>
      {text ? <Text style={styles.emptyText}>{text}</Text> : null}
    </View>
  );
}

export function ErrorBox({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <View style={styles.err}>
      <Text style={{ color: "#ffc2c2", fontSize: 15 }}>{message}</Text>
      {onRetry ? <Button label="Try again" small kind="quiet" onPress={onRetry} style={{ marginTop: 10, alignSelf: "flex-start" }} /> : null}
    </View>
  );
}

export function Loading() {
  return <View style={{ padding: 30, alignItems: "center" }}><ActivityIndicator color={colors.accent} /></View>;
}

// --- toasts ----------------------------------------------------------------

type ToastFn = (message: string, opts?: { error?: boolean }) => void;
const ToastContext = createContext<ToastFn>(() => {});

export function useToast() {
  return useContext(ToastContext);
}

export function ToastHost({ children }: { children: ReactNode }) {
  const [msg, setMsg] = useState<{ text: string; error?: boolean } | null>(null);
  const [opacity] = useState(() => new Animated.Value(0));
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const show = useCallback<ToastFn>((text, opts) => {
    setMsg({ text, error: opts?.error });
    if (timer.current) clearTimeout(timer.current);
    Animated.timing(opacity, { toValue: 1, duration: 160, useNativeDriver: true }).start();
    timer.current = setTimeout(() => Animated.timing(opacity, { toValue: 0, duration: 200, useNativeDriver: true }).start(() => setMsg(null)), 2800);
    if (opts?.error) Haptics.notificationAsync(Haptics.NotificationFeedbackType.Error).catch(() => {});
  }, [opacity]);
  return (
    <ToastContext.Provider value={show}>
      {children}
      {msg ? (
        <Animated.View pointerEvents="none" style={[styles.toast, msg.error && styles.toastErr, { opacity }]}>
          <Text style={[styles.toastText, msg.error && { color: "#4a0a0a" }]}>{msg.text}</Text>
        </Animated.View>
      ) : null}
    </ToastContext.Provider>
  );
}

export const styles = StyleSheet.create({
  btn: { height: 54, borderRadius: 15, paddingHorizontal: 18, flexDirection: "row", alignItems: "center", justifyContent: "center", gap: 10, borderWidth: 1, borderColor: "transparent" },
  btnSmall: { height: 40, borderRadius: 12, paddingHorizontal: 14 },
  btnText: { fontSize: 17, fontWeight: "700" },
  chip: { height: 40, paddingHorizontal: 16, borderRadius: 999, flexDirection: "row", alignItems: "center", gap: 8, backgroundColor: "rgba(255,255,255,0.07)", borderWidth: 1, borderColor: colors.line },
  chipOn: { backgroundColor: colors.accentSoft, borderColor: "rgba(245,183,59,0.45)" },
  chipText: { color: colors.text2, fontWeight: "600", fontSize: 15 },
  seg: { flexDirection: "row", backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.line, borderRadius: 13, padding: 4, marginHorizontal: gutter, marginVertical: 8 },
  segBtn: { flex: 1, height: 40, borderRadius: 10, alignItems: "center", justifyContent: "center" },
  segOn: { backgroundColor: colors.surface3 },
  segText: { fontWeight: "700", fontSize: 15, color: colors.text3 },
  sec: { flexDirection: "row", alignItems: "center", gap: 11, paddingHorizontal: gutter, marginTop: 24, marginBottom: 12 },
  secBar: { width: 4, alignSelf: "stretch", borderRadius: 2, backgroundColor: colors.accent },
  kicker: { color: colors.accent, fontSize: 12, fontWeight: "800", letterSpacing: 1 },
  secTitle: { color: colors.text, fontSize: 21, fontWeight: "800" },
  secSub: { color: colors.text3, fontSize: 14, marginTop: 1 },
  empty: { alignItems: "center", padding: 36 },
  emptyTitle: { color: colors.text, fontSize: 18, fontWeight: "700", marginBottom: 6, textAlign: "center" },
  emptyText: { color: colors.text3, fontSize: 15.5, textAlign: "center" },
  err: { margin: gutter, padding: 14, borderRadius: 14, backgroundColor: "rgba(255,100,100,0.1)", borderWidth: 1, borderColor: "rgba(255,100,100,0.3)" },
  toast: { position: "absolute", left: 20, right: 20, bottom: 110, backgroundColor: "#f3f4f7", borderRadius: 14, paddingVertical: 12, paddingHorizontal: 14 },
  toastErr: { backgroundColor: "#ffd9d9" },
  toastText: { color: "#0d0f14", fontWeight: "600", fontSize: 15 },
});
