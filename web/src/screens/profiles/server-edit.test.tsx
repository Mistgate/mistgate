import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { en } from "@/i18n/en";
import { ServerEdit } from "./server-edit";

const updateInbound = vi.fn();
vi.mock("@/lib/api", () => ({
  profiles: { updateInbound: (...a: unknown[]) => updateInbound(...a) },
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
  updateInbound.mockReset();
});

const vless = { id: "inb_v", profileId: "prf_v", protocol: "vless", port: 443, tlsServerName: "", tlsServerNameOverride: "", lastError: "", realityTarget: "www.example.org:443", realityTargetOverride: "" };

async function mount(inbound: object) {
  updateInbound.mockResolvedValue({ inbound: {}, warnings: [], freePort: 0 });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const row = { inbound, nodeId: "nod_1", nodeName: "de1", countryCode: "DE", port: 443, kind: "ok", gl: "ok" } as never;
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <ServerEdit row={row} nodeAddress="de1.example.com" onClose={() => {}} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const labels = () => [...document.querySelectorAll("label")].map((l) => l.textContent ?? "");
const domain = () => labels().find((l) => l.startsWith("Domain (SNI)"));

describe("the edit row of a server on the Profiles screen", () => {
  it("has no domain field where the node shows a site of its own: that name is the stranger's, and editing it only restarts the server", async () => {
    await mount({ ...vless, realityTarget: "shop.example.com:443", realityTargetOverride: "shop.example.com:443", tlsServerName: "shop.example.com" });
    expect(labels().some((l) => l.includes(en["ps.ed.port"].replace("{node}", "de1")))).toBe(true);
    expect(domain()).toBeUndefined();
  });

  it("keeps the domain field for a server that has a certificate name of its own", async () => {
    await mount({ ...vless, realityTarget: "self", tlsServerName: "de1.example.com" });
    expect(domain()).toBeDefined();
  });
});
