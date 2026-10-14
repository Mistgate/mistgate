import { readFileSync } from "node:fs";
import { join } from "node:path";
import { convertV4MiniflareOptions, Miniflare, type V4WorkerOptions as WorkerOptions } from "miniflare";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

// Loads the six scripts of the signed edge bundle exactly as they were built (front.js alone, and <role>.js with <role>.wasm as
// a CompiledWasm module for the five Go roles), wired as edge.json says, and sends a real request per role through FRONT:
//
//   empty database, first request  -> ADMIN (creates the instance)
//   the setup token, the admin API -> ADMIN, and a call that ADMIN forwards to the Provisioner object of PROVISION
//   the admin SPA                  -> ADMIN (static assets)
//   a subscription                 -> PUBLIC
//   a link upgrade                 -> LINK -> the node's NodeLink object in FRONT -> PanelLink in LINK (the challenge frame)
//   a cron tick                    -> JOBS (a user whose expiry has passed becomes EXPIRED)
//
// MISTGATE_EDGE_BUNDLE is the directory the tarball was extracted to (scripts/test-edge-bundle.ps1 and the release workflow set
// it); without it the suite is skipped, so a plain `pnpm test` stays fast and needs no Go build.
const dir = process.env.MISTGATE_EDGE_BUNDLE;

interface Script {
  role: string;
  default_name: string;
  modules: { name: string; type: string }[];
  durable_objects?: { binding: string; class_name: string }[];
  durable_objects_external?: { binding: string; class_name: string; script_role: string }[];
  services?: { binding: string; service_role: string; entrypoint?: string }[];
  secrets?: string[];
  vars?: string[];
  d1: { binding: string }[];
  assets?: { directory: string; binding: string };
}
interface EdgeJson {
  schema: number;
  compatibility_date: string;
  compatibility_flags: string[];
  scripts: Script[];
}

const MASTER_KEY = "0123456789abcdef".repeat(4);
const ADMIN_PREFIX = "/adm-secret/";
const SUB_PREFIX = "/sub-secret/";
const ORIGIN = "https://panel.example.test";
const SETUP_TOKEN = "ab".repeat(32);

const hex = (bytes: ArrayBuffer) => [...new Uint8Array(bytes)].map((b) => b.toString(16).padStart(2, "0")).join("");

/** An RFC 6238 code (SHA-1, 30 s, 6 digits) of a base32 secret: the authenticator the first admin enrols. */
async function totp(secret: string, at = Date.now()): Promise<string> {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const c of secret.toUpperCase().replace(/=+$/, "")) bits += alphabet.indexOf(c).toString(2).padStart(5, "0");
  const key = new Uint8Array(bits.length >> 3);
  for (let i = 0; i < key.length; i++) key[i] = parseInt(bits.slice(i * 8, i * 8 + 8), 2);
  const counter = new DataView(new ArrayBuffer(8));
  counter.setBigUint64(0, BigInt(Math.floor(at / 30_000)));
  const hmac = await crypto.subtle.importKey("raw", key, { name: "HMAC", hash: "SHA-1" }, false, ["sign"]);
  const mac = new Uint8Array(await crypto.subtle.sign("HMAC", hmac, counter));
  const o = mac[mac.length - 1]! & 15;
  const n = ((mac[o]! & 0x7f) << 24) | (mac[o + 1]! << 16) | (mac[o + 2]! << 8) | mac[o + 3]!;
  return String(n % 1_000_000).padStart(6, "0");
}

describe.skipIf(!dir)("the built edge bundle in workerd", () => {
  let mf: Miniflare;
  let edge: EdgeJson;
  let log: { level: string; message: string }[];
  let cookie = "";

  const script = (role: string) => edge.scripts.find((s) => s.role === role)!;
  const name = (role: string) => script(role).default_name;

  /** One worker of the Miniflare instance, from the bundle's own edge.json: modules, bindings, objects, services, assets. */
  function worker(s: Script): WorkerOptions {
    // exactly the secrets and vars edge.json lists for the script (SETUP_TOKEN_SHA256 is filled in by beforeAll)
    const values: Record<string, string> = { MASTER_KEY, ADMIN_PREFIX, SUB_PREFIX, PUBLIC_URL: ORIGIN, SETUP_TOKEN_SHA256: "", ADMIN_HOST: "" };
    const bindings: Record<string, string> = {};
    for (const key of [...(s.secrets ?? []), ...(s.vars ?? [])]) {
      if (!(key in values)) throw new Error(`${s.role}: edge.json lists ${key}, which this test has no value for`);
      if (key !== "ADMIN_HOST") bindings[key] = values[key]!; // no admin host: the admin is under its secret prefix
    }
    return {
      name: s.default_name,
      modulesRoot: dir!,
      modules: s.modules.map((m) => ({ type: m.type === "esm" ? "ESModule" : "CompiledWasm", path: join(dir!, m.name) })),
      compatibilityDate: edge.compatibility_date,
      compatibilityFlags: edge.compatibility_flags,
      bindings,
      d1Databases: Object.fromEntries(s.d1.map((d) => [d.binding, "mistgate-test"])),
      durableObjects: {
        ...Object.fromEntries((s.durable_objects ?? []).map((d) => [d.binding, { className: d.class_name, useSQLite: true }])),
        ...Object.fromEntries((s.durable_objects_external ?? []).map((d) => [d.binding, { className: d.class_name, scriptName: name(d.script_role) }])),
      },
      serviceBindings: Object.fromEntries(
        (s.services ?? []).map((v) => [v.binding, v.entrypoint ? { name: name(v.service_role), entrypoint: v.entrypoint } : name(v.service_role)]),
      ),
      ...(s.assets
        ? {
            assets: {
              directory: join(dir!, s.assets.directory),
              binding: s.assets.binding,
              // what the wrangler config says: every request goes to the Worker first, only an exact file path is a hit
              run_worker_first: true,
              routerConfig: { has_user_worker: true, invoke_user_worker_ahead_of_assets: true },
              assetConfig: { html_handling: "none" as const, not_found_handling: "none" as const },
            },
          }
        : {}),
    };
  }

  /** The admin API through FRONT, as the browser or the e2e script calls it. */
  async function api(method: string, body: unknown = {}, init: { cookie?: boolean } = {}): Promise<{ status: number; json: any; headers: Headers }> {
    const response = await mf.dispatchFetch(`${ORIGIN}${ADMIN_PREFIX}api/mistgate.admin.v1.${method}`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...(init.cookie !== false && cookie ? { Cookie: cookie } : {}) },
      body: JSON.stringify(body),
    });
    const text = await response.text();
    let json: unknown = text;
    try {
      json = JSON.parse(text);
    } catch {
      /* a body that is not JSON stays a string */
    }
    return { status: response.status, json, headers: response.headers as unknown as Headers };
  }

  const settings = async () => {
    const db = await mf.getD1Database("DB", name("front"));
    const rows = await db.prepare("SELECT k, v FROM setting WHERE k IN ('link_prefix','admin_prefix','sub_prefix')").all<{ k: string; v: string }>();
    return Object.fromEntries(rows.results.map((r) => [r.k, r.v]));
  };

  beforeAll(async () => {
    edge = JSON.parse(readFileSync(join(dir!, "edge.json"), "utf8"));
    expect(edge.schema).toBe(2);
    expect(edge.scripts.map((s) => s.role)).toEqual(["link", "provision", "public", "admin", "jobs", "front"]);
    log = [];
    const workers = edge.scripts.map(worker);
    // the setup token's hash goes to the admin script; FRONT is first: dispatchFetch enters there, like the route of the install
    const admin = workers.find((w) => w.name === name("admin"))!;
    (admin.bindings as Record<string, string>).SETUP_TOKEN_SHA256 = hex(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(SETUP_TOKEN)));
    workers.sort((a, b) => (a.name === name("front") ? -1 : b.name === name("front") ? 1 : 0));
    mf = new Miniflare(
      convertV4MiniflareOptions({
        workers,
        handleStructuredLogs: (entry) => log.push({ level: entry.level, message: entry.message }),
      }),
    );
    await mf.ready;
  }, 180_000);

  afterAll(async () => {
    await mf?.dispose();
  });

  it("an empty database: the first request goes to ADMIN, which creates the instance, and answers the decoy", async () => {
    const db = await mf.getD1Database("DB", name("front"));
    await expect(db.prepare("SELECT k FROM setting").all()).rejects.toThrow(/no such table/);
    // the only role that may start a panel on an empty database is the admin's: the others refuse ("not initialised"), so a
    // 200 here is the admin role's answer, and the instance exists afterwards
    const response = await mf.dispatchFetch(`${ORIGIN}/`);
    const body = await response.text();
    expect(response.status, body.slice(0, 500)).toBe(200);
    expect(response.headers.get("content-type")).toMatch(/text\/html/);
    expect(body).not.toMatch(/setup|admin|sign in/i);
    const stored = await settings();
    expect(stored.admin_prefix).toBe(ADMIN_PREFIX);
    expect(stored.sub_prefix).toBe(SUB_PREFIX);
    expect(stored.link_prefix).toMatch(/^\/[\w-]+\/$/);
  }, 180_000);

  it("the setup token creates the first admin through ADMIN; a token nobody offered is refused", async () => {
    const stranger = await api("AuthService/BeginSetup", {
      setupToken: "cd".repeat(32),
      displayName: "Stranger",
      method: "SETUP_METHOD_PASSWORD",
      login: "stranger",
      password: "stranger-password-1",
    });
    expect(stranger.status).not.toBe(200);
    const begin = await api("AuthService/BeginSetup", {
      setupToken: SETUP_TOKEN,
      displayName: "Bundle admin",
      method: "SETUP_METHOD_PASSWORD",
      login: "bundle-admin",
      password: "bundle-admin-password-1",
    });
    expect(begin.status, JSON.stringify(begin.json)).toBe(200);
    const finish = await api("AuthService/FinishSetup", { setupToken: SETUP_TOKEN, ceremonyId: begin.json.ceremonyId, totpCode: await totp(begin.json.totpSecret) });
    expect(finish.status, JSON.stringify(finish.json)).toBe(200);
    const setCookie = finish.headers.get("set-cookie") ?? "";
    cookie = /(__Host-sid=[^;]+)/.exec(setCookie)?.[1] ?? "";
    expect(cookie).not.toBe("");
    const me = await api("AuthService/Me");
    expect(me.status).toBe(200);
    expect(me.json.admin.id).toBeTruthy();
  }, 120_000);

  it("the admin SPA is ADMIN's: its static assets under the secret prefix", async () => {
    const response = await mf.dispatchFetch(`${ORIGIN}${ADMIN_PREFIX}`, { headers: { Cookie: cookie } });
    const body = await response.text();
    expect(response.status, body.slice(0, 300)).toBe(200);
    expect(response.headers.get("content-type")).toMatch(/text\/html/);
    expect(body).toMatch(/<script/);
  }, 60_000);

  let subscription = "";
  let userId = "";

  it("a subscription is PUBLIC's: a user made through ADMIN is served with the Happ and the Mihomo headers", async () => {
    const protocols = await api("ProfileService/ListProtocols", {});
    const settingsJson = JSON.stringify({
      ...JSON.parse(protocols.json.protocols.find((p: { id: string }) => p.id === "hysteria2").defaultSettingsJson),
      tls_mode: "self_signed",
      port: 8443,
      obfs: { password: "$generate" },
    });
    const profile = await api("ProfileService/CreateProfile", { protocol: "hysteria2", name: "bundle-hy2", settingsJson });
    expect(profile.status, JSON.stringify(profile.json)).toBe(200);
    const group = await api("GroupService/CreateGroup", { name: "bundle", profileIds: [profile.json.profile.id] });
    expect(group.status, JSON.stringify(group.json)).toBe(200);
    const created = await api("UserService/CreateUser", { name: "alice", groupId: group.json.group.id });
    expect(created.status, JSON.stringify(created.json)).toBe(200);
    userId = created.json.user.id;
    subscription = created.json.subscriptionUrl;
    expect(subscription.startsWith(`${ORIGIN}${SUB_PREFIX}`)).toBe(true);

    for (const agent of ["Happ/3.0", "mihomo/1.19.31"]) {
      const response = await mf.dispatchFetch(subscription, { headers: { "User-Agent": agent } });
      const body = await response.text();
      expect(response.status, `${agent}: ${body.slice(0, 300)}`).toBe(200);
      expect(response.headers.get("subscription-userinfo")).toMatch(/^upload=\d+; download=\d+; total=\d+; expire=\d+$/);
    }
    // a token nobody has is the decoy's 404 (the same answer as any unknown path), not an error
    const unknown = await mf.dispatchFetch(`${ORIGIN}${SUB_PREFIX}${"0".repeat(32)}`, { headers: { "User-Agent": "Happ/3.0" } });
    expect(unknown.status).toBe(404);
  }, 120_000);

  it("a link upgrade is LINK's: 101 from the node's NodeLink object in FRONT, whose first frame is the challenge from PanelLink", async () => {
    const { link_prefix } = await settings();
    const response = await mf.dispatchFetch(`${ORIGIN}${link_prefix}link/nod_bundle_probe`, { headers: { Upgrade: "websocket" } });
    expect(response.status).toBe(101);
    const socket = (response as unknown as { webSocket: WebSocket }).webSocket;
    expect(socket).toBeTruthy();
    const first = new Promise<ArrayBuffer | string>((resolve, reject) => {
      socket.addEventListener("message", (event) => resolve(event.data as ArrayBuffer | string));
      socket.addEventListener("close", () => reject(new Error("closed before the challenge")));
    });
    socket.accept();
    const frame = await first;
    expect(typeof frame).not.toBe("string");
    // a LinkChallenge: a 32-byte nonce and the audience, the host the agent dialled
    const bytes = new Uint8Array(frame as ArrayBuffer);
    expect(bytes.length).toBeGreaterThan(32);
    expect(new TextDecoder("latin1").decode(bytes)).toContain("panel.example.test");
    socket.close(1000, "done");
    // without the upgrade the same path is the decoy
    expect((await mf.dispatchFetch(`${ORIGIN}${link_prefix}link/nod_bundle_probe`)).status).toBe(404);
  }, 120_000);

  it("a cron tick is JOBS's: a user whose expiry has passed becomes EXPIRED", async () => {
    const soon = Math.floor(Date.now() / 1000) + 3;
    const updated = await api("UserService/UpdateUser", { userId, expiresUnix: soon });
    expect(updated.status, JSON.stringify(updated.json)).toBe(200);
    await new Promise((resolve) => setTimeout(resolve, 4000));
    expect((await api("UserService/GetUser", { userId })).json.user.status).not.toBe("USER_STATUS_EXPIRED"); // nothing else flips it
    const jobs = await mf.getWorker(name("jobs"));
    const result = await jobs.scheduled({ cron: "* * * * *" });
    expect(result.outcome).toBe("ok");
    const status = async () => (await api("UserService/GetUser", { userId })).json.user.status as string;
    for (let i = 0; i < 20 && (await status()) !== "USER_STATUS_EXPIRED"; i++) await new Promise((resolve) => setTimeout(resolve, 500));
    expect(await status()).toBe("USER_STATUS_EXPIRED");
    // and JOBS answers no request
    expect((await jobs.fetch("https://jobs.invalid/")).status).toBe(404);
  }, 120_000);

  it("an install request is PROVISION's: ADMIN forwards it to the Provisioner object, whose Go answers", async () => {
    // a private address is refused by the Provisioner's own checks, in its wasm: the answer proves the whole path (a missing or
    // unreachable Provisioner would be the Worker's 503 "provisioner_unavailable")
    const response = await api("ProvisioningService/GetSSHFingerprint", { host: "127.0.0.1", port: 22 });
    expect(response.status).not.toBe(503);
    expect(JSON.stringify(response.json)).toMatch(/ssh_target_not_public|invalid/);
    const provision = await mf.getWorker(name("provision"));
    expect((await provision.fetch("https://provision.invalid/anything")).status).toBe(404);
  }, 120_000);

  it("no role logged a failure while it ran", () => {
    // FRONT logs one read error on the very first request: there is no `setting` table before the admin role makes the instance,
    // and "no instance" is how it knows to send that request to ADMIN
    const failures = log.filter(
      (e) => /panel (init|request|cron) failed|front: reading the routes failed|provisioner request failed|link challenge failed/.test(e.message) && !/no such table: setting/.test(e.message),
    );
    expect(failures, failures.map((e) => e.message).join("\n")).toEqual([]);
  });
});
