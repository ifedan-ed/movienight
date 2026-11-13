// Everything behind sign-in: the tabs, and the pages opened from them.
import { Stack } from "expo-router";

import { colors } from "@/lib/theme";
import { useReportTimeZone } from "@/lib/timeZone";

export const unstable_settings = { anchor: "(tabs)" };

export default function AppLayout() {
  useReportTimeZone(); // signed in: this person's days follow the phone's zone
  return (
    <Stack screenOptions={{ headerShown: false, contentStyle: { backgroundColor: colors.bg }, animation: "slide_from_right" }}>
      <Stack.Screen name="(tabs)" />
      <Stack.Screen name="play/[session]" options={{ presentation: "modal" }} />
    </Stack>
  );
}
