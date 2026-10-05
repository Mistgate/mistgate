import { awgConfig, awgDevice, serverEntry } from "./logic";
import type { AwgConfig, AwgDevice, ServerEntry } from "./types";

// The self-service calls of the Amnezia section (subs/devices.go): POST, JSON in and out, under the subscription link.
// The page only ever talks to its own origin (CSP connect-src 'self'): the address in the page data is reduced to its
// path, so a page opened through a mirror domain still posts to itself. Nothing is stored; configs live in memory.

export class ApiError extends Error {
  constructor(
    readonly status: number,
    /** The panel's stable code ("device_limit", "too_many_requests", ...); "network" when nothing answered. */
    readonly code: string,
    readonly retryAfter = 0,
    /** Password entries left before "try later" (a wrong password only). */
    readonly left = 0,
  ) {
    super(code);
  }
}

export type Answer = { device?: AwgDevice; configs: AwgConfig[] };

/** `endpoints` is the page's base ("https://host/<prefix>/<token>/devices", or ".../unlock"); `path` is "" or "/<id>/configs". */
export function endpoint(endpoints: string, path: string, here: Location = location): string {
  let pathname = "";
  try {
    pathname = new URL(endpoints, here.href).pathname;
  } catch {
    // an unusable address: the call fails below with "network"
  }
  return here.origin + pathname + path;
}

async function post(endpoints: string, path: string, body: object): Promise<Record<string, unknown>> {
  let res: Response;
  try {
    res = await fetch(endpoint(endpoints, path), {
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "application/json" },
      body: JSON.stringify(body),
      // same-origin: the cookie of an unlocked page goes along (the page password, subs/unlock.go)
      credentials: "same-origin",
      referrerPolicy: "no-referrer",
      cache: "no-store",
    });
  } catch {
    throw new ApiError(0, "network");
  }
  let json: Record<string, unknown> = {};
  try {
    const v: unknown = await res.json();
    if (v && typeof v === "object") json = v as Record<string, unknown>;
  } catch {
    // the decoy answers a wrong link with a page, not JSON
  }
  if (!res.ok) {
    throw new ApiError(res.status, typeof json.error === "string" && json.error ? json.error : res.status === 404 ? "not_found" : "failed", Number(res.headers.get("Retry-After")) || Number(json.retry_after) || 0, Number(json.left) || 0);
  }
  return json;
}

const answer = (j: Record<string, unknown>): Answer => ({
  device: j.device ? awgDevice(j.device) : undefined,
  configs: Array.isArray(j.configs) ? j.configs.map(awgConfig) : [],
});

/** Enters the page password; resolves when the server set the cookie, throws ApiError(401 "wrong_password", left) or (429 "locked", retryAfter). */
export const unlock = async (url: string, password: string) => {
  await post(url, "", { password });
};

export const addDevice = async (endpoints: string, body: { profile_id: string; platform: string; label: string }) => answer(await post(endpoints, "", body));
export const getConfigs = async (endpoints: string, id: string) => answer(await post(endpoints, `/${encodeURIComponent(id)}/configs`, {}));
export const rotateKey = async (endpoints: string, id: string) => answer(await post(endpoints, `/${encodeURIComponent(id)}/rotate`, {}));
export const revokeDevice = async (endpoints: string, id: string) => {
  await post(endpoints, `/${encodeURIComponent(id)}/revoke`, {});
};
export const renameDevice = async (endpoints: string, id: string, label: string) => answer(await post(endpoints, `/${encodeURIComponent(id)}/rename`, { label }));

/** What the DNS call answers: the server as it is now, and the key devices that need a key with the new DNS. */
export type DnsAnswer = { server: ServerEntry | undefined; stale_devices: string[] };

/** `endpoint` is the page's ".../dns" address; `preset` "" brings back the server's default. */
export const setDns = async (url: string, body: { server: string; preset: string }): Promise<DnsAnswer> => {
  const j = await post(url, "", body);
  return { server: j.server && typeof j.server === "object" ? serverEntry(j.server) : undefined, stale_devices: Array.isArray(j.stale_devices) ? j.stale_devices.filter((x): x is string => typeof x === "string") : [] };
};
