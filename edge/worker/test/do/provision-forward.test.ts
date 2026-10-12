import { env } from "cloudflare:workers";
import { describe, expect, it } from "vitest";
import { forwardProvision, kickProvisioner } from "../../src/shell";

// The main Worker's forwarding across the script boundary: the binding in wrangler.example.toml names an object of another
// script (script_name), and the Provisioner there answers. The other script here is a plain-JavaScript auxiliary worker with an
// object that echoes what it received (vitest.config.ts), so the real forwardProvision and kickProvisioner run against a
// real cross-script Durable Object namespace in workerd.

const ns = () => env.AUX_PROVISIONER as unknown as Parameters<typeof forwardProvision>[0];
const marker = (build: string): [string, string][] => [["X-Mistgate-Forward", `provision;v=${build}`]];
const answer = (headers: [string, string][]) => ({ status: 204, headers, body: new Uint8Array(), waitUntil: Promise.resolve() });

describe("forwardProvision across scripts", () => {
  it("delivers the method, URL, headers and body to the object of the other script, with the marked build", async () => {
    const body = new Uint8Array(Array.from({ length: 300 }, (_, i) => i % 256)); // binary, not text
    const response = await forwardProvision(
      ns(),
      {
        method: "POST",
        url: "https://example.com/admin/api/mistgate.admin.v1.ProvisioningService/StartNodeProvision",
        headers: [["content-type", "application/octet-stream"], ["cookie", "__Host-sid=abc"], ["x-mistgate-build", "from-the-client"], ["cf-connecting-ip", "203.0.113.7"]],
        body,
      },
      answer(marker("3.1.4")),
    );
    expect(response?.status).toBe(201);
    const seen = (await response!.json()) as { method: string; url: string; headers: [string, string][]; body: number[] };
    expect(seen.method).toBe("POST");
    expect(seen.url).toBe("https://example.com/admin/api/mistgate.admin.v1.ProvisioningService/StartNodeProvision");
    const header = (name: string) => seen.headers.find(([key]) => key === name)?.[1];
    expect(header("cookie")).toBe("__Host-sid=abc");
    expect(header("cf-connecting-ip")).toBe("203.0.113.7");
    expect(header("x-mistgate-build")).toBe("3.1.4");
    expect(header("x-mistgate-forward")).toBeUndefined();
    expect(seen.body).toEqual(Array.from(body)); // binary, byte for byte
  });

  it("returns a compressed answer of the object with its Content-Encoding, compressed once", async () => {
    const response = await forwardProvision(
      ns(),
      { method: "POST", url: "https://example.com/admin/api/x/gzip", headers: [], body: new Uint8Array() },
      answer(marker("1")),
    );
    expect(response?.headers.get("content-encoding")).toBe("gzip");
    expect(await response!.json()).toEqual({ hello: "gzip" }); // a body compressed twice, or not at all, would not decode to this
  });

  it("wakes the object of the other script with kick()", async () => {
    await kickProvisioner(ns()!);
    await kickProvisioner(ns()!);
    const response = await forwardProvision(ns(), { method: "GET", url: "https://example.com/admin/nodes/install", headers: [], body: null }, answer(marker("1")));
    const seen = (await response!.json()) as { kicks: number };
    expect(seen.kicks).toBeGreaterThanOrEqual(2);
  });
});
