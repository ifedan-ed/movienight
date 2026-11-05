// The web app's palette and type scale, so the two look like one product.
import { Platform } from "react-native";

export const colors = {
  bg: "#0d0f14",
  bg2: "#12151c",
  surface: "#171b24",
  surface2: "#1e2330",
  surface3: "#2a3041",
  line: "rgba(255,255,255,0.08)",
  line2: "rgba(255,255,255,0.15)",
  text: "#f3f4f7",
  text2: "#b9bfcb",
  text3: "#8a91a1",
  accent: "#f5b73b",
  accentInk: "#1b1303",
  accentSoft: "rgba(245,183,59,0.15)",
  ok: "#3ad483",
  okInk: "#062a16",
  info: "#79a8ff",
  infoSoft: "rgba(121,168,255,0.15)",
  danger: "#ff6464",
  fav: "#ff7a8a",
};

// The text cursor in the accent, on every text box: Android draws the caret
// from cursorColor and the selection from selectionColor (kept see-through so
// selected text stays readable); iOS draws both from selectionColor.
export const caret = Platform.select({
  android: { cursorColor: colors.accent, selectionColor: "rgba(245,183,59,0.35)" },
  default: { selectionColor: colors.accent },
});

export const gutter = 16;
export const radius = 14;

export const TMDB_IMG = "https://image.tmdb.org/t/p/";

export function img(path?: string | null, size = "w342"): string | undefined {
  return path ? `${TMDB_IMG}${size}${path}` : undefined;
}
