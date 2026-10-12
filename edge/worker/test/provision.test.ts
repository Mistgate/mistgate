import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Env } from "../src/env";
import { fetchPanelRequest } from "../src/fetch";
import { getPanel } from "../src/panel";
import { type Connect, forwardProvision, kickProvisioner, openSocket } from "../src/shell";

vi.mock("../src/panel", () => ({ getPanel: vi.fn() }));
const mockedGetPanel = vi.mocked(getPanel);

describe("forwardProvision", () => {
  const marker = (build = "1.2.3"): [string, string][] => [["X-Mistgate-Forward", `provision;v=${build}`]];
  const answer = (status: number, headers: [string, string][]) => ({ status, headers, body: new Uint8Array(), waitUntil: Promise.resolve() });
  const fetchRequest = (over: Partial<Parameters<typeof forwardProvision>[1]> = {}) => ({
    method: "POST",
    url: "https://example.com/admin/api/mistgate.admin.v1.ProvisioningService/StartNodeProvision",
    headers: [
      ["content-type", "application/json"],
      ["cookie", "__Host-sid=abc"],
      ["content-length", "99"],
      ["x-mistgate-build", "client-says-so"],
      ["cf-connecting-ip", "203.0.113.7"],
    ] as [string, string][],
    body: new TextEncoder().encode('{"name":"node-a"}'),
    ...over,
  });
  const provisioner = (reply: Response | Error = new Response("done", { status: 201 })) => {
    const fetch = vi.fn(async (_request: Request) => {
      if (reply instanceof Error) throw reply;
      return reply;
    });
    const idFromName = vi.fn((name: string) => name);
    const get = vi.fn((_id: string) => ({ fetch }));
    return { ns: { idFromName, get } as unknown as NonNullable<Parameters<typeof forwardProvision>[0]>, fetch, idFromName, get };
  };

  it("sends the method, URL and body of the request that was already read to the one Provisioner, with the marked build", async () => {
    const p = provisioner();
    const response = await forwardProvision(p.ns, fetchRequest(), answer(204, marker("0.9.0")));
    expect(response?.status).toBe(201);
    expect(await response?.text()).toBe("done");
    expect(p.idFromName).toHaveBeenCalledWith("provisioner");
    expect(p.get).toHaveBeenCalledWith("provisioner");
    const sent = p.fetch.mock.calls[0]?.[0] as Request;
    expect(sent.method).toBe("POST");
    expect(sent.url).toBe("https://example.com/admin/api/mistgate.admin.v1.ProvisioningService/StartNodeProvision");
    expect(await sent.text()).toBe('{"name":"node-a"}');
    expect(sent.headers.get("content-type")).toBe("application/json");
    expect(sent.headers.get("cookie")).toBe("__Host-sid=abc");
    expect(sent.headers.get("cf-connecting-ip")).toBe("203.0.113.7");
    expect(sent.headers.get("x-mistgate-build")).toBe("0.9.0"); // the marker's build, not the client's
    expect(sent.headers.get("content-length")).not.toBe("99");
    expect(sent.headers.has("x-mistgate-forward")).toBe(false);
  });

  it("sends a GET without a body, and finds the marker whatever its case", async () => {
    const p = provisioner();
    const response = await forwardProvision(
      p.ns,
      fetchRequest({ method: "GET", url: "https://example.com/admin/nodes/install?x=1", body: null }),
      answer(204, [["x-mistgate-forward", "provision;v=dev"]]),
    );
    expect(response?.status).toBe(201);
    const sent = p.fetch.mock.calls[0]?.[0] as Request;
    expect(sent.method).toBe("GET");
    expect(sent.url).toBe("https://example.com/admin/nodes/install?x=1");
    expect(sent.body).toBeNull();
  });

  it("leaves every other panel answer alone", () => {
    const p = provisioner();
    for (const a of [
      answer(204, []),
      answer(200, marker()),
      answer(204, [["X-Mistgate-Forward", "link;v=1"]]),
      answer(204, [["X-Mistgate-Forward", "provision;v="]]),
      answer(204, [["X-Mistgate-Link", "nod_1"]]),
    ]) {
      expect(forwardProvision(p.ns, fetchRequest(), a)).toBeUndefined();
    }
    expect(p.get).not.toHaveBeenCalled();
  });

  it("answers 503 unavailable when there is no binding, and when the object cannot be reached", async () => {
    const log = vi.spyOn(console, "error").mockImplementation(() => {});
    const without = await forwardProvision(undefined, fetchRequest(), answer(204, marker()));
    expect(without?.status).toBe(503);
    expect(without?.headers.get("content-type")).toBe("application/json");
    expect(await without?.json()).toEqual({ code: "unavailable", message: "provisioner_unavailable" });

    const down = provisioner(new Error("Durable Object reset because its code was updated"));
    const response = await forwardProvision(down.ns, fetchRequest(), answer(204, marker()));
    expect(response?.status).toBe(503);
    expect(log).toHaveBeenCalledWith("provisioner request failed:", "Durable Object reset because its code was updated");
    log.mockRestore();
  });

  it("passes the object's answer through as it is, a compressed body included", async () => {
    const gzipped = new Uint8Array([0x1f, 0x8b, 8, 0, 1, 2, 3]);
    const reply = new Response(gzipped, { status: 200, headers: { "Content-Encoding": "gzip", "Content-Type": "application/json" } });
    const p = provisioner(reply);
    const response = await forwardProvision(p.ns, fetchRequest(), answer(204, marker()));
    expect(response).toBe(reply);
  });
});

describe("kickProvisioner", () => {
  it("calls kick on the one Provisioner and lets a failure through", async () => {
    const kick = vi.fn().mockResolvedValue(undefined);
    const idFromName = vi.fn((name: string) => name);
    const ns = { idFromName, get: vi.fn(() => ({ kick })) } as unknown as Parameters<typeof kickProvisioner>[0];
    await kickProvisioner(ns);
    expect(idFromName).toHaveBeenCalledWith("provisioner");
    expect(kick).toHaveBeenCalledOnce();
    kick.mockRejectedValueOnce(new Error("no such object"));
    await expect(kickProvisioner(ns)).rejects.toThrow("no such object");
    const broken = {
      idFromName: () => {
        throw new Error("sync");
      },
    } as unknown as Parameters<typeof kickProvisioner>[0];
    await expect(kickProvisioner(broken)).rejects.toThrow("sync"); // a rejection, never a sync throw
  });
});

describe("openSocket", () => {
  /** A socket whose readable and writable the test drives; `opened` settles as the test says. */
  function fakeSocket() {
    let openIt!: () => void;
    let failIt!: (error: Error) => void;
    const opened = new Promise<void>((resolve, reject) => {
      openIt = resolve;
      failIt = reject;
    });
    opened.catch(() => {});
    const incoming = new TransformStream<Uint8Array, Uint8Array>();
    const written: Uint8Array[] = [];
    const outgoing = new WritableStream<Uint8Array>({
      write(chunk) {
        written.push(chunk);
      },
    });
    const socket = { opened, readable: incoming.readable, writable: outgoing, close: vi.fn(async () => {}) };
    return { socket, openIt, failIt, feed: incoming.writable.getWriter(), written };
  }
  const connectTo = (socket: unknown) => vi.fn(() => socket) as unknown as Connect & ReturnType<typeof vi.fn>;

  it("opens a plain TCP socket to the host and port, and reads, writes and closes it", async () => {
    const f = fakeSocket();
    const connect = connectTo(f.socket);
    f.openIt();
    const result = await openSocket(connect, "198.51.100.5", 22, 1000);
    expect(connect).toHaveBeenCalledWith({ hostname: "198.51.100.5", port: 22 }, { secureTransport: "off", allowHalfOpen: false });
    if (!result.ok) throw new Error("expected an open socket");
    await result.write(new Uint8Array([1, 2, 3]));
    expect(f.written).toEqual([new Uint8Array([1, 2, 3])]);
    const fed = f.feed.write(new Uint8Array([9, 8])); // a write waits for its reader
    expect(await result.read()).toEqual(new Uint8Array([9, 8]));
    await fed;
    void f.feed.close();
    expect(await result.read()).toBeNull(); // the end of the stream
    result.close();
    result.close();
    expect(f.socket.close).toHaveBeenCalledTimes(1);
  });

  it("maps the platform's refusal text to refused and any other failure to failed", async () => {
    for (const [text, kind] of [
      ["proxy request failed, cannot connect to the specified address", "refused"],
      ["Error: Proxy Request Failed", "refused"],
      ["connect(): Network connection lost", "failed"],
    ] as const) {
      const f = fakeSocket();
      f.failIt(new Error(text));
      expect(await openSocket(connectTo(f.socket), "198.51.100.5", 22, 1000)).toEqual({ ok: false, error: kind });
      expect(f.socket.close).toHaveBeenCalledOnce();
    }
    const throws = vi.fn(() => {
      throw new Error("cannot connect to the specified address");
    }) as unknown as Connect;
    expect(await openSocket(throws, "10.0.0.1", 22, 1000)).toEqual({ ok: false, error: "refused" });
  });

  it("gives up at the limit and closes the socket that was still opening", async () => {
    vi.useFakeTimers();
    try {
      const f = fakeSocket(); // never opens
      const result = openSocket(connectTo(f.socket), "198.51.100.5", 22, 500);
      await vi.advanceTimersByTimeAsync(499);
      expect(f.socket.close).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(2);
      expect(await result).toEqual({ ok: false, error: "timeout" });
      expect(f.socket.close).toHaveBeenCalledOnce();
      expect(vi.getTimerCount()).toBe(0); // no timer is left behind
    } finally {
      vi.useRealTimers();
    }
  });

  it("clears its timer when the socket opens", async () => {
    vi.useFakeTimers();
    try {
      const f = fakeSocket();
      f.openIt();
      await openSocket(connectTo(f.socket), "198.51.100.5", 22, 60_000);
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not throw out of a read or a write once it was closed, and ends a waiting read", async () => {
    const f = fakeSocket();
    f.openIt();
    const result = await openSocket(connectTo(f.socket), "198.51.100.5", 22, 1000);
    if (!result.ok) throw new Error("expected an open socket");
    const waiting = result.read(); // nothing to read yet
    result.close();
    // a closed Worker socket rejects the read that was waiting
    await f.feed.abort(new Error("This socket has been closed")).catch(() => {});
    expect(await waiting).toBeNull();
    expect(await result.read()).toBeNull();
    await expect(result.write(new Uint8Array([1]))).resolves.toBeUndefined();
  });

  it("reports a failure of the socket while it is open", async () => {
    const f = fakeSocket();
    f.openIt();
    const result = await openSocket(connectTo(f.socket), "198.51.100.5", 22, 1000);
    if (!result.ok) throw new Error("expected an open socket");
    const waiting = result.read();
    await f.feed.abort(new Error("Network connection lost")).catch(() => {});
    await expect(waiting).rejects.toThrow("Network connection lost");
  });
});

describe("fetchPanelRequest: SSH installs", () => {
  beforeEach(() => vi.resetAllMocks());

  const marked = (build = "7.7.7") => ({
    status: 204,
    headers: [["X-Mistgate-Forward", `provision;v=${build}`]],
    body: new Uint8Array(),
    waitUntil: Promise.resolve(),
  });
  const ctx = () => ({ waitUntil: vi.fn() }) as unknown as ExecutionContext;
  const provisionerNamespace = (reply: Response) => {
    const fetch = vi.fn(async (_request: Request) => reply);
    return { ns: { idFromName: (name: string) => name, get: () => ({ fetch }) } as unknown as Env["PROVISIONER"], fetch };
  };

  it("hands a marked request, with the body the panel already read, to the Provisioner and returns its answer", async () => {
    const panel = { fetch: vi.fn().mockResolvedValue(marked()) };
    mockedGetPanel.mockResolvedValue(panel as unknown as Awaited<ReturnType<typeof getPanel>>);
    const p = provisionerNamespace(new Response('{"job":{}}', { status: 200, headers: { "Content-Type": "application/json" } }));
    const request = new Request("https://example.com/admin/api/mistgate.admin.v1.ProvisioningService/GetNodeProvision", {
      method: "POST",
      headers: { "Content-Type": "application/json", Cookie: "__Host-sid=abc" },
      body: '{"jobId":"prv_x"}',
    });

    const response = await fetchPanelRequest(request, { PROVISIONER: p.ns } as unknown as Env, ctx());

    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ job: {} });
    expect(p.fetch).toHaveBeenCalledOnce();
    const sent = p.fetch.mock.calls[0]?.[0] as Request;
    expect(await sent.text()).toBe('{"jobId":"prv_x"}');
    expect(sent.headers.get("x-mistgate-build")).toBe("7.7.7");
    expect(sent.headers.get("cookie")).toBe("__Host-sid=abc");
    expect(response.headers.has("x-mistgate-forward")).toBe(false);
  });

  it("answers 503 for a marked request when the Provisioner is not bound", async () => {
    const panel = { fetch: vi.fn().mockResolvedValue(marked()) };
    mockedGetPanel.mockResolvedValue(panel as unknown as Awaited<ReturnType<typeof getPanel>>);
    const response = await fetchPanelRequest(new Request("https://example.com/admin/nodes/install"), {} as Env, ctx());
    expect(response.status).toBe(503);
  });

  it("serves an unmarked answer itself and never touches the Provisioner", async () => {
    const panel = { fetch: vi.fn().mockResolvedValue({ status: 200, headers: [], body: new TextEncoder().encode("ok"), waitUntil: Promise.resolve() }) };
    mockedGetPanel.mockResolvedValue(panel as unknown as Awaited<ReturnType<typeof getPanel>>);
    const p = provisionerNamespace(new Response("no"));
    const response = await fetchPanelRequest(new Request("https://example.com/admin/api/x"), { PROVISIONER: p.ns } as unknown as Env, ctx());
    expect(await response.text()).toBe("ok");
    expect(p.fetch).not.toHaveBeenCalled();
  });
});
