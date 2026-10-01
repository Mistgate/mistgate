import { afterEach, describe, expect, it, vi } from "vitest";
import { goToSignIn, useSignOut } from "./session";

const logout = vi.hoisted(() => vi.fn());
vi.mock("./api", async (orig) => ({ ...(await orig<typeof import("./api")>()), auth: { logout } }));

afterEach(() => {
  vi.unstubAllGlobals();
  logout.mockReset();
});

function stubLocation() {
  const assign = vi.fn();
  vi.stubGlobal("location", { ...window.location, assign });
  return assign;
}

// A router transition would keep the document (and its CSP) that was loaded before the owner switched
// Turnstile on, and Cloudflare's script would be blocked on the sign-in screen.
describe("leaving for the sign-in screen", () => {
  it("is a full page load on the sign-in URL", () => {
    const assign = stubLocation();
    goToSignIn();
    expect(assign).toHaveBeenCalledWith(new URL("login", document.baseURI).href);
  });

  it("happens on sign out, also when the server call fails", async () => {
    const assign = stubLocation();
    logout.mockResolvedValue({});
    await useSignOut()();
    expect(assign).toHaveBeenCalledTimes(1);

    logout.mockRejectedValue(new Error("offline"));
    await expect(useSignOut()()).rejects.toThrow("offline");
    expect(assign).toHaveBeenCalledTimes(2);
  });
});
