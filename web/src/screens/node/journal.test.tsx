import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { EventSeverity } from "@/gen/mistgate/admin/v1/fleet_pb";
import { JournalTab } from "./journal";

const getNodeJournal = vi.fn();
vi.mock("@/lib/api", () => ({ nodes: { getNodeJournal: (...a: unknown[]) => getNodeJournal(...a) } }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  getNodeJournal.mockReset();
  vi.useRealTimers();
});

const now = Math.floor(Date.now() / 1000);
const entry = (over: Record<string, unknown> = {}) => ({
  nodeId: "nod_1",
  nodeName: "de1",
  hourStartUnix: BigInt(Math.floor(now / 3600) * 3600),
  level: EventSeverity.WARNING,
  source: "awg",
  message: "amneziawg-go: %v - Failed to send handshake response: %v",
  inboundId: "inb_1",
  profileName: "Home AWG",
  attrs: { code: "send_failed", err: "sendmsg <ip>:port: network unreachable" },
  count: 12,
  firstUnix: BigInt(now - 600),
  lastUnix: BigInt(now - 60),
  ...over,
});
const answer = (over: Record<string, unknown> = {}) => ({
  sinceUnix: BigInt(now - 86400),
  nowUnix: BigInt(now),
  entries: [entry(), entry({ level: EventSeverity.ERROR, source: "agent", message: "inbound failed to start", inboundId: "", profileName: "", attrs: {}, count: 1 })],
  truncated: false,
  nodesWithoutJournal: [],
  ...over,
});

const flush = async () => {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(<QueryClientProvider client={qc}><JournalTab nodeId="nod_1" /></QueryClientProvider>));
  await flush();
}
const text = () => document.body.textContent ?? "";
const radio = (label: string) => [...document.querySelectorAll("[role=radio]")].find((r) => r.textContent === label);
const click = (el: Element | null | undefined) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const lastCall = () => getNodeJournal.mock.calls.at(-1)?.[0];

describe("the node's journal tab", () => {
  it("shows each kind of record with its level, source, profile, count and attributes, newest first", async () => {
    getNodeJournal.mockResolvedValue(answer());
    await mount();
    expect(lastCall()).toMatchObject({ nodeId: "nod_1", hours: 24, minLevel: EventSeverity.WARNING, query: "", limit: 200 });
    expect(text()).toContain("amneziawg-go: %v - Failed to send handshake response: %v");
    expect(text()).toContain("Home AWG");
    expect(text()).toContain("×12");
    expect(text()).toContain("code=send_failed");
    expect(text()).toContain("err=sendmsg <ip>:port: network unreachable");
    expect(text()).toContain("ERROR");
    expect(text()).toContain("inbound failed to start");
    expect(text().split("×")).toHaveLength(2); // a record that came once has no count
  });

  it("asks the panel again for a longer period, errors only and a search", async () => {
    getNodeJournal.mockResolvedValue(answer());
    await mount();
    await click(radio("7 days"));
    await flush();
    expect(lastCall()).toMatchObject({ hours: 168, minLevel: EventSeverity.WARNING });
    await click(radio("Errors only"));
    await flush();
    expect(lastCall()).toMatchObject({ hours: 168, minLevel: EventSeverity.ERROR });

    const input = document.querySelector('input[aria-label="Search the journal"]') as HTMLInputElement;
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      set.call(input, "handshake");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => void (await new Promise((r) => setTimeout(r, 350))));
    await flush();
    expect(lastCall()).toMatchObject({ query: "handshake", minLevel: EventSeverity.ERROR });
  });

  it("says when the agent sends no journal, when nothing came and when the list is cut", async () => {
    getNodeJournal.mockResolvedValue(answer({ entries: [], nodesWithoutJournal: ["nod_1"] }));
    await mount();
    expect(text()).toContain("This node's agent does not send its journal yet: update the agent.");
    expect(text()).toContain("No warnings or errors in this period.");
    act(() => root?.unmount());
    host?.remove();

    getNodeJournal.mockResolvedValue(answer({ truncated: true }));
    await mount();
    expect(text()).toContain("Only the newest 200 are shown");
    expect(text()).not.toContain("does not send its journal");
  });
});
