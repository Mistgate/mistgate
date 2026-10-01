import { describe, expect, it } from "vitest";
import { confParts } from "./conf-preview";

const kinds = (line: string) => confParts(line).map((p) => `${p.kind}:${p.text}`);

describe("confParts", () => {
  it("colours section headers, keys, the obfuscation keys apart, and dims masked secrets", () => {
    expect(kinds("[Interface]")).toEqual(["section:[Interface]"]);
    expect(kinds("Address = 10.0.0.2/32")).toEqual(["key:Address", "plain: = ", "plain:10.0.0.2/32"]);
    expect(kinds("Jc = 6")[0]).toBe("obf:Jc");
    expect(kinds("I1 = <b 0x00>")[0]).toBe("obf:I1");
    expect(kinds("PrivateKey = ••••••••")).toEqual(["key:PrivateKey", "plain: = ", "mask:••••••••"]);
    expect(kinds("# a note")).toEqual(["comment:# a note"]);
  });

  it("keeps the text whole and only dims the masks in a share link", () => {
    const line = "hysteria2://••••@de1.example.com:443/?obfs-password=••••&sni=x";
    const parts = confParts(line);
    expect(parts.map((p) => p.text).join("")).toBe(line);
    expect(parts.filter((p) => p.kind === "mask")).toHaveLength(2);
    expect(parts.every((p) => p.kind === "plain" || p.kind === "mask")).toBe(true);
  });
});
