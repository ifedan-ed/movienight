// Everything behind sign-in: the tabs, and the pages opened from them.
import { Stack } from "expo-router";

import { colors } from "@/lib/theme";

export const unstable_settings = { anchor: "(tabs)" };

export default function AppLayout() {
  return (
    <Stack screenOptions={{ headerShown: false, contentStyle: { backgroundColor: colors.bg }, animation: "slide_from_right" }}>
      <Stack.Screen name="(tabs)" />
      <Stack.Screen name="play/[session]" options={{ presentation: "modal" }} />
    </Stack>
  );
}
