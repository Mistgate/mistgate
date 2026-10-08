import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { AlertKind, AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import type { Alert } from "@/lib/health";
import { AlertsTab } from "./alerts";
import { useFixFlow } from "./fix";
import { PeopleTab } from "./people";

vi.mock("@/lib/api", () => ({
  health: { muteAlert: vi.fn(), acceptDoctorItem: vi.fn(), applyFix: vi.fn() },
  nodes: {},
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: Role.OWNER } }) },
  fleet: {},
  users: {},
  assetUrl: (p: string) => p,
  isUnauthenticated: () => false,
}));
// no router in the test: a link is an anchor that says where it goes
vi.mock("@tanstack/react-router", () => ({
  useSearch: () => ({}), // the paging of the history list
  useNavigate: () => vi.fn(),
  Link: ({ children, to, params, className }: { children?: ReactNode; to: string; params?: { id: string }; className?: string }) => (
    <a href={to.replace("$id", params?.id ?? "")} className={className}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  // the history list of the Alerts tab asks for the phone layout
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
const node = (over: Partial<Alert> = {}): Alert => ({
  id: "alt_node",
  severity: AlertSeverity.CRITICAL,
  kind: AlertKind.NODE_DOWN,
  nodeId: "nod_1",
  nodeName: "de1",
  subject: "",
  titleKey: "health.alert.node_down.title",
  params: { minutes: "5" },
  whyKey: "health.alert.node_down.why",
  firstSeenUnix: NOW - 600,
  openedUnix: NOW - 600,
  lastSeenUnix: NOW,
  resolvedAtUnix: 0,
  resolution: "",
  mutedUntilUnix: 0,
  actions: ["open_node", "mute"],
  ...over,
});
const ended = (over: Partial<Alert> = {}): Alert =>
  node({
    id: "alt_ended",
    severity: AlertSeverity.WARNING,
    kind: AlertKind.ACCESS_ENDED,
    nodeId: "",
    nodeName: "",
    subject: "usr_masha",
    titleKey: "health.alert.access_ended.title",
    whyKey: "health.alert.access_ended.why.expired",
    params: { user_name: "Masha", user_id: "usr_masha", since: String(NOW - 86_400) },
    firstSeenUnix: NOW - 7200,
    openedUnix: NOW - 3600,
    actions: ["open_user", "mute"],
    ...over,
  });
const connection = (over: Partial<Alert> = {}): Alert =>
  ended({
    id: "alt_conn",
    severity: AlertSeverity.INFO,
    kind: AlertKind.USER_CONNECTION,
    subject: "dev_1",
    titleKey: "health.alert.user_connection.title",
    whyKey: "health.alert.user_connection.why.never_connected",
    params: { user_name: "Oleg", user_id: "usr_oleg" },
    ...over,
  });
const torrent = (over: Partial<Alert> = {}): Alert =>
  ended({
    id: "alt_torrent",
    kind: AlertKind.TORRENT,
    subject: "usr_alice",
    titleKey: "health.alert.torrent.title",
    whyKey: "health.alert.torrent.why",
    params: { user_id: "usr_alice", user_name: "alice", nodes: "EE, DE", count: "7", last_unix: String(NOW - 60), evidence: "tracker_connect", ports: "6969" },
    ...over,
  });
const impacted = node({
  id: "alt_impacted",
  severity: AlertSeverity.WARNING,
  kind: AlertKind.USERS_IMPACTED,
  subject: "hysteria2",
  titleKey: "health.alert.users_impacted.title",
  whyKey: "health.alert.users_impacted.why.gone",
  params: { now: "0", usual: "6", users: "6" },
});

function Screen({ people, alerts }: { people?: Alert[]; alerts?: Alert[] }) {
  const flow = useFixFlow();
  return (
    <>
      {people && <PeopleTab active={people} now={NOW} flow={flow} />}
      {alerts && <AlertsTab active={alerts} history={[]} now={NOW} flow={flow} />}
    </>
  );
}
async function mount(props: { people?: Alert[]; alerts?: Alert[] }) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <Screen {...props} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => host?.textContent ?? "";
const cards = () => [...document.querySelectorAll("article")];

describe("Health ▸ People", () => {
  it("lists the alerts about people, warnings first, and none about nodes", async () => {
    await mount({ people: [connection(), node(), impacted, ended()] });
    const list = cards();
    expect(list).toHaveLength(2);
    expect(list[0]!.textContent).toContain("Masha");
    expect(list[0]!.textContent).toContain("Access ended, but the person keeps trying");
    expect(list[0]!.textContent).toContain("Masha’s subscription ended on"); // the explanation, with the name
    expect(list[0]!.textContent).toContain("Warning");
    expect(list[0]!.textContent).toContain("for 1 h"); // from the start of the episode, not first_seen
    expect(list[1]!.textContent).toContain("Oleg");
    expect(list[1]!.textContent).toContain("Someone may not be able to connect");
    expect(list[1]!.textContent).toContain("Info");
    expect(text()).not.toContain("de1");
    expect(text()).not.toContain("Connections dropped on the node");
  });

  it("shows a torrent attempt: who, where, how many, what was caught and the port, and no node alert around it", async () => {
    await mount({ people: [connection(), torrent(), node()] });
    const list = cards();
    expect(list).toHaveLength(2);
    expect(list[0]!.textContent).toContain("alice");
    expect(list[0]!.textContent).toContain("Torrent attempts");
    expect(list[0]!.textContent).toContain("alice is trying to use torrents on EE, DE: 7 attempts in a day, blocked.");
    expect(list[0]!.textContent).toContain("Evidence: tracker connect request (the protocol’s magic number), port 6969.");
    expect(list[0]!.textContent).toContain("Warning");
    expect([...document.querySelectorAll("a")].some((a) => a.getAttribute("href") === "/users/usr_alice")).toBe(true);
    expect(list[1]!.textContent).toContain("Oleg");
  });

  it("keeps a torrent alert out of the node alerts", async () => {
    await mount({ alerts: [torrent(), node()] });
    expect(cards()).toHaveLength(1);
    expect(text()).not.toContain("alice");
  });

  it("opens the user from the button, by params.user_id (a device alert's subject is the device)", async () => {
    await mount({ people: [ended(), connection()] });
    const hrefs = [...document.querySelectorAll("a")].filter((a) => a.textContent === "Open user").map((a) => a.getAttribute("href"));
    expect(hrefs).toEqual(["/users/usr_masha", "/users/usr_oleg"]);
  });

  it("offers Mute like the other alerts", async () => {
    await mount({ people: [ended()] });
    expect([...document.querySelectorAll("button")].some((b) => b.textContent?.includes("Mute"))).toBe(true);
  });

  it("says so when nothing is open, even with node alerts around", async () => {
    await mount({ people: [node(), impacted] });
    expect(cards()).toHaveLength(0);
    expect(text()).toContain("Nobody has trouble connecting.");
  });
});

describe("Health ▸ Alerts", () => {
  it("does not show the alerts about people, only the node ones (the dropped connections of a node included)", async () => {
    await mount({ alerts: [ended(), connection(), node(), impacted] });
    expect(cards()).toHaveLength(2);
    expect(text()).toContain("Connections dropped on the node");
    expect(text()).not.toContain("Masha");
    expect(text()).not.toContain("Oleg");
    expect([...document.querySelectorAll("a")].some((a) => a.textContent === "Open user")).toBe(false);
  });

  it("is quiet when only people have alerts", async () => {
    await mount({ alerts: [ended(), connection()] });
    expect(cards()).toHaveLength(0);
    expect(text()).toContain("All quiet.");
  });
});
