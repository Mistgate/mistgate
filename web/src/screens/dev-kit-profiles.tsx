import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { ChatButton } from "@/components/assistant/chat";
import { AddNodeProvider } from "@/components/add-node";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { BundleStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { isLang, setLang, useLang } from "@/i18n";
import { groups as groupsApi, profiles as profilesApi } from "@/lib/api";
import { setEffectsEnabled } from "@/lib/effects";
import { awaitingQuery } from "@/lib/integrations";
import { checksQuery } from "@/lib/health";
import { nodesQuery, overviewQuery, userCountQuery } from "@/lib/queries";
import { relaysQuery } from "@/lib/relay";
import { meQuery } from "@/lib/session";
import { setTheme, useTheme } from "@/lib/theme";
import { updatesQuery } from "@/lib/updates";
import { Shot } from "@/screens/dev-kit-redesign";
import { checksOf, connectionsOf, GROUPS, nid, NODES, NOW, nodeRows, PROTOCOLS, relaysOf, summaries, type Fleet } from "@/screens/dev-kit-profiles-data";
import { ProfilesScreen, type ProfilesDemo } from "@/screens/profiles/list";
import { fleetConnectionsQuery } from "@/screens/profiles/profiles-data";
import { groupsQuery, profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { ShellLayout } from "./shell";

// Development only. /dev-kit?only=pn-profiles: the Profiles list of the redesign in every state of the board (PnProfiles), each in a frame
// of its own width (an iframe, so the window's breakpoints answer to the frame); each frame is a page of its own, for the screenshot
// script and for the side-by-side with the board:
//   /dev-kit?only=profiles-page&state=<state>[&lang=en|ru|zh|fa|tr][&theme=light|dark][&role=owner|helper|readonly][&fx=1]
// states: list (the board's ten profiles) | none (the board's "0": no profile yet) | load | err (the data's own states) | nochecks (the checks have not run)
// | out (nl3 out of touch) | find (a search that finds one) | nomatch | proto (filtered to VLESS: ?protocol= in the frame) | facet-warn | facet-none |
// facet-nogroup | facet-warp (the state chips) | where (the matrix "Where it passes") | where-ok | where-bad | where-part | where-busy |
// where-gone | where-warp (the matrix with a cell open: a server that works, one that did not start, a failing check, one that starts, a node out of touch) |
// where-nochecks | where-load | where-find | where-nomatch.
// Made-up data of the board: de1 … spb1, the profiles "Main · 443" … "Telegram · 8443", the groups family and friends, documentation addresses.

const OPEN: Record<string, string> = { "where-ok": "prf_hy1|de1", "where-bad": "prf_hy1|fi1", "where-part": "prf_vl2|nl1", "where-busy": "prf_vl2|nl2", "where-gone": "prf_aw2|nl3", "where-warp": "prf_vl1|de1" };

/** What a scene starts the screen with (exported for its smoke test). */
export const demoOf = (state: string): ProfilesDemo => ({
  ...(state.startsWith("facet-") && { facet: ({ "facet-warn": "warn", "facet-none": "idle", "facet-nogroup": "nogroup", "facet-warp": "warp" } as const)[state as "facet-warn"] }),
  ...(OPEN[state] && { open: `${OPEN[state]!.split("|")[0]}|${nid(OPEN[state]!.split("|")[1]!)}` }),
});

const fleetOf = (state: string): Fleet => ({ out: state === "out" || state === "where-gone", noChecks: state === "nochecks" || state === "where-nochecks" });
const isWhere = (state: string) => state.startsWith("where");

/** The panel's calls of this screen the states of load and error answer by hand (the scene is its own page, so patching the shared client is safe). */
function fakeCalls(state: string) {
  const p = profilesApi as unknown as Record<string, unknown>;
  const g = groupsApi as unknown as Record<string, unknown>;
  if (state === "load" || state === "where-load") {
    p.listProfiles = () => new Promise(() => {});
    p.listProtocols = () => new Promise(() => {});
    g.listGroups = () => new Promise(() => {});
  }
  if (state === "err") p.listProfiles = () => Promise.reject(new Error("unavailable"));
}

/** The private query cache of a scene (exported for its smoke test). */
export function seeded(state: string, role: Role) {
  fakeCalls(state);
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
  c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role }, version: "0.1.71", sourceUrl: "https://github.com/example/mistgate", sourceStars: 298, stepUpUntilUnix: 0 } as never);
  c.setQueryData(nodesQuery.queryKey, { nodes: nodeRows(), expectedAgentVersion: "0.1.71" } as never);
  c.setQueryData(overviewQuery().queryKey, { nodesTotal: NODES.length, nodes: nodeRows().map((n) => ({ id: n.id, name: n.name, countryCode: n.countryCode, location: n.location, status: NodeStatus.ONLINE, online: [], sparkBytes: [] })), alertsActive: 0, alertsCritical: 0 } as never);
  c.setQueryData(userCountQuery.queryKey, { counts: { all: 140, online: 31, devicesUsed: 0, devicesOnline: 0 } } as never);
  c.setQueryData(awaitingQuery.queryKey, { awaiting: 0 } as never);
  c.setQueryData(updatesQuery.queryKey, { nowUnix: NOW, panel: { version: "v0.1.71", built: NOW, update: { version: "v0.1.71", url: "", checkedUnix: NOW, available: false, supported: true, installable: true, installing: false, errorKey: "" } }, bundle: { status: BundleStatus.TRUSTED, version: "v0.1.71", built: NOW }, nodes: [] } as never);
  if (state !== "load" && state !== "where-load" && state !== "err") {
    const none = state === "none";
    c.setQueryData(profileListQuery.queryKey, (none ? [] : summaries()) as never);
    c.setQueryData(protocolsQuery.queryKey, PROTOCOLS as never);
    c.setQueryData(groupsQuery.queryKey, GROUPS as never);
  }
  const fleet = fleetOf(state);
  c.setQueryData(checksQuery("").queryKey, checksOf(fleet) as never);
  c.setQueryData(fleetConnectionsQuery.queryKey, connectionsOf() as never);
  c.setQueryData(relaysQuery("").queryKey, (role === Role.OWNER ? relaysOf() : []) as never);
  return c;
}

/** One whole page of the scene: the screen in the real shell, at the window's own width. */
export function ProfilesPage() {
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "list";
  const [qc] = useState(() => {
    const lang = params.get("lang");
    if (isLang(lang)) setLang(lang);
    const theme = params.get("theme");
    if (theme === "light" || theme === "dark") setTheme(theme);
    setEffectsEnabled(params.get("fx") === "1", false);
    const role = params.get("role");
    return seeded(state, role === "helper" ? Role.HELPER : role === "readonly" ? Role.READONLY : Role.OWNER);
  });
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <AddNodeProvider>
          <ShellLayout current="profiles" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
            <ProfilesScreen demo={demoOf(state)} />
          </ShellLayout>
        </AddNodeProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

/** The view and the filters a scene's address carries (the screen reads them from the URL, like the app). */
export const sceneSearch = (state: string) => (isWhere(state) ? "&view=where" : "") + (state === "find" || state === "where-find" ? "&q=vless" : state === "nomatch" || state === "where-nomatch" ? "&q=zzz" : state === "proto" ? "&protocol=vless" : "");

export const frames: { id: string; state: string; title: string; w: number; h: number; params?: string }[] = [
  { id: "profiles-list", state: "list", title: "The board's ten profiles (1440)", w: 1440, h: 900 },
  { id: "profiles-list-light", state: "list", title: "The same on the light theme", w: 1440, h: 900, params: "&theme=light" },
  { id: "profiles-list-1000", state: "list", title: "A 1000 window: the rail, the people column leaves", w: 1000, h: 900 },
  { id: "profiles-list-800", state: "list", title: "A 800 window", w: 800, h: 900 },
  { id: "profiles-list-phone", state: "list", title: "A phone, 400 wide: the rows stack", w: 400, h: 1100 },
  { id: "profiles-list-fa", state: "list", title: "Right to left (fa)", w: 1440, h: 900, params: "&lang=fa" },
  { id: "profiles-none", state: "none", title: "No profile yet (the board's 0)", w: 1440, h: 640 },
  { id: "profiles-none-helper", state: "none", title: "No profile yet, a helper: the owner makes them", w: 1440, h: 640, params: "&role=helper" },
  { id: "profiles-load", state: "load", title: "Loading", w: 1440, h: 720 },
  { id: "profiles-err", state: "err", title: "The panel did not answer", w: 1440, h: 520 },
  { id: "profiles-nochecks", state: "nochecks", title: "The checks have not run yet: the servers work, with no number", w: 1440, h: 900 },
  { id: "profiles-out", state: "out", title: "nl3 out of touch", w: 1440, h: 900 },
  { id: "profiles-find", state: "find", title: "A search that finds VLESS", w: 1440, h: 520 },
  { id: "profiles-nomatch", state: "nomatch", title: "A search that finds nothing", w: 1440, h: 520 },
  { id: "profiles-proto", state: "proto", title: "Filtered to VLESS", w: 1440, h: 560 },
  { id: "profiles-facet-warn", state: "facet-warn", title: "The state chip: with remarks", w: 1440, h: 560 },
  { id: "profiles-facet-none", state: "facet-none", title: "The state chip: not on any node", w: 1440, h: 560 },
  { id: "profiles-facet-nogroup", state: "facet-nogroup", title: "The state chip: in no group", w: 1440, h: 640 },
  { id: "profiles-facet-warp", state: "facet-warp", title: "The state chip: via WARP", w: 1440, h: 520 },
  { id: "profiles-helper", state: "list", title: "A helper: no New profile, no offer in the rows", w: 1440, h: 900, params: "&role=helper" },
  { id: "profiles-readonly", state: "list", title: "An observer: the same", w: 1440, h: 900, params: "&role=readonly" },
  { id: "profiles-where", state: "where", title: "Where it passes: twelve nodes, ten profiles (1440)", w: 1440, h: 900 },
  { id: "profiles-where-light", state: "where", title: "The same on the light theme", w: 1440, h: 900, params: "&theme=light" },
  { id: "profiles-where-1000", state: "where", title: "A 1000 window: the matrix scrolls sideways, the first column stays", w: 1000, h: 900 },
  { id: "profiles-where-phone", state: "where", title: "A phone: every profile its own list of nodes", w: 400, h: 1400 },
  { id: "profiles-where-fa", state: "where", title: "Right to left (fa)", w: 1440, h: 900, params: "&lang=fa" },
  { id: "profiles-where-ok", state: "where-ok", title: "A cell open: a server that works", w: 1440, h: 900 },
  { id: "profiles-where-bad", state: "where-bad", title: "A cell open: a server that did not start", w: 1440, h: 900 },
  { id: "profiles-where-part", state: "where-part", title: "A cell open: the check fails", w: 1440, h: 900 },
  { id: "profiles-where-busy", state: "where-busy", title: "A cell open: a server that starts", w: 1440, h: 900 },
  { id: "profiles-where-gone", state: "where-gone", title: "A cell open: its node is out of touch", w: 1440, h: 900 },
  { id: "profiles-where-warp", state: "where-warp", title: "A cell open: a server a relay also reaches", w: 1440, h: 900 },
  { id: "profiles-where-ok-phone", state: "where-ok", title: "A cell open on a phone: the drawer under the list", w: 400, h: 1500 },
  { id: "profiles-where-nochecks", state: "where-nochecks", title: "The matrix with no checks: \"works, no check\"", w: 1440, h: 900 },
  { id: "profiles-where-load", state: "where-load", title: "The matrix loading", w: 1440, h: 720 },
  { id: "profiles-where-find", state: "where-find", title: "The matrix of a search", w: 1440, h: 520 },
  { id: "profiles-where-nomatch", state: "where-nomatch", title: "The matrix of a search that finds nothing", w: 1440, h: 520 },
];

export function PnProfilesKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Profiles</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((f) => (
        <Shot key={f.id} id={f.id} title={f.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={f.title} src={`${path}?only=profiles-page&state=${f.state}${sceneSearch(f.state)}${f.params ?? ""}`} width={f.w} height={f.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
