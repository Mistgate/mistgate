import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { AddNodeProvider, useAddNode } from "@/components/add-node";
import { EditionNotice } from "@/components/edition-notice";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { BundleStatus, NodeUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { editionFeatures } from "@/i18n/edition";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { zh } from "@/i18n/zh";
import { setLang } from "@/i18n";
import { meQuery } from "@/lib/session";
import { ChecksTab } from "@/screens/health/checks";
import { IntegrationsScreen } from "@/screens/integrations";
import { LogsTab } from "@/screens/node/logs";
import { BackupsPage } from "@/screens/settings-pages/backups";
import { UpdatesScreen } from "@/screens/updates";

// The Cloudflare edition (MeResponse.edition "edge" with its list of unavailable features) says so where a feature is missing and
// hides the buttons that would start it; the VPS edition is untouched. One mocked panel for all the screens.

let edition = "edge";
const unavailable = () => (edition === "edge" ? [...editionFeatures] : []);
const getBackupSettings = vi.fn();
const listBackups = vi.fn();
const getTelegram = vi.fn();
const streamLogs = vi.fn();
const createEnrollment = vi.fn();
vi.mock("@/lib/api", () => ({
  basepath: "/",
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: Role.OWNER } }) },
  apiTokens: { listApiTokens: () => Promise.resolve({ tokens: [], nowUnix: 1000 }) },
  approvals: { listApprovals: () => Promise.resolve({ approvals: [], awaiting: 0, nowUnix: 1000 }) },
  telegram: { getTelegram: (...a: unknown[]) => getTelegram(...a) },
  backups: { getBackupSettings: (...a: unknown[]) => getBackupSettings(...a), listBackups: (...a: unknown[]) => listBackups(...a) },
  nodes: { createEnrollment: (...a: unknown[]) => createEnrollment(...a), getNode: vi.fn(), streamLogs: (...a: unknown[]) => streamLogs(...a) },
  health: { runChecksNow: () => Promise.resolve({ scheduled: 1, skipped: 0 }) },
  updates: { getUpdates: () => Promise.resolve(updatesPage) },
  fleet: {},
  users: {},
  provisioning: {},
  assetUrl: (p: string) => p,
  isUnauthenticated: () => false,
  webauthnSupported: () => false,
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className }: { children?: ReactNode; to: string; className?: string }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
  useSearch: () => ({}),
  useNavigate: () => () => {},
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  edition = "edge";
  setLang("en");
  getTelegram.mockResolvedValue({ status: { bot: { configured: false, username: "", error: "" }, links: [], adminUrlKnown: false } });
  getBackupSettings.mockResolvedValue({ settings: { accountId: "", jurisdiction: "default", bucket: "", accessKeyId: "", hasSecret: false, ageRecipient: "", enabled: false, intervalHours: 24, retentionDays: 0, lastSuccessUnix: 0, lastErrorCode: "" } });
  listBackups.mockResolvedValue({ backups: [] });
  streamLogs.mockImplementation(async function* () {});
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [getBackupSettings, listBackups, getTelegram, streamLogs, createEnrollment]) m.mockReset();
});

async function mount(ui: ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role: Role.OWNER }, edition, unavailable: unavailable() } as never); // the route guards have asked already
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>{ui}</ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => document.body.textContent ?? "";
const buttons = () => [...document.querySelectorAll("button")].map((b) => b.textContent?.trim() ?? "");
const notice = (feature: (typeof editionFeatures)[number]) => `Not available in the Cloudflare edition yet: ${en[`edition.feature.${feature}`]}.`;

describe("EditionNotice", () => {
  it("says the feature is missing in both languages, and says nothing on the VPS", async () => {
    await mount(<EditionNotice feature="backups">hint</EditionNotice>);
    expect(text()).toContain("Not available in the Cloudflare edition yet: Backups.");
    expect(text()).toContain("hint");
    act(() => root?.unmount());
    host?.remove();
    setLang("ru");
    await mount(<EditionNotice feature="backups" />);
    expect(text()).toContain(`Пока недоступно в Cloudflare-версии: ${ru["edition.feature.backups"]}.`);
    act(() => root?.unmount());
    host?.remove();
    edition = "vps";
    await mount(<EditionNotice feature="backups" />);
    expect(text()).toBe("");
  });

  it("names every feature the server can list", () => {
    for (const f of editionFeatures) {
      expect(en[`edition.feature.${f}`], f).toBeTruthy();
      expect(ru[`edition.feature.${f}`], f).not.toBe(en[`edition.feature.${f}`]);
      expect(zh[`edition.feature.${f}`], f).not.toBe(en[`edition.feature.${f}`]);
    }
  });
});

describe("Integrations on the edge", () => {
  it("replaces the Telegram and MCP cards with a notice and offers no new API token, but keeps the token list", async () => {
    await mount(<IntegrationsScreen />);
    expect(text()).toContain(notice("telegram"));
    expect(text()).toContain(notice("mcp"));
    expect(getTelegram).not.toHaveBeenCalled();
    expect(document.querySelector("[data-testid='mcp-url']")).toBeNull();
    expect(buttons().some((b) => b.includes("New token") || b.includes("Create token"))).toBe(false);
    expect(text()).toContain("API tokens");
  });

  it("is unchanged on the VPS", async () => {
    edition = "vps";
    await mount(<IntegrationsScreen />);
    expect(text()).not.toContain("Not available in the Cloudflare edition yet");
    expect(getTelegram).toHaveBeenCalled();
    expect(document.querySelector("[data-testid='mcp-url']")).not.toBeNull();
    expect(buttons().some((b) => b.includes("New token"))).toBe(true);
  });
});

describe("Backups on the edge", () => {
  it("shows the notice and does not ask for the settings", async () => {
    await mount(<BackupsPage />);
    expect(text()).toContain(notice("backups"));
    expect(getBackupSettings).not.toHaveBeenCalled();
    expect(buttons()).toEqual([]);
  });

  it("is unchanged on the VPS", async () => {
    edition = "vps";
    await mount(<BackupsPage />);
    expect(text()).not.toContain("Not available in the Cloudflare edition yet");
    expect(getBackupSettings).toHaveBeenCalled();
    expect(buttons().length).toBeGreaterThan(0);
  });
});

function Opener() {
  const addNode = useAddNode();
  return (
    <button type="button" onClick={() => addNode()}>
      open
    </button>
  );
}
async function openAddNode() {
  await mount(
    <AddNodeProvider>
      <Opener />
    </AddNodeProvider>,
  );
  await act(async () => void [...document.querySelectorAll("button")].find((b) => b.textContent === "open")?.click());
}

describe("Add node on the edge", () => {
  it("offers the SSH install next to the manual command, with no notice", async () => {
    await openAddNode();
    expect(text()).not.toContain("Not available in the Cloudflare edition yet");
    expect(buttons()).toContain("Install automatically over SSH");
    expect(buttons()).toContain("Get a manual install command");
  });
});

const updatesPage = {
  nowUnix: 1000,
  scheduleTimezoneOffsetMinutes: 0,
  panel: {
    version: "0.2.0-bbb", built: 200, hasReleaseKey: true, releaseKeyFingerprint: "abcd1234abcd1234",
    update: { version: "v0.3.0", url: "", publishedUnix: 2000, checkedUnix: 1000, available: true, supported: true, installable: true, installing: false, errorKey: "" },
  },
  bundle: { status: BundleStatus.TRUSTED, version: "0.2.0-bbb", built: 200, expiresUnix: 0, files: [], errorKey: "", params: {}, scannedUnix: 0 },
  nodes: [
    { nodeId: "nod_1", name: "de1", version: "0.1.0-aaa", built: 100, supportsUpdate: true, crashGuard: true, state: NodeUpdateState.OUTDATED, scheduledUnix: 0, scheduledVersion: "", scheduledBuilt: 0, scheduledTimezoneOffsetMinutes: 0, scheduledMissed: false, inbounds: 1, onlineUsers: 0, address: "de1.example.com", arch: "amd64" },
  ],
  distDir: "/data/dist",
};

describe("Updates on the edge", () => {
  it("lists the nodes and the release but offers no way to start an update", async () => {
    await mount(<UpdatesScreen />);
    expect(text()).toContain(notice("agent_updates"));
    expect(text()).toContain(notice("panel_update"));
    expect(text()).toContain("de1");
    expect(text()).not.toContain("Only the owner");
    const all = buttons().join("|");
    expect(all).not.toMatch(/Update \d+ nodes?/);
    expect(all).not.toContain("Update panel");
    expect(all).not.toContain("Read the folder again");
  });

  it("is unchanged on the VPS", async () => {
    edition = "vps";
    await mount(<UpdatesScreen />);
    expect(text()).not.toContain("Not available in the Cloudflare edition yet");
    expect(buttons()).toContain("Update panel");
    expect(buttons().some((b) => /^Update \d+ nodes?$/.test(b))).toBe(true);
  });
});

describe("Node logs on the edge", () => {
  const data = { node: { id: "nod_1", name: "de1", status: NodeStatus.ONLINE } } as never;
  it("shows the notice and never opens the stream", async () => {
    await mount(<LogsTab data={data} />);
    expect(text()).toContain(notice("agent_logs"));
    expect(streamLogs).not.toHaveBeenCalled();
    expect(buttons()).toEqual([]);
  });

  it("opens the stream on the VPS", async () => {
    edition = "vps";
    await mount(<LogsTab data={data} />);
    expect(text()).not.toContain("Not available in the Cloudflare edition yet");
    expect(streamLogs).toHaveBeenCalled();
  });
});

describe("Health checks on the edge", () => {
  const checks = { nowUnix: 200, intervalS: 300, columns: [{ profileId: "prf_1", profileName: "hy2 · 443", protocol: "hysteria2" }], rows: [{ nodeId: "nod_1", nodeName: "de1", countryCode: "DE", nodeStatus: NodeStatus.ONLINE, cells: [{ deployed: false, inboundId: "", failStreak: 0, history: [] }] }] };
  it("says no checker runs and offers no Check now", async () => {
    await mount(<ChecksTab data={checks as never} />);
    expect(text()).toContain(notice("health_checks"));
    expect(buttons()).not.toContain("Check now");
  });

  it("offers Check now on the VPS", async () => {
    edition = "vps";
    await mount(<ChecksTab data={checks as never} />);
    expect(text()).not.toContain("Not available in the Cloudflare edition yet");
    expect(buttons()).toContain("Check now");
  });
});
