import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { en } from "@/i18n/en";
import type { RelayMsg } from "@/lib/relay";
import { ChangeRelayPort } from "./relays";

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const relay = (protocol: string) => ({ id: "rly_1", profileName: "Profile", protocol, network: "udp", port: 51820, exitPort: 51820, viaNodeName: "msk1", exitNodeName: "de1" }) as unknown as RelayMsg;

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(ui: ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>{ui}</ToastProvider>
      </QueryClientProvider>,
    ),
  );
}
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]")!;

describe("Change port", () => {
  it("of an AmneziaWG relay warns that AmneziaVPN configs through the entry need downloading again; of another protocol it does not", async () => {
    await mount(<ChangeRelayPort r={relay("awg")} onClose={() => {}} />);
    expect(dialog().textContent).toContain(en["relay.dlg.portAwg"]);
    await act(async () => root!.unmount());
    root = null;
    host?.remove();
    await mount(<ChangeRelayPort r={relay("vless")} onClose={() => {}} />);
    expect(dialog().textContent).not.toContain(en["relay.dlg.portAwg"]);
  });
});
