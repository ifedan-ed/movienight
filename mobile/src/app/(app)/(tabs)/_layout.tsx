import { Tabs } from "expo-router";

import { Icon, type IconName } from "@/components/Icon";
import { colors } from "@/lib/theme";

const TABS: [string, string, IconName][] = [
  ["index", "Home", "home"],
  ["discover", "Discover", "compass"],
  ["search", "Search", "search"],
  ["lists", "Lists", "lists"],
  ["profile", "Profile", "user"],
];

export default function TabLayout() {
  return (
    <Tabs
      screenOptions={{
        headerShown: false,
        tabBarActiveTintColor: colors.accent,
        tabBarInactiveTintColor: colors.text3,
        tabBarStyle: { backgroundColor: "rgba(23,27,36,0.97)", borderTopColor: colors.line2 },
        tabBarLabelStyle: { fontSize: 11.5, fontWeight: "700" },
      }}
    >
      {TABS.map(([name, title, icon]) => (
        <Tabs.Screen key={name} name={name} options={{ title, tabBarIcon: ({ color }) => <Icon name={icon} size={25} color={String(color)} /> }} />
      ))}
    </Tabs>
  );
}
