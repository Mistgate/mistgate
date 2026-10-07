import { describe, expect, it, vi } from "vitest";
import { forwardLink, memoizeRetry, panelURL, readAsset, toFetchRequest, toResponse } from "../src/shell";

describe("toFetchRequest", () => {
  it("forces https and keeps the path, query and port", async () => {
    const out = await toFetchRequest(new Request("http://localhost:8787/a/b?x=1&y=%20z"));
    expect(out.url).toBe("https://localhost:8787/a/b?x=1&y=%20z");
    expect(out.method).toBe("GET");
    expect(out.body).toBeNull();
  });

  it("does not lose a repeated request header", async () => {
    const headers = new Headers();
    headers.append("X-Multi", "one");
    headers.append("X-Multi", "two");
    headers.append("CF-Connecting-IP", "203.0.113.7");
    const out = await toFetchRequest(new Request("https://example.com/", { headers }));
    const multi = out.headers.filter(([name]) => name === "x-multi").map(([, value]) => value);
    expect(multi.join(",").replace(/\s/g, "")).toBe("one,two");
    expect(out.headers).toContainEqual(["cf-connecting-ip", "203.0.113.7"]);
  });

  it("carries a binary body byte for byte", async () => {
    const bytes = Uint8Array.from({ length: 256 }, (_, i) => i);
    const out = await toFetchRequest(new Request("https://example.com/rpc", { method: "POST", body: bytes }));
    expect(out.method).toBe("POST");
    expect(out.body).toBeInstanceOf(Uint8Array);
    expect(Array.from(out.body ?? [])).toEqual(Array.from(bytes));
  });
});

describe("toResponse", () => {
  it("keeps every Set-Cookie as its own header", () => {
    const response = toResponse({
      status: 201,
      headers: [
        ["Content-Type", "text/plain"],
        ["Set-Cookie", "first=one; Path=/"],
        ["Set-Cookie", "second=two; Path=/"],
        ["X-Multi-Response", "a"],
        ["X-Multi-Response", "b"],
      ],
      body: new TextEncoder().encode("cookie-test"),
    });
    expect(response.status).toBe(201);
    expect(response.headers.getSetCookie()).toEqual(["first=one; Path=/", "second=two; Path=/"]);
    expect(response.headers.get("x-multi-response")).toBe("a, b");
    expect(response.headers.get("content-type")).toBe("text/plain");
  });

  it("returns binary bytes unchanged", async () => {
    const bytes = Uint8Array.from({ length: 256 }, (_, i) => 255 - i);
    const response = toResponse({ status: 200, headers: [], body: bytes });
    expect(Array.from(new Uint8Array(await response.arrayBuffer()))).toEqual(Array.from(bytes));
  });

  it("gives a null-body status no body", async () => {
    for (const status of [204, 205, 304]) {
      const response = toResponse({ status, headers: [], body: new Uint8Array(0) });
      expect(response.status).toBe(status);
      expect(response.body).toBeNull();
    }
  });
});

describe("panelURL", () => {
  it("only changes the scheme", () => {
    expect(panelURL("http://example.com/p?q=1#h")).toBe("https://example.com/p?q=1#h");
    expect(panelURL("https://example.com:8443/")).toBe("https://example.com:8443/");
  });
});

describe("readAsset", () => {
  const fetcher = (respond: (url: string) => Response) =>
    ({ fetch: vi.fn(async (url: string) => respond(url)) }) as unknown as Fetcher & { fetch: ReturnType<typeof vi.fn> };

  it("returns the bytes of a 200 and encodes each path segment", async () => {
    const assets = fetcher(() => new Response(new Uint8Array([1, 2, 3]), { status: 200 }));
    expect(Array.from((await readAsset(assets, "assets/a b#c.js")) ?? [])).toEqual([1, 2, 3]);
    expect(assets.fetch.mock.calls[0]?.[0]).toBe("https://assets.invalid/assets/a%20b%23c.js");
  });

  it("is null for a missing file and for a redirect", async () => {
    expect(await readAsset(fetcher(() => new Response("nope", { status: 404 })), "gone.js")).toBeNull();
    expect(await readAsset(fetcher(() => new Response(null, { status: 307, headers: { Location: "/x" } })), "dir")).toBeNull();
  });

  it("rejects when the binding fails", async () => {
    const assets = { fetch: vi.fn().mockRejectedValue(new Error("boom")) } as unknown as Fetcher;
    await expect(readAsset(assets, "index.html")).rejects.toThrow("boom");
  });
});

describe("memoizeRetry", () => {
  it("shares one start between concurrent callers", async () => {
    let release!: (value: string) => void;
    const start = vi.fn(() => new Promise<string>((resolve) => (release = resolve)));
    const get = memoizeRetry(start);
    const first = get();
    const second = get();
    release("ready");
    expect(await Promise.all([first, second])).toEqual(["ready", "ready"]);
    expect(await get()).toBe("ready");
    expect(start).toHaveBeenCalledTimes(1);
  });

  it("starts again after a failure, and every concurrent caller of the failed start sees it", async () => {
    const start = vi.fn<(n: number) => Promise<string>>().mockRejectedValueOnce(new Error("d1 down")).mockResolvedValue("up");
    const get = memoizeRetry(start);
    const a = get(1);
    const b = get(2);
    await expect(a).rejects.toThrow("d1 down");
    await expect(b).rejects.toThrow("d1 down");
    expect(await get(3)).toBe("up");
    expect(start).toHaveBeenCalledTimes(2);
    expect(start).toHaveBeenLastCalledWith(3);
  });
});

describe("forwardLink", () => {
  const forwarded = new Response("101");
  const ns = () => {
    const fetch = vi.fn(async (_request: Request) => forwarded);
    const idFromName = vi.fn((name: string) => name);
    const get = vi.fn((_id: string) => ({ fetch }));
    return { ns: { idFromName, get } as unknown as Parameters<typeof forwardLink>[0], fetch, idFromName, get };
  };
  const request = new Request("https://example.com/p/link/nod_1", { headers: { Upgrade: "websocket" } });
  const answer = (status: number, headers: [string, string][]) => ({ status, headers, body: new Uint8Array() });

  it("sends the original request, as it is, to the object of the marked node, whatever the header's case", async () => {
    for (const name of ["X-Mistgate-Link", "x-mistgate-link"]) {
      const f = ns();
      expect(await forwardLink(f.ns, request, answer(204, [[name, "nod_1"]]))).toBe(forwarded);
      expect(f.idFromName).toHaveBeenCalledWith("nod_1");
      expect(f.get).toHaveBeenCalledWith("nod_1");
      const sent = f.fetch.mock.calls[0]?.[0];
      expect(sent).toBe(request);
      expect(sent?.headers.get("Upgrade")).toBe("websocket");
      expect(sent?.headers.has("X-Mistgate-Link")).toBe(false); // the object names its node itself (ctx.id.name)
    }
  });

  it("leaves any other panel answer alone", () => {
    const f = ns();
    expect(forwardLink(f.ns, request, answer(204, []))).toBeUndefined();
    expect(forwardLink(f.ns, request, answer(200, [["X-Mistgate-Link", "nod_1"]]))).toBeUndefined();
    expect(forwardLink(f.ns, request, answer(204, [["X-Mistgate-Link", ""]]))).toBeUndefined();
    expect(f.get).not.toHaveBeenCalled();
  });
});