import { describe, expect, it } from "vitest";
import { draftProblem } from "./cloudflare";

describe("draftProblem", () => {
  const off = { enabled: false, siteKey: "", secret: "" };
  it("lets the check stay off without keys", () => {
    expect(draftProblem(off, { turnstileSecretSet: false })).toBeNull();
  });
  it("needs a site key and a secret (stored or typed) to turn it on", () => {
    expect(draftProblem({ ...off, enabled: true }, { turnstileSecretSet: true })).toBe("cf.needKeys");
    expect(draftProblem({ enabled: true, siteKey: "0x4AAA", secret: "" }, { turnstileSecretSet: false })).toBe("cf.needKeys");
    expect(draftProblem({ enabled: true, siteKey: "0x4AAA", secret: "" }, { turnstileSecretSet: true })).toBeNull();
    expect(draftProblem({ enabled: true, siteKey: "0x4AAA", secret: "s3cret" }, { turnstileSecretSet: false })).toBeNull();
  });
  it("applies the server's limits before asking the server", () => {
    expect(draftProblem({ ...off, siteKey: "bad key!" }, { turnstileSecretSet: false })).toBe("cf.siteKeyError");
    expect(draftProblem({ ...off, siteKey: "a".repeat(129) }, { turnstileSecretSet: false })).toBe("cf.siteKeyError");
    expect(draftProblem({ ...off, secret: "x".repeat(257) }, { turnstileSecretSet: false })).toBe("cf.secretError");
  });
});
