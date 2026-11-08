import { describe, expect, it } from "vitest";
import { looksSecret } from "./assistant";

describe("assistant secret hint", () => {
  it("recognizes labelled passwords, Russian labels, and token shapes", () => {
    expect(looksSecret("SSH password: test-only-password")).toBe(true);
    expect(looksSecret("Токен = test-only-token")).toBe(true);
    expect(looksSecret("bot 123456:" + "Ab3_-".repeat(6))).toBe(true);
    expect(looksSecret("-----BEGIN OPENSSH PRIVATE KEY-----\nprivate-key-test-data\n-----END OPENSSH PRIVATE KEY-----")).toBe(true);
    expect(looksSecret("key 0123456789abcdef0123456789abcdef")).toBe(true);
  });

  it("does not warn about an unlabelled word, UUID URL, or node name", () => {
    const text = "The word password is harmless; https://example.com/550e8400-e29b-41d4-a716-446655440000 is for node de1.";
    expect(looksSecret(text)).toBe(false);
  });
});
