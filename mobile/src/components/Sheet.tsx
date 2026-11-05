// A bottom sheet (rate, play, add to list, finished it?).
import { useEffect, type ReactNode } from "react";
import { KeyboardAvoidingView, Modal, Platform, Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { holdBusy } from "../lib/busy";
import { colors, gutter } from "../lib/theme";

export function Sheet({ visible, onClose, title, sub, children }: {
  visible: boolean; onClose: () => void; title?: string; sub?: string | null; children: ReactNode;
}) {
  const insets = useSafeAreaInsets();
  // An open sheet is something in progress: no update restarts the app under it.
  useEffect(() => (visible ? holdBusy() : undefined), [visible]);
  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={onClose} statusBarTranslucent>
      <KeyboardAvoidingView behavior={Platform.OS === "ios" ? "padding" : undefined} style={{ flex: 1 }}>
        <Pressable style={st.bg} onPress={onClose} accessibilityLabel="Close" />
        <View style={[st.sheet, { paddingBottom: 20 + insets.bottom }]}>
          <View style={st.grab} />
          <ScrollView keyboardShouldPersistTaps="handled" bounces={false}>
            {title ? <Text style={st.title}>{title}</Text> : null}
            {sub ? <Text style={st.sub}>{sub}</Text> : null}
            {children}
          </ScrollView>
        </View>
      </KeyboardAvoidingView>
    </Modal>
  );
}

const st = StyleSheet.create({
  bg: { flex: 1, backgroundColor: "rgba(0,0,0,0.6)" },
  sheet: { backgroundColor: colors.bg2, borderTopLeftRadius: 24, borderTopRightRadius: 24, borderWidth: 1, borderColor: colors.line2, borderBottomWidth: 0,
    paddingHorizontal: gutter, paddingTop: 8, maxHeight: "88%" },
  grab: { width: 40, height: 5, borderRadius: 9, backgroundColor: colors.surface3, alignSelf: "center", marginBottom: 12 },
  title: { color: colors.text, fontSize: 21, fontWeight: "800", marginBottom: 4 },
  sub: { color: colors.text3, fontSize: 14.5, marginBottom: 14 },
});
