import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FrontEnv } from "../../src/env";
import { READ_BOUND_MS, RETRY_MS, ROUTE_TTL_MS, createFront, hasSecretPrefix, parseRoutes, pickRole } from "../../src/route";

// The router of src/front.ts with fake roles behind its bindings and a fake database: which role a request reaches, what the
// instance settings do to that, and what a failing database does. It runs in workerd because the router compares prefixes with
// crypto.subtle.timingSafeEqual, which only the Workers runtime has. Real bindings, real D1 and a WebSocket are in
// integration.test.ts.

const LINK = "/lnk-secret-1/";
const ADMIN = "/adm-secret-2/";
const HOST = "admin.example.test";

type Rows = { k: string; v: string }[];
const settings = (over: Record<string, string> = {}): Rows =>
  Object.entries({ link_prefix: LINK, admin_prefix: ADMIN, ...over }).map(([k, v]) => ({ k, v }));

/**
 * A database whose answer a test sets: rows, or an Error to throw. `reads` counts the queries. With `hang` set a read waits for
 * the test: `held[i]` settles the i-th held read (a read whose request was cancelled stays pending like this).
 */
function fakeDb(answer: Rows | Error = settings()) {
  const db = {
    answer,
    hang: false,
    held: [] as ((rows: Rows) => void)[],
    reads: 0,
    queries: [] as string[],
    prepare(sql: string) {
      db.queries.push(sql);
      return {
        all: async () => {
          db.reads++;
          if (db.hang) return new Promise<{ results: Rows }>((resolve) => db.held.push((rows) => resolve({ results: rows })));
          if (db.answer instanceof Error) throw db.answer;
          return { results: db.answer };
        },
      };
    },
  };
  return db;
}

/** The three roles: each answers with its own name and records what it was asked. */
function fakeRoles() {
  const asked: { role: string; request: Request }[] = [];
  const role = (name: string) => ({
    fetch: async (request: Request) => {
      asked.push({ role: name, request });
      return new Response(name);
    },
  });
  return { asked, PUBLIC: role("PUBLIC"), ADMIN: role("ADMIN"), LINK: role("LINK") };
}

function setup(answer?: Rows | Error) {
  const db = fakeDb(answer);
  const roles = fakeRoles();
  const env = { DB: db, PUBLIC: roles.PUBLIC, ADMIN: roles.ADMIN, LINK: roles.LINK } as unknown as FrontEnv;
  let time = 1_000_000;
  const equalCalls: [number, number][] = [];
  const front = createFront({
    now: () => time,
    timingSafeEqual: (a, b) => {
      equalCalls.push([a.byteLength, b.byteLength]);
      return crypto.subtle.timingSafeEqual(a, b);
    },
  });
  const kept: Promise<unknown>[] = [];
  const ctx = { waitUntil: (promise: Promise<unknown>) => void kept.push(promise) } as unknown as ExecutionContext;
  const req = (url: string, init?: RequestInit) => new Request(url, init) as unknown as Parameters<NonNullable<typeof front.fetch>>[0];
  /** The role that answered a GET of this URL. */
  const roleOf = async (url: string, init?: RequestInit) => (await (await front.fetch!(req(url, init), env, ctx)).text()) as string;
  return { db, roles, env, front, ctx, kept, req, roleOf, equalCalls, advance: (ms: number) => void (time += ms) };
}

let consoleError: ReturnType<typeof vi.spyOn>;
beforeEach(() => {
  consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
});
afterEach(() => vi.restoreAllMocks());

describe("the routing table", () => {
  const cases: [string, string, string][] = [
    // [what it is, URL, the role]
    ["agent link socket", `https://p.example.test${LINK}link/nod_1`, "LINK"],
    ["agent link enrol", `https://p.example.test${LINK}mistgate.agent.v1.EnrollmentService/Enroll`, "LINK"],
    ["the link prefix itself", `https://p.example.test${LINK}`, "LINK"],
    ["admin SPA", `https://p.example.test${ADMIN}`, "ADMIN"],
    ["admin API", `https://p.example.test${ADMIN}api/mistgate.admin.v1.AuthService/Login`, "ADMIN"],
    ["admin setup", `https://p.example.test${ADMIN}setup`, "ADMIN"],
    ["subscription", "https://p.example.test/sub-secret/token-1", "PUBLIC"],
    ["the root", "https://p.example.test/", "PUBLIC"],
    ["an unknown path", "https://p.example.test/wp-login.php", "PUBLIC"],
    ["admin prefix without its closing slash", `https://p.example.test${ADMIN.slice(0, -1)}`, "PUBLIC"],
    ["link prefix without its closing slash", `https://p.example.test${LINK.slice(0, -1)}`, "PUBLIC"],
    ["a longer segment that starts like the admin prefix", `https://p.example.test${ADMIN.slice(0, -1)}x/`, "PUBLIC"],
    ["the admin prefix in other case", `https://p.example.test${ADMIN.toUpperCase()}`, "PUBLIC"],
    ["a doubled slash before the admin prefix", `https://p.example.test/${ADMIN}`, "PUBLIC"],
    ["a path shorter than the prefixes", "https://p.example.test/a", "PUBLIC"],
    ["the admin prefix as a query only", `https://p.example.test/?p=${ADMIN}`, "PUBLIC"],
    ["the admin prefix percent-encoded", `https://p.example.test/%61dm-secret-2/`, "PUBLIC"],
  ];
  it.each(cases)("%s -> %s", async (_what, url, role) => {
    const t = setup();
    expect(await t.roleOf(url)).toBe(role);
  });

  it("passes the very request on: method, headers and body reach the role unchanged", async () => {
    const t = setup();
    const request = t.req(`https://p.example.test${ADMIN}api/x`, { method: "POST", headers: { "X-Test": "1", Cookie: "sid=a" }, body: "hello" });
    const response = await t.front.fetch!(request, t.env, t.ctx);
    expect(await response.text()).toBe("ADMIN");
    expect(t.roles.asked).toHaveLength(1);
    expect(t.roles.asked[0]!.request).toBe(request);
  });

  it("returns the role's answer as it is (status, headers, body), and answers nothing of its own", async () => {
    const t = setup();
    const roles = { ...t.env, PUBLIC: { fetch: async () => new Response("decoy", { status: 404, headers: { "X-Decoy": "1" } }) } } as unknown as FrontEnv;
    const response = await t.front.fetch!(t.req("https://p.example.test/anything"), roles, t.ctx);
    expect([response.status, response.headers.get("X-Decoy"), await response.text()]).toEqual([404, "1", "decoy"]);
  });

  it("reads the three settings in one query", async () => {
    const t = setup();
    await t.roleOf("https://p.example.test/");
    await t.roleOf("https://p.example.test/");
    expect(t.db.queries).toHaveLength(1);
    expect(t.db.queries[0]).toMatch(/link_prefix.*admin_prefix.*admin_host/);
  });
});

describe("an admin on its own host", () => {
  const rows = () => settings({ admin_prefix: "/", admin_host: HOST });

  it("sends the host to ADMIN, whatever the path, and everything else to PUBLIC", async () => {
    const t = setup(rows());
    expect(await t.roleOf(`https://${HOST}/`)).toBe("ADMIN");
    expect(await t.roleOf(`https://${HOST}/api/x`)).toBe("ADMIN");
    expect(await t.roleOf(`https://${HOST.toUpperCase()}/x`)).toBe("ADMIN"); // the URL parser lowercases the host
    expect(await t.roleOf("https://p.example.test/")).toBe("PUBLIC");
    expect(await t.roleOf("https://p.example.test/anything")).toBe("PUBLIC"); // the stored "/" is not an admin prefix
    expect(await t.roleOf(`https://x${HOST}/`)).toBe("PUBLIC");
    expect(await t.roleOf(`https://${HOST}.evil.test/`)).toBe("PUBLIC");
  });

  it("still sends the link prefix to LINK first, on any host", async () => {
    const t = setup(rows());
    expect(await t.roleOf(`https://${HOST}${LINK}link/nod_1`)).toBe("LINK");
    expect(await t.roleOf(`https://p.example.test${LINK}link/nod_1`)).toBe("LINK");
  });
});

describe("no instance yet (first run)", () => {
  it("sends everything to ADMIN, which creates the instance", async () => {
    const t = setup([]);
    for (const url of ["https://p.example.test/", `https://p.example.test${LINK}link/nod_1`, "https://p.example.test/sub-secret/t", `https://p.example.test${ADMIN}setup`]) {
      expect(await t.roleOf(url)).toBe("ADMIN");
    }
  });

  it("treats settings no panel writes as no instance", async () => {
    for (const rows of [[{ k: "link_prefix", v: "nope" }], [{ k: "link_prefix", v: "/a/../b/" }], [{ k: "admin_prefix", v: "/" }], [{ k: "admin_host", v: "not a host" }], [{ k: "other", v: "/lnk-secret-1/" }]]) {
      const t = setup(rows);
      expect(await t.roleOf("https://p.example.test/sub-secret/t"), JSON.stringify(rows)).toBe("ADMIN");
    }
  });

  it("looks again on the next request: the instance appears within seconds, and an empty answer is never cached", async () => {
    const t = setup([]);
    expect(await t.roleOf("https://p.example.test/sub-secret/t")).toBe("ADMIN");
    t.db.answer = settings();
    expect(await t.roleOf("https://p.example.test/sub-secret/t")).toBe("PUBLIC");
    expect(await t.roleOf(`https://p.example.test${LINK}link/nod_1`)).toBe("LINK");
    expect(t.db.reads).toBe(2);
  });
});

describe("the cache and a failing database", () => {
  it("without a cache, a database error sends the request to ADMIN (it answers 503 itself) and is logged", async () => {
    const t = setup(new Error("D1_ERROR: no such table: setting"));
    expect(await t.roleOf("https://p.example.test/sub-secret/t")).toBe("ADMIN");
    expect(await t.roleOf(`https://p.example.test${LINK}link/nod_1`)).toBe("ADMIN");
    expect(consoleError).toHaveBeenCalled();
    expect(String(consoleError.mock.calls[0]![1])).toContain("no such table");
  });

  it("recovers as soon as the database answers", async () => {
    const t = setup(new Error("D1 down"));
    expect(await t.roleOf("https://p.example.test/sub-secret/t")).toBe("ADMIN");
    t.db.answer = settings();
    expect(await t.roleOf("https://p.example.test/sub-secret/t")).toBe("PUBLIC");
  });

  it("uses the values it has for ROUTE_TTL_MS without asking again, then asks again", async () => {
    const t = setup();
    await t.roleOf("https://p.example.test/");
    t.advance(ROUTE_TTL_MS - 1);
    await t.roleOf("https://p.example.test/");
    expect(t.db.reads).toBe(1);
    t.db.answer = settings({ link_prefix: "/new-link-9/" });
    t.advance(1);
    expect(await t.roleOf("https://p.example.test/new-link-9/link/nod_1")).toBe("LINK");
    expect(await t.roleOf(`https://p.example.test${LINK}link/nod_1`)).toBe("PUBLIC");
    expect(t.db.reads).toBe(2);
  });

  it("with an old cache, a database error keeps the old routes", async () => {
    const t = setup();
    expect(await t.roleOf(`https://p.example.test${ADMIN}x`)).toBe("ADMIN");
    t.advance(ROUTE_TTL_MS + 1);
    t.db.answer = new Error("D1 down");
    expect(await t.roleOf(`https://p.example.test${LINK}link/nod_1`)).toBe("LINK");
    expect(await t.roleOf(`https://p.example.test${ADMIN}x`)).toBe("ADMIN");
    expect(await t.roleOf("https://p.example.test/sub-secret/t")).toBe("PUBLIC");
    expect(consoleError).toHaveBeenCalledTimes(1); // the second and third requests did not ask again
  });

  it("retries a failed refresh after RETRY_MS, not on every request, and recovers", async () => {
    const t = setup();
    await t.roleOf("https://p.example.test/");
    t.advance(ROUTE_TTL_MS + 1);
    t.db.answer = new Error("D1 down");
    await t.roleOf("https://p.example.test/");
    expect(t.db.reads).toBe(2);
    t.advance(RETRY_MS - 1);
    await t.roleOf("https://p.example.test/");
    expect(t.db.reads).toBe(2);
    t.advance(1);
    t.db.answer = settings({ link_prefix: "/new-link-9/" });
    expect(await t.roleOf("https://p.example.test/new-link-9/link/nod_1")).toBe("LINK");
    expect(t.db.reads).toBe(3);
  });

  it("shares one read between requests that arrive together", async () => {
    const t = setup();
    const urls = ["https://p.example.test/", `https://p.example.test${ADMIN}`, `https://p.example.test${LINK}link/nod_1`];
    expect(await Promise.all(urls.map((u) => t.roleOf(u)))).toEqual(["PUBLIC", "ADMIN", "LINK"]);
    expect(t.db.reads).toBe(1);
  });

  it("drops the old routes when the database answers that there is no instance any more", async () => {
    const t = setup();
    await t.roleOf("https://p.example.test/");
    t.advance(ROUTE_TTL_MS + 1);
    t.db.answer = [];
    expect(await t.roleOf("https://p.example.test/sub-secret/t")).toBe("ADMIN");
  });
});

describe("a read of the routes that never settles (its request was cancelled)", () => {
  const url = "https://p.example.test/sub-secret/t";
  const adminUrl = `https://p.example.test${ADMIN}x`;
  // Only the timers: the router's clock is the test's own `now`.
  beforeEach(() => vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] }));
  afterEach(() => vi.useRealTimers());

  it("hands the shared read to waitUntil, once, when it starts; a cache hit starts nothing", async () => {
    const t = setup();
    expect(await t.roleOf(url)).toBe("PUBLIC");
    expect(t.kept).toHaveLength(1);
    await t.roleOf(url);
    expect(t.kept).toHaveLength(1);
  });

  it("a waiter goes on with the old routes after the bound, and the next request reads again", async () => {
    const t = setup();
    expect(await t.roleOf(adminUrl)).toBe("ADMIN");
    t.advance(ROUTE_TTL_MS + 1);
    t.db.hang = true;
    const waiter = t.roleOf(adminUrl);
    const other = t.roleOf(url);
    await vi.advanceTimersByTimeAsync(READ_BOUND_MS - 1);
    expect(t.db.reads).toBe(2); // the first read, shared by both
    await vi.advanceTimersByTimeAsync(1);
    expect(await Promise.all([waiter, other])).toEqual(["ADMIN", "PUBLIC"]); // by the routes it had
    expect(consoleError).toHaveBeenCalledTimes(1);
    t.db.hang = false;
    await t.roleOf(url);
    expect(t.db.reads).toBe(3);
  });

  it("with no routes at all the waiter goes to ADMIN, the fail-safe of a failed read", async () => {
    const t = setup();
    t.db.hang = true;
    const waiter = t.roleOf(url);
    await vi.advanceTimersByTimeAsync(READ_BOUND_MS);
    expect(await waiter).toBe("ADMIN");
    t.db.hang = false;
    expect(await t.roleOf(url)).toBe("PUBLIC");
  });

  it("requests that arrive together still share one read", async () => {
    const t = setup();
    t.db.hang = true;
    const all = [t.roleOf(url), t.roleOf(adminUrl), t.roleOf(url)];
    await vi.advanceTimersByTimeAsync(10);
    expect(t.db.reads).toBe(1);
    t.db.held[0]!(settings());
    expect(await Promise.all(all)).toEqual(["PUBLIC", "ADMIN", "PUBLIC"]);
    expect(consoleError).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("the late result of a dropped read does not overwrite what a newer read cached", async () => {
    const t = setup();
    t.db.hang = true;
    const first = t.roleOf(url);
    await vi.advanceTimersByTimeAsync(READ_BOUND_MS);
    expect(await first).toBe("ADMIN");
    const second = t.roleOf(`https://p.example.test/new-link-9/link/nod_1`); // the newer read
    await vi.advanceTimersByTimeAsync(1);
    expect(t.db.held).toHaveLength(2);
    t.db.held[1]!(settings({ link_prefix: "/new-link-9/" }));
    expect(await second).toBe("LINK");
    t.db.held[0]!(settings({ link_prefix: "/old-link-1/" })); // the cancelled request's read finally answers
    await vi.advanceTimersByTimeAsync(1);
    expect(await t.roleOf("https://p.example.test/new-link-9/link/nod_1")).toBe("LINK");
    expect(await t.roleOf("https://p.example.test/old-link-1/link/nod_1")).toBe("PUBLIC");
    expect(t.db.reads).toBe(2);
  });

  it("the late result of a dropped read does not end the newer read's sharing", async () => {
    const t = setup();
    t.db.hang = true;
    void t.roleOf(url);
    await vi.advanceTimersByTimeAsync(READ_BOUND_MS);
    void t.roleOf(url); // the newer read, still in flight
    await vi.advanceTimersByTimeAsync(1);
    t.db.held[0]!(settings());
    await vi.advanceTimersByTimeAsync(1);
    void t.roleOf(url);
    await vi.advanceTimersByTimeAsync(1);
    expect(t.db.reads).toBe(2); // not a third: the second read is still the shared one
  });
});

describe("prefix comparison", () => {
  it("goes through timingSafeEqual, with buffers of one length, for every prefix it checks", async () => {
    const t = setup();
    await t.roleOf("https://p.example.test/"); // PUBLIC: the link and the admin prefix were both compared
    expect(t.equalCalls.length).toBe(2);
    for (const [a, b] of t.equalCalls) expect(a).toBe(b); // timingSafeEqual throws on different lengths
    t.equalCalls.length = 0;
    await t.roleOf(`https://p.example.test${LINK}link/nod_1`); // LINK: decided by the first comparison
    expect(t.equalCalls.length).toBe(1);
  });

  it("compares a path shorter than the prefix, or a longer one, with the same call", async () => {
    const t = setup();
    for (const path of ["/", "/l", `${LINK}${"x".repeat(500)}`, "/é".repeat(40)]) {
      t.equalCalls.length = 0;
      await t.roleOf(`https://p.example.test${path}`);
      expect(t.equalCalls.length, path).toBeGreaterThan(0);
      for (const [a, b] of t.equalCalls) expect(a).toBe(b);
    }
  });

  it("compares the admin host with it too", async () => {
    const t = setup(settings({ admin_prefix: "/", admin_host: HOST }));
    await t.roleOf(`https://${HOST}/`);
    expect(t.equalCalls.length).toBeGreaterThanOrEqual(2); // the link prefix, then the host
    for (const [a, b] of t.equalCalls) expect(a).toBe(b);
  });

  it("hasSecretPrefix decides like httpserver.hasSecretPrefix", () => {
    const eq = (a: ArrayBufferView, b: ArrayBufferView) => crypto.subtle.timingSafeEqual(a, b);
    expect(hasSecretPrefix("/abc/def", "/abc/", eq)).toBe(true);
    expect(hasSecretPrefix("/abc/", "/abc/", eq)).toBe(true);
    expect(hasSecretPrefix("/abc", "/abc/", eq)).toBe(false);
    expect(hasSecretPrefix("/abd/def", "/abc/", eq)).toBe(false);
    expect(hasSecretPrefix("", "/abc/", eq)).toBe(false);
  });
});

describe("parseRoutes and pickRole", () => {
  it("keeps what is valid and drops what is not", () => {
    expect(parseRoutes(settings())).toEqual({ link: LINK, admin: ADMIN, adminHost: undefined });
    expect(parseRoutes(settings({ admin_host: "Admin.Example.Test" }))?.adminHost).toBe("admin.example.test");
    expect(parseRoutes(settings({ admin_prefix: "no-slashes" }))).toEqual({ link: LINK, admin: undefined, adminHost: undefined });
    expect(parseRoutes([])).toBeNull();
  });

  it("is ADMIN for a null table", () => {
    expect(pickRole(null, "/x", "h", () => false)).toBe("ADMIN");
  });
});
