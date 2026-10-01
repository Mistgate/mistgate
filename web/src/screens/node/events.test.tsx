import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { EventSeverity } from "@/gen/mistgate/admin/v1/fleet_pb";
import { EventsTab } from "./events";

const listEvents = vi.fn();
vi.mock("@/lib/api", () => ({ fleet: { listEvents: (...a: unknown[]) => listEvents(...a) } }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  listEvents.mockReset();
});

const ev = (id: number, at: number, code: string, params: Record<string, string> = {}, over: Record<string, unknown> = {}) => ({
  id: BigInt(id),
  timeUnix: BigInt(at),
  severity: EventSeverity.INFO,
  code,
  params,
  nodeId: "nod_1",
  nodeName: "de1",
  userId: "",
  userName: "",
  inboundId: "",
  profileName: "",
  protocol: "",
  source: "agent",
  ...over,
});

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(<QueryClientProvider client={qc}><EventsTab nodeId="nod_1" /></QueryClientProvider>));
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => document.body.textContent ?? "";
const click = (el: Element | null | undefined) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));

describe("the node's events tab", () => {
  const now = Math.floor(Date.now() / 1000);
  const rows = [
    ev(4, now, "state_applied", { revision: "9", inbounds: "2", added: "0", removed: "0", changed: "0", users: "0" }),
    ev(3, now - 1, "engine_started", { protocol: "awg", reason: "agent_start" }, { inboundId: "inb_2", profileName: "Amnezia 3.1 test", protocol: "awg" }),
    ev(2, now - 2, "engine_started", { protocol: "hysteria2", reason: "agent_start" }, { inboundId: "inb_1", profileName: "test", protocol: "hysteria2" }),
    ev(1, now - 3, "agent_started", { version: "0.1.0-b", prev_version: "0.1.0-a", reason: "update" }),
  ];

  it("shows one line for an agent update with the profiles that came up, under a day heading, and the rows behind it on request", async () => {
    listEvents.mockResolvedValue({ events: rows, hasMore: false });
    await mount();
    expect(text()).toContain("Today");
    expect(text()).toContain("Agent updated 0.1.0-a → 0.1.0-b");
    expect(text()).toContain("profiles up: test, Amnezia 3.1 test");
    expect(document.querySelectorAll("section")).toHaveLength(1);
    expect(text()).not.toContain("engine_started");

    await click([...document.querySelectorAll("button")].find((b) => b.textContent === "Details"));
    expect(text()).toContain("engine_started");
    expect(text()).toContain("Amnezia 3.1 test");
    expect(text()).toContain("reason=agent_start");
    await click([...document.querySelectorAll("button")].find((b) => b.textContent === "Hide"));
    expect(text()).not.toContain("reason=agent_start");
  });

  it("asks the server for the problems only when that filter is picked", async () => {
    listEvents.mockResolvedValue({ events: rows, hasMore: false });
    await mount();
    expect(listEvents.mock.calls[0]![0]).toMatchObject({ nodeId: "nod_1", minSeverity: undefined, family: "" });
    listEvents.mockResolvedValue({ events: [], hasMore: false });
    await click([...document.querySelectorAll("[role=radio]")].find((r) => r.textContent === "Problems"));
    await settle();
    await settle();
    expect(listEvents.mock.calls.at(-1)![0]).toMatchObject({ nodeId: "nod_1", minSeverity: EventSeverity.WARNING });
    expect(text()).toContain("No events of this kind so far.");
  });

  it("leaves the Profiles and Agent filters to the server, so a page is a page of that kind", async () => {
    listEvents.mockResolvedValue({ events: rows, hasMore: false });
    await mount();
    await click([...document.querySelectorAll("[role=radio]")].find((r) => r.textContent === "Profiles"));
    await settle();
    await settle();
    expect(listEvents.mock.calls.at(-1)![0]).toMatchObject({ nodeId: "nod_1", family: "profiles", minSeverity: undefined });
    await click([...document.querySelectorAll("[role=radio]")].find((r) => r.textContent === "Agent"));
    await settle();
    await settle();
    expect(listEvents.mock.calls.at(-1)![0]).toMatchObject({ family: "agent" });
  });

  it("never says 'none' while older events are left: it offers to search further", async () => {
    // a page of the agent family as an older panel would send it, with only profile rows in it
    listEvents.mockResolvedValue({ events: [rows[1], rows[2]], hasMore: true });
    await mount();
    await click([...document.querySelectorAll("[role=radio]")].find((r) => r.textContent === "Agent"));
    await settle();
    await settle();
    expect(text()).toContain("None among the latest 2 events");
    expect(text()).not.toContain("No events of this kind so far.");
    listEvents.mockResolvedValue({ events: [rows[3]], hasMore: false });
    await click([...document.querySelectorAll("button")].find((b) => b.textContent === "Search further"));
    await settle();
    await settle();
    expect(text()).toContain("Agent updated 0.1.0-a → 0.1.0-b");
  });
});
