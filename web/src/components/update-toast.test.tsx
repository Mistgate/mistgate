import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { BundleStatus, NodeUpdateState, RolloutStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { setLang } from "@/i18n";
import { meQuery } from "@/lib/session";
import { UpdateToast } from "./update-toast";

const getUpdates = vi.fn();
let pathname = "/nodes";
vi.mock("@/lib/api", () => ({
  updates: { getUpdates: (...a: unknown[]) => getUpdates(...a) },
  auth: { me: () => Promise.resolve({}) },
  basepath: "/",
  isUnauthenticated: () => false,
}));
// no router here: a link is an anchor with where it goes, and the page is whatever the test says
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className }: { children?: ReactNode; to: string; className?: string }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
  useRouterState: ({ select }: { select: (s: { location: { pathname: string } }) => unknown }) => select({ location: { pathname } }),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  setLang("en");
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  pathname = "/nodes";
  localStorage.clear();
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  getUpdates.mockReset();
  vi.restoreAllMocks();
});

const release = (version: string, over: Record<string, unknown> = {}) => ({
  version,
  url: `https://github.com/Mistgate/mistgate/releases/tag/${version}`,
  checkedUnix: 1000,
  available: true,
  supported: true,
  installable: true,
  installing: false,
  errorKey: "",
  ...over,
});
const node = (id: string, state: NodeUpdateState) => ({ nodeId: id, name: id, state, supportsUpdate: true, built: 100 });
const status = (update: Record<string, unknown>, over: Record<string, unknown> = {}) => ({
  nowUnix: 1000,
  panel: { version: "v0.1.17", built: 100, update },
  bundle: { status: BundleStatus.TRUSTED, version: "v0.1.17", built: 100 },
  nodes: [node("de1", NodeUpdateState.UP_TO_DATE)],
  ...over,
});

/** Mounts the card for `role` (a fresh page load each time: the storage is the only thing that survives it). */
async function mount(data: unknown, role = Role.OWNER) {
  act(() => root?.unmount());
  host?.remove();
  getUpdates.mockResolvedValue(data);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(meQuery.queryKey, { admin: { role } } as never);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <UpdateToast />
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => host!.textContent ?? "";
const button = (label: string) => host!.querySelector<HTMLElement>(`button[aria-label^="${label}"]`);
const click = (el: Element | null) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));

describe("the update notice", () => {
  it("tells the owner about a newer signed panel release, with the way to install it and its notes", async () => {
    await mount(status(release("v0.1.18")));
    expect(text()).toContain("Version v0.1.18 is out");
    const live = host!.querySelector("[role=status]")!;
    expect(live.getAttribute("aria-live")).toBe("polite");
    // it only leads to the Updates page, where the install keeps its own confirmation
    expect(host!.querySelector("a[href='/updates']")?.textContent).toBe("Update");
    const news = [...host!.querySelectorAll("a")].find((a) => a.textContent === "What’s new")!;
    expect(news.getAttribute("href")).toBe("https://github.com/Mistgate/mistgate/releases/tag/v0.1.18");
    expect(news.getAttribute("rel")).toContain("noreferrer");
  });

  it("is silent when nothing is new, while the release installs, and says so when the panel cannot install it itself", async () => {
    await mount(status(release("v0.1.17", { available: false })));
    expect(text()).toBe("");
    await mount(status(release("v0.1.18", { installing: true })));
    expect(text()).toBe("");
    await mount(status(release("v0.1.18", { installable: false })));
    expect(text()).toContain("cannot install it by itself");
  });

  it("is not shown to anyone but the owner, who is the only one asked", async () => {
    await mount(status(release("v0.1.18")), Role.HELPER);
    expect(text()).toBe("");
    expect(getUpdates).not.toHaveBeenCalled();
  });

  it("is not shown on the Updates page, which says it itself", async () => {
    pathname = "/updates";
    await mount(status(release("v0.1.18")));
    expect(text()).toBe("");
    expect(getUpdates).not.toHaveBeenCalled();
  });

  it("closing hides this version for good and a newer one shows again", async () => {
    await mount(status(release("v0.1.18")));
    await click(button("Hide until the next version"));
    expect(text()).toBe("");
    expect(localStorage.getItem("update-toast-dismissed-panel")).toBe("v0.1.18");

    await mount(status(release("v0.1.18"))); // a new page load
    expect(text()).toBe("");
    await mount(status(release("v0.1.19")));
    expect(text()).toContain("Version v0.1.19 is out");
  });

  it("collapses to a pill with the version, opens again on a click, and remembers which it was", async () => {
    await mount(status(release("v0.1.18")));
    await click(button("Collapse"));
    expect(text()).toBe("v0.1.18");
    expect(host!.querySelector("a")).toBeNull();

    await mount(status(release("v0.1.18"))); // still a pill after a reload
    expect(text()).toBe("v0.1.18");
    await click(button("Show the update notice"));
    expect(text()).toContain("Version v0.1.18 is out");

    await mount(status(release("v0.1.18")));
    expect(text()).toContain("Version v0.1.18 is out");
  });

  it("works without storage: it shows, closes and collapses for this page load", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    await mount(status(release("v0.1.18")));
    expect(text()).toContain("Version v0.1.18 is out");
    await click(button("Collapse"));
    expect(text()).toBe("v0.1.18");
    await click(button("Show the update notice"));
    await click(button("Hide until the next version"));
    expect(text()).toBe("");
  });

  it("offers the nodes when the panel is current but nodes run an older agent than the trusted bundle", async () => {
    const nodes = [node("de1", NodeUpdateState.OUTDATED), node("nl1", NodeUpdateState.UP_TO_DATE), node("fi1", NodeUpdateState.OUTDATED)];
    await mount(status(release("v0.1.17", { available: false }), { nodes }));
    expect(text()).toContain("Nodes can be updated to v0.1.17");
    expect(text()).toContain("To update: 2 of 3");
    expect(host!.querySelector("a[href='/updates']")).toBeTruthy();
    expect([...host!.querySelectorAll("a")].some((a) => a.textContent === "What’s new")).toBe(false);

    // a rollout in flight, or a bundle the panel does not trust, is the Updates page's business
    await mount(status(release("v0.1.17", { available: false }), { nodes, rollout: { status: RolloutStatus.RUNNING } }));
    expect(text()).toBe("");
    await mount(status(release("v0.1.17", { available: false }), { nodes, bundle: { status: BundleStatus.UNTRUSTED, version: "v0.1.17", built: 100 } }));
    expect(text()).toBe("");
  });

  it("falls through to the nodes when the panel notice was closed, and closing one does not close the other", async () => {
    const nodes = [node("de1", NodeUpdateState.OUTDATED)];
    await mount(status(release("v0.1.18"), { nodes }));
    await click(button("Hide until the next version"));
    expect(text()).toContain("Nodes can be updated to");
    await click(button("Hide until the next version"));
    expect(text()).toBe("");
    expect(localStorage.getItem("update-toast-dismissed-panel")).toBe("v0.1.18");
    expect(localStorage.getItem("update-toast-dismissed-nodes")).toBe("v0.1.17@100");
  });
});
