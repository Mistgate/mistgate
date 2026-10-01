import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { LinkModal, type LinkTarget } from "./link-modal";

const getSubscriptionLink = vi.fn();
const getSubscriptionSettings = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  users: { getSubscriptionLink: (...a: unknown[]) => getSubscriptionLink(...a) },
  subscriptions: { getSubscriptionSettings: (...a: unknown[]) => getSubscriptionSettings(...a) },
}));
// no router in the test: a link is an anchor that carries its target
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, search, className }: { children?: ReactNode; to: string; params?: object; search?: object; className?: string }) => (
    <a href={to} data-params={JSON.stringify(params ?? {})} data-search={JSON.stringify(search ?? {})} className={className}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const written: string[] = [];
const shared: unknown[] = [];
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  getSubscriptionLink.mockReset();
  written.length = 0;
  shared.length = 0;
  delete (navigator as { share?: unknown }).share;
});

async function mount(props: { initialUrl?: string; initialPassword?: string; amnezia?: boolean; target?: Partial<LinkTarget> } = {}) {
  const { amnezia = false, target: over, ...rest } = props;
  Object.assign(navigator, { clipboard: { writeText: (s: string) => (written.push(s), Promise.resolve()) } });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const target: LinkTarget = { id: "usr_1", name: "Marina", happ: true, amnezia, ...over };
  await act(async () => root!.render(<QueryClientProvider client={qc}><LinkModal target={target} onClose={() => {}} {...rest} /></QueryClientProvider>));
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const byLabel = (label: string) => document.querySelector(`[aria-label='${label}']`);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const settle = async () => {
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};

const url = "https://sub.example.com/p/TOKEN";

describe("the link modal and the page password", () => {
  it("shows the page password next to the link; the primary button copies a ready-to-send message with both", async () => {
    getSubscriptionLink.mockResolvedValue({ url, pagePassword: "k3m9-x7pq" });
    await mount();
    expect(text()).toContain("Page password");
    expect(text()).toContain("k3m9-x7pq");

    await click(button("Copy to send"));
    expect(written).toEqual([`Your link: ${url}\nPage password: k3m9-x7pq`]);
    expect(button("Copied")).toBeDefined();
  });

  it("copies the password alone and the link alone from the icon buttons, and confirms with Copied", async () => {
    getSubscriptionLink.mockResolvedValue({ url, pagePassword: "k3m9-x7pq" });
    await mount();
    expect(byLabel("Copy the page password")?.getAttribute("title")).toBe("Copy the page password");
    await click(byLabel("Copy the page password"));
    expect(byLabel("Copied")).not.toBeNull();
    await click(byLabel("Copy the link"));
    expect(written).toEqual(["k3m9-x7pq", url]);
  });

  it("never truncates the password; the link is cut in the middle and keeps its tail", async () => {
    const long = "https://sub.example.com/p/Y4pt5Qm8ZrLw3VcXnB7d2Ke9HsAf";
    await mount({ initialUrl: long, initialPassword: "rrgu-k3m9" });
    const pw = [...document.querySelectorAll("span")].find((s) => s.textContent === "rrgu-k3m9")!;
    expect(pw).toBeDefined();
    for (let el: Element | null = pw; el && el.getAttribute("role") !== "dialog"; el = el.parentElement) {
      expect(el.className).not.toMatch(/truncate|text-ellipsis|overflow-hidden/);
    }
    const tail = [...document.querySelectorAll("span")].find((s) => s.textContent === long.slice(-10))!;
    expect(tail).toBeDefined();
    expect(tail.className).not.toContain("truncate");
    expect(text()).toContain(long);
  });

  it("right after creating a user it uses what the create call returned and asks for nothing", async () => {
    await mount({ initialUrl: url, initialPassword: "aaaa-2345" });
    expect(getSubscriptionLink).not.toHaveBeenCalled();
    expect(text()).toContain("aaaa-2345");
  });

  it("without a password (the setting is off) there is no password row and the primary button copies just the link", async () => {
    getSubscriptionLink.mockResolvedValue({ url, pagePassword: "" });
    await mount();
    expect(text()).not.toContain("Page password");
    expect(byLabel("Copy the page password")).toBeNull();
    expect(text()).not.toContain("once per device");
    await click(button("Copy to send"));
    expect(written).toEqual([url]);
  });

  it("explains how it works in a disclosure, with the Amnezia line only for a user who has Amnezia", async () => {
    await mount({ initialUrl: url, initialPassword: "aaaa-2345" });
    expect(document.querySelector("details summary")?.textContent).toContain("How it works");
    expect(text()).toContain("once per device");
    expect(text()).toContain("A new link means a new password");
    expect(text()).not.toContain("Devices on the page");
    act(() => root?.unmount());
    host?.remove();
    await mount({ initialUrl: url, initialPassword: "aaaa-2345", amnezia: true });
    expect(text()).toContain("Devices on the page");
  });

  it("offers Share only where the browser has it, with the same message", async () => {
    await mount({ initialUrl: url, initialPassword: "aaaa-2345" });
    expect(byLabel("Share")).toBeNull();
    act(() => root?.unmount());
    host?.remove();

    Object.assign(navigator, { share: (d: unknown) => (shared.push(d), Promise.resolve()) });
    await mount({ initialUrl: url, initialPassword: "aaaa-2345" });
    await click(byLabel("Share"));
    expect(shared).toEqual([{ text: `Your link: ${url}\nPage password: aaaa-2345` }]);
    expect(written).toEqual([]);
  });

  it("a new link asks first, and only the confirmation issues it and shows the new password", async () => {
    getSubscriptionLink.mockImplementation(async (req: { rotate?: boolean }) => (req.rotate ? { url: `${url}2`, pagePassword: "new1-new2" } : { url, pagePassword: "old1-old2" }));
    await mount();
    expect(text()).toContain("old1-old2");

    await click(button("New link"));
    expect(text()).toContain("Issue a new link?");
    expect(getSubscriptionLink).not.toHaveBeenCalledWith({ userId: "usr_1", rotate: true });

    await click(button("Cancel"));
    await settle();
    expect(getSubscriptionLink).not.toHaveBeenCalledWith({ userId: "usr_1", rotate: true });
    expect(text()).toContain("old1-old2");

    await click(button("New link"));
    await click(button("Issue new link"));
    await settle();
    expect(getSubscriptionLink).toHaveBeenLastCalledWith({ userId: "usr_1", rotate: true });
    expect(text()).toContain("new1-new2");
    expect(text()).not.toContain("old1-old2");
  });
});

describe("a link that gives nothing yet", () => {
  const nothing = { access: { happ: false, amnezia: false }, active: true, group: { id: "grp_f", name: "Friends", givesNothing: true } };

  it("says so in red above the button, with the way to the group", async () => {
    await mount({ initialUrl: url, initialPassword: "", target: nothing });
    const alert = document.querySelector("[role=alert]")!;
    expect(alert.textContent).toContain("Marina gets nothing yet: no profile of the group “Friends” runs on a node.");
    const open = alert.querySelector<HTMLAnchorElement>("a")!;
    expect(open.textContent).toBe("Open group “Friends”");
    expect(open.dataset.search).toBe(JSON.stringify({ tab: "groups", group: "grp_f" }));
  });

  it("names the apps and nodes when the group itself gives something", async () => {
    await mount({ initialUrl: url, target: { ...nothing, group: { ...nothing.group, givesNothing: false } } });
    expect(text()).toContain("the group “Friends” gives nothing to the apps and nodes switched on");
  });

  it("stays quiet for a person who gets something, or who is not active (their page says why)", async () => {
    await mount({ initialUrl: url, target: { ...nothing, access: { happ: true, amnezia: false } } });
    expect(document.querySelector("[role=alert]")).toBeNull();
    act(() => root?.unmount());
    host?.remove();
    await mount({ initialUrl: url, target: { ...nothing, active: false } });
    expect(document.querySelector("[role=alert]")).toBeNull();
  });
});

describe("a person of AmneziaVPN keys only", () => {
  const keys = { happ: false, amnezia: true };

  it("says the friend makes the key on the page when self-service is on, and what the QR opens", async () => {
    getSubscriptionSettings.mockResolvedValue({ settings: { userPage: { allowDeviceSelfService: true } }, effectiveTitle: "" });
    await mount({ initialUrl: url, target: keys });
    expect(text()).toContain("A page where your friend gets an AmneziaVPN key");
    expect(text()).toContain("Scan it with the phone camera: the page with the key opens.");
    expect(text()).toContain("Your friend makes the AmneziaVPN key on the page.");
    expect(document.querySelector("a[href='/users/$id']")).toBeNull();
  });

  it("with self-service off says to make the key and leads to “Add device” in the card", async () => {
    getSubscriptionSettings.mockResolvedValue({ settings: { userPage: { allowDeviceSelfService: false } }, effectiveTitle: "" });
    await mount({ initialUrl: url, target: keys });
    expect(text()).toContain("Self-service is off: make the key yourself.");
    const add = document.querySelector<HTMLAnchorElement>("a[href='/users/$id']")!;
    expect(add.textContent).toBe("Add device");
    expect(add.dataset.params).toBe(JSON.stringify({ id: "usr_1" }));
    expect(add.dataset.search).toBe(JSON.stringify({ add: "device" }));
  });

  it("describes a person of both ways as one page with both", async () => {
    const apps = [{ kind: App.HAPP, name: "Happ" }, { kind: App.HAPP, name: "FlClash" }, { kind: App.AMNEZIA, name: "AmneziaVPN" }];
    getSubscriptionSettings.mockResolvedValue({ settings: { apps }, effectiveTitle: "" });
    await mount({ initialUrl: url, target: { happ: true, amnezia: true } });
    expect(text()).toContain("One page: the subscription (Happ, FlClash) and AmneziaVPN keys");
  });
});
