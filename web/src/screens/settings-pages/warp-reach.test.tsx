import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { en } from "@/i18n/en";
import { cleanReachDomain, isReachDomain, listProblem, reachProblemOf } from "@/lib/warpreach";
import { WarpReachCard } from "./warp-reach";

// Settings -> System: the sites WARP is checked against.

const auth = vi.hoisted(() => ({ me: vi.fn() }));
const warpReach = vi.hoisted(() => ({ getWarpReachSettings: vi.fn(), updateWarpReachSettings: vi.fn() }));
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), auth, warpReach }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const builtin = ["example.com", "example.org"];
let role = Role.OWNER;
let unavailable: string[] = [];
let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  role = Role.OWNER;
  unavailable = [];
  auth.me.mockImplementation(async () => ({ admin: { id: "adm_1", displayName: "Alice", role }, unavailable }));
  warpReach.getWarpReachSettings.mockResolvedValue({ settings: { domains: builtin }, defaults: { domains: builtin }, customised: false });
  warpReach.updateWarpReachSettings.mockImplementation(async (r: { settings?: { domains: string[] }; useDefaults?: boolean }) =>
    r.useDefaults ? { settings: { domains: builtin }, customised: false } : { settings: { domains: r.settings?.domains ?? [] }, customised: true },
  );
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  auth.me.mockReset();
  Object.values(warpReach).forEach((m) => m.mockReset());
});

const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <WarpReachCard />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await settle();
}
const text = () => document.body.textContent ?? "";
const chips = () => [...document.querySelectorAll(".sx-wl li b")].map((b) => b.textContent);
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label) as HTMLButtonElement | undefined;
const click = async (b: Element | undefined | null) => {
  await act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  for (let i = 0; i < 3; i++) await settle();
};
const type = async (value: string) => {
  const input = document.querySelector<HTMLInputElement>(".sx-wi input")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const add = async (value: string) => {
  await type(value);
  await click(button("Add"));
};

describe("the checks of a list in the browser (hints only: the panel decides)", () => {
  it("knows a lower-case host name of two or more parts", () => {
    for (const ok of ["example.com", "a-b.example.co.uk", "x1.y2"]) expect(isReachDomain(ok)).toBe(true);
    for (const bad of ["example", "Example.com", "https://example.com", "example.com/x", "example.com:443", "1.2.3.4:80", "-a.com", "a..com", ""]) expect(isReachDomain(bad)).toBe(false);
  });

  it("names the first problem of a list by the panel's own code", () => {
    expect(listProblem(["a.com", "b.com"])).toBeUndefined();
    expect(listProblem(Array.from({ length: 9 }, (_, i) => `h${i}.example.com`))).toBe("reach_domains_too_many");
    expect(listProblem(["a.com", "nope"])).toBe("reach_domain_invalid");
    expect(listProblem(["a.com", "a.com"])).toBe("reach_domain_duplicate");
    expect(cleanReachDomain("  Example.COM ")).toBe("example.com");
  });

  it("tells a refusal of a list from any other failure", () => {
    expect(reachProblemOf(new ConnectError("reach_domain_duplicate", Code.InvalidArgument))).toBe("reach_domain_duplicate");
    expect(reachProblemOf(new ConnectError("reach_domains_too_many", Code.InvalidArgument))).toBe("reach_domains_too_many");
    expect(reachProblemOf(new ConnectError("boom", Code.Internal))).toBeUndefined();
  });
});

describe("the card of the sites WARP is checked against", () => {
  it("shows the built-in list as it is, with nothing to save or to go back to", async () => {
    await mount();
    expect(text()).toContain(en["sx.warp.title"]);
    expect(text()).toContain(en["sx.warp.tag.default"]);
    expect(chips()).toEqual(builtin);
    expect(button("Save")?.disabled).toBe(true);
    expect(button(en["sx.warp.reset"])?.disabled).toBe(true);
  });

  it("adds names (cleaned, several at once, no repeats), removes one, and saves the list", async () => {
    await mount();
    await add(" News.Example.Org, video.example.net example.com ");
    expect(chips()).toEqual([...builtin, "news.example.org", "video.example.net"]);
    expect(text()).toContain(en["err.reach_domain_duplicate"]);
    await click(document.querySelector('button[aria-label="Remove example.org"]'));
    expect(chips()).toEqual(["example.com", "news.example.org", "video.example.net"]);
    await click(button("Save"));
    expect(warpReach.updateWarpReachSettings).toHaveBeenCalledWith({ settings: { domains: ["example.com", "news.example.org", "video.example.net"] } });
    expect(text()).toContain(en["sx.warp.saved"]);
    expect(text()).toContain(en["sx.warp.tag.custom"]);
    expect(button(en["sx.warp.reset"])?.disabled).toBe(false);
  });

  it("flags a name the panel would refuse but still lets the panel judge", async () => {
    await mount();
    await add("https://example.com/x");
    expect(document.querySelectorAll(".sx-wl li.bad")).toHaveLength(1);
    expect(text()).toContain(en["err.reach_domain_invalid"]);
    warpReach.updateWarpReachSettings.mockRejectedValueOnce(new ConnectError("reach_domain_invalid", Code.InvalidArgument));
    await click(button("Save"));
    expect(warpReach.updateWarpReachSettings).toHaveBeenCalledTimes(1);
    expect(document.querySelector("small[role=alert]")?.textContent).toBe(en["err.reach_domain_invalid"]);
  });

  it.each([
    ["reach_domains_too_many", "err.reach_domains_too_many"],
    ["reach_domain_invalid", "err.reach_domain_invalid"],
    ["reach_domain_duplicate", "err.reach_domain_duplicate"],
  ] as const)("shows the panel's refusal %s by its code", async (code, key) => {
    await mount();
    await add("news.example.org");
    warpReach.updateWarpReachSettings.mockRejectedValueOnce(new ConnectError(code, Code.InvalidArgument));
    await click(button("Save"));
    expect(document.querySelector("small[role=alert]")?.textContent).toBe(en[key]);
    // an edit takes the refusal away
    await click(document.querySelector('button[aria-label="Remove news.example.org"]'));
    expect(document.querySelector("small[role=alert]")).toBeNull();
  });

  it("turns the check off with an empty list and says so", async () => {
    await mount();
    for (const d of builtin) await click(document.querySelector(`button[aria-label="Remove ${d}"]`));
    expect(text()).toContain(en["sx.warp.empty"]);
    await click(button("Save"));
    expect(warpReach.updateWarpReachSettings).toHaveBeenCalledWith({ settings: { domains: [] } });
    expect(text()).toContain(en["sx.warp.tag.off"]);
  });

  it("goes back to the built-in list with use_defaults", async () => {
    warpReach.getWarpReachSettings.mockResolvedValue({ settings: { domains: ["news.example.org"] }, defaults: { domains: builtin }, customised: true });
    await mount();
    expect(chips()).toEqual(["news.example.org"]);
    await click(button(en["sx.warp.reset"]));
    expect(warpReach.updateWarpReachSettings).toHaveBeenCalledWith({ useDefaults: true });
    expect(chips()).toEqual(builtin);
    expect(text()).toContain(en["sx.warp.resetDone"]);
    expect(text()).toContain(en["sx.warp.tag.default"]);
  });

  it("is not there for a helper (the panel refuses both calls), and no call is made", async () => {
    role = Role.HELPER;
    await mount();
    expect(document.querySelector("[data-card=warp-reach]")).toBeNull();
    expect(warpReach.getWarpReachSettings).not.toHaveBeenCalled();
  });

  it("says it is not in the Cloudflare edition, from the session's list or from the panel's refusal", async () => {
    unavailable = ["health_checks"];
    await mount();
    expect(text()).toContain("Not available in the Cloudflare edition yet: Tunnel checks.");
    expect(warpReach.getWarpReachSettings).not.toHaveBeenCalled();
    act(() => root?.unmount());
    host?.remove();
    unavailable = [];
    warpReach.getWarpReachSettings.mockRejectedValue(new ConnectError("unavailable_in_edition: feature=health_checks", Code.FailedPrecondition));
    await mount();
    expect(text()).toContain("Not available in the Cloudflare edition yet: Tunnel checks.");
    expect(document.querySelector(".sx-wi")).toBeNull();
  });

  it("shows a failed load with a retry, not an empty list", async () => {
    warpReach.getWarpReachSettings.mockRejectedValue(new ConnectError("down", Code.Unavailable));
    await mount();
    expect(document.querySelector("[data-card=warp-reach] [role=alert]")).not.toBeNull();
    expect(document.querySelector(".sx-wl")).toBeNull();
  });
});
