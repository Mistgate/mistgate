import { describe, expect, it } from "vitest";
import { hy2, other } from "./schema.fixtures";
import { allFields, changedFields, errorsOf, getAt, isChanged, MASK, parseSchema, parseSettings, pointerOf, setAt, withGeneratedSecrets } from "./schema";

describe("parseSchema", () => {
  const groups = parseSchema(hy2);
  const fields = allFields(groups);
  const by = (key: string) => fields.find((f) => f.key === key)!;

  it("orders groups basics, obfuscation, advanced and fields by x-order", () => {
    expect(groups.map((g) => g.id)).toEqual(["basics", "obfuscation", "advanced"]);
    expect(groups[0]!.fields.map((f) => f.key)).toEqual(["/port", "/hop", "/tls_mode"]);
  });

  it("reads kinds, units, choices and the critical flag", () => {
    expect(by("/port")).toMatchObject({ kind: "number", integer: true, critical: true, hint: "Port the node listens on." });
    expect(by("/up_mbps")).toMatchObject({ unit: "Mbit/s", critical: false });
    expect(by("/udp").kind).toBe("toggle");
    expect(by("/tls_mode")).toMatchObject({ kind: "choice", segmented: true });
    expect(by("/tls_mode").choices.map((c) => c.label)).toEqual(["Let's Encrypt", "Self-signed (pinned)"]);
  });

  it("makes a port range one field with two ends", () => {
    expect(by("/hop")).toMatchObject({ kind: "range", parts: [["hop", "from"], ["hop", "to"]], critical: true });
  });

  it("flattens an object into its fields, inheriting group and critical", () => {
    expect(by("/obfs/type")).toMatchObject({ id: "obfs.type", kind: "choice", critical: true, title: "Type" });
    expect(by("/obfs/password")).toMatchObject({ kind: "generate", secret: true, critical: true });
    expect(groups[1]!.fields.map((f) => f.key)).toEqual(["/obfs/type", "/obfs/password"]);
  });

  it("lends the title and description of an object to its only field", () => {
    expect(by("/masquerade/type")).toMatchObject({ title: "Masquerade", hint: "What the node answers to anything that is not a client." });
  });

  it("returns no groups for a schema it cannot read", () => {
    expect(parseSchema("not json")).toEqual([]);
    expect(parseSchema("{}")).toEqual([]);
  });
});

describe("a second protocol", () => {
  const groups = parseSchema(other);
  const fields = allFields(groups);
  const by = (key: string) => fields.find((f) => f.key === key)!;

  it("puts fields without a group into advanced and drops what it cannot edit", () => {
    expect(groups.map((g) => g.id)).toEqual(["basics", "obfuscation", "advanced"]);
    expect(fields.map((f) => f.key)).not.toContain("/peers");
    expect(groups[2]!.fields.map((f) => f.key)).toEqual(["/dns", "/mtu", "/preset", "/ratio"]);
  });

  it("handles secrets without the generate widget, many choices and decimals", () => {
    expect(by("/key")).toMatchObject({ kind: "secret", secret: true });
    expect(by("/preset")).toMatchObject({ kind: "choice", segmented: false });
    expect(by("/ratio")).toMatchObject({ kind: "number", integer: false });
    expect(by("/junk/count").title).toBe("Jc");
  });
});

describe("values by path", () => {
  it("escapes pointers", () => {
    expect(pointerOf(["a/b", "c~d"])).toBe("/a~1b/c~0d");
  });

  it("reads and writes without touching the original", () => {
    const base = { obfs: { type: "none", password: "••••" }, port: 443 };
    const next = setAt(base, ["obfs", "type"], "salamander");
    expect(getAt(next, ["obfs", "type"])).toBe("salamander");
    expect(getAt(base, ["obfs", "type"])).toBe("none");
    expect(getAt(next, ["obfs", "password"])).toBe("••••");
    expect(getAt(setAt({}, ["a", "b", "c"], 1), ["a", "b", "c"])).toBe(1);
  });

  it("detects changes, including either end of a range, and matches errors to fields", () => {
    const fields = allFields(parseSchema(hy2));
    const base = parseSettings('{"port":443,"hop":{"from":0,"to":0},"obfs":{"type":"none","password":"••••"}}');
    expect(changedFields(fields, base, base)).toEqual([]);
    const cur = setAt(setAt(base, ["hop", "to"], 50000), ["obfs", "password"], "$generate");
    const changed = changedFields(fields, cur, base).map((f) => f.key);
    expect(changed).toEqual(["/hop", "/obfs/password"]);
    const hop = fields.find((f) => f.key === "/hop")!;
    expect(isChanged(hop, base, base)).toBe(false);
    expect(errorsOf(hop, [{ pointer: "/hop/to" }, { pointer: "/port" }])).toEqual([{ pointer: "/hop/to" }]);
  });
});

describe("withGeneratedSecrets", () => {
  const fields = allFields(parseSchema(hy2));
  it("fills a masked secret and leaves every other value alone", () => {
    const out = withGeneratedSecrets({ port: 443, obfs: { type: "salamander", password: MASK } }, fields, () => "fresh");
    expect(out).toEqual({ port: 443, obfs: { type: "salamander", password: "fresh" } });
  });
  it("creates a secret that is missing, keeps one that is set", () => {
    expect(getAt(withGeneratedSecrets({}, fields, () => "x"), ["obfs", "password"])).toBe("x");
    expect(getAt(withGeneratedSecrets({ obfs: { password: "mine" } }, fields, () => "x"), ["obfs", "password"])).toBe("mine");
  });
});
