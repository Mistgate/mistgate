import { describe, expect, it } from "vitest";
import { mtprotoHidden, mtprotoOn443 } from "./mtproto-form";
import { allFields, parseSchema, type Settings } from "./schema";

// the schema as the panel serves it (internal/panel/protocols/mtproto/schema.go), reduced to what the form reads
const schema = JSON.stringify({
  type: "object",
  properties: {
    port: { type: "integer", title: "TCP port", "x-group": "basics", "x-order": 10 },
    fake_tls: {
      type: "object",
      title: "Fake TLS",
      "x-group": "basics",
      "x-order": 20,
      properties: {
        kind: { type: "string", enum: ["own", "site"], default: "own", title: "Name in the TLS handshake", "x-widget": "segmented", "x-order": 5 },
        domain: { type: "string", title: "Site", "x-order": 10 },
      },
    },
  },
});
const fields = allFields(parseSchema(schema));
const hidden = (s: Settings) => [...mtprotoHidden(fields, s)];

describe("mtprotoHidden", () => {
  it("asks for the site only when the name is another site", () => {
    expect(fields.map((f) => f.id)).toContain("fake_tls.domain");
    expect(hidden({ port: 443, fake_tls: { kind: "own" } })).toEqual(["fake_tls.domain"]);
    expect(hidden({ port: 443 })).toEqual(["fake_tls.domain"]);
    expect(hidden({ port: 443, fake_tls: { kind: "site", domain: "www.google.com" } })).toEqual([]);
  });

  it("warns about port 80 only for another site on TCP 443", () => {
    expect(mtprotoOn443({ port: 443, fake_tls: { kind: "site" } })).toBe(true);
    expect(mtprotoOn443({ port: 8443, fake_tls: { kind: "site" } })).toBe(false);
    expect(mtprotoOn443({ port: 443, fake_tls: { kind: "own" } })).toBe(false);
    expect(mtprotoOn443({ port: 443 })).toBe(false);
  });
});
