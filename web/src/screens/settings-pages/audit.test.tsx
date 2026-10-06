import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { AuditSource } from "@/gen/mistgate/admin/v1/auth_pb";
import { memoryRouter } from "@/test/router";
import { AuditPage } from "./audit";

const listAudit = vi.fn();
vi.mock("@/lib/api", () => ({ auth: { listAudit: (...a: unknown[]) => listAudit(...a) } }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  listAudit.mockReset();
});

const entry = (id: number) => ({ id, timeUnix: 1_800_000_000 - id, source: AuditSource.PANEL, actorId: "adm_1", actorName: "Alice", action: "login", paramsJson: "{}", result: "ok", ip: "203.0.113.7" });
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const click = (el: Element | null | undefined) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const button = (label: string) => document.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
const before = (r: { state: { location: { search: unknown } } }) => (r.state.location.search as { before?: number }).before;

async function mount(url = "/") {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const { router, element } = memoryRouter(<AuditPage />, url);
  await act(async () => root!.render(<QueryClientProvider client={qc}>{element}</QueryClientProvider>));
  for (let i = 0; i < 4; i++) await settle();
  return router;
}

describe("Settings → Audit, a page of the log at a time", () => {
  it("asks for a page, shows Older when the server has more, and goes on from its cursor", async () => {
    listAudit.mockImplementation(async (req: { beforeId: bigint }) =>
      Number(req.beforeId) === 0 ? { entries: [entry(100), entry(99)], nextBeforeId: 99 } : { entries: [entry(98)], nextBeforeId: 0 },
    );
    const router = await mount();
    expect(listAudit.mock.calls[0]![0]).toMatchObject({ beforeId: 0n, pageSize: 50 });
    expect(button("Newer entries")!.disabled).toBe(true);
    expect(button("Older entries")!.disabled).toBe(false);

    await click(button("Older entries"));
    await settle();
    await settle();
    expect(listAudit.mock.calls.at(-1)![0]).toMatchObject({ beforeId: 99n, pageSize: 50 });
    expect(before(router)).toBe(99);
    expect(button("Older entries")!.disabled).toBe(true);
    expect(button("Newer entries")!.disabled).toBe(false);

    await click(button("Newer entries"));
    await settle();
    await settle();
    expect(before(router)).toBeUndefined();
  });

  it("opens where a link points and asks for the chosen number of rows", async () => {
    listAudit.mockResolvedValue({ entries: [entry(40)], nextBeforeId: 0 });
    await mount("/?before=41&size=25");
    expect(listAudit.mock.calls[0]![0]).toMatchObject({ beforeId: 41n, pageSize: 25 });
    expect(button("Back to the newest entries")).not.toBeNull();
  });

  it("shows no pager while the whole log fits one page", async () => {
    listAudit.mockResolvedValue({ entries: [entry(3), entry(2)], nextBeforeId: 0 });
    await mount();
    expect(document.querySelector("nav[aria-label='Audit']")).toBeNull();
    expect(button("Older entries")).toBeNull();
  });
});
