import { Code, ConnectError, type Interceptor } from "@connectrpc/connect";
import { useSyncExternalStore } from "react";

// A tab can outlive a panel update: the old JS stays in memory while the API (and the profile schemas it serves) is already
// the new version's. The panel stamps its version on the app shell (<meta name="mistgate-version">, internal/panel/httpserver/spa.go)
// and on every API answer to a signed-in admin (X-Mistgate-Version). Different values = this tab is stale.
// No meta (the Vite dev server, an older panel) or no header (signed out) = nothing to compare, nothing is shown.

const metaName = "mistgate-version";
const header = "x-mistgate-version";

/** The version of the panel that served this page's index.html; "" when it did not say. */
export const loadedVersion = (() => {
  try {
    return document.querySelector<HTMLMetaElement>(`meta[name="${metaName}"]`)?.content.trim() ?? "";
  } catch {
    return "";
  }
})();

/** The panel version the page is stale against (the one the server reports now), or "" when it is not stale or cannot tell. */
export function staleVersion(loaded: string, seen: string): string {
  return loaded && seen && loaded !== seen ? seen : "";
}

let seen = "";
const listeners = new Set<() => void>();

/** Called with every API answer's headers; remembers the newest version the server reported. */
export function noteServerVersion(headers: Headers | undefined) {
  const v = headers?.get(header)?.trim();
  if (!v || v === seen) return;
  seen = v;
  listeners.forEach((l) => l());
}

/** The transport interceptor: reads the version off successful answers and off errors (the failing save is the one that matters). */
export const versionInterceptor: Interceptor = (next) => async (req) => {
  try {
    const res = await next(req);
    noteServerVersion(res.header);
    return res;
  } catch (e) {
    const c = ConnectError.from(e);
    if (c.code !== Code.Canceled) noteServerVersion(c.metadata);
    throw e;
  }
};

/** The version this tab is stale against, or "" while it is current. */
export function useStaleVersion(): string {
  return useSyncExternalStore(
    (cb) => {
      listeners.add(cb);
      return () => listeners.delete(cb);
    },
    () => staleVersion(loadedVersion, seen),
  );
}

export const reloadPage = () => window.location.reload();
