import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { en } from "@/i18n/en";
import { isIp, refusalOf } from "./deploy";

// the dictionary as useTx gives it, enough for refusalOf
const t = Object.assign((k: string, v?: Record<string, string | number>) => ((en as Record<string, string>)[k] ?? k).replace(/\{(\w+)\}/g, (_, n: string) => String(v?.[n] ?? "")), {
  n: (k: string) => k,
  opt: (k: string) => (en as Record<string, string>)[k],
  lang: "en" as const,
}) as never;
const fi1 = { name: "fi1", address: "203.0.113.10" };
const err = (msg: string, code = Code.InvalidArgument) => new ConnectError(msg, code);

describe("reading a node's refusal", () => {
  it("knows a taken port in the coded form, with the free one, and in the older sentence", () => {
    expect(refusalOf(err("port_taken: port=443&profile=Main&free=8443", Code.AlreadyExists), t, fi1)).toEqual({
      kind: "port",
      free: "8443",
      text: "Port 443 on fi1 is taken by the profile “Main”; 8443 is free.",
    });
    expect(refusalOf(err('UDP port 51820 is already used by profile "awg · 2" on this node', Code.AlreadyExists), t, fi1)).toEqual({
      kind: "port",
      free: undefined,
      text: "Port 51820 on fi1 is taken by the profile “awg · 2”.",
    });
    expect(refusalOf(err("port override lies inside the hop range"), t, fi1).kind).toBe("port");
  });

  it("knows that Let's Encrypt needs a domain, coded or as the plugin says it", () => {
    const coded = refusalOf(err("acme_needs_domain: address=198.51.100.7"), t, fi1);
    expect(coded.kind).toBe("domain");
    expect(coded.text).toContain("the address of fi1 is the IP 198.51.100.7");
    const old = refusalOf(err("a host name is required for a Let's Encrypt certificate: the node address is an IP, set an SNI"), t, fi1);
    expect(old.kind).toBe("domain");
    expect(old.text).toContain("203.0.113.10");
  });

  it("treats a profile already on the node as done, and reads anything else as the panel says it", () => {
    expect(refusalOf(err("this profile is already deployed on the node", Code.AlreadyExists), t, fi1).kind).toBe("already");
    expect(refusalOf(err("already_on_node", Code.AlreadyExists), t, fi1).kind).toBe("already");
    expect(refusalOf(err("node_retired", Code.FailedPrecondition), t, fi1)).toEqual({ kind: "other", text: "The node is retired from the fleet." });
  });

  it("tells an IP address from a host name", () => {
    expect(isIp("203.0.113.10")).toBe(true);
    expect(isIp("2001:db8::1")).toBe(true);
    expect(isIp("de1.example.com")).toBe(false);
  });
});
