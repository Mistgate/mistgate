import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { EventSeverity } from "@/gen/mistgate/admin/v1/fleet_pb";
import { EventsCard } from "./overview-events";

// The fleet feed: a person's line opens the person, and the logs of their app open the person at "Logs for support".

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, search, hash, className }: { children?: ReactNode; to: string; params?: object; search?: object; hash?: string; className?: string }) => (
    <a href={to} data-params={JSON.stringify(params ?? {})} data-search={JSON.stringify(search ?? {})} data-hash={hash} className={className}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

const ev = (id: number, code: string, over: Record<string, unknown> = {}) => ({
  id, timeUnix: 1_800_000_000 + id, severity: EventSeverity.INFO, code, source: "panel", nodeId: "", nodeName: "", userId: "", userName: "", deviceId: "", profileName: "", protocol: "", params: {}, ...over,
});

function mount(events: unknown[]) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<EventsCard events={events as never} />));
}
const rows = () => [...document.querySelectorAll("a")];

describe("the lines of the feed that lead to a person", () => {
  it("send the logs of the app, sent on request, on their own or refused, to the logs of that person", () => {
    mount([
      ev(1, "client_logs_received", { userId: "usr_alice", userName: "alice-liddell", params: { lines: "40", unasked: "false" } }),
      ev(2, "client_logs_received", { userId: "usr_bob", userName: "bob-the-builder", params: { lines: "22", unasked: "true" } }),
      ev(3, "client_logs_declined", { userId: "usr_alice", userName: "alice-liddell" }),
      ev(4, "user_created", { userId: "usr_carol", userName: "carol" }),
      ev(5, "node_down", { nodeId: "nod_de1", nodeName: "de1", severity: EventSeverity.ERROR }),
    ]);
    const [got, own, declined, created, down] = rows();
    expect(got!.textContent).toContain("the Mistgate app sent its logs (40 lines)");
    expect(own!.textContent).toContain("a person sent the logs of the Mistgate app on their own (22 lines)");
    expect(declined!.textContent).toContain("a person declined to send the logs of the Mistgate app");
    for (const [a, id] of [[got, "usr_alice"], [own, "usr_bob"], [declined, "usr_alice"]] as const) {
      expect(a!.getAttribute("href")).toBe("/users/$id");
      expect(a!.getAttribute("data-params")).toBe(JSON.stringify({ id }));
      expect(a!.getAttribute("data-hash")).toBe("client-logs");
    }
    // other lines of a person stay the top of the page, and a node's line is a node's
    expect(created!.getAttribute("href")).toBe("/users/$id");
    expect(created!.hasAttribute("data-hash")).toBe(false);
    expect(down!.getAttribute("href")).toBe("/nodes/$id");
  });
});
