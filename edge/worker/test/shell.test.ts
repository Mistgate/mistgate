import { afterEach, describe, expect, it, vi } from "vitest";
import { ASSET_MARKER, PANEL_START_BOUND_MS, askNode, closeNode, detachWaitUntil, forwardAsset, forwardLink, memoizeRetry, panelURL, pokeNodes, readAsset, sendNode, toFetchRequest, toResponse } from "../src/shell";

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
      waitUntil: Promise.resolve(),
    });
    expect(response.status).toBe(201);
    expect(response.headers.getSetCookie()).toEqual(["first=one; Path=/", "second=two; Path=/"]);
    expect(response.headers.get("x-multi-response")).toBe("a, b");
    expect(response.headers.get("content-type")).toBe("text/plain");
  });

  it("returns binary bytes unchanged", async () => {
    const bytes = Uint8Array.from({ length: 256 }, (_, i) => 255 - i);
    const response = toResponse({ status: 200, headers: [], body: bytes, waitUntil: Promise.resolve() });
    expect(Array.from(new Uint8Array(await response.arrayBuffer()))).toEqual(Array.from(bytes));
  });

  it("gives a null-body status no body", async () => {
    for (const status of [204, 205, 304]) {
      const response = toResponse({ status, headers: [], body: new Uint8Array(0), waitUntil: Promise.resolve() });
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
  afterEach(() => vi.useRealTimers());

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

  it("a start that never settles makes every waiter fail after the bound, and the next call starts again", async () => {
    vi.useFakeTimers();
    const start = vi.fn<(n: number) => Promise<string>>().mockReturnValueOnce(new Promise(() => {})).mockResolvedValue("up");
    const get = memoizeRetry(start);
    const first = get(1).catch((error: Error) => error);
    const second = get(2).catch((error: Error) => error);
    await vi.advanceTimersByTimeAsync(PANEL_START_BOUND_MS - 1);
    expect(start).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(((await first) as Error).message).toMatch(/did not start within/);
    expect(((await second) as Error).message).toMatch(/did not start within/);
    expect(await get(3)).toBe("up");
    expect(start).toHaveBeenCalledTimes(2);
    expect(start).toHaveBeenLastCalledWith(3);
  });

  it("the bound runs from the start of the attempt, not from the arrival of a later waiter", async () => {
    vi.useFakeTimers();
    const get = memoizeRetry(() => new Promise<string>(() => {}));
    const early = get().catch(() => "early");
    await vi.advanceTimersByTimeAsync(PANEL_START_BOUND_MS - 1000);
    const late = get().catch(() => "late");
    await vi.advanceTimersByTimeAsync(1000);
    expect(await Promise.all([early, late])).toEqual(["early", "late"]);
  });

  it("a start that finishes in time clears its timer", async () => {
    vi.useFakeTimers();
    const get = memoizeRetry(async () => "ready");
    expect(await get()).toBe("ready");
    expect(vi.getTimerCount()).toBe(0);
    const again = get(); // the panel is up: the hot path of every request sets no timer at all
    expect(vi.getTimerCount()).toBe(0);
    expect(await again).toBe("ready");
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
  const answer = (status: number, headers: [string, string][]) => ({ status, headers, body: new Uint8Array(), waitUntil: Promise.resolve() });

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

describe("NodeLink shell calls", () => {
  const namespace = (getNode: (id: string) => object) => {
    const idFromName = vi.fn((name: string) => name);
    const get = vi.fn((id: string) => getNode(id));
    return {
      ns: { idFromName, get } as unknown as Parameters<typeof askNode>[0],
      idFromName,
      get,
    };
  };

  it("maps ask replies, null, the exact timeout error, link loss, and object resets without rejecting", async () => {
    const frame = new Uint8Array([1, 2]);
    const reply = new Uint8Array([3, 4]);
    const ask = vi.fn().mockResolvedValue(reply);
    const f = namespace(() => ({ ask }));
    expect(await askNode(f.ns, "node-a", "request-1", frame, 1234)).toEqual({ reply });
    expect(f.idFromName).toHaveBeenCalledWith("node-a");
    expect(f.get).toHaveBeenCalledWith("node-a");
    expect(ask).toHaveBeenCalledWith("request-1", frame, 1234);

    ask.mockResolvedValueOnce(null);
    expect(await askNode(f.ns, "node-a", "request-2", frame, 1234)).toEqual({ reply: null });

    for (const [failure, result] of [
      [new Error("timeout"), { error: "timeout" }],
      [new Error("link lost"), { error: "lost" }],
      [{ message: "object reset" }, { error: "lost" }],
    ] as const) {
      ask.mockRejectedValueOnce(failure);
      await expect(askNode(f.ns, "node-a", "request-3", frame, 1234)).resolves.toEqual(result);
    }
  });

  it("closes through code 4000 with the supplied reason", async () => {
    const close = vi.fn().mockResolvedValue(undefined);
    const f = namespace(() => ({ close }));
    await closeNode(f.ns, "node-a", "retired");
    expect(close).toHaveBeenCalledWith(4000, "retired");
  });

  it("counts rejected and synchronously failing poke calls", async () => {
    const f = namespace((id: string) => ({
      poke: () => {
        if (id === "node-b") return Promise.reject(new Error("reset"));
        if (id === "node-c") throw new Error("unavailable");
        return Promise.resolve();
      },
    }));
    expect(await pokeNodes(f.ns, ["node-a", "node-b", "node-c"])).toBe(2);
  });

  it("moves waitUntil to the execution context and strips it from the output", () => {
    const waitUntil = Promise.resolve();
    const ctx = { waitUntil: vi.fn() } as unknown as Pick<ExecutionContext, "waitUntil">;
    const out = detachWaitUntil({ state: "s", waitUntil }, ctx);
    expect(ctx.waitUntil).toHaveBeenCalledOnce();
    expect(ctx.waitUntil).toHaveBeenCalledWith(waitUntil);
    expect(out).toEqual({ state: "s" });
    expect(out).not.toHaveProperty("waitUntil");
  });
});

describe("sendNode", () => {
  const namespace = (send: () => unknown) => {
    const idFromName = vi.fn((name: string) => name);
    const get = vi.fn((_id: string) => ({ send }));
    return { ns: { idFromName, get } as unknown as Parameters<typeof sendNode>[0], idFromName, get };
  };

  it("passes the arguments to the objects of the node and returns its answer", async () => {
    const send = vi.fn().mockResolvedValue(true);
    const f = namespace(send);
    const frame = new Uint8Array([1, 2]);
    expect(await sendNode(f.ns, "node-a", "request-1", frame, 1234)).toBe(true);
    expect(f.idFromName).toHaveBeenCalledWith("node-a");
    expect(send).toHaveBeenCalledWith("request-1", frame, 1234);
    send.mockResolvedValueOnce(false);
    expect(await sendNode(f.ns, "node-a", "request-2", frame, 1234)).toBe(false);
  });

  it("never rejects: an unreachable object is not sent", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      expect(await sendNode(namespace(() => Promise.reject(new Error("reset"))).ns, "node-a", "r", new Uint8Array(), 1)).toBe(false);
      const throwing = () => {
        throw new Error("sync");
      };
      expect(await sendNode(namespace(throwing).ns, "node-a", "r", new Uint8Array(), 1)).toBe(false);
      expect(await sendNode(namespace(() => Promise.resolve("yes")).ns, "node-a", "r", new Uint8Array(), 1)).toBe(false); // only true is sent
    } finally {
      error.mockRestore();
    }
  });
});

describe("forwardAsset", () => {
  const url = "https://github.com/example/mistgate/releases/download/v1.2.3/mistgate-node-linux-amd64";
  const answer = (status: number, headers: [string, string][]) => ({ status, headers, body: new Uint8Array(), waitUntil: Promise.resolve() });
  const request = (method = "GET") => ({
    method,
    url: "https://panel.example.com/p/link/update/nl1/mistgate-node-linux-amd64",
    headers: [
      ["authorization", "Bearer invented-token"],
      ["cookie", "session=alice"],
      ["Range", "bytes=100-"],
      ["User-Agent", "mistgate-node/0.1.50"],
      ["Accept-Encoding", "gzip"],
      ["X-Forwarded-For", "203.0.113.7"],
    ] as [string, string][],
    body: null,
  });
  const upstream = (response: Response) => vi.fn(async (_url: URL, _init: RequestInit) => response);

  it("leaves every other panel answer alone and does not fetch", () => {
    const fetchUpstream = upstream(new Response("x"));
    expect(forwardAsset(request(), answer(204, []), fetchUpstream)).toBeUndefined();
    expect(forwardAsset(request(), answer(200, [[ASSET_MARKER, url]]), fetchUpstream)).toBeUndefined();
    expect(forwardAsset(request(), answer(204, [[ASSET_MARKER, ""]]), fetchUpstream)).toBeUndefined();
    expect(fetchUpstream).not.toHaveBeenCalled();
  });

  it("fetches the marked URL with only Range, User-Agent and Accept-Encoding: identity, whatever the marker's case", async () => {
    for (const name of [ASSET_MARKER, "X-Mistgate-Asset"]) {
      const fetchUpstream = upstream(new Response("tail", { status: 206 }));
      await forwardAsset(request(), answer(204, [[name, url]]), fetchUpstream);
      expect(fetchUpstream).toHaveBeenCalledOnce();
      const [target, init] = fetchUpstream.mock.calls[0]!;
      expect(target.href).toBe(url);
      expect(init.method).toBe("GET");
      expect(init.redirect).toBe("follow");
      const sent = new Headers(init.headers);
      expect([...sent.keys()].sort()).toEqual(["accept-encoding", "range", "user-agent"]);
      expect(sent.get("range")).toBe("bytes=100-");
      expect(sent.get("user-agent")).toBe("mistgate-node/0.1.50");
      expect(sent.get("accept-encoding")).toBe("identity");
    }
  });

  it("names itself when the node sent no User-Agent, and sends no Range for a whole-file request", async () => {
    const fetchUpstream = upstream(new Response("all"));
    await forwardAsset({ ...request(), headers: [["Authorization", "Bearer invented-token"]] }, answer(204, [[ASSET_MARKER, url]]), fetchUpstream);
    const sent = new Headers(fetchUpstream.mock.calls[0]![1].headers);
    expect([...sent.keys()].sort()).toEqual(["accept-encoding", "user-agent"]);
    expect(sent.get("user-agent")).toBe("mistgate-edge");
  });

  it("passes 200 and 206 with their length headers, as an unencoded no-store stream; the marker does not reach the node", async () => {
    const partial = new Response("tail", {
      status: 206,
      headers: { "Content-Length": "4", "Content-Range": "bytes 100-103/104", "Accept-Ranges": "bytes", "Content-Type": "text/html", "Set-Cookie": "x=1", "X-Github": "no" },
    });
    const out = (await forwardAsset(request(), answer(204, [[ASSET_MARKER, url]]), upstream(partial)))!;
    expect(out.status).toBe(206);
    expect(out.headers.get("content-length")).toBe("4");
    expect(out.headers.get("content-range")).toBe("bytes 100-103/104");
    expect(out.headers.get("accept-ranges")).toBe("bytes");
    expect(out.headers.get("content-type")).toBe("application/octet-stream");
    expect(out.headers.get("cache-control")).toBe("no-store, no-transform");
    expect(out.headers.has("set-cookie")).toBe(false);
    expect(out.headers.has("x-github")).toBe(false);
    expect(out.headers.has(ASSET_MARKER)).toBe(false);
    expect(await out.text()).toBe("tail");
    const whole = (await forwardAsset(request(), answer(204, [[ASSET_MARKER, url]]), upstream(new Response("all", { headers: { "Content-Length": "3" } }))))!;
    expect(whole.status).toBe(200);
    expect(whole.headers.get("content-length")).toBe("3");
    expect(whole.headers.has("content-range")).toBe(false);
  });

  it("streams the body: a chunk reaches the node before the upstream is read to its end, and it is never buffered", async () => {
    let pulls = 0;
    const chunks = 50;
    const body = new ReadableStream<Uint8Array>(
      {
        pull(controller) {
          pulls++;
          if (pulls > chunks) controller.close();
          else controller.enqueue(new Uint8Array(1024).fill(pulls));
        },
      },
      { highWaterMark: 0 },
    );
    const source = new Response(body);
    const spies = (["arrayBuffer", "blob", "text", "json", "formData"] as const).map((name) => vi.spyOn(source, name));
    const out = (await forwardAsset(request(), answer(204, [[ASSET_MARKER, url]]), upstream(source)))!;
    const reader = out.body!.getReader();
    const first = await reader.read();
    expect(first.value?.[0]).toBe(1);
    expect(pulls).toBeLessThan(chunks); // the rest of the file has not been pulled
    await reader.cancel();
    for (const spy of spies) expect(spy).not.toHaveBeenCalled();
  });

  it("keeps 416 as 416 and turns every other upstream status or failure into 502", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      const range = (await forwardAsset(request(), answer(204, [[ASSET_MARKER, url]]), upstream(new Response("x", { status: 416, headers: { "Content-Range": "bytes */104" } }))))!;
      expect(range.status).toBe(416);
      expect(range.headers.get("content-range")).toBe("bytes */104");
      for (const status of [403, 404, 429, 500, 503]) {
        const out = (await forwardAsset(request(), answer(204, [[ASSET_MARKER, url]]), upstream(new Response("upstream page", { status }))))!;
        expect(out.status, `upstream ${status}`).toBe(502);
        expect(await out.text()).toBe("Bad Gateway");
      }
      const failing = vi.fn(async () => {
        throw new Error("connection reset");
      });
      expect((await forwardAsset(request(), answer(204, [[ASSET_MARKER, url]]), failing))!.status).toBe(502);
    } finally {
      error.mockRestore();
    }
  });

  it("fetches a loopback http marker (the e2e's fake release) but no other http host", async () => {
    for (const ok of ["http://127.0.0.1:8123/v1.2.3/f", "http://localhost/v1.2.3/f", "http://[::1]:9/v1.2.3/f"]) {
      const fetchUpstream = upstream(new Response("x", { headers: { "Content-Length": "1" } }));
      expect((await forwardAsset(request(), answer(204, [[ASSET_MARKER, ok]]), fetchUpstream))!.status, ok).toBe(200);
      expect(fetchUpstream).toHaveBeenCalledOnce();
    }
    for (const bad of ["http://127.0.0.1.example.com/a", "http://10.0.0.1/a", "http://github.com/a"]) {
      const fetchUpstream = upstream(new Response("x"));
      expect((await forwardAsset(request(), answer(204, [[ASSET_MARKER, bad]]), fetchUpstream))!.status, bad).toBe(502);
      expect(fetchUpstream).not.toHaveBeenCalled();
    }
  });

  it("does not fetch a marker that is not an https URL", async () => {
    for (const bad of ["http://github.com/a", "file:///etc/passwd", "not a url"]) {
      const fetchUpstream = upstream(new Response("x"));
      expect((await forwardAsset(request(), answer(204, [[ASSET_MARKER, bad]]), fetchUpstream))!.status, bad).toBe(502);
      expect(fetchUpstream).not.toHaveBeenCalled();
    }
  });
});
