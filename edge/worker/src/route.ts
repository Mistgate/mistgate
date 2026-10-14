import type { FrontEnv } from "./env";

// The router of the FRONT script (src/front.ts) of the split edition. It looks at the path and hands the request, unchanged, to the role that serves it through a service binding. It decides only
// "which role"; every role checks the path again in Go (canonical form, constant-time prefix) and answers anything that is not
// its own surface with the decoy, so a wrong decision here shows the decoy, never another surface.
//
// The order is the one of httpserver.Public (internal/panel/httpserver/server.go):
//   1. the secret link prefix                         -> LINK
//   2. the request's host is the admin host           -> ADMIN
//   3. the secret admin prefix                        -> ADMIN
//   4. everything else (subscriptions, decoy, odd paths) -> PUBLIC
// and, while there is no instance in the database (first run) or its settings cannot be read, everything goes to ADMIN: it is the
// only role that creates the instance, and it answers 503 itself when the database is not reachable.
//
// The prefixes are secrets kept in D1 (setting: link_prefix, admin_prefix, admin_host), not in a route table of the zone. They
// are read at most once per ROUTE_TTL_MS per isolate: on edge they do not change after the first run. A failed read keeps
// using the last good values.

/** How long the prefixes read from D1 are trusted. */
export const ROUTE_TTL_MS = 5 * 60_000;
/** After a failed read the old values are tried again no sooner than this. */
export const RETRY_MS = 5_000;

const SQL = "SELECT k, v FROM setting WHERE k IN ('link_prefix', 'admin_prefix', 'admin_host')";

/** What the router knows of the instance. A field the database does not hold (or holds in a shape no panel writes) is absent. */
export interface Routes {
  link?: string;
  admin?: string;
  adminHost?: string;
}

export type Role = "PUBLIC" | "ADMIN" | "LINK";

// The shape of internal/panel's validatePrefix and newSecretPrefix: /secret/, no empty segment, no "..". "/" (what an instance with
// an admin host stores as its admin prefix) is not a secret prefix and is not routed on.
const PREFIX = /^\/(?:[^\s/?#\\]+\/)+$/;
const HOST = /^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$/;
const validPrefix = (p: string | undefined): p is string => p !== undefined && p.length >= 4 && PREFIX.test(p) && !p.includes("..");

/** Reads the three settings into Routes. Null: there is no instance (none of the three is usable). */
export function parseRoutes(rows: readonly { k: unknown; v: unknown }[]): Routes | null {
  const get = (key: string) => {
    const row = rows.find((r) => r.k === key);
    return typeof row?.v === "string" ? row.v : undefined;
  };
  const link = get("link_prefix");
  const admin = get("admin_prefix");
  const host = get("admin_host")?.toLowerCase();
  const routes: Routes = {
    link: validPrefix(link) ? link : undefined,
    admin: validPrefix(admin) ? admin : undefined,
    adminHost: host !== undefined && HOST.test(host) ? host : undefined,
  };
  return routes.link || routes.admin || routes.adminHost ? routes : null;
}

type Equal = (a: ArrayBufferView, b: ArrayBufferView) => boolean;

export interface FrontOptions {
  /** The clock of the cache (ms); tests move it. */
  now?: () => number;
  /** crypto.subtle.timingSafeEqual; tests count its calls. */
  timingSafeEqual?: Equal;
}

const text = new TextEncoder();

/**
 * Compares a and b in time that does not depend on where they differ (timingSafeEqual wants equal lengths, so both are
 * zero-padded to the longer one and the lengths are compared separately).
 */
function constantTimeEqual(a: string, b: string, equal: Equal): boolean {
  const x = text.encode(a);
  const y = text.encode(b);
  const n = Math.max(x.length, y.length);
  const px = new Uint8Array(n);
  const py = new Uint8Array(n);
  px.set(x);
  py.set(y);
  return equal(px, py) && x.length === y.length;
}

/** Whether path starts with prefix, as httpserver.hasSecretPrefix does it: the first len(prefix) characters of the path against the prefix. */
export function hasSecretPrefix(path: string, prefix: string, equal: Equal): boolean {
  return constantTimeEqual(path.slice(0, prefix.length), prefix, equal);
}

/** Which role serves a request with this path and host. Routes null: no instance yet. */
export function pickRole(routes: Routes | null, pathname: string, hostname: string, equal: Equal): Role {
  if (!routes) return "ADMIN";
  if (routes.link && hasSecretPrefix(pathname, routes.link, equal)) return "LINK"; // the WebSocket upgrade too
  if (routes.adminHost && constantTimeEqual(hostname.toLowerCase(), routes.adminHost, equal)) return "ADMIN";
  // With an admin host the admin has no prefix (its stored prefix is "/", which parseRoutes drops).
  if (!routes.adminHost && routes.admin && hasSecretPrefix(pathname, routes.admin, equal)) return "ADMIN";
  return "PUBLIC";
}

/** A FRONT handler with its own route cache: the Worker below is one; the tests make more. */
export function createFront(options: FrontOptions = {}): ExportedHandler<FrontEnv> {
  const now = options.now ?? Date.now;
  const equal: Equal = options.timingSafeEqual ?? ((a, b) => crypto.subtle.timingSafeEqual(a, b));
  let cache: { routes: Routes; readAt: number } | undefined;
  let reading: Promise<Routes | null> | undefined;

  async function read(db: D1Database): Promise<Routes | null> {
    try {
      const { results } = await db.prepare(SQL).all<{ k: string; v: string }>();
      const routes = parseRoutes(results);
      // Not cached when there is no instance: the admin role creates it within seconds of the first request, and the
      // next request must see it. (Until then every request is one small read; nobody has anything to protect yet.)
      cache = routes ? { routes, readAt: now() } : undefined;
      return routes;
    } catch (error) {
      console.error("front: reading the routes failed:", error instanceof Error ? error.message : String(error));
      if (!cache) return null; // fail safe: the admin role, which answers 503 itself when the database is down
      cache.readAt = now() - ROUTE_TTL_MS + RETRY_MS; // keep the old values, try again soon
      return cache.routes;
    }
  }

  function routes(db: D1Database): Promise<Routes | null> {
    if (cache && now() - cache.readAt < ROUTE_TTL_MS) return Promise.resolve(cache.routes);
    // Concurrent requests share one read.
    reading ??= read(db).finally(() => {
      reading = undefined;
    });
    return reading;
  }

  return {
    async fetch(request, env) {
      const url = new URL(request.url);
      const role = pickRole(await routes(env.DB), url.pathname, url.hostname, equal);
      return env[role].fetch(request);
    },
  };
}
