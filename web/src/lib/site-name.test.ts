import { describe, expect, it } from "vitest";
import { siteNameOf } from "./site-name";

describe("siteNameOf", () => {
  it("takes a host name address and nothing else", () => {
    expect(siteNameOf("DE1.Example.com.", "sub.example.com")).toBe("de1.example.com");
    expect(siteNameOf("203.0.113.5", "sub.example.com")).toBe("");
    expect(siteNameOf("2001:db8::1", "sub.example.com")).toBe("");
    expect(siteNameOf("localhost", "sub.example.com")).toBe("");
    expect(siteNameOf("sub.example.com", "Sub.Example.com")).toBe("");
  });
});
