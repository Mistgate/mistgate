import { Code, ConnectError } from "@connectrpc/connect";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { beforeAll, describe, expect, it } from "vitest";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { dictionaries, fill, langs, setLang, type Lang } from "@/i18n";
import { portNotes, takenNote } from "@/lib/port-check";
import type { Tx } from "@/screens/users/t";
import { viaOf } from "@/screens/users/format";
import { refusalOf } from "./deploy";
import { ownLinkOnly, summaryParts, telegramOnly, trustTunnelOnly } from "./protocol-meta";
import { problemText } from "./problem-text";
import { parseSettings } from "./schema";
import { TrustTunnelLink } from "./tt-link";

// TrustTunnel on the profile pages: the mark, the words of every code the plugin and the panel send, in all five languages.

const txOf = (lang: Lang) => {
  const t = ((k: string, v?: Record<string, string | number>) => fill((dictionaries[lang] as Record<string, string>)[k] ?? k, v)) as unknown as Tx;
  t.opt = (k: string, v?: Record<string, string | number>) => (k in dictionaries[lang] ? fill((dictionaries[lang] as Record<string, string>)[k]!, v) : undefined);
  return t;
};

describe("the apps of the protocol", () => {
  it("has its own apps, not the subscription's", () => {
    expect(trustTunnelOnly([App.TRUST_TUNNEL])).toBe(true);
    expect(trustTunnelOnly([App.HAPP])).toBe(false);
    expect(telegramOnly([App.TRUST_TUNNEL])).toBe(false);
    expect(ownLinkOnly([App.TRUST_TUNNEL]) && ownLinkOnly([App.TELEGRAM]) && !ownLinkOnly([App.HAPP, App.TRUST_TUNNEL])).toBe(true);
  });

  it("splits its summary into parts the list knows", () => {
    expect(summaryParts("TCP 443 + UDP 443 · HTTP/2 + HTTP/3 · Let's Encrypt · WARP")).toEqual(["TCP 443 + UDP 443", "HTTP/2 + HTTP/3", "Let's Encrypt", "WARP"]);
  });
});

describe("the words for what the validator and the panel say about a TrustTunnel profile", () => {
  // every (pointer, code) of internal/panel/protocols/trusttunnel/trusttunnel.go
  const codes: [string, string][] = [["/port", "out_of_range"], ["/tls_mode", "invalid_enum"], ["/egress", "invalid_enum"], ["/sni", "length"], ["/sni", "invalid"], ["/h3", "invalid_type"], ["", "invalid_json"]];
  it.each(langs)("has a text in %s for every code, the field's own for the name", (lang) => {
    const t = txOf(lang);
    expect(codes.filter(([pointer, code]) => !problemText(t, "trusttunnel", { pointer, code }))).toEqual([]);
    expect(problemText(t, "trusttunnel", { pointer: "/sni", code: "invalid" })).toBe(dictionaries[lang]["field.trusttunnel.sni.invalid"]);
  });

  it.each(langs)("words hy2_site_taken, a taken TCP or UDP port and the cloud edition's refusal in %s", (lang) => {
    const t = txOf(lang);
    const params = { node: "de1", port: "443", hy2: "Main", trusttunnel: "TT 443" };
    const note = takenNote(t, params);
    expect(note).toContain("Main");
    expect(note).toContain("TT 443");
    expect(note).not.toContain("{");
    expect(portNotes(t, [{ code: "hy2_site_taken", params }])).toEqual([note]);
    const node = { name: "de1", address: "de1.example.com" };
    const taken = refusalOf(new ConnectError("port_taken: port=443&profile=Main&free=8443", Code.AlreadyExists), t, node, "trusttunnel");
    expect(taken.text).toContain("UDP");
    expect(taken.text).not.toContain("{");
    expect(refusalOf(new ConnectError("port_taken: port=443&profile=Main&free=8443", Code.AlreadyExists), t, node, "hysteria2").text).not.toBe(taken.text);
    expect(refusalOf(new ConnectError("acme_needs_domain: address=203.0.113.10", Code.FailedPrecondition), t, node, "trusttunnel").kind).toBe("domain");
    expect(dictionaries[lang]["approval.value.warn.hy2_site_taken"]).toBeTruthy();
  });
});

describe("the words for a Hysteria2 site that gives way to MTProto", () => {
  it.each(langs)("words hy2_site_taken_mtproto in %s", (lang) => {
    const t = txOf(lang);
    const params = { node: "de1", port: "443", hy2: "Main", mtproto: "Telegram 443" };
    const note = takenNote(t, params);
    expect(note).toContain("Main");
    expect(note).toContain("Telegram 443");
    expect(note).not.toContain("{");
    expect(note).not.toBe(takenNote(t, { node: "de1", port: "443", hy2: "Main", trusttunnel: "Telegram 443" }));
    expect(portNotes(t, [{ code: "hy2_site_taken_mtproto", params }])).toEqual([note]);
    expect(dictionaries[lang]["approval.value.warn.hy2_site_taken_mtproto"]).toBeTruthy();
  });
});

describe("the apps of a person", () => {
  it("name TrustTunnel among the ways of the list's chips", () => {
    const user = (apps: object, via: App[] = []) => ({ apps: { happ: true, amnezia: false, ...apps }, via }) as never;
    expect(viaOf(user({}, [App.TRUST_TUNNEL]))).toEqual({ link: false, keys: false, trustTunnel: true });
  });
});

describe("what the official apps get from the profile", () => {
  beforeAll(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    setLang("en");
  });
  const show = (settings: object) => {
    const host = document.createElement("div");
    document.body.append(host);
    act(() => createRoot(host).render(<TrustTunnelLink settings={parseSettings(JSON.stringify(settings))} />));
    return host;
  };

  it("is a masked tt:// link, always HTTP/2, and says the key is the person's own", () => {
    const text = show({ tls_mode: "acme_domain", h3: true }).textContent ?? "";
    expect(text).toContain("tt://?");
    expect(text).toContain("always says HTTP/2");
    expect(text).toContain("the person’s own");
    expect(text).not.toMatch(/[A-Za-z0-9]{20}/);
  });

  it("is no link at all for a self-signed certificate, and says why", () => {
    const text = show({ tls_mode: "self_signed" }).textContent ?? "";
    expect(text).toContain("No link for this certificate");
    expect(text).not.toContain("tt://?");
  });
});
