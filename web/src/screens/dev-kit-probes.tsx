import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { StepUpProvider } from "@/components/step-up";
import { SectionLabel } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { editionFeatures } from "@/i18n/edition";
import { isLang, setLang } from "@/i18n";
import { countryReach, health, nodes, ownProbe } from "@/lib/api";
import { checksQuery } from "@/lib/health";
import { ownProbeQuery, useOwnProbeActions, type Issued } from "@/lib/ownprobe";
import { meQuery } from "@/lib/session";
import { CountryRow } from "@/screens/health/country";
import { ReachabilityScreen } from "@/screens/reachability";
import { AddProbeModal, RevokeModal, UpdateModal } from "@/screens/reachability/probes";
import { ProbesCard } from "@/screens/reachability/sources";

// Development only, /dev-kit?only=probes: the own probes on made-up data (the documentation networks, probes "dacha" and "office",
// operators that do not exist). Each scene has a private query cache and no panel behind it; ?scene=<id> shows one alone, for its
// screenshots. The windows open on their own: the add form, the screen with the one-time command, the revoke, the update command.
// ?lang=ru|fa|zh|tr|en is the page's.

const wantedLang = new URLSearchParams(location.search).get("lang");
if (isLang(wantedLang)) setLang(wantedLang);

const now = Math.floor(Date.now() / 1000);
const min = 60;
const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

const me = (role: number, edge = false) => ({
  admin: { id: "adm_1", displayName: "Owner", login: "owner", role },
  version: "0.1.24",
  stepUpUntilUnix: 0n,
  edition: edge ? "edge" : "vps",
  unavailable: edge ? [...editionFeatures] : [],
});

const settings = (over: object = {}) => ({ enabled: true, intervalMin: 60, fallbackUrl: "", ...over });
const probe = (over: object) => ({
  id: "prb_0123456789abcdef01234567", name: "дача", city: "Тверь", kind: "home", state: "active", asn: 64496, operator: "ExampleNet", pinnedOperator: "ExampleNet",
  version: "0a1b2c3d4e5f", lastSeenUnix: now - 4 * min, lastResultUnix: now - 12 * min, checksDay: 24, tokenExpiresUnix: 0, ...over,
});
const dacha = probe({});
const office = probe({
  id: "prb_89abcdef0123456789abcdef", name: "офис", city: "Казань", kind: "dc", asn: 64497, operator: "Demo Telecom", pinnedOperator: "Demo Telecom",
  version: "9f8e7d6c5b4a", lastSeenUnix: now - 9 * min, lastResultUnix: now - 21 * min, checksDay: 24,
});
// the owner's own router abroad: it sees no filtering, which is the right state for it; its point is "office" in the checks below
const router = probe({ ...office, name: "роутер", city: "Лиссабон", kind: "home", version: "0a1b2c3d4e5f" });
const waiting = probe({ id: "prb_aaaaaaaaaaaaaaaaaaaaaaaa", name: "дача", operator: "", pinnedOperator: "", asn: 0, version: "", state: "pending", lastSeenUnix: 0, lastResultUnix: 0, checksDay: 0, tokenExpiresUnix: now + 22 * 3600 });
const latest = "0a1b2c3d4e5f";

// one dark point and one that reaches, for the node rows
const vantage = (over: object) => ({ source: "probes", key: "probe:prb_x", network: "", region: "", asn: "", kind: "home", state: "reach", atUnix: now - 11 * min, dpiHop: 0, icmpOk: false, tcpOk: true, why: "", ...over });
const darkDacha = vantage({ key: "probe:prb_0123456789abcdef01234567", network: "ExampleNet · Тверь · probe дача", region: "Тверь", asn: "64496", state: "dark", why: "tls_reset" });
const reachDacha = vantage({ key: "probe:prb_0123456789abcdef01234567", network: "ExampleNet · Тверь · probe дача", region: "Тверь", asn: "64496" });
const openOffice = vantage({ key: "probe:prb_89abcdef0123456789abcdef", network: "Demo Telecom · Казань · probe офис", region: "Казань", asn: "64497", kind: "dc", state: "uncertain", why: "no_filter", atUnix: now - 17 * min });
const gp = (network: string, state: string, region = "") => ({ source: "globalping", key: `gp:${network}`, network, region, asn: "", kind: "home", state, atUnix: now - 6 * min, dpiHop: 0, icmpOk: state === "reach", tcpOk: state === "reach", why: "" });

const check = (nodeId: string, verdict: CountryVerdict, over: object = {}) => ({
  nodeId, country: "RU", verdict, atUnix: now - 6 * min, probes: [], reason: "", eyeball: true, confidence: "eyeball", listedBy: [], cdnProvider: "", cdnRegion: "",
  sources: ["globalping"], globalpingVerdict: verdict, tspuVerdict: CountryVerdict.UNSPECIFIED, tspuPorts: [], dpiHop: 0, tspuVantageId: "", tspuVantageName: "", tspuAtUnix: 0, noProbe: [],
  vantages: [], sourceVerdicts: [], ...over,
});
// de1: a block on some networks, the probe among the dark ones; nl1: all well, the probe reaches it; se1: a probe that sees no filtering
const countryChecks = [
  check("nod_de1", CountryVerdict.PARTIAL, { sources: ["globalping", "probes"], vantages: [gp("Demo Telecom", "dark", "Казань"), gp("Sample Mobile", "reach", "Москва"), darkDacha] }),
  check("nod_nl1", CountryVerdict.OK, { sources: ["globalping", "probes"], vantages: [gp("Demo Telecom", "reach", "Казань"), reachDacha] }),
  check("nod_se1", CountryVerdict.OK, { sources: ["globalping", "probes"], vantages: [gp("Sample Mobile", "reach", "Москва"), openOffice] }),
];

const calls = { n: 0 };
function fakeCalls() {
  const stub = (client: object, methods: Record<string, (...a: never[]) => unknown>) => Object.assign(client as Record<string, unknown>, methods);
  stub(ownProbe, {
    create: (async (req: { name: string; city: string; kind: string }) => {
      await wait(500);
      calls.n++;
      return {
        probe: { ...waiting, ...req, id: `prb_new${calls.n}` },
        token: "x",
        command:
          "opkg update && opkg install curl ca-bundle coreutils-sha256sum && cd /tmp && curl -fsSo mgp.sh 'https://panel.example.com/mgp-kb3x2o7q5vwiz4nrp6sleu5hfa/probe.sh' && echo '0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b  mgp.sh' | sha256sum -c - && sh mgp.sh install 'https://panel.example.com/mgp-kb3x2o7q5vwiz4nrp6sleu5hfa' 'mgp_kb3x2o7q5vwiz4nrp6sleu5hfa2o7q5vwiz4nrp6sleu5hf'",
        scriptSha256: latest.padEnd(64, "0"),
      };
    }) as never,
    revoke: (async () => {
      await wait(300);
      return {};
    }) as never,
  });
  stub(countryReach, {
    getCountryReachability: (async () => ({ settings: { country: "RU" } })) as never,
    getReachabilityHistory: (async () => ({
      country: "RU", sources: ["globalping", "lists", "probes"], sinceUnix: now - 7 * 86400, nowUnix: now,
      nodes: [
        { nodeId: "nod_de1", lanes: [{ source: "globalping", untilUnix: now, points: [{ atUnix: now - 7 * 86400 + 600, verdict: CountryVerdict.OK }, { atUnix: now - 30 * 3600, verdict: CountryVerdict.PARTIAL }] }, { source: "probes", untilUnix: now, points: [{ atUnix: now - 30 * 3600, verdict: CountryVerdict.PARTIAL }] }] },
        { nodeId: "nod_nl1", lanes: [{ source: "globalping", untilUnix: now, points: [{ atUnix: now - 7 * 86400 + 600, verdict: CountryVerdict.OK }] }, { source: "probes", untilUnix: now, points: [{ atUnix: now - 2 * 86400, verdict: CountryVerdict.OK }] }] },
      ],
    })) as never,
  });
  stub(health, {
    getChecks: (async () => ({ nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks })) as never,
    listAlerts: (async () => ({ nowUnix: now, active: [], history: [] })) as never,
  });
  stub(nodes, {
    listNodes: (async () => ({
      nodes: [
        { id: "nod_de1", name: "de1", countryCode: "DE", status: NodeStatus.ONLINE },
        { id: "nod_nl1", name: "nl1", countryCode: "NL", status: NodeStatus.ONLINE },
        { id: "nod_se1", name: "se1", countryCode: "SE", status: NodeStatus.ONLINE },
      ],
    })) as never,
  });
}
fakeCalls();

type Seed = { settings?: object; probes?: object[]; role?: number; edge?: boolean; checks?: object[] };

/** A scene: its own cache, seeded with what the panel would answer; the checks carry the points of the probes. */
function Frame({ id, title, seed = {}, wide, children }: { id: string; title: string; seed?: Seed; wide?: boolean; children: ReactNode }) {
  const [qc] = useState(() => {
    const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
    client.setQueryData(meQuery.queryKey, me(seed.role ?? Role.OWNER, seed.edge) as never);
    client.setQueryData(ownProbeQuery.queryKey, { settings: seed.settings ?? settings(), probes: seed.probes ?? [] } as never);
    client.setQueryData(["ownProbe", "script"], latest);
    client.setQueryData(checksQuery().queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: seed.checks ?? [] } as never);
    return client;
  });
  return (
    <section data-shot={id} className={`flex w-full min-w-0 flex-col gap-3 ${wide ? "max-w-[1100px]" : "max-w-[780px]"}`}>
      <SectionLabel>{title}</SectionLabel>
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <StepUpProvider>{children}</StepUpProvider>
        </ToastProvider>
      </QueryClientProvider>
    </section>
  );
}

/** The add window, over the page; `preset` is the screen with the command, as it comes after the form. */
function AddScene({ preset }: { preset?: Issued }) {
  const actions = useOwnProbeActions();
  const [open, setOpen] = useState(true);
  return <>{open && <AddProbeModal actions={actions} preset={preset} onClose={() => setOpen(false)} />}</>;
}


const scenes: { id: string; title: string; seed: Seed; render: () => ReactNode; wide?: boolean }[] = [
  { id: "off", title: "Off", seed: { settings: settings({ enabled: false }), probes: [dacha] }, render: () => <ProbesCard /> },
  { id: "empty", title: "On, no probes yet", seed: {}, render: () => <ProbesCard /> },
  { id: "pending", title: "Waiting for the install", seed: { probes: [waiting] }, render: () => <ProbesCard /> },
  { id: "works", title: "Two probes work, one has an older script", seed: { probes: [dacha, office] }, render: () => <ProbesCard /> },
  {
    id: "netchanged",
    title: "A network changed: waits for the owner",
    seed: { probes: [probe({ state: "network_changed", operator: "Sample Mobile", asn: 64498 }), office] },
    render: () => <ProbesCard />,
  },
  {
    id: "offline",
    title: "No contact",
    seed: { probes: [probe({ lastSeenUnix: now - 7 * 3600, lastResultUnix: now - 7 * 3600 }), probe({ ...office, lastSeenUnix: now - 26 * 3600, lastResultUnix: now - 26 * 3600 })] },
    render: () => <ProbesCard />,
  },
  {
    id: "nofilter",
    title: "No filtering on its path (a router outside the country)",
    seed: { probes: [router], checks: [countryChecks[2]!] },
    render: () => <ProbesCard />,
  },
  { id: "add", title: "The add window", seed: { probes: [dacha] }, render: () => <AddScene /> },
  {
    id: "command",
    title: "The one-time command",
    seed: { probes: [dacha] },
    render: () => (
      <AddScene
        preset={{
          probe: waiting as never,
          command:
            "opkg update && opkg install curl ca-bundle coreutils-sha256sum && cd /tmp && curl -fsSo mgp.sh 'https://panel.example.com/mgp-kb3x2o7q5vwiz4nrp6sleu5hfa/probe.sh' && echo '0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b  mgp.sh' | sha256sum -c - && sh mgp.sh install 'https://panel.example.com/mgp-kb3x2o7q5vwiz4nrp6sleu5hfa' 'mgp_kb3x2o7q5vwiz4nrp6sleu5hfa2o7q5vwiz4nrp6sleu5hf'",
        }}
      />
    ),
  },
  { id: "revoke", title: "Revoke asks first", seed: { probes: [dacha] }, render: () => <RevokeModal probe={dacha as never} busy={false} onClose={() => {}} onConfirm={() => {}} /> },
  { id: "update", title: "The update command", seed: { probes: [dacha, office] }, render: () => <UpdateModal probe={office as never} onClose={() => {}} /> },
  {
    id: "points",
    title: "The probes' points in the rows of a node (the node's card and page)",
    seed: {},
    render: () => (
      <div className="flex flex-col gap-3 rounded-card-lg border border-line bg-surface p-4">
        {countryChecks.map((c) => (
          <div key={c.nodeId} className="flex flex-col gap-1.5">
            <span className="font-mono text-[13px] font-bold">{c.nodeId.replace("nod_", "")}</span>
            <CountryRow check={c as never} code="RU" />
          </div>
        ))}
      </div>
    ),
  },
  { id: "assistant", title: "The helper's view: the Reachability page, the sources are the owner's", seed: { role: Role.HELPER, probes: [dacha], checks: countryChecks }, wide: true, render: () => <ReachabilityScreen /> },
  { id: "edge", title: "The Cloudflare edition", seed: { edge: true }, render: () => <ProbesCard /> },
];

export function ProbesKit() {
  const only = new URLSearchParams(location.search).get("scene");
  return (
    <div className="flex flex-col gap-8">
      {scenes
        .filter((s) => !only || s.id === only)
        .map((s) => (
          <Frame key={s.id} id={s.id} title={s.title} seed={s.seed} wide={s.wide}>
            {s.render()}
          </Frame>
        ))}
    </div>
  );
}
