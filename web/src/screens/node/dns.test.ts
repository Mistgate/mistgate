import { describe, expect, it } from "vitest";
import { nodeDnsMode, nodeDnsPresets, nodeDnsResolvers } from "./dns";

describe("per-node DNS presets", () => {
  it("means the server's own resolver when no resolvers are stored", () => {
    expect(nodeDnsMode([])).toBe("system");
    expect(nodeDnsResolvers("system")).toEqual([]);
  });

  it("recognizes and applies the Yandex preset", () => {
    expect(nodeDnsMode(nodeDnsPresets.yandex)).toBe("yandex");
    expect(nodeDnsResolvers("yandex")).toEqual(["77.88.8.8", "77.88.8.1"]);
  });

  it("recognizes and applies Cloudflare plus Google", () => {
    expect(nodeDnsMode(nodeDnsPresets.cloudflareGoogle)).toBe("cloudflareGoogle");
    expect(nodeDnsResolvers("cloudflareGoogle")).toEqual(["1.1.1.1", "8.8.8.8"]);
  });

  it("keeps custom resolver lists editable", () => {
    expect(nodeDnsMode(["9.9.9.9", "149.112.112.112"])).toBe("custom");
  });
});
