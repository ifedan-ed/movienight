// The App Store screenshots' way in: movienight://e2e-signin?token=<token>
// signs the test build in as the screenshot user, with a device token minted
// for the run (scripts/test-session.sh). It works only in builds made with
// EXPO_PUBLIC_E2E=1 (eas.json's e2e profile); in every other build the value
// is inlined as undefined, this page only goes home, and the sign-in code is
// dropped.
import { useEffect } from "react";
import { router, useLocalSearchParams } from "expo-router";

import { useSession } from "@/lib/session";

const E2E = process.env.EXPO_PUBLIC_E2E === "1";

export default function E2ESignIn() {
  const { token } = useLocalSearchParams<{ token?: string }>();
  const { server, setSignedIn } = useSession();
  useEffect(() => {
    if (E2E && token) setSignedIn(server, token, null).then(() => router.replace("/"));
    else router.replace("/");
  }, [token, server, setSignedIn]);
  return null;
}
