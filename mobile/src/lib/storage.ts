// Where the app keeps its server address and device token: the keychain /
// keystore on phones (expo-secure-store), localStorage on the web build.
import { Platform } from "react-native";
import * as SecureStore from "expo-secure-store";

const web = Platform.OS === "web";

export async function load(key: string): Promise<string | null> {
  if (web) {
    try { return globalThis.localStorage?.getItem(`mn:${key}`) ?? null; } catch { return null; }
  }
  return SecureStore.getItemAsync(key);
}

export async function save(key: string, value: string): Promise<void> {
  if (web) {
    try { globalThis.localStorage?.setItem(`mn:${key}`, value); } catch { /* private mode */ }
    return;
  }
  await SecureStore.setItemAsync(key, value);
}

export async function remove(key: string): Promise<void> {
  if (web) {
    try { globalThis.localStorage?.removeItem(`mn:${key}`); } catch { /* ignore */ }
    return;
  }
  await SecureStore.deleteItemAsync(key);
}
