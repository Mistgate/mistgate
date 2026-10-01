import { describe, expect, it } from "vitest";
import { initialOf } from "./initial";

describe("initialOf", () => {
  it("upper-cases the first letter in both alphabets", () => {
    expect(initialOf("тест", "ru")).toBe("Т");
    expect(initialOf("alice", "en")).toBe("A");
    expect(initialOf("ёлка", "ru")).toBe("Ё");
  });
  it("takes one grapheme, not one UTF-16 unit", () => {
    expect(initialOf("𝒜lpha")).toBe("𝒜");
    expect(initialOf("école")).toBe("É");
  });
  it("skips leading punctuation but keeps an emoji or symbol when there is no letter", () => {
    expect(initialOf("  (боб)")).toBe("Б");
    expect(initialOf("🐈")).toBe("🐈");
    expect(initialOf("@@")).toBe("@");
  });
  it("gives a placeholder for an empty name", () => {
    expect(initialOf("   ")).toBe("?");
  });
});
