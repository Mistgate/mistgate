import { describe, expect, it } from "vitest";
import { allFields, parseSchema, type Settings } from "./schema";
import { vless } from "./schema.fixtures";
import { vlessEdited, vlessHidden, vlessOffers } from "./vless-form";

const fields = allFields(parseSchema(vless));
const field = (id: string) => fields.find((f) => f.id === id)!;
const base: Settings = { port: 443, transport: "xhttp", security: "reality", reality: { target_kind: "self", fingerprint: "chrome" }, xhttp: { path: "/p", host: "", mode: "auto" } };
const tls: Settings = { ...base, security: "tls", tls_mode: "acme_domain" };
const ids = (s: Settings) => [...vlessHidden(fields, s)].sort();

describe("vlessEdited", () => {
  it("picks a certificate mode and drops TCP when TLS is chosen", () => {
    const out = vlessEdited({ ...base, transport: "tcp" }, { ...base, transport: "tcp", security: "tls" });
    expect(out).toMatchObject({ security: "tls", transport: "xhttp", tls_mode: "acme_domain" });
  });

  it("drops what only TLS accepts when REALITY is chosen again", () => {
    const prev = { ...tls, transport: "ws", sni: "n.example.org", cdn: { host: "cdn.example.org", port: 443 }, ws: { path: "/w" } };
    const out = vlessEdited(prev, { ...prev, security: "reality" });
    expect(JSON.parse(JSON.stringify(out))).toEqual({ ...base, transport: "xhttp" });
  });

  it("makes XHTTP packet-up and the port 443 when a CDN host is first typed", () => {
    const out = vlessEdited(tls, { ...tls, cdn: { host: "cdn.example.org" } });
    expect(out).toMatchObject({ cdn: { host: "cdn.example.org", port: 443 }, xhttp: { mode: "packet-up" } });
    const kept = vlessEdited({ ...tls, xhttp: { ...(tls.xhttp as object), mode: "stream-up" } }, { ...tls, xhttp: { ...(tls.xhttp as object), mode: "stream-up" }, cdn: { host: "cdn.example.org" } });
    expect(kept).toMatchObject({ xhttp: { mode: "stream-up" } });
  });
});

describe("vlessHidden", () => {
  it("shows REALITY fields and no TLS field for REALITY", () => {
    expect(ids(base)).toEqual(["cdn.host", "cdn.port", "reality.server_names", "reality.target", "sni", "tls_mode", "ws.host", "ws.path"]);
  });

  it("hides every REALITY field, and the CDN port without a CDN host, for TLS", () => {
    expect(ids(tls)).toEqual(["cdn.port", "reality.fingerprint", "reality.server_names", "reality.short_ids", "reality.target", "reality.target_kind", "ws.host", "ws.path", "xhttp.host"]);
  });

  it("shows the WebSocket path, not XHTTP, for WebSocket; the Host header only without a CDN", () => {
    const ws = { ...tls, transport: "ws" };
    expect(ids(ws)).toContain("xhttp.path");
    expect(ids(ws)).not.toContain("ws.path");
    expect(ids(ws)).not.toContain("ws.host");
    expect(ids({ ...ws, cdn: { host: "cdn.example.org" } })).toContain("ws.host");
  });
});

describe("vlessOffers", () => {
  it("keeps TCP to REALITY and WebSocket to TLS", () => {
    expect(vlessOffers(field("transport"), "tcp", base)).toBe(true);
    expect(vlessOffers(field("transport"), "ws", base)).toBe(false);
    expect(vlessOffers(field("transport"), "tcp", tls)).toBe(false);
    expect(vlessOffers(field("transport"), "ws", tls)).toBe(true);
  });

  it("offers a self-signed origin only with a CDN host, or when it is already chosen", () => {
    expect(vlessOffers(field("tls_mode"), "self_signed", tls)).toBe(false);
    expect(vlessOffers(field("tls_mode"), "self_signed", { ...tls, cdn: { host: "cdn.example.org" } })).toBe(true);
    expect(vlessOffers(field("tls_mode"), "self_signed", { ...tls, tls_mode: "self_signed" })).toBe(true);
    expect(vlessOffers(field("tls_mode"), "acme_domain", tls)).toBe(true);
  });
});
