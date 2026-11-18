import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { AddNodeProvider, AddNodeWindow, type AddNodeSeed } from "@/components/add-node";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeShareMode, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { NodePreflightSchema } from "@/gen/mistgate/admin/v1/provisioning_pb";
import { isLang, setLang, useLang } from "@/i18n";
import { setEffectsEnabled } from "@/lib/effects";
import type { ShareChoice } from "@/lib/node-share";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { setTheme, useTheme } from "@/lib/theme";
import { nodePageData } from "./dev-kit-node-page";
import { Shot } from "./dev-kit-redesign";
import { NodeDetail } from "./node";
import { shareDemo, type ShareDemo } from "./node/share";
import { ShellLayout } from "./shell";
import { groupsQuery } from "./users/rpc";

// Development only. /dev-kit?only=node-share: who gets a node, in every state, each in a frame of its own width (an iframe, so the
// window's breakpoints answer to the frame); each frame is a page of its own, for the screenshot script:
//   /dev-kit?only=node-share-page&state=<state>[&lang=en|ru|zh|fa|tr][&theme=light|dark][&fx=1]
// The card "Who gets this node" on the Overview of a node (the whole page, in the shell):
//   all | listed (family and friends) | none | none-idle (for over a day) | load (the groups are coming) | error (the groups did not
//   come) | ask-none (the question before "Take off everyone", with the panel's count) | ask-none-count (the panel still counts) |
//   ask-none-error (the count was refused) | ask-all (the question before "Give to everyone") | viewer (a read-only role)
// The choice in the New node window (the window alone), win-<way>-<state>:
//   win-cmd-none | win-cmd-all | win-cmd-listed | win-cmd-pick (some groups, none ticked) | win-cmd-first (the first node: only the
//   note) | win-cmd-load | win-cmd-error | win-ssh-none | win-ssh-listed
// The label of a node nobody gets in the Nodes list is the Nodes kit's scene `held` (/dev-kit?only=nodes).
// Made-up data: the node de1, the groups family, friends, team and guests (17 people), the nodes of the board, documentation addresses.

const NOW = Math.floor(Date.now() / 1000);
const D = 86_400;
const node = "nod_de1";

// name, color, people
const specs = [
  ["family", "lavender", 4],
  ["friends", "sand", 9],
  ["team", "sage", 3],
  ["guests", "rose", 1],
] as const;
const gid = (name: string) => `grp_${name}`;
const everyGroup = specs.map(([n]) => gid(n));

/** The groups, `receiving` of them with the node (the others have it on their disabled list, as the panel keeps a node it holds back). */
export const groupRows = (receiving: readonly string[], nodeId = node) =>
  specs.map(([name, color, people]) => ({
    id: gid(name),
    name,
    profileIds: ["p_vless", "p_hy"],
    userCount: people,
    dnsPresetId: "",
    happNodes: 4,
    amneziaNodes: 0,
    telegramNodes: 0,
    color,
    disabledNodeIds: receiving.includes(gid(name)) ? [] : [nodeId],
  }));

const summary = (mode: NodeShareMode, receiving: number, idle = false) => ({ mode, groupsReceiving: receiving, groupsTotal: specs.length, changedUnix: NOW - 3 * D, idle });

type Card = { mode: NodeShareMode; receiving: readonly string[]; idle?: boolean; groups?: "load" | "error"; demo?: ShareDemo; role?: Role };
const both = [gid("family"), gid("friends")];
export const cards: Record<string, Card> = {
  all: { mode: NodeShareMode.ALL, receiving: everyGroup },
  listed: { mode: NodeShareMode.LISTED, receiving: both },
  none: { mode: NodeShareMode.NONE, receiving: [] },
  "none-idle": { mode: NodeShareMode.NONE, receiving: [], idle: true },
  load: { mode: NodeShareMode.ALL, receiving: everyGroup, groups: "load" },
  error: { mode: NodeShareMode.ALL, receiving: everyGroup, groups: "error" },
  "ask-none": { mode: NodeShareMode.ALL, receiving: everyGroup, demo: { ask: "none", count: { gain: 0, lose: 17 } } },
  "ask-none-count": { mode: NodeShareMode.ALL, receiving: everyGroup, demo: { ask: "none", count: "load" } },
  "ask-none-error": { mode: NodeShareMode.ALL, receiving: everyGroup, demo: { ask: "none", count: "error" } },
  "ask-all": { mode: NodeShareMode.LISTED, receiving: both, demo: { ask: "all", count: { gain: 4, lose: 0 } } },
  viewer: { mode: NodeShareMode.LISTED, receiving: both, role: Role.READONLY },
};

/** The groups that are on their way, or did not come: the query is made to wait for ever, or to have failed. */
function settle(qc: QueryClient, how: "load" | "error" | undefined, rows: unknown) {
  qc.removeQueries({ queryKey: groupsQuery.queryKey }); // the page's own seeds may hold groups of another shape
  if (how === "load") void qc.prefetchQuery({ queryKey: groupsQuery.queryKey, queryFn: () => new Promise(() => {}) });
  else if (how === "error") {
    qc.setQueryDefaults(groupsQuery.queryKey, { retryOnMount: false });
    qc.getQueryCache().build(qc, { queryKey: groupsQuery.queryKey }).setState({ status: "error", fetchStatus: "idle", error: new ConnectError("no answer", Code.Unavailable), errorUpdateCount: 1, errorUpdatedAt: Date.now(), data: undefined });
  } else qc.setQueryData(groupsQuery.queryKey, rows as never);
}

/** The private query cache of a card scene, and the node page it draws (exported for its smoke test). */
export function seededCard(state: string) {
  const sc = cards[state] ?? cards.all!;
  const made = nodePageData("calm");
  Object.assign(made.data.node, { sharing: summary(sc.mode, sc.receiving.length, sc.idle) });
  settle(made.qc, sc.groups, groupRows(sc.receiving));
  if (sc.role) {
    const me = made.qc.getQueryData(meQuery.queryKey) as { admin: object };
    made.qc.setQueryData(meQuery.queryKey, { ...me, admin: { ...me.admin, role: sc.role } } as never);
  }
  shareDemo.current = sc.demo;
  return made;
}

// ---- the New node window

const fingerprint = "SHA256:Vq3n0cX9Lr2bT8kM1pZs6YwE4hJ7uGdA5fNiQoRlKe0";
const preflight = create(NodePreflightSchema, {
  distribution: "Ubuntu",
  version: "24.04",
  kernel: "6.8.0",
  architecture: "amd64",
  cpuCount: 2,
  memoryBytes: 2_040_109_466n,
  diskAvailableBytes: 18_683_971_379n,
  systemd: true,
  alreadyEnrolled: false,
  panelReachable: true,
  nftables: true,
});
const checkSeed = { host: "203.0.113.24", port: "22", fingerprint, algorithm: "ssh-ed25519", step: 2 as const, confirmedKey: true, preflight, method: "key" as const, answered: 99 };
const fleet = ["de1", "fi1", "nl1", "msk1"].map((n) => ({ id: `nod_${n}`, name: n, countryCode: "DE", location: "", provider: "", address: `${n}.example.com`, status: NodeStatus.ONLINE, online: [], sparkBytes: [] }));

type Win = { mode: "ssh" | "cmd"; share?: ShareChoice; first?: boolean; groups?: "load" | "error" };
const listed = (...ids: string[]): ShareChoice => ({ mode: "listed", groupIds: ids });
export const windows: Record<string, Win> = {
  "win-cmd-none": { mode: "cmd" },
  "win-cmd-all": { mode: "cmd", share: { mode: "all", groupIds: [] } },
  "win-cmd-listed": { mode: "cmd", share: listed(gid("family"), gid("friends")) },
  "win-cmd-pick": { mode: "cmd", share: listed() },
  "win-cmd-first": { mode: "cmd", first: true },
  "win-cmd-load": { mode: "cmd", share: listed(), groups: "load" },
  "win-cmd-error": { mode: "cmd", share: listed(), groups: "error" },
  "win-ssh-none": { mode: "ssh" },
  "win-ssh-listed": { mode: "ssh", share: listed(gid("family"), gid("friends")) },
};

/** The private query cache of a window scene (exported for its smoke test). */
export function seededWindow(state: string) {
  const w = windows[state] ?? windows["win-cmd-none"]!;
  const qc = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role: Role.OWNER }, version: "0.1.71", stepUpUntilUnix: 0n } as never);
  qc.setQueryData(nodesQuery.queryKey, { nodes: w.first ? [] : fleet } as never);
  qc.setQueryData(["panel-ssh-key"], { publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK7f2mQ9xW4bN1cR8vT3yL6pZ0sJ5hD2gF9aE7uK4oM mistgate-panel", fingerprint: "SHA256:3kQz7mVb1xN8cR2tY5pL0sW9hD4gF6aJ8uE1oK7iM3n" });
  qc.setQueryData(["node-server-access"], { access: [] });
  settle(qc, w.groups, groupRows([]));
  return qc;
}

export const windowSeed = (state: string): AddNodeSeed => {
  const w = windows[state] ?? windows["win-cmd-none"]!;
  return { draft: { name: "se1", address: "se1.example.com", country: "SE", ...(w.share && { share: w.share }) }, ...(w.mode === "ssh" && { ssh: checkSeed }) };
};

// ---- a page of one scene

/** One whole page of the scene: the node's page in the real shell, or the New node window alone, at the frame's own width. */
export function NodeSharePage() {
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "all";
  const win = state.startsWith("win-");
  const [scene] = useState(() => {
    const lang = params.get("lang");
    if (isLang(lang)) setLang(lang);
    const theme = params.get("theme");
    if (theme === "light" || theme === "dark") setTheme(theme);
    if (params.get("fx") === "1") setEffectsEnabled(true);
    return win ? { qc: seededWindow(state), data: undefined } : seededCard(state);
  });
  return (
    <QueryClientProvider client={scene.qc}>
      <ToastProvider>
        <AddNodeProvider>
          {win ? (
            <AddNodeWindow open initialMode={(windows[state] ?? windows["win-cmd-none"]!).mode} onClose={() => {}} seed={windowSeed(state)} />
          ) : (
            <ShellLayout current="nodes" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
              <NodeDetail data={scene.data as never} />
            </ShellLayout>
          )}
        </AddNodeProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

export const frames: { id: string; state: string; title: string; w: number; h: number; params?: string }[] = [
  { id: "share-all", state: "all", title: "Everyone: every group is switched on", w: 1440, h: 780 },
  { id: "share-all-light", state: "all", title: "The same on the light theme", w: 1440, h: 780, params: "&theme=light" },
  { id: "share-listed", state: "listed", title: "Some groups: family and friends, the rest off", w: 1440, h: 780 },
  { id: "share-listed-phone", state: "listed", title: "Some groups on a phone, 400 wide", w: 400, h: 1100 },
  { id: "share-listed-fa", state: "listed", title: "Right to left (fa)", w: 1440, h: 780, params: "&lang=fa" },
  { id: "share-none", state: "none", title: "Nobody yet", w: 1440, h: 780 },
  { id: "share-none-idle", state: "none-idle", title: "Nobody for over a day: a quiet hint", w: 1440, h: 780 },
  { id: "share-none-phone", state: "none", title: "Nobody yet on a phone", w: 400, h: 1000 },
  { id: "share-load", state: "load", title: "The groups are coming", w: 1440, h: 780 },
  { id: "share-error", state: "error", title: "The groups did not come", w: 1440, h: 780 },
  { id: "share-ask-none", state: "ask-none", title: "Take off everyone: the question with the panel's count", w: 1440, h: 780 },
  { id: "share-ask-none-light", state: "ask-none", title: "The same on the light theme", w: 1440, h: 780, params: "&theme=light" },
  { id: "share-ask-none-phone", state: "ask-none", title: "The question on a phone", w: 400, h: 1100 },
  { id: "share-ask-none-ru", state: "ask-none", title: "The question in Russian", w: 1440, h: 780, params: "&lang=ru" },
  { id: "share-ask-none-count", state: "ask-none-count", title: "The panel is still counting", w: 1440, h: 780 },
  { id: "share-ask-none-error", state: "ask-none-error", title: "The count was refused", w: 1440, h: 780 },
  { id: "share-ask-all", state: "ask-all", title: "Give to everyone: the question", w: 1440, h: 780 },
  { id: "share-viewer", state: "viewer", title: "A read-only role: the switches are still, no buttons", w: 1440, h: 780 },
  { id: "share-win-cmd-none", state: "win-cmd-none", title: "New node by a command: nobody yet (the start from the second node)", w: 960, h: 820 },
  { id: "share-win-cmd-all", state: "win-cmd-all", title: "New node: everyone", w: 960, h: 820 },
  { id: "share-win-cmd-listed", state: "win-cmd-listed", title: "New node: some groups, two ticked", w: 960, h: 900 },
  { id: "share-win-cmd-listed-light", state: "win-cmd-listed", title: "The same on the light theme", w: 960, h: 900, params: "&theme=light" },
  { id: "share-win-cmd-pick", state: "win-cmd-pick", title: "New node: some groups, none ticked (the button waits)", w: 960, h: 900 },
  { id: "share-win-cmd-first", state: "win-cmd-first", title: "New node: the first node, only the note", w: 960, h: 760 },
  { id: "share-win-cmd-load", state: "win-cmd-load", title: "New node: the groups are coming", w: 960, h: 820 },
  { id: "share-win-cmd-error", state: "win-cmd-error", title: "New node: the groups did not come", w: 960, h: 820 },
  { id: "share-win-ssh-none", state: "win-ssh-none", title: "New node by SSH, 3 Check: who gets it, nobody yet", w: 960, h: 900 },
  { id: "share-win-ssh-listed", state: "win-ssh-listed", title: "New node by SSH: some groups", w: 960, h: 1000 },
  { id: "share-win-listed-phone", state: "win-cmd-listed", title: "New node on a phone: some groups", w: 400, h: 780 },
  { id: "share-win-ssh-phone", state: "win-ssh-none", title: "New node by SSH on a phone, the check", w: 400, h: 780 },
  { id: "share-win-listed-ru", state: "win-cmd-listed", title: "New node in Russian", w: 960, h: 900, params: "&lang=ru" },
];

export function NodeShareKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Who gets a node</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((f) => (
        <Shot key={f.id} id={f.id} title={f.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={f.title} src={`${path}?only=node-share-page&state=${f.state}${f.params ?? ""}`} width={f.w} height={f.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
