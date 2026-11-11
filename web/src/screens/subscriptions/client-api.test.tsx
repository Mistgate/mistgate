import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang } from "@/i18n";
import { meQuery } from "@/lib/session";
import { ClientApiCard } from "./client-api";

const getClientApi = vi.fn();
const updateClientApi = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  clientApi: { getClientApi: (...a: unknown[]) => getClientApi(...a), updateClientApi: (...a: unknown[]) => updateClientApi(...a) },
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  setLang("en");
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [getClientApi, updateClientApi]) m.mockReset();
});

/** The card as `role` sees it, with the panel saying `enabled`. */
async function mount(enabled: boolean, role = Role.OWNER) {
  getClientApi.mockResolvedValue({ settings: { enabled } });
  updateClientApi.mockImplementation(async (r: { enabled: boolean }) => ({ settings: { enabled: r.enabled } }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role } } as never);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <ClientApiCard />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const text = () => document.body.textContent ?? "";
const toggle = () => document.querySelector<HTMLElement>("[role=switch]")!;
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));

describe("client API for the Mistgate app", () => {
  it("is off by default and says what turning it on gives", async () => {
    await mount(false);
    expect(toggle().getAttribute("aria-checked")).toBe("false");
    expect(text()).toContain("Off: the Mistgate app cannot find the panel");
    expect(text()).toContain("devices that are already connected keep working");
    expect(text()).not.toContain("Only the owner");
  });

  it("is turned on by the owner: one update with enabled, a toast, the card reads on", async () => {
    await mount(false);
    await click(toggle());
    await settle();
    expect(updateClientApi).toHaveBeenCalledTimes(1);
    expect(updateClientApi.mock.calls[0]![0]).toEqual({ enabled: true });
    expect(text()).toContain("Client API turned on");
  });

  it("is turned off by the owner, and the card says closing it leaves the connected devices working", async () => {
    await mount(true);
    expect(toggle().getAttribute("aria-checked")).toBe("true");
    expect(text()).toContain("Turning it off closes discovery and new connections only");
    getClientApi.mockResolvedValue({ settings: { enabled: false } });
    await click(toggle());
    await settle();
    expect(updateClientApi.mock.calls[0]![0]).toEqual({ enabled: false });
    expect(text()).toContain("Client API turned off");
    expect(toggle().getAttribute("aria-checked")).toBe("false");
  });

  it("is read only for a helper: the state shows, the switch is locked, nothing is sent", async () => {
    await mount(true, Role.HELPER);
    expect(toggle().getAttribute("aria-checked")).toBe("true");
    expect(toggle().hasAttribute("data-disabled")).toBe(true);
    expect(text()).toContain("Only the owner can change this.");
    await click(toggle());
    await settle();
    expect(updateClientApi).not.toHaveBeenCalled();
  });

  it("speaks Russian", async () => {
    setLang("ru");
    await mount(false);
    expect(text()).toContain("API клиента для приложения Mistgate");
    expect(text()).toContain("Выключено: приложение Mistgate не находит панель");
  });
});
