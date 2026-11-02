import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { AlertKind, AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import type { Alert } from "@/lib/health";
import { memoryRouter } from "@/test/router";
import { AlertsTab } from "./alerts";
import { useFixFlow } from "./fix";

vi.mock("@/lib/api", () => ({
  health: {},
  nodes: {},
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: 1 } }) },
  fleet: {},
  users: {},
  assetUrl: (p: string) => p,
  isUnauthenticated: () => false,
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
  history.replaceState(null, "", location.pathname);
});

const NOW = 2_000_000_000;
// the nth resolved alert: n = 1 is the newest (the panel sends newest first)
const closed = (n: number): Alert => ({
  id: `alt_${n}`,
  severity: AlertSeverity.WARNING,
  kind: AlertKind.CHECK_FAILED,
  nodeId: "nod_1",
  nodeName: `node${n}`,
  subject: "inb_1",
  titleKey: "health.alert.check_failed.title",
  params: {},
  whyKey: "",
  firstSeenUnix: NOW - n * 600 - 300,
  openedUnix: NOW - n * 600 - 300,
  lastSeenUnix: NOW - n * 600,
  resolvedAtUnix: NOW - n * 600,
  resolution: "recovered",
  mutedUntilUnix: 0,
  actions: [],
});

function Tab({ history }: { history: Alert[] }) {
  const flow = useFixFlow();
  return <AlertsTab active={[]} history={history} now={NOW} flow={flow} />;
}

async function settle() {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
async function mount(count: number, url = "/", rows: Alert[] = Array.from({ length: count }, (_, i) => closed(i + 1))) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const { router, element } = memoryRouter(<Tab history={rows} />, url);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>{element}</ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
  return router;
}
const text = () => document.body.textContent ?? "";
const click = (el: Element | null | undefined) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const more = () => [...document.querySelectorAll("button")].find((b) => b.textContent?.startsWith("Show "));

describe("Health → Alerts → History, twenty events at a time", () => {
  it("shows the latest twenty events, newest first, then twenty more at a time", async () => {
    await mount(45);
    expect(text()).toContain("node1“");
    expect(text()).toContain("node20“");
    expect(text()).not.toContain("node21“");
    expect(more()?.textContent).toBe("Show 20 more");
    await click(more());
    expect(text()).toContain("node40“");
    expect(more()?.textContent).toBe("Show 5 more");
    await click(more());
    expect(text()).toContain("node45“");
    expect(more()).toBeUndefined();
  });

  it("leaves a short history alone: nothing more to show", async () => {
    await mount(20);
    expect(text()).toContain("node20“");
    expect(more()).toBeUndefined();
  });

  it("shows enough of a long history to reach the row a link points at", async () => {
    history.replaceState(null, "", `#alert-alt_57-${closed(57).resolvedAtUnix}`);
    await mount(137);
    expect(document.getElementById(`alert-alt_57-${closed(57).resolvedAtUnix}`)).toBeTruthy();
    expect(text()).not.toContain("node58“");
    expect(more()?.textContent).toBe("Show 20 more");
  });

  it("a reopened alert lasted since it opened again: the pause before it is not counted", async () => {
    const reopened: Alert = { ...closed(1), firstSeenUnix: NOW - 3600, openedUnix: NOW - 600, resolvedAtUnix: NOW - 480 };
    await mount(0, "/", [reopened]);
    expect(text()).toContain("lasted 2 min");
    expect(text()).not.toContain("lasted 52 min");
  });
});
