// The sign-in sheet hands its one-time code back on <scheme>://auth; the
// sign-in screen reads it itself (WebBrowser.openAuthSessionAsync). On
// Android that same link also reaches the router, so this screen takes it
// and steps straight back instead of an "Unmatched Route" page. On the web
// it is the page the sign-in popup lands on and hands the code back from.
import { useEffect } from "react";
import { Platform } from "react-native";
import { router } from "expo-router";
import * as WebBrowser from "expo-web-browser";

export default function AuthReturn() {
  useEffect(() => {
    if (Platform.OS === "web" && WebBrowser.maybeCompleteAuthSession().type === "success") return;
    if (router.canGoBack()) router.back();
    else router.replace("/");
  }, []);
  return null;
}
