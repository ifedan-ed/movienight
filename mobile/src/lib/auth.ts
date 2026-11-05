// Signing in: Movie Night's own page (/app/connect, behind your sign-in)
// opens in a secure browser sheet; you confirm, the page hands back a
// one-time code, and the app swaps it for its own device token, proving
// with a PKCE verifier that it is the app that asked.
import * as Crypto from "expo-crypto";
import Constants from "expo-constants";
import * as Linking from "expo-linking";
import * as WebBrowser from "expo-web-browser";
import { Platform } from "react-native";

import { ApiError } from "./api";

function base64url(bytes: Uint8Array): string {
  let s = "";
  bytes.forEach((b) => { s += String.fromCharCode(b); });
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function b64ToUrl(b64: string): string {
  return b64.replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function normalizeServer(input: string): string {
  let s = input.trim();
  if (!s) return "";
  if (!/^https?:\/\//i.test(s)) s = `https://${s}`;
  return s.replace(/\/+$/, "");
}

function deviceName(): string {
  const name = Constants.deviceName || "";
  const os = Platform.OS === "ios" ? "iPhone" : Platform.OS === "android" ? "Android" : "Browser";
  return (name ? `${name} (Movie Night app)` : `Movie Night app on ${os}`).slice(0, 100);
}

export async function signIn(server: string): Promise<{ token: string; user: string; deviceId: number | null } | null> {
  const verifier = base64url(Crypto.getRandomBytes(48));
  const challenge = b64ToUrl(await Crypto.digestStringAsync(Crypto.CryptoDigestAlgorithm.SHA256, verifier, {
    encoding: Crypto.CryptoEncoding.BASE64,
  }));
  const redirect = Linking.createURL("auth");
  const url = `${server}/app/connect?challenge=${encodeURIComponent(challenge)}&redirect=${encodeURIComponent(redirect)}&device=${encodeURIComponent(deviceName())}`;
  // A sheet of its own on iOS, sharing no cookies with Safari, as other
  // apps do: with the shared session, signing out of the app left the SSO provider
  // signed in there, and the next Sign in went straight back into the last
  // account (2026-10-08).
  const result = await WebBrowser.openAuthSessionAsync(url, redirect, { preferEphemeralSession: true });
  if (result.type !== "success") return null;
  const parsed = Linking.parse(result.url);
  const code = parsed.queryParams?.code;
  if (typeof code !== "string" || !code) {
    if (parsed.queryParams?.error === "cancelled") return null;
    throw new ApiError(400, "Sign-in didn't finish; try again");
  }
  const res = await fetch(`${server}/api/v1/app/token`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Requested-With": "movienight" },
    body: JSON.stringify({ code, verifier }),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(res.status, data.detail || "Sign-in failed");
  return { token: data.token, user: data.user, deviceId: typeof data.device_id === "number" ? data.device_id : null };
}
