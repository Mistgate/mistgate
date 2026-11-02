import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { hy2 } from "./schema.fixtures";
import { ProfileEditorScreen } from "./editor";
import { validateNewProfileSearch } from "./search";

const mtSchema = JSON.stringify({ type: "object", properties: { port: { type: "integer", title: "TCP port", "x-group": "basics", "x-order": 10 } } });
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  profiles: {
    // the server's order; the choice shows Hysteria2, VLESS, AmneziaWG, MTProto
    listProtocols: () =>
      Promise.resolve({
        protocols: [
          { id: "mtproto", displayName: "MTProto", settingsSchemaJson: mtSchema, defaultSettingsJson: JSON.stringify({ port: 443 }), apps: [App.TELEGRAM] },
          { id: "hysteria2", displayName: "Hysteria2", settingsSchemaJson: hy2, defaultSettingsJson: JSON.stringify({ port: 443, obfs: { type: "salamander", password: "••••" } }), apps: [App.HAPP] },
        ],
      }),
    listProfiles: () => Promise.resolve({ profiles: [] }),
    previewProfile: () => Promise.resolve({ errors: [], warnings: [], clientPreview: "tg://proxy?…", clientLabel: "", formats: [] }),
  },
  groups: { listGroups: () => Promise.resolve({ groups: [] }) },
  nodes: { listNodes: () => Promise.resolve({ nodes: [] }) },
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { updateIntervalHours: 12, apps: [] }, effectiveTitle: "" }) },
  dns: { listDnsPresets: () => Promise.resolve({ presets: [], providers: [], clientSupport: [] }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: Role.OWNER } }) },
}));

// the router: navigate moves the URL of /profiles/new (and re-renders); anything else is only recorded
let search: Record<string, unknown> = {};
let rerender = () => {};
const away = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useBlocker: () => ({ status: "idle" }),
  useParams: () => ({}),
  useSearch: () => search,
  useNavigate: () => (o: { to?: string; search?: Record<string, unknown> }) => {
    if (o.to === "/profiles/new") {
      search = validateNewProfileSearch(o.search ?? {});
      rerender();
    } else away(o);
    return Promise.resolve();
  },
  Link: ({ children, to }: { children?: ReactNode; to: string }) => <a href={to}>{children}</a>,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  search = {};
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  away.mockReset();
});

const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const draw = () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <ProfileEditorScreen />
        </ToastProvider>
      </QueryClientProvider>,
    );
  rerender = () => act(draw);
  await act(async () => draw());
  await settle();
}
const cards = () => [...document.querySelectorAll<HTMLElement>("[role=radiogroup] [role=radio]")];
const card = (name: string) => cards().find((c) => c.textContent!.startsWith(name))!;
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label);
const click = (el: Element | undefined) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const nameBox = () => [...document.querySelectorAll<HTMLInputElement>("input")].find((i) => i.placeholder === "E.g. Main · 443")!;
const type = (box: HTMLInputElement, value: string) =>
  act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(box, value);
    box.dispatchEvent(new Event("input", { bubbles: true }));
  });

describe("a new profile, step 1", () => {
  it("offers the protocols as cards of one radio group, in the guide's order, with what each is for", async () => {
    await mount();
    expect(document.querySelector("[role=radiogroup]")?.getAttribute("aria-label")).toBe("Protocol");
    expect(cards().map((c) => c.textContent!.split(/(?=Start here|Experimental|A fast|A proxy)/)[0])).toEqual(["Hysteria2", "MTProto"]);
    expect(card("Hysteria2").getAttribute("aria-checked")).toBe("true");
    // one tagline, a few traits, the apps by name for a screen reader (the eye sees their monograms)
    expect(card("Hysteria2").textContent).toContain("Start here");
    expect(card("Hysteria2").textContent).toContain("A fast tunnel for lossy networks");
    expect(card("Hysteria2").textContent).toContain("UDP · QUIC");
    expect(card("Hysteria2").textContent).toContain("Happ, kl!ck, Clash Verge Rev, sing-box");
    expect(card("MTProto").textContent).toContain("Experimental");
    expect(card("MTProto").textContent).toContain("A proxy for Telegram only");
    expect(card("MTProto").textContent).toContain("tg:// link");
  });

  it("goes on with the chosen card, by the button or by Enter, and the URL keeps the step", async () => {
    search = { node: "nod_1" };
    await mount();
    await click(card("MTProto"));
    expect(card("MTProto").getAttribute("aria-checked")).toBe("true");
    await act(async () => void card("MTProto").dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect(search).toEqual({ node: "nod_1", protocol: "mtproto" });
    // step 2: the chosen protocol on top of its form, with the way back
    expect(cards()).toHaveLength(0);
    expect(document.body.textContent).toContain("Step 2 of 2 · Settings");
    expect(document.body.textContent).toContain("Telegram without a VPN: people connect the proxy from a tg:// link.");
    expect(document.body.textContent).toContain("TCP port");
    expect(button("Change")).toBeTruthy();
  });

  it("opens on the choice when the URL names a protocol there is none of", async () => {
    search = { protocol: "nope" };
    await mount();
    expect(cards()).toHaveLength(2);
  });
});

describe("a new profile, back and forth", () => {
  it("keeps what was typed when the protocol is changed and chosen again, and says so on its card", async () => {
    search = { protocol: "mtproto" };
    await mount();
    await type(nameBox(), "Telegram · de1");
    await click(button("Change"));
    expect(search).toEqual({});
    expect(card("MTProto").getAttribute("aria-checked")).toBe("true");
    expect(card("MTProto").textContent).toContain("Draft kept");
    expect(card("Hysteria2").textContent).not.toContain("Draft kept");
    await click(button("Continue"));
    expect(search).toEqual({ protocol: "mtproto" });
    expect(nameBox().value).toBe("Telegram · de1");
  });

  it("asks before leaving with something typed, and leaves at once with nothing", async () => {
    search = { protocol: "hysteria2" };
    await mount();
    await click(button("Back to profiles"));
    expect(away).toHaveBeenCalledWith(expect.objectContaining({ to: "/profiles" }));
    away.mockReset();

    search = { protocol: "mtproto" };
    rerender();
    await settle();
    await type(nameBox(), "x");
    await click(button("Back to profiles"));
    expect(away).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("Leave without creating the profile?");
    await click(button("Leave"));
    await settle();
    expect(away).toHaveBeenCalledWith(expect.objectContaining({ to: "/profiles" }));
  });
});

describe("the new profile's URL", () => {
  it("keeps the node and a protocol id, and drops what is not one", () => {
    expect(validateNewProfileSearch({ node: "nod_1", protocol: "vless" })).toEqual({ node: "nod_1", protocol: "vless" });
    expect(validateNewProfileSearch({ protocol: "Robert'); DROP" })).toEqual({});
  });
});
