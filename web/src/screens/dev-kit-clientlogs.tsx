import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { StepUpProvider } from "@/components/step-up";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { isLang, setLang } from "@/i18n";
import { ops } from "@/lib/api";
import { clientLogsQuery, type ClientLog } from "@/lib/client-debug";
import { useFmt } from "@/lib/format";
import { meQuery } from "@/lib/session";
import { aboutText, LogViewerBody, RequestLogsModal, deviceLabel } from "@/screens/users/client-logs";
import { useTx } from "@/screens/users/t";
import { DevicesPanel } from "@/screens/users/user-detail";

// Development only, /dev-kit?only=client-logs: logs for support on the person's page, on made-up data. alice-liddell has three
// devices of the Mistgate app: the office PC has an open request (and an older refusal), the MacBook sent the logs a request
// asked for, the iPhone sent its logs on its own. Then the viewer, open, with 40 cleaned records of the app (it filters, searches,
// pages and cuts to a time like the panel does), and the request window. The calls are answered here and nothing goes to the
// network. ?dialog=request opens the request window; ?lang=ru|fa|zh|tr|en is the page's.

const wantedLang = new URLSearchParams(location.search).get("lang");
if (isLang(wantedLang)) setLang(wantedLang);

const now = Math.floor(Date.now() / 1000);
const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

const device = (over: { id: string; platform: string; model: string } & Record<string, unknown>) => ({
  kind: 1, osVersion: "", client: "Mistgate/0.3.1", fingerprint: "", usedBytes: 0, onlineNodeIds: [], awgProfileId: "", awgProfileName: "", awgVersion: "", protocols: ["hysteria2"],
  online: false, lastSeenUnix: now - 600, firstSeenUnix: now - 12 * 86400, lastHandshakeUnix: 0, stale: false, address: "", enrolled: true, enrolLinkStale: false, ...over,
});
const devices = [
  device({ id: "dev_pc", platform: "windows", model: "Office PC", osVersion: "Windows 11", online: true, onlineNodeIds: ["de1"], lastSeenUnix: now - 30, usedBytes: 4.1e9, fingerprint: "c3d4e5" }),
  device({ id: "dev_mac", platform: "macos", model: "MacBook Air", osVersion: "macOS 15", usedBytes: 1.3e9, fingerprint: "f6a7b8" }),
  device({ id: "dev_phone", platform: "ios", model: "iPhone 15", osVersion: "iOS 18.1", lastSeenUnix: now - 3 * 3600, usedBytes: 7.9e8, fingerprint: "a1b2c3" }),
];

const base = { userId: "usr_alice", userName: "alice-liddell", droppedAttrs: 0, readUnix: 0, closedUnix: 0, note: "", actor: "", unasked: false, level: "info", app: "", records: 0, bytes: 0, firstUnixMs: 0, lastUnixMs: 0 };
const info = (over: object): ClientLog => ({ ...base, deviceId: "", devicePlatform: "", deviceModel: "", ...over }) as ClientLog;
const pcOf = { deviceId: "dev_pc", devicePlatform: "windows", deviceModel: "Office PC" };
const macOf = { deviceId: "dev_mac", devicePlatform: "macos", deviceModel: "MacBook Air" };
const phoneOf = { deviceId: "dev_phone", devicePlatform: "ios", deviceModel: "iPhone 15" };

const open = info({ id: "dbr_open", ...pcOf, state: "open", actor: "owner", note: "Evenings the tunnel drops for a minute. Could you send the logs?", requestedUnix: now - 3600, expiresUnix: now + 71 * 3600 });
const declined = info({ id: "dbr_declined", ...pcOf, state: "declined", actor: "owner", requestedUnix: now - 3 * 86400, expiresUnix: now - 2 * 86400, closedUnix: now - 3 * 86400 + 900 });
const received = info({
  id: "dbr_received", ...macOf, state: "received", actor: "owner", level: "debug", note: "The connection stops after sleep. Debug level please.", requestedUnix: now - 7200, expiresUnix: now + 70 * 3600,
  closedUnix: now - 5400, app: "Mistgate/0.3.1", records: 40, bytes: 6420, droppedAttrs: 3, readUnix: now - 3000,
});
const unasked = info({ id: "dbr_unasked", ...phoneOf, state: "received", unasked: true, requestedUnix: now - 86400, expiresUnix: now - 86400, closedUnix: now - 86400, app: "Mistgate/0.3.0", records: 22, bytes: 3100 });

let items: ClientLog[] = [open, received, unasked, declined];

// 40 cleaned records of the app, as the panel keeps them: addresses, names and keys are already <ip>, <user>, pth_… and srv_… ids.
const t0 = (now - 5400 - 3000) * 1000;
type Row = [number, "debug" | "info" | "warn" | "error", string, string, Record<string, string>?];
const rows: Row[] = [
  [0, "info", "service", "service started, version 0.3.1", { version: "0.3.1" }],
  [1, "info", "service", "state loaded from %USERPROFILE%\\AppData\\Roaming\\Mistgate\\state.json", { path: "%USERPROFILE%\\AppData\\Roaming\\Mistgate\\state.json" }],
  [1, "debug", "service", "sync requested", { event: "sync" }],
  [2, "info", "service", "sync done, 6 paths", { count: "6", ms: "212", status: "200" }],
  [3, "info", "network", "network changed", { net: "wifi" }],
  [3, "debug", "network", "gateway <gw>, dns <dns>"],
  [4, "info", "service", "connecting", { path: "pth_q1W2e3R4t5Y6u7I8", protocol: "hysteria2", attempt: "1" }],
  [5, "debug", "tunnel", "udp socket bound to <ip>:51820", { engine: "sing-box", phase: "bind" }],
  [9, "warn", "tunnel", "handshake timed out after 5000 ms", { path: "pth_q1W2e3R4t5Y6u7I8", code: "handshake_timeout", ms: "5000", attempt: "1" }],
  [10, "info", "service", "connecting", { path: "pth_q1W2e3R4t5Y6u7I8", protocol: "hysteria2", attempt: "2" }],
  [11, "warn", "tunnel", "handshake timed out after 5000 ms", { path: "pth_q1W2e3R4t5Y6u7I8", code: "handshake_timeout", ms: "5000", attempt: "2" }],
  [12, "info", "service", "trying the next path", { path: "pth_m9N8b7V6c5X4z3L2", protocol: "vless", via: "direct" }],
  [13, "debug", "tunnel", "tls client hello sent to <host>:443", { engine: "xray", phase: "tls" }],
  [14, "info", "tunnel", "connected", { path: "pth_m9N8b7V6c5X4z3L2", protocol: "vless", ms: "842", engine: "xray" }],
  [15, "info", "probe", "reachability check started", { event: "probe" }],
  [18, "info", "probe", "server srv_Zz98Yy76Xx54Ww32: tcp ok, 3 of 3", { server: "srv_Zz98Yy76Xx54Ww32", count: "3", ms: "52" }],
  [19, "warn", "probe", "server srv_Qq12Ww34Ee56Rr78: no answer over udp", { server: "srv_Qq12Ww34Ee56Rr78", code: "udp_no_answer", reason: "no_answer" }],
  [20, "debug", "probe", "dns answer for the server name is <ip>, same as the sync"],
  [22, "info", "service", "traffic started", { state: "up", bytes: "0" }],
  [60, "debug", "tunnel", "keepalive ok", { ms: "38" }],
  [180, "info", "update", "checking for a new version", { version: "0.3.1" }],
  [181, "info", "update", "0.3.1 is the latest"],
  [900, "debug", "tunnel", "keepalive ok", { ms: "41" }],
  [1500, "warn", "network", "network changed", { net: "ethernet", event: "network_change" }],
  [1501, "warn", "tunnel", "no packets from the server for 8 s", { path: "pth_m9N8b7V6c5X4z3L2", code: "silent", ms: "8000" }],
  [1503, "info", "service", "reconnecting", { path: "pth_m9N8b7V6c5X4z3L2", attempt: "1", reason: "silent" }],
  [1506, "error", "tunnel", "connection reset by peer <ip>:443", { path: "pth_m9N8b7V6c5X4z3L2", code: "reset", engine: "xray" }],
  [1507, "info", "service", "trying the next path", { path: "pth_q1W2e3R4t5Y6u7I8", protocol: "hysteria2" }],
  [1508, "debug", "tunnel", "quic initial packet sent, 1252 bytes", { engine: "sing-box", phase: "initial", bytes: "1252" }],
  [1509, "warn", "tunnel", "handshake timed out after 5000 ms", { path: "pth_q1W2e3R4t5Y6u7I8", code: "handshake_timeout", ms: "5000", attempt: "1" }],
  [1514, "error", "service", "cannot write C:\\Users\\<user>\\AppData\\Roaming\\Mistgate\\state.json: access denied", { code: "state_write_failed", path: "C:\\Users\\<user>\\AppData\\Roaming\\Mistgate\\state.json" }],
  [1515, "info", "service", "state kept in memory, will retry the write"],
  [1520, "info", "service", "connecting", { path: "pth_k4J3h2G1f0D9s8A7", protocol: "awg", attempt: "1" }],
  [1521, "debug", "tunnel", "awg handshake initiation sent", { engine: "awg", phase: "handshake" }],
  [1522, "info", "tunnel", "connected", { path: "pth_k4J3h2G1f0D9s8A7", protocol: "awg", ms: "1180", engine: "awg" }],
  [1560, "info", "probe", "reachability check done in 41 s", { ms: "41000", count: "6" }],
  [2400, "debug", "tunnel", "keepalive ok", { ms: "44" }],
  [2700, "info", "service", "sync done, 6 paths", { count: "6", ms: "190", status: "200" }],
  [2800, "info", "service", "logs requested by support, the person agreed", { event: "debug_request", mode: "debug" }],
  [2850, "info", "service", "the person looked at the logs and sent them", { event: "debug_send", count: "40" }],
];
const level = { debug: 0, info: 1, warn: 2, error: 3 } as const;
const lines = rows.map(([offset, lvl, source, message, attrs], i) => ({ seq: i + 1, timeUnixMs: t0 + offset * 1000 + ((i * 137) % 900), level: lvl, source, message, attrs: attrs ?? {} }));
received.firstUnixMs = lines[0]!.timeUnixMs;
received.lastUnixMs = lines[lines.length - 1]!.timeUnixMs;

function fakeCalls() {
  const api = ops as unknown as Record<string, unknown>;
  api.listClientDebug = async () => ({ items });
  api.requestClientDebug = async ({ deviceId, note, level: lvl }: { deviceId: string; note: string; level: string }) => {
    await wait(500);
    const d = devices.find((x) => x.id === deviceId)!;
    const row = info({ id: `dbr_${items.length}new`, deviceId, devicePlatform: d.platform, deviceModel: d.model, state: "open", actor: "owner", note, level: lvl, requestedUnix: now, expiresUnix: now + 72 * 3600 });
    items = [row, ...items];
    return { request: row };
  };
  api.cancelClientDebug = async ({ id }: { id: string }) => {
    await wait(300);
    items = items.map((x) => (x.id === id ? { ...x, state: "cancelled", closedUnix: now } : x));
    return { request: items.find((x) => x.id === id) };
  };
  api.deleteClientDebug = async ({ id }: { id: string }) => {
    await wait(300);
    items = items.filter((x) => x.id !== id);
    return {};
  };
  api.getClientDebug = async (req: { minLevel: string; source: string; query: string; fromMs: bigint; toMs: bigint; limit: number; afterSeq: bigint }) => {
    await wait(120);
    const min = req.minLevel ? level[req.minLevel as keyof typeof level] : 0;
    const q = req.query.toLowerCase();
    const hit = lines.filter(
      (l) =>
        l.seq > Number(req.afterSeq) &&
        level[l.level] >= min &&
        (!req.source || l.source === req.source) &&
        (!req.fromMs || l.timeUnixMs >= Number(req.fromMs)) &&
        (!req.toMs || l.timeUnixMs <= Number(req.toMs)) &&
        (!q || l.message.toLowerCase().includes(q) || Object.values(l.attrs).some((v) => v.toLowerCase().includes(q))),
    );
    const page = hit.slice(0, req.limit || 100);
    return {
      bundle: received,
      records: page.map((l) => ({ ...l, seq: BigInt(l.seq), timeUnixMs: BigInt(l.timeUnixMs) })),
      nextCursor: hit.length > page.length ? BigInt(page[page.length - 1]!.seq) : 0n,
      sources: [...new Set(lines.map((l) => l.source))].sort(),
    };
  };
  api.exportClientDebug = async function* () {
    yield { data: new TextEncoder().encode(lines.map((l) => JSON.stringify({ seq: l.seq, ts: l.timeUnixMs, lvl: l.level, src: l.source, msg: l.message, kv: l.attrs })).join("\n") + "\n") };
  };
}
fakeCalls();

const owner = { admin: { id: "adm_1", login: "owner", role: Role.OWNER }, edition: "vps", unavailable: [] };
const user = { id: "usr_alice", name: "alice-liddell", groupName: "Everyone", deviceLimit: 5, devicesUsed: 3, apps: { happ: true, amnezia: false } } as never;

function Frame({ id, title, wide, children }: { id: string; title: string; wide?: boolean; children: ReactNode }) {
  const [qc] = useState(() => {
    const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false } } });
    client.setQueryData(meQuery.queryKey, owner as never);
    client.setQueryData(clientLogsQuery("usr_alice").queryKey, { items } as never);
    return client;
  });
  return (
    <section data-shot={id} className={`flex w-full flex-col gap-3 ${wide ? "max-w-[760px]" : "max-w-[560px]"}`}>
      <SectionLabel>{title}</SectionLabel>
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <StepUpProvider>{children}</StepUpProvider>
        </ToastProvider>
      </QueryClientProvider>
    </section>
  );
}

/** The open bundle, drawn in the page (the panel opens it in a window): the title and the line under it, then what the window holds. */
function ViewerBlock() {
  const t = useTx();
  const fmt = useFmt();
  const [downloading, setDownloading] = useState(false);
  return (
    <div className="flex flex-col gap-4 rounded-card-lg border border-line bg-surface p-5">
      <div className="flex flex-col gap-1.5">
        <h3 className="text-[19px] leading-tight font-extrabold tracking-[-0.03em]">{t("clientlogs.viewerT", { name: deviceLabel(received) })}</h3>
        <p className="text-[13px] leading-normal text-muted">{aboutText(received, t, fmt.stamp)}</p>
      </div>
      <LogViewerBody
        log={received}
        downloading={downloading}
        onDownload={() => {
          setDownloading(true);
          setTimeout(() => setDownloading(false), 600);
        }}
      />
    </div>
  );
}

function RequestBlock({ auto }: { auto: boolean }) {
  const [openIt, setOpenIt] = useState(auto);
  return (
    <>
      <div>
        <Button onClick={() => setOpenIt(true)}>Open the request window</Button>
      </div>
      <RequestLogsModal shot="client-logs-request" device={{ id: "dev_mac", name: "MacBook Air" }} open={openIt} onOpenChange={setOpenIt} onSend={async () => wait(400)} />
    </>
  );
}

export function ClientLogsKit() {
  const dialog = new URLSearchParams(location.search).get("dialog");
  return (
    <div className="flex flex-col gap-8">
      <Frame id="client-logs-devices" title="alice-liddell: three devices of the Mistgate app (an open request, logs that came on request, logs sent on their own) and the list of logs">
        <DevicesPanel user={user} devices={devices as never} profiles={[]} actions={{ refresh: async () => {}, revoke: async () => {} } as never} onEditGroup={() => {}} />
      </Frame>
      <Frame id="client-logs-viewer" wide title="The logs of the MacBook, open: 40 records, filters by level, source, text and time">
        <ViewerBlock />
      </Frame>
      <Frame id="client-logs-request-block" title={`The request window (${dialog === "request" ? "open" : "?dialog=request opens it"})`}>
        <RequestBlock auto={dialog === "request"} />
      </Frame>
    </div>
  );
}
