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
  // the wide layout of the rows (the phone stacks them)
  window.matchMedia = ((media: string) => ({ matches: false, media, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
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
async function mount(count: number, url = "/") {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const { router, element } = memoryRouter(<Tab history={Array.from({ length: count }, (_, i) => closed(i + 1))} />, url);
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
const byLabel = (label: string) => document.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
const nav = () => document.querySelector("nav[aria-label]");

describe("Health → Alerts → History, paged on the screen", () => {
  it("cuts a long history into pages of twenty, newest first, with the count under the rows", async () => {
    await mount(137);
    expect(text()).toContain("node1");
    expect(text()).toContain("node20");
    expect(text()).not.toContain("node21");
    expect(text()).toContain("Showing 1–20 of 137");
    expect(nav()!.getAttribute("aria-label")).not.toBeNull();
  });

  it("leaves a short history alone: no pager", async () => {
    await mount(20);
    expect(text()).toContain("node20");
    expect(nav()).toBeNull();
  });

  it("opens on the page of the URL and keeps the position in it when the owner moves", async () => {
    const router = await mount(137, "/?page=3");
    expect(text()).toContain("Showing 41–60 of 137");
    expect(text()).toContain("node41");
    expect(text()).not.toContain("node40");
    await click(byLabel("Next page"));
    await settle();
    expect((router.state.location.search as { page?: number }).page).toBe(4);
    expect(text()).toContain("Showing 61–80 of 137");
    await click(byLabel("Page 1"));
    await settle();
    expect((router.state.location.search as { page?: number }).page).toBeUndefined();
    expect(text()).toContain("Showing 1–20 of 137");
  });

  it("steps back to the last page when the URL points past the end", async () => {
    await mount(45, "/?page=40");
    expect(text()).toContain("Showing 41–45 of 45");
    expect(byLabel("Next page")!.disabled).toBe(true);
  });

  it("takes the size from the URL", async () => {
    await mount(137, "/?size=50");
    expect(text()).toContain("Showing 1–50 of 137");
    expect(text()).toContain("node50");
    expect(text()).not.toContain("node51");
  });
});
