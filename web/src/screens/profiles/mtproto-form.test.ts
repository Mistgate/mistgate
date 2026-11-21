import { describe, expect, it } from "vitest";
import { dictionaries, langs } from "@/i18n";
import { mtprotoEdited, mtprotoGroups, mtprotoHidden, mtprotoLocked, mtprotoOn443, mtprotoWeb, mtprotoWebOverrides } from "./mtproto-form";
import { allFields, parseSchema, type Settings } from "./schema";
import { mtproto } from "./schema.fixtures";

// the schema as the panel serves it (internal/panel/protocols/mtproto/schema.go), reduced to what the form reads
const groups = parseSchema(mtproto);
const fields = allFields(groups);
const hidden = (s: Settings) => [...mtprotoHidden(fields, s)];

describe("mtprotoHidden", () => {
  it("asks for the site only when the name is another site", () => {
    expect(fields.map((f) => f.id)).toContain("fake_tls.domain");
    expect(hidden({ port: 443, fake_tls: { kind: "own" } })).toEqual(["fake_tls.domain"]);
    expect(hidden({ port: 443 })).toEqual(["fake_tls.domain"]);
    expect(hidden({ port: 443, fake_tls: { kind: "site", domain: "www.google.com" } })).toEqual([]);
  });

  it("leaves no Fake TLS name in the Web mode, whatever the old document says", () => {
    expect(hidden({ mode: "web", port: 443, fake_tls: { kind: "own" } }).sort()).toEqual(["fake_tls.domain", "fake_tls.kind"]);
    expect(hidden({ mode: "web", port: 443, fake_tls: { kind: "site", domain: "www.google.com" } }).sort()).toEqual(["fake_tls.domain", "fake_tls.kind"]);
    expect(hidden({ mode: "fake_tls", port: 443, fake_tls: { kind: "own" } })).toEqual(["fake_tls.domain"]);
  });

  it("warns about port 80 only for another site on TCP 443", () => {
    expect(mtprotoOn443({ port: 443, fake_tls: { kind: "site" } })).toBe(true);
    expect(mtprotoOn443({ port: 8443, fake_tls: { kind: "site" } })).toBe(false);
    expect(mtprotoOn443({ port: 443, fake_tls: { kind: "own" } })).toBe(false);
    expect(mtprotoOn443({ port: 443 })).toBe(false);
    expect(mtprotoOn443({ mode: "web", port: 443, fake_tls: { kind: "site" } })).toBe(false);
  });
});

describe("the Web mode", () => {
  it("is on only for mode web", () => {
    expect([{}, { mode: "fake_tls" }, { mode: "web" }].map(mtprotoWeb)).toEqual([false, false, true]);
  });

  it("fixes the port and nothing else", () => {
    expect([...mtprotoLocked({ mode: "web", port: 443 })]).toEqual(["port"]);
    expect(mtprotoLocked({ mode: "fake_tls", port: 8443 }).size).toBe(0);
  });

  it("takes port 443 and the node's own name when it is chosen, and leaves other edits alone", () => {
    const prev = { mode: "fake_tls", port: 8443, fake_tls: { kind: "site", domain: "www.google.com" } };
    expect(mtprotoEdited(prev, { ...prev, mode: "web" })).toEqual({ mode: "web", port: 443, fake_tls: { kind: "own", domain: "www.google.com" } });
    expect(mtprotoEdited(prev, { ...prev, port: 9443 })).toEqual({ ...prev, port: 9443 });
    const web = { mode: "web", port: 443, fake_tls: { kind: "own" } };
    expect(mtprotoEdited(web, { ...web, mode: "fake_tls" })).toEqual({ ...web, mode: "fake_tls" });
  });

  it("names the nodes where a port of its own is not 443, only in the Web mode", () => {
    const inbounds = [
      { nodeName: "de1", portOverride: 8443 },
      { nodeName: "nl1", portOverride: 0 },
      { nodeName: "fi1", portOverride: 443 },
    ];
    expect(mtprotoWebOverrides(inbounds, { mode: "web" })).toEqual([{ node: "de1", port: 8443 }]);
    expect(mtprotoWebOverrides(inbounds, { mode: "fake_tls" })).toEqual([]);
  });
});

describe("mtprotoGroups", () => {
  it("puts the mode, which the panel leaves without a group, first in the basics instead of the fold of the advanced ones", () => {
    expect(groups.find((g) => g.id === "advanced")?.fields.map((f) => f.id)).toEqual(["mode"]);
    const fixed = mtprotoGroups(groups);
    expect(fixed.map((g) => g.id)).toEqual(["basics"]);
    expect(fixed[0]!.fields.map((f) => f.id)).toEqual(["mode", "port", "fake_tls.kind", "fake_tls.domain"]);
  });

  it("leaves a schema alone that already groups the mode, or has none", () => {
    const grouped = [{ id: "basics", fields: allFields(groups) }];
    expect(mtprotoGroups(grouped)).toBe(grouped);
    const noMode = [{ id: "basics", fields: fields.filter((f) => f.id !== "mode") }];
    expect(mtprotoGroups(noMode)).toBe(noMode);
  });
});

describe("the words of the Web mode", () => {
  const keys = [
    "profiles.f.mtproto.mode.title",
    "profiles.f.mtproto.mode.hint",
    "profiles.f.mtproto.mode.enum.fake_tls",
    "profiles.f.mtproto.mode.enum.web",
    "mtproto.web.dns",
    "mtproto.web.port",
    "mtproto.web.override",
    "relay.unsupported.webproxy",
    "node.inbound.err.agentOld",
    "tgproxy.web",
    "tgproxy.webHint",
  ] as const;
  it("are in all five languages, and the sentence about the nodes keeps its {nodes}", () => {
    for (const lang of langs) {
      const d = dictionaries[lang] as Record<string, string>;
      for (const k of keys) expect(d[k], `${lang} ${k}`).toBeTruthy();
      expect(d["mtproto.web.override"], lang).toContain("{nodes}");
      if (lang !== "en") for (const k of ["profiles.f.mtproto.mode.hint", "mtproto.web.dns", "mtproto.web.port", "relay.unsupported.webproxy", "node.inbound.err.agentOld"]) expect(d[k], `${lang} ${k}`).not.toBe((dictionaries.en as Record<string, string>)[k]);
    }
  });
});
