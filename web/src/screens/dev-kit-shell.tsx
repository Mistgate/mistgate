import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { ChatButton } from "@/components/assistant/chat";
import { playWave, originOf } from "@/components/fx/wave";
import { Card, PageTitle } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { ToastProvider, useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { BundleStatus, NodeUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { useLang } from "@/i18n";
import { setEffectsEnabled } from "@/lib/effects";
import { awaitingQuery } from "@/lib/integrations";
import { overviewQuery, userCountQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { useTheme } from "@/lib/theme";
import { updatesQuery } from "@/lib/updates";
import { Shot } from "./dev-kit-redesign";
import { ShellLayout } from "./shell";

// Development only. /dev-kit?only=shell: the shell of the admin in every state, each in a frame of its own width (an iframe,
// so the window's breakpoints answer to the frame): the open menu, the rail, the list of the nodes, the language list, the
// owner's menu, the phone with its menu open, the toast and the wave. The frames follow the theme and language chosen at the
// top (they reload with them). Each frame is a page of its own, /dev-kit?only=shell-page&state=<state>, for the screenshot script:
//   full | rail (a 900px window folds the menu by itself) | fleet | lang | owner | phone | mobile | toast | wave
// `fx=1` on a page turns the reveal animation on (it is off by default; the wave and the toast's halo are part of it).
// Made-up data: nodes de1, nl1, fi1 and pl1, a person called owner, a newer release.

const now = Math.floor(Date.now() / 1000);
const card = (id: string, cc: string, location: string, status: NodeStatus, reason?: string) => ({ id: `nod_${id}`, name: id, countryCode: cc, location, status, reason: reason ? { code: reason, params: {} } : undefined, online: [], sparkBytes: [] });

function seeded() {
  const qc = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
  qc.setQueryData(meQuery.queryKey, {
    admin: { id: "adm_1", displayName: "owner", role: Role.OWNER },
    version: "0.1.71",
    sourceUrl: "https://github.com/example/mistgate",
    sourceStars: 298,
    stepUpUntilUnix: 0,
  } as never);
  qc.setQueryData(overviewQuery().queryKey, {
    nodesTotal: 4,
    nodes: [
      card("nl1", "NL", "Rotterdam", NodeStatus.ONLINE),
      card("de1", "DE", "Nuremberg", NodeStatus.DOWN, "agent_silent"),
      card("fi1", "FI", "Helsinki", NodeStatus.ONLINE, "no_profiles"),
      card("pl1", "PL", "Warsaw", NodeStatus.ONLINE),
    ],
    alertsActive: 0,
    alertsCritical: 0,
  } as never);
  qc.setQueryData(userCountQuery.queryKey, { counts: { all: 12, online: 4 } } as never);
  qc.setQueryData(awaitingQuery.queryKey, { awaiting: 1 } as never);
  qc.setQueryData(updatesQuery.queryKey, {
    nowUnix: now,
    panel: { version: "v0.1.71", built: now, update: { version: "v0.1.86", url: "https://github.com/example/mistgate/releases/tag/v0.1.86", checkedUnix: now, available: true, supported: true, installable: true, installing: false, errorKey: "" } },
    bundle: { status: BundleStatus.TRUSTED, version: "v0.1.71", built: now },
    nodes: [{ nodeId: "nod_nl1", name: "nl1", state: NodeUpdateState.UP_TO_DATE, supportsUpdate: true, built: now }],
  } as never);
  return qc;
}

type State = "full" | "rail" | "fleet" | "lang" | "owner" | "phone" | "mobile" | "toast" | "wave";
const demos: Record<State, { fleet?: boolean; lang?: boolean; owner?: boolean; mobile?: boolean }> = {
  full: {},
  rail: {},
  fleet: { fleet: true },
  lang: { lang: true },
  owner: { owner: true },
  phone: {},
  mobile: { mobile: true },
  toast: {},
  wave: {},
};

function PageBody({ state }: { state: State }) {
  const toast = useToast();
  useEffect(() => {
    if (state === "toast") toast("Saved", { undo: () => {} });
  }, [state, toast]);
  return (
    <div className="flex flex-col gap-4">
      <PageTitle>Overview</PageTitle>
      <Card className="p-6">
        <p className="text-sec">The page: 1240px at most, 32px at its sides from 1000px, 20 below, 16 on a phone.</p>
      </Card>
      <Card className="p-6">
        <p className="text-sec">de1 is down, fi1 has no profile, nl1 and pl1 work.</p>
      </Card>
      {state === "wave" && (
        <div className="flex items-center gap-3">
          <Button onClick={(e) => playWave(originOf(e.currentTarget), { light: false })}>Play the wave</Button>
          <span className="text-sm text-muted">Needs fx=1 in the address, or the reveal animation on in Settings → Interface.</span>
        </div>
      )}
    </div>
  );
}

/** One whole page of the scene, for a frame or a screenshot at the window's own width. */
export function ShellPage() {
  const params = new URLSearchParams(location.search);
  const state = (params.get("state") ?? "full") as State;
  const [qc] = useState(() => {
    if (params.get("fx") === "1") setEffectsEnabled(true);
    return seeded();
  });
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <ShellLayout current="nodes" demo={demos[state]} fab={<ChatButton onClick={() => {}} running={false} unread={2} />}>
          <PageBody state={state} />
        </ShellLayout>
      </ToastProvider>
    </QueryClientProvider>
  );
}

const frames: { id: string; state: State; title: string; w: number; h: number }[] = [
  { id: "shell-full", state: "full", title: "The open menu, 1440 wide: groups, counters, the update card, the owner", w: 1440, h: 780 },
  { id: "shell-rail", state: "rail", title: "The rail (a 900px window folds the menu by itself; from 1000px the button at its top does)", w: 900, h: 780 },
  { id: "shell-fleet", state: "fleet", title: "The list of the nodes under the fleet button: problems first", w: 1440, h: 780 },
  { id: "shell-lang", state: "lang", title: "The language list", w: 1440, h: 780 },
  { id: "shell-owner", state: "owner", title: "The owner's menu", w: 1440, h: 780 },
  { id: "shell-phone", state: "phone", title: "A phone, 400 wide: the bar, the dock, the assistant's button", w: 400, h: 780 },
  { id: "shell-mobile", state: "mobile", title: "A phone with the full menu open under the bar", w: 400, h: 780 },
  { id: "shell-toast", state: "toast", title: "The toast over the page (with fx=1 it comes out with its halo of dots)", w: 1440, h: 780 },
];

export function ShellKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Shell</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((f) => (
        <Shot key={f.id} id={f.id} title={f.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={f.title} src={`${path}?only=shell-page&state=${f.state}`} width={f.w} height={f.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
