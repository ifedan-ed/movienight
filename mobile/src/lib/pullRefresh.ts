// The pull-to-refresh wheel, shown only for a pull the person made.
//
// Screens used to pass their query's isRefetching straight to RefreshControl's
// `refreshing`. isRefetching is also true when React Query reloads by itself
// (the app opening with saved data, coming back to the front, a write
// invalidating the screen), and on iPhone a wheel switched on with no pull
// stuck at the top of the page and pushed it down. So the wheel follows the
// refetch only from a pull until that refetch settles (as danitex's
// usePullRefresh, 2026-11-13).
import { useCallback, useState } from "react";

/**
 * `refresh` is what a pull does (its promise settling ends the wheel);
 * `refetching` is the screen's own isRefetching, when it has one: a pull
 * whose reload has already finished, or never started, shows no wheel.
 * Screens with no single query (Home invalidates everything) leave it out
 * and the wheel runs for as long as `refresh` does.
 */
export function usePullRefresh(refresh: () => Promise<unknown>, refetching?: boolean) {
  const [pulled, setPulled] = useState(false);
  const onRefresh = useCallback(() => {
    setPulled(true);
    // Errors show on the screen itself (its ErrorBox); the wheel just stops.
    refresh().catch(() => {}).finally(() => setPulled(false));
  }, [refresh]);
  return { refreshing: pulled && (refetching ?? true), onRefresh };
}
