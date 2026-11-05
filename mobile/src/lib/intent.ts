// Android intent: links (the stream, its type, the player's package and
// extras such as the subtitle file), as the server writes them for VLC on
// Android. Only browsers understand them; Linking.openURL can't start one,
// so on Android the app takes the link apart and starts that intent itself.
import * as IntentLauncher from "expo-intent-launcher";

// The player screen that takes a stream directly, for apps that name one
// (VideoLAN's own instructions for starting VLC from another app).
const PLAYER_SCREEN: Record<string, string> = { "org.videolan.vlc": "org.videolan.vlc.gui.video.VideoPlayerActivity" };

export const isIntentLink = (url: string) => /^intent:/i.test(url);

function decode(v: string): string {
  try { return decodeURIComponent(v); } catch { return v; }
}

/** Starts an intent: link; throws when no app on the phone takes it. */
export async function openIntentLink(url: string): Promise<void> {
  const at = url.indexOf("#Intent;");
  if (at < 0) throw new Error("Not an intent link");
  const fields: Record<string, string> = {};
  const extra: Record<string, string> = {};
  for (const part of url.slice(at + "#Intent;".length).split(";")) {
    const eq = part.indexOf("=");
    if (eq < 0) continue;
    const key = part.slice(0, eq);
    if (key.startsWith("S.")) extra[key.slice(2)] = decode(part.slice(eq + 1));  // text extras only
    else fields[key] = decode(part.slice(eq + 1));
  }
  const rest = url.slice("intent:".length, at);
  const [pkg, cls] = fields.component ? fields.component.split("/") : [fields.package, fields.package && PLAYER_SCREEN[fields.package]];
  await IntentLauncher.startActivityAsync(fields.action || "android.intent.action.VIEW", {
    data: fields.scheme ? `${fields.scheme}:${rest}` : undefined,
    type: fields.type,
    packageName: cls ? pkg : undefined,
    className: cls ? (cls.startsWith(".") ? pkg + cls : cls) : undefined,
    extra,
  });
}
