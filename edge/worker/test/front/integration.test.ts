import { env, exports } from "cloudflare:workers";
import { describe, expect, it } from "vitest";

// FRONT (src/front.ts) as the Worker, with everything real that workerd offers around it: a D1 database, the service bindings
// to the stand-in roles (public-aux, admin-aux, link-aux in vitest.config.ts) and the NodeLink class living in this script. The
// tests run in order: the database starts without a table, then gets an empty one, then an instance.

const LINK = "/lnk-secret-1/";
const ADMIN = "/adm-secret-2/";
// the pool's `exports` is typed per project; the main module's default handler is FRONT here
const main = (exports as unknown as { default: Fetcher }).default;
const front = (path: string, init?: RequestInit) => main.fetch(new Request(`https://panel.example.test${path}`, init));
const roleOf = async (path: string, init?: RequestInit) => {
  const response = await front(path, init);
  await response.body?.cancel();
  return response.headers.get("X-Role");
};

describe("the first run: no instance", () => {
  it("sends everything to ADMIN while the database has no tables", async () => {
    // the real D1 answers "no such table: setting"; FRONT has no cache, so the answer is the admin role
    expect(await roleOf("/sub-secret/token")).toBe("admin");
    expect(await roleOf(`${LINK}link/nod_1`)).toBe("admin");
  });

  it("sends everything to ADMIN while the table is empty", async () => {
    await env.DB.exec("CREATE TABLE setting (k TEXT PRIMARY KEY, v TEXT NOT NULL) STRICT");
    expect(await roleOf("/sub-secret/token")).toBe("admin");
    expect(await roleOf("/")).toBe("admin");
  });
});

describe("with an instance", () => {
  it("reads the prefixes from D1 and routes by them", async () => {
    await env.DB.prepare("INSERT INTO setting (k, v) VALUES ('link_prefix', ?), ('admin_prefix', ?), ('sub_prefix', '/sub-secret/'), ('admin_host', '')").bind(LINK, ADMIN).run();
    // admin_host '' is what an instance without an admin host would not even store; it must not make everything admin
    expect(await roleOf("/sub-secret/token")).toBe("public");
    expect(await roleOf(`${ADMIN}api/x`)).toBe("admin");
    expect(await roleOf(`${LINK}enroll`)).toBe("link");
  });

  it("hands a request over unchanged: method, headers, body, URL", async () => {
    const response = await front("/sub-secret/token?x=1", { method: "POST", headers: { "X-Test": "hello" }, body: "payload" });
    expect(response.status).toBe(201);
    expect(await response.json()).toMatchObject({ role: "public", method: "POST", header: "hello", body: "payload", url: "https://panel.example.test/sub-secret/token?x=1" });
  });

  it("answers a bad or unknown path with exactly what the public role answers (its decoy), never with a page of its own", async () => {
    for (const path of ["/", "/wp-login.php", "/.env", "/sub-secret", ADMIN.slice(0, -1), `/${ADMIN}`, `${ADMIN.toUpperCase()}`, `${LINK.slice(0, -1)}x/`, "/%61dm-secret-2/"]) {
      const viaFront = await front(path);
      const direct = await env.PUBLIC.fetch(`https://panel.example.test${path}`);
      expect([path, viaFront.status, await viaFront.text(), viaFront.headers.get("X-Role"), viaFront.headers.get("X-Decoy")]).toEqual([path, 404, "decoy", "public", "1"]);
      expect([viaFront.status, [...viaFront.headers]]).toEqual([direct.status, [...direct.headers]]);
      await direct.body?.cancel();
    }
  });

  it("sends the link prefix without an upgrade to the link role (it answers what it answers, FRONT adds nothing)", async () => {
    const response = await front(`${LINK}link/nod_1`);
    expect(response.status).toBe(201);
    expect(response.headers.get("X-Role")).toBe("link");
    await response.body?.cancel();
  });
});

const text = (b: ArrayBuffer | string) => (typeof b === "string" ? b : new TextDecoder().decode(b));
const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

describe("the agent link through FRONT", () => {
  it("upgrades through the LINK binding to the NodeLink object of FRONT's script, whose Go side is the link role's PanelLink", async () => {
    const response = await front(`${LINK}link/nod_ws1`, { headers: { Upgrade: "websocket" } });
    expect(response.status).toBe(101);
    const ws = response.webSocket!;
    const inbox: string[] = [];
    ws.binaryType = "arraybuffer";
    ws.accept();
    ws.addEventListener("message", (e) => {
      inbox.push(text(e.data as ArrayBuffer | string));
    });
    const until = async (n: number) => {
      for (let i = 0; i < 300 && inbox.length < n; i++) await sleep(10);
      expect(inbox.length, `frames: ${inbox.join(" | ")}`).toBeGreaterThanOrEqual(n);
    };
    try {
      // The challenge comes from PanelLink: FRONT has no such entrypoint of its own, so this went through the PANEL_LINK binding.
      await until(1);
      expect(inbox[0]).toBe("challenge panel.example.test");
      ws.send(new TextEncoder().encode("auth"));
      await until(2);
      expect(inbox[1]).toBe("accept nod_ws1");
      ws.send(new TextEncoder().encode("ping"));
      await until(3);
      expect(inbox[2]).toBe("ping"); // a session step of PanelLink echoed the frame
    } finally {
      ws.close(1000, "done");
      await sleep(100);
    }
  });

  it("does not upgrade a request on another path: the same socket request to the public surface is just a page", async () => {
    const response = await front(`/sub-secret/link/nod_ws2`, { headers: { Upgrade: "websocket" } });
    expect(response.status).toBe(201);
    expect(response.webSocket).toBeNull();
    expect(response.headers.get("X-Role")).toBe("public");
  });
});
