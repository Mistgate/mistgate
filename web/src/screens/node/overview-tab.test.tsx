import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { setLang } from "@/i18n";
import { nodePageData, type NState } from "../dev-kit-node-page";
import { NodeDetail } from "./index";

// The node page's header and Overview on the data of the dev-kit scene (the board's demo data): what each state shows.

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, className, title }: { children?: ReactNode; to: string; params?: Record<string, string>; className?: string; title?: string }) => (
    <a href={to.replace(/\$(\w+)/g, (_, k: string) => params?.[k] ?? "")} className={className} title={title}>
      {children}
    </a>
  ),
  useNavigate: () => () => {},
  useSearch: () => ({}),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});
beforeEach(() => setLang("en"));

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(state: NState) {
  const { qc, data } = nodePageData(state);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <NodeDetail data={data as never} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => host!.textContent ?? "";
const buttons = (label: string) => [...host!.querySelectorAll("button")].filter((b) => b.textContent?.includes(label));
const headerButtons = (label: string) => [...host!.querySelectorAll("header button")].filter((b) => b.textContent?.includes(label));

describe("the header", () => {
  it("names the node, its state and what is known of it, and the two actions", async () => {
    await mount("calm");
    expect(host!.querySelector("h1")!.textContent).toBe("de1");
    expect(text()).toContain("Frankfurt · Example Cloud");
    expect(text()).toContain("203.0.113.7");
    expect(text()).toContain("WARP · FRA");
    expect(text()).toContain("0.1.71");
    expect(text()).toContain("load is low");
    expect(headerButtons("Restart profiles")).toHaveLength(1);
    expect(headerButtons("Doctor")).toHaveLength(1);
  });

  it("shows the doctor's open problems on its button, and for how long a silent node has said nothing", async () => {
    await mount("down");
    expect(headerButtons("Doctor")[0]!.textContent).toContain("1");
    expect(text()).toContain("no data since");
  });

  it("opens the sentence about the agent's way when it goes through an entry", async () => {
    await mount("via");
    const chip = headerButtons("agent via msk1")[0] as HTMLElement;
    act(() => chip.click());
    expect(chip.getAttribute("aria-expanded")).toBe("true");
    expect(host!.querySelector("header p")).not.toBeNull();
  });
});

describe("the Overview", () => {
  it("is calm when nothing waits: the resources with what took them, the checks, who is on the node", async () => {
    await mount("calm");
    expect(text()).toContain("All quiet on this node");
    for (const word of ["Resources", "CPU", "Memory", "Disk", "Network", "Kernel networking", "Everything else"]) expect(text()).toContain(word);
    expect(text()).toContain("Client-eye checks");
    expect(text()).toContain("udp/443");
    expect(text()).toContain("tcp/443");
    expect(text()).toContain("alice");
    expect(text()).toContain("Traffic today");
    expect(text()).toContain("Resource headroom");
    expect(text()).toContain("Recently closed");
    // who gets the node: under what waits for you, in the first column
    expect(text()).toContain("Who gets this node");
    // 24 cells an hour for each of the three profiles
    expect(host!.querySelectorAll(".nd-tk").length).toBe(3);
    expect(host!.querySelectorAll(".nd-tk")[0]!.children.length).toBe(24);
  });

  it("lists the alerts, a critical one open, and folds past four into 'show more'", async () => {
    await mount("alerts");
    const heads = [...host!.querySelectorAll("button[aria-controls^='alert-body-']")];
    expect(heads).toHaveLength(3);
    expect(heads.filter((h) => h.getAttribute("aria-expanded") === "true")).toHaveLength(1);
    act(() => root!.unmount());
    host!.remove();
    await mount("many");
    expect(host!.querySelectorAll("button[aria-controls^='alert-body-']")).toHaveLength(4);
    const more = buttons("Show 2 more")[0]!;
    act(() => more.click());
    expect(host!.querySelectorAll("button[aria-controls^='alert-body-']")).toHaveLength(6);
    expect(buttons("Collapse")).toHaveLength(1);
  });

  it("opens an alert on a click and closes the open one", async () => {
    await mount("alerts");
    const heads = [...host!.querySelectorAll<HTMLButtonElement>("button[aria-controls^='alert-body-']")];
    const shut = heads.find((h) => h.getAttribute("aria-expanded") === "false")!;
    act(() => shut.click());
    expect(shut.getAttribute("aria-expanded")).toBe("true");
    act(() => shut.click());
    expect(shut.getAttribute("aria-expanded")).toBe("false");
  });

  it("turns the readings amber and red as the load grows", async () => {
    await mount("limit");
    expect(text()).toContain("load is high");
    const cpu = [...host!.querySelectorAll("section")].find((s) => s.textContent?.startsWith("Resources"))!;
    expect(cpu.querySelector(".text-danger-text")).not.toBeNull(); // 93% CPU
    const lim = [...host!.querySelectorAll("section")].find((s) => s.textContent?.startsWith("Resource headroom"))!;
    expect(lim.querySelector("i.nd-bar")!.getAttribute("style")).toContain("--danger"); // connection tracking at 88%
  });

  it("dims what a silent node last said and leaves out the headroom", async () => {
    await mount("down");
    expect(text()).toContain("Node not responding");
    expect(text()).not.toContain("Resource headroom");
    expect(text()).toContain("The node has been offline since");
  });

  it("says a new node has no history yet, with what it has: no one on it", async () => {
    await mount("new");
    expect(text()).toContain("The chart appears in an hour");
    expect(text()).toContain("No readings yet");
    expect(text()).toContain("nobody yet");
  });

  it("leaves a retired node what it was: no resources, no load, only the three tabs", async () => {
    await mount("retired");
    expect(text()).not.toContain("Resource headroom");
    expect(text()).not.toContain("Kernel networking");
    // a retired node is given to nobody: the panel refuses it (node_retired), so the card is not there
    expect(text()).not.toContain("Who gets this node");
    expect(buttons("Restart profiles")).toHaveLength(0);
    expect([...host!.querySelectorAll("[role=tab]")].map((t) => t.textContent)).toEqual(["Overview", "History", "Server"]);
  });
});
