import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { dictionaries, fill, langs, type Lang } from "@/i18n";
import { refusalOf } from "@/screens/profiles/deploy";
import type { Tx } from "@/screens/users/t";
import { errorCodes, errorText } from "./errors";
import { portNotes } from "./port-check";
import { inboundErrorKind, inboundErrorText } from "./inbound-error";
import { describeEvent } from "./events";

// Services that share a TCP port by the name in the connection: the panel's four refusals and two warnings, in all five languages.

const dict = (lang: Lang) => dictionaries[lang] as Record<string, string>;
const txOf = (lang: Lang) => {
  const t = ((k: string, v?: Record<string, string | number>) => fill(dict(lang)[k] ?? k, v)) as unknown as Tx;
  t.opt = (k: string, v?: Record<string, string | number>) => (k in dictionaries[lang] ? fill(dict(lang)[k]!, v) : undefined);
  return t;
};

const refusals: [string, string, Code][] = [
  ["sni_taken: port=443&node=de1&profile=Reality+main&name=de1.example.com", "Reality main", Code.AlreadyExists],
  ["sni_taken: port=443&node=de1&profile=Reality+main&name=de1.example.com&free=8443", "8443", Code.AlreadyExists],
  ["share_needs_site: port=443&node=de1&profile=Reality+main", "Reality main", Code.FailedPrecondition],
  ["share_unsupported: port=443&node=de1&profile=Reality+main&protocol=vless", "vless", Code.FailedPrecondition],
  ["share_agent_old: port=443&node=de1&profile=Reality+main", "Reality main", Code.FailedPrecondition],
];

describe("the refusals of a profile that cannot stand beside another on a TCP port", () => {
  it("lists each code the panel sends", () => {
    for (const code of ["sni_taken", "share_needs_site", "share_unsupported", "share_agent_old"]) expect(errorCodes).toContain(code);
  });

  it.each(langs)("words each of them in %s, with the values the panel sent and no placeholder left", (lang) => {
    const t = txOf(lang);
    const en = txOf("en");
    for (const [message, shown, code] of refusals) {
      const text = errorText(new ConnectError(message, code), t as never);
      expect(text, message).toContain(shown);
      expect(text, message).toContain("de1");
      expect(text, message).not.toMatch(/[{}]/);
      expect(text, message).not.toBe(message);
      if (lang !== "en") expect(text, message).not.toBe(errorText(new ConnectError(message, code), en as never));
    }
  });

  it.each(langs)("shows the same words where a profile is put on a node (deploy window) in %s", (lang) => {
    const t = txOf(lang);
    const node = { name: "de1", address: "de1.example.com" };
    for (const [message, , code] of refusals) {
      const got = refusalOf(new ConnectError(message, code), t, node);
      expect(got.kind, message).toBe("share");
      expect(got.text, message).toBe(errorText(new ConnectError(message, code), t as never));
    }
  });

  it("names the free port only when the panel found one", () => {
    const t = txOf("en");
    expect(errorText(new ConnectError(refusals[0]![0], Code.AlreadyExists), t as never)).not.toContain("8443");
    expect(errorText(new ConnectError(refusals[1]![0], Code.AlreadyExists), t as never)).toContain("8443");
  });
});

describe("the warnings of a profile that does stand beside another", () => {
  const shared = { node: "de1", port: "443", profiles: "Reality main, Telegram", default: "Reality main" };
  it.each(langs)("words tcp_port_shared and share_two_faces in %s", (lang) => {
    const t = txOf(lang);
    const plain = portNotes(t, [{ code: "tcp_port_shared", params: shared }]);
    const restarts = portNotes(t, [{ code: "tcp_port_shared", params: { ...shared, restarts: "Reality main" } }]);
    const faces = portNotes(t, [{ code: "share_two_faces", params: { node: "de1", port: "443", profile: "Other site", default: "Reality main" } }]);
    expect(plain).toHaveLength(1);
    expect(plain[0]).toContain("Telegram");
    expect(plain[0]).toContain("443");
    expect(restarts[0]!.length).toBeGreaterThan(plain[0]!.length); // who restarts is added when the panel says so
    expect(faces[0]).toContain("Other site");
    for (const text of [...plain, ...restarts, ...faces]) expect(text).not.toMatch(/[{}]/);
    expect(dict(lang)["approval.value.warn.tcp_port_shared"]).toBeTruthy();
    expect(dict(lang)["approval.value.warn.share_two_faces"]).toBeTruthy();
    expect(dict(lang)["approval.fact.sni"]).toBeTruthy();
  });
});

describe("the words of the dialogs and the card of a shared port", () => {
  const keys = Object.keys(dictionaries.en).filter((k) => k.startsWith("share.") || k.startsWith("sp.") || k === "node.inbound.err.shareOld" || k === "node.inbound.err.shadowed" || k === "event.front_overloaded");
  const holes = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join(",");

  it("has every key in all five languages, with the same values to fill in and none left over", () => {
    expect(keys.length).toBeGreaterThan(40);
    for (const lang of langs) {
      for (const k of keys) {
        const text = dict(lang)[k];
        expect(text, `${lang} ${k}`).toBeTruthy();
        expect(holes(text!), `${lang} ${k}`).toBe(holes(dict("en")[k]!));
        if (lang !== "en" && !/^(share\.dns\.(type|name|value)|sp\.old\.go)$/.test(k)) expect(text, `${lang} ${k}`).not.toBe(dict("en")[k]);
      }
    }
  });

  it.each(langs)("words the node's reasons for a profile that does not run, and the flood, in %s", (lang) => {
    const t = txOf(lang);
    expect(inboundErrorKind("share_agent_old: this node's agent cannot share a TCP port")).toBe("share");
    expect(inboundErrorKind("sni_shadowed: the name a.example.com is served by inbound inb_b")).toBe("shadowed");
    expect(inboundErrorText(t as never, "share_agent_old: x", { port: 443 })).toBe(dict(lang)["node.inbound.err.shareOld"]);
    expect(inboundErrorText(t as never, "sni_shadowed: x", { port: 443 })).toBe(dict(lang)["node.inbound.err.shadowed"]);
    const e = describeEvent(t as never, { code: "front_overloaded", params: { port: "443", evicted: "1480" } } as never).text;
    expect(e).toContain("443");
    expect(e).toContain("1480");
    expect(e).not.toMatch(/[{}]/);
  });
});
