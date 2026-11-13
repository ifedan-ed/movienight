// Signing in, two ways to the same device token (mn_app_...):
//  - the browser: Movie Night's own page (/app/connect, behind your sign-in)
//    opens in a secure browser sheet; you confirm, the page hands back a
//    one-time code, and the app swaps it for its own device token, proving
//    with a PKCE verifier that it is the app that asked;
//  - a username and password typed in the app (POST /api/v1/auth/password),
//    where the install has PASSWORD_SIGNIN on (GET /api/v1/auth/methods says).
import * as Crypto from "expo-crypto";
import Constants from "expo-constants";
import * as Linking from "expo-linking";
import * as WebBrowser from "expo-web-browser";
import { Platform } from "react-native";

import { ApiError } from "./api";
import type { AppTokenOut, PasswordSignInIn, SignInMethodsOut } from "./types";

export type SignedIn = { token: string; user: string; deviceId: number | null };

const NO_CONNECTION = "No connection — check your network and the address";

function signedIn(data: AppTokenOut): SignedIn {
  return { token: data.token, user: data.user, deviceId: typeof data.device_id === "number" ? data.device_id : null };
}

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

/** How this server lets people sign in, or null when it can't be told (no
 * answer, not a Movie Night, or one from before the password sign-in): the
 * screen then offers the browser sign-in only, which every version has. */
export async function signInMethods(server: string, signal: AbortSignal): Promise<SignInMethodsOut | null> {
  try {
    const res = await fetch(`${server}/api/v1/auth/methods`, { signal, headers: { Accept: "application/json" } });
    if (!res.ok) return null;
    const data = await res.json();
    return typeof data?.password === "boolean" ? (data as SignInMethodsOut) : null;
  } catch {
    return null;
  }
}

/** The username and password sign-in. Wrong pair, a username locked for 15
 * minutes after five wrong passwords, password sign-in turned off: each is
 * the server's own sentence, thrown as an ApiError. */
export async function signInWithPassword(server: string, username: string, password: string): Promise<SignedIn> {
  const body: PasswordSignInIn = { username, password, device: deviceName() };
  let res: Response;
  try {
    res = await fetch(`${server}/api/v1/auth/password`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Requested-With": "movienight" },
      body: JSON.stringify(body),
    });
  } catch {
    throw new ApiError(0, NO_CONNECTION);
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(res.status, (data && data.detail && String(data.detail)) || `Sign-in failed (${res.status})`);
  return signedIn(data as AppTokenOut);
}

export async function signIn(server: string): Promise<SignedIn | null> {
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
  return signedIn(data as AppTokenOut);
}
