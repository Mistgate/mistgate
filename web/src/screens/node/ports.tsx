import "./ways.css";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Fragment, useEffect, useState, type ReactNode } from "react";
import { ProtocolTag } from "@/components/ui/protocol-tag";
import { Switch } from "@/components/ui/switch";
import { useIsOwner } from "@/lib/updates";
import { useInSubscriptions } from "./subs-switches";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import {
  StatusDot,
  kindTextClass,
  type StatusKind,
} from "@/components/ui/status";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, type Inbound } from "@/gen/mistgate/admin/v1/common_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT, type T } from "@/i18n";
import { nodes as nodesApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { agentLinked } from "@/lib/node-status";
import {
  bestDelivered,
  lossPct,
  portRuns,
  reasonShort,
  reasonText,
  regionWord,
  renewalOf,
  renewalText,
  senderLabel,
  testBlocked,
  udpFilters,
  udpRegion,
  type PortRun,
} from "@/lib/port-check";
import { plain, type Plain } from "@/lib/plain";
import { nodesQuery } from "@/lib/queries";
import { protocolName } from "@/lib/series";
import { meQuery } from "@/lib/session";

// "Ports" in the Direct way in of a node (design/udp-port-check.md §6.6, design/vless.md §7.4): what the delivery check found
// for each UDP port and each TCP port (does a connection reach Mistgate), when and from where, which profile listens there,
// one line on whether the certificates renew, and the button that runs the check (about 8 seconds). It answers "do people
// get through to the node", so it lives with the ways in; the row names the profile, the profile row has the port's dot.

type Check = Plain<GetNodeResponse>["portChecks"][number];
type TcpCheck = Plain<GetNodeResponse>["tcpChecks"][number];

/**
 * How a stored verdict reads: the pill and its tone. A loss whose protocol works through the port is the test being
 * blocked: amber, not the loss. "" (not checked) and anything unknown read as not checked.
 */
export function verdictPill(
  t: T,
  c: Pick<Check, "verdict" | "sent" | "got"> &
    Partial<Pick<Check, "checkedUnix" | "protocolOkUnix">>,
): { kind: StatusKind; label: string } {
  if (testBlocked(c)) return { kind: "warn", label: t("ports.testBlocked") };
  const lost = lossPct(c.sent, c.got);
  switch (c.verdict) {
    case "ok":
      return { kind: "ok", label: t("ports.ok") };
    case "lossy":
      return { kind: "warn", label: t("ports.lossy", { lost }) };
    case "broken":
      return { kind: "bad", label: t("ports.broken", { lost }) };
    default:
      return { kind: "off", label: t("ports.unchecked") };
  }
}

/** How a stored TCP verdict reads: the pill and its tone. "" (not probed, see the reason) and anything unknown read as not checked. */
export function tcpPill(
  t: T,
  verdict: string,
): { kind: StatusKind; label: string } {
  switch (verdict) {
    case "ours":
      return { kind: "ok", label: t("ports.tcp.ours") };
    case "other":
    case "busy":
      return { kind: "warn", label: t(`ports.tcp.${verdict}`) };
    case "refused":
    case "timeout":
      return { kind: "bad", label: t(`ports.tcp.${verdict}`) };
    default:
      return { kind: "off", label: t("ports.unchecked") };
  }
}

/** A bar that fills in the time a run takes (`ms`, about 8 s for the ports) and stops short of full until the answer comes. */
export function Progress({ label, ms = 8000 }: { label: string; ms?: number }) {
  const [started, setStarted] = useState(false);
  useEffect(() => {
    const id = requestAnimationFrame(() => setStarted(true));
    return () => cancelAnimationFrame(id);
  }, []);
  return (
    <div
      role="progressbar"
      aria-label={label}
      className="h-1 overflow-hidden rounded-full bg-surface-2"
    >
      <div
        className="h-full rounded-full bg-accent transition-[width] ease-linear motion-reduce:transition-none"
        style={{ width: started ? "92%" : "4%", transitionDuration: `${ms}ms` }}
      />
    </div>
  );
}

/** The port check of a node: one run at a time, its answer kept for the lines under the rows. */
export function usePortCheck(nodeId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => plain(await nodesApi.checkPorts({ nodeId })),
    onSettled: () => void qc.invalidateQueries({ queryKey: ["node", nodeId] }),
  });
}
export type PortCheck = ReturnType<typeof usePortCheck>;

/** "Check ports", for those who may run it (owner, helper); off while the agent is not on the line. */
export function CheckPortsButton({
  data,
  run,
  primary,
}: {
  data: Plain<GetNodeResponse>;
  run: PortCheck;
  primary?: boolean;
}) {
  const t = useT();
  const role = useQuery(meQuery).data?.admin?.role;
  const linked = agentLinked(data.node!.status);
  if (role !== Role.OWNER && role !== Role.HELPER) return null;
  return (
    <span title={linked ? undefined : t("ports.offline")} className="flex">
      <Button
        variant={primary ? "primary" : "secondary"}
        size="md"
        disabled={run.isPending || !linked}
        onClick={() => run.mutate()}
      >
        {run.isPending ? t("ports.checking") : t("ports.check")}
      </Button>
    </span>
  );
}

/**
 * The rows the port check shows: the UDP ports (those of the UDP profiles first, a profile port never checked among them
 * once anything was), then the TCP ports. The Direct lane counts them, so the list and its count never disagree.
 */
export function portRows(
  data: Pick<
    Plain<GetNodeResponse>,
    "node" | "inbounds" | "portChecks" | "tcpChecks"
  >,
) {
  const stored = data.portChecks ?? [];
  const tcp = [...(data.tcpChecks ?? [])].sort((a, b) => a.port - b.port);
  // the UDP rows: the profiles that listen on UDP (VLESS is TCP: its ports are the TCP rows)
  const udpInbounds = data.inbounds.filter((i) => i.protocol !== "vless");
  const profilesOn = (port: number) =>
    udpInbounds.filter((i) => i.port === port).map((i) => i.profileName);
  // the ports of the profiles come first (the stored ones, then any that were never checked), then the others
  const unchecked: Check[] = [];
  if (stored.length > 0) {
    for (const port of new Set(udpInbounds.map((i) => i.port))) {
      if (port > 0 && !stored.some((c) => c.port === port))
        unchecked.push({
          nodeId: data.node!.id,
          port,
          verdict: "",
          sent: 0,
          got: 0,
          checkedUnix: 0,
          badUnix: 0,
          sender: "",
          reason: "",
          senderVerdict: "",
          confirmSender: "",
          confirmSent: 0,
          confirmGot: 0,
          confirmVerdict: "",
          blame: "",
          protocolOkUnix: 0,
        });
    }
  }
  const udp = [...stored, ...unchecked].sort(
    (a, b) =>
      Number(profilesOn(b.port).length > 0) -
        Number(profilesOn(a.port).length > 0) || a.port - b.port,
  );
  return { udp, tcp, profilesOn };
}

const tones: Record<StatusKind, string> = {
  ok: "ok",
  warn: "warn",
  bad: "crit",
  off: "idle",
  busy: "idle",
  blip: "idle",
};

/** One row of the table: a server and the port it listens on, with what the check found for the port (either may be missing). */
type PortRow = {
  key: string;
  net: "udp" | "tcp";
  port: number;
  inbound?: Plain<Inbound>;
  check?: Check;
  tcp?: TcpCheck;
};

/**
 * The servers and their ports as one table (the board's .nd-pt): the protocol, the name with its port, the check's word and when and
 * from where it ran, and a switch that keeps the server in subscriptions or out of them (`subs`, for the Direct lane). A port the
 * check found with no profile on it has its row, a profile whose port was never checked has its row.
 */
export function Ports({
  data,
  run,
  subs,
}: {
  data: Plain<GetNodeResponse>;
  run: PortCheck;
  subs?: boolean;
}) {
  const t = useT();
  const fmt = useFmt();
  const node = data.node!;
  const linked = agentLinked(node.status);
  const owner = useIsOwner();
  const inSubs = useInSubscriptions(node.id, node.name);

  const { udp: rows, tcp, profilesOn } = portRows(data);
  const stored = data.portChecks ?? [];
  const renewal = renewalOf(tcp);
  // a loss on the way names the region of the sender that lost the packets: its country is in the fleet list
  const fleet = useQuery({
    ...nodesQuery,
    enabled: rows.some((c) => c.blame === "path"),
  }).data?.nodes;
  const regionOf = (sender: string) =>
    udpRegion(fleet?.find((n) => n.name === sender)?.countryCode ?? "");
  // a sender whose way here lets some ports through whole and drops the rest: a filter (a hoster's DDoS protection), not this node
  const filters = udpFilters(rows);
  const filtered = new Set(filters.map((f) => f.sender));
  // both senders got only UDP 443 through: the node's own hoster filters, which says more than any one sender's caption
  const hosterFilter = (data.udpFilteredUnix ?? 0) > 0;
  // the protocols whose panel check runs through a port (protocolOkUnix): its enabled UDP profiles
  const protocolsOn = (port: number) =>
    [
      ...new Set(
        data.inbounds
          .filter(
            (i) =>
              i.port === port &&
              i.protocol !== "vless" &&
              i.state !== InboundState.DISABLED,
          )
          .map((i) => protocolName(i.protocol)),
      ),
    ].join(", ");

  const result = run.data;
  // a run whose TCP part could not probe anything stores nothing, so its reason would otherwise go unsaid
  const tcpRun = result && !result.errorCode ? (result.tcp ?? []) : [];
  const tcpReason =
    tcpRun.length > 0 && tcpRun.every((r) => !r.verdict)
      ? (tcpRun.find((r) => r.reason)?.reason ?? "")
      : "";
  const why = run.error
    ? errorText(run.error, t)
    : result?.errorCode
      ? reasonText(t, result.errorCode, { best: bestDelivered(result.ports) })
      : tcpReason
        ? reasonText(t, tcpReason)
        : "";
  // the counts of a run that could not tell (inconclusive) or stayed on one host: what did arrive
  const counts =
    result?.errorCode &&
    (result.errorCode === "inconclusive" || result.errorCode === "same_host")
      ? result.ports.map((p) => `${p.port}: ${p.got}/${p.sent}`).join(" · ")
      : "";

  // the table: every UDP port (of a profile or checked), then every TCP one; a profile on a port gets the row, two on one port get two
  const own = data.inbounds.filter((i) => !i.cdn);
  const table: PortRow[] = [];
  for (const c of rows) {
    const ins = own.filter((i) => i.port === c.port && i.protocol !== "vless");
    if (ins.length === 0)
      table.push({ key: `udp/${c.port}`, net: "udp", port: c.port, check: c });
    for (const i of ins)
      table.push({
        key: `udp/${c.port}/${i.id}`,
        net: "udp",
        port: c.port,
        inbound: i,
        check: c,
      });
  }
  for (const i of own.filter(
    (x) =>
      x.protocol !== "vless" &&
      x.port > 0 &&
      !rows.some((c) => c.port === x.port),
  ))
    table.push({
      key: `udp/${i.port}/${i.id}`,
      net: "udp",
      port: i.port,
      inbound: i,
    });
  for (const c of tcp) {
    const ins = own.filter((i) => i.port === c.port && i.protocol === "vless");
    if (ins.length === 0)
      table.push({ key: `tcp/${c.port}`, net: "tcp", port: c.port, tcp: c });
    for (const i of ins)
      table.push({
        key: `tcp/${c.port}/${i.id}`,
        net: "tcp",
        port: c.port,
        inbound: i,
        tcp: c,
      });
  }
  for (const i of own.filter(
    (x) =>
      x.protocol === "vless" &&
      x.port > 0 &&
      !tcp.some((c) => c.port === x.port),
  ))
    table.push({
      key: `tcp/${i.port}/${i.id}`,
      net: "tcp",
      port: i.port,
      inbound: i,
    });
  const udpRows = table.filter((r) => r.net === "udp");
  const tcpRows = table.filter((r) => r.net === "tcp");
  // two servers on one port share the check: its words stand on the first row
  const seen = new Set<string>();
  const firstOf = (r: PortRow) => {
    const k = `${r.net}/${r.port}`;
    if (seen.has(k)) return false;
    seen.add(k);
    return true;
  };

  const line = (r: PortRow) => {
    const lead = firstOf(r);
    const c = r.check;
    const tc = r.tcp;
    const pill = c
      ? verdictPill(t, c)
      : tc
        ? tcpPill(t, tc.verdict)
        : { kind: "off" as StatusKind, label: t("ports.unchecked") };
    const profiles = c ? profilesOn(c.port) : [];
    const checked = (c?.checkedUnix ?? tc?.checkedUnix ?? 0) > 0;
    // the test datagrams were lost but the protocol works through the port: the counts stay quiet, a line says why
    const blocked = c ? testBlocked(c) : false;
    // checked again from a second sender: both runs take the place of "N of M arrived · from X"
    const runs = c ? portRuns(c) : undefined;
    const detail = c
      ? [
          !runs && c.sent > 0
            ? t("ports.row.arrived", { got: c.got, sent: c.sent })
            : "",
          checked ? fmt.ago(c.checkedUnix) : "",
          !runs && checked && c.sender
            ? t("ports.row.from", { sender: senderLabel(t, c.sender) })
            : "",
          !checked && c.reason ? reasonShort(t, c.reason) : "",
        ]
          .filter(Boolean)
          .join(" · ")
      : tc
        ? [
            !tc.verdict && tc.reason ? reasonShort(t, tc.reason) : "",
            tc.checkedUnix > 0 ? fmt.ago(tc.checkedUnix) : "",
            tc.checkedUnix > 0 && tc.sender
              ? t("ports.row.from", { sender: senderLabel(t, tc.sender) })
              : "",
          ]
            .filter(Boolean)
            .join(" · ")
        : "";
    // port 80 belongs to no profile: it is the certificates'; a site is the Hysteria2 profile's
    const owned = tc
      ? tc.role === "acme"
        ? t("ports.tcp.role.acme")
        : [tc.role === "site" ? t("ports.tcp.role.site") : "", tc.profile]
            .filter(Boolean)
            .join(" · ")
      : "";
    const i = r.inbound;
    const name =
      i?.profileName ??
      (profiles.length > 0
        ? profiles.join(", ")
        : owned || t("ports.row.noProfile"));
    const tone = tones[pill.kind];
    return (
      <li
        key={r.key}
        className={cx("wy-po", i && !i.directInSubscriptions && "off")}
        data-testid="port-row"
      >
        <span className="tg">
          {i ? (
            <ProtocolTag protocol={i.protocol} compact />
          ) : (
            <span className="inline-grid h-7 w-7 place-items-center rounded-ctl bg-step text-sec">
              <Icon name="lock" size={14} />
            </span>
          )}
        </span>
        <div className="nm">
          <b>
            {!i && profiles.length > 0 ? (
              <Link
                to="/nodes/$id"
                params={{ id: node.id }}
                search={{ tab: "profiles" }}
                className="hover:text-accent-text hover:underline"
              >
                {name}
              </Link>
            ) : (
              name
            )}
          </b>
          <span className="mono">
            {r.net}/{r.port}
          </span>
        </div>
        <div className="ck">
          {lead && (
            <>
              <span className={cx("wy-gs", tone)}>
                <i
                  className={cx(
                    "wy-dot",
                    tone === "warn" || tone === "crit" || tone === "idle"
                      ? tone
                      : "",
                  )}
                  aria-hidden
                />
                {pill.label}
              </span>
              <span className="dt">
                {runs && (
                  <Runs runs={runs} filtered={filtered} quiet={blocked} />
                )}
                {runs && detail && " · "}
                {/* beside the runs, the age is one unbroken piece too: the line wraps only at a " · " */}
                {runs ? (
                  <span className="whitespace-nowrap">{detail}</span>
                ) : (
                  detail
                )}
                {/* a port that lost packets once and is clean now is still avoided for 30 days: say when */}
                {c && c.verdict === "ok" && c.badUnix > 0 && (
                  <span className="block text-warn-text">
                    {t("ports.row.cleanNow", { when: fmt.ago(c.badUnix) })}
                  </span>
                )}
                {c && blocked ? (
                  <VerdictLine
                    id="blocked"
                    kind="ok"
                    text={t("ports.blame.blocked", {
                      protocols: protocolsOn(c.port),
                    })}
                  />
                ) : (
                  c &&
                  runs && (
                    <Blame
                      blame={c.blame}
                      runs={runs}
                      kind={pill.kind}
                      regionOf={regionOf}
                      filtered={filtered}
                    />
                  )
                )}
              </span>
            </>
          )}
        </div>
        {subs && i ? (
          <span className="sw">
            <Switch
              checked={i.directInSubscriptions}
              disabled={!owner || inSubs.busy}
              onCheckedChange={(on) => void inSubs.toggle(i, on)}
              aria-label={t("ways.subs.switch", { name: i.profileName })}
            />
          </span>
        ) : (
          <span className="na">—</span>
        )}
      </li>
    );
  };

  return (
    <div data-testid="ports" className="wy-pt">
      {run.isPending && (
        <div className="px-6 pb-3 max-[720px]:px-4">
          <Progress label={t("ports.checking")} />
        </div>
      )}
      {why && !run.isPending && (
        <Notice className="wy-nt items-start">
          <span className="flex flex-col gap-1">
            {why}
            {counts && (
              <span className="font-mono text-[11px] text-muted">{counts}</span>
            )}
          </span>
        </Notice>
      )}
      {table.length > 0 && (
        <div role="presentation">
          <div className="wy-po wy-ph" aria-hidden>
            <span />
            <span>{t("wy.col.server")}</span>
            <span>{t("wy.col.check")}</span>
            <span>{subs ? t("wy.col.subs") : ""}</span>
          </div>
          <ul>
            {udpRows.map(line)}
            {/* under the UDP rows it explains: checks from that sender (or from any of ours, the hoster's filter) cannot pass, the node is fine */}
            {hosterFilter ? (
              <Caption id="udp-filtered">{t("ports.filter.hoster")}</Caption>
            ) : (
              filters.map((f) => (
                <Caption key={f.sender} id="udp-filter">
                  {t("ports.filter", {
                    sender: senderLabel(t, f.sender),
                    open: f.open.join(", "),
                    node: node.name,
                  })}
                </Caption>
              ))
            )}
            {tcpRows.map(line)}
          </ul>
        </div>
      )}
      <p className="wy-hint">
        {run.isPending
          ? t("ports.checkingNote")
          : !linked
            ? t("ports.offline")
            : stored.length + tcp.length === 0
              ? t("ports.never")
              : t("ports.intro")}
      </p>
      {renewal && (
        <div className="wy-hint !mt-0">
          {renewal.kind === "risk" ? (
            <Notice>{renewalText(t, renewal)}</Notice>
          ) : (
            <RenewalLine text={renewalText(t, renewal)} />
          )}
        </div>
      )}
      {subs && <p className="wy-hint !mt-0">{t("wy.hint.subs")}</p>}
      {inSubs.error && !inSubs.asking && (
        <Notice tone="danger" className="wy-nt">
          {inSubs.error}
        </Notice>
      )}
      {inSubs.dialog}
    </div>
  );
}

/**
 * Both runs of a port checked from two senders, "from minsk1 236/300 · from de1 299/300": the one that lost packets in its tone.
 * A sender behind a filter (`filtered`) is amber even where it got nothing: its way is filtered, the port is not broken.
 * `quiet` (the test is blocked, the protocol works): every run in the muted text of the counts.
 */
function Runs({
  runs,
  filtered,
  quiet,
}: {
  runs: readonly PortRun[];
  filtered: ReadonlySet<string>;
  quiet?: boolean;
}) {
  const t = useT();
  return (
    <>
      {runs.map((r, i) => {
        const loss = quiet
          ? ""
          : r.loss && filtered.has(r.sender)
            ? "warn"
            : r.loss;
        return (
          <Fragment key={i}>
            {i > 0 && " · "}
            <span
              className={cx(
                "whitespace-nowrap",
                loss && kindTextClass[loss],
                loss && "font-semibold",
              )}
            >
              {t("ports.row.run", {
                sender: senderLabel(t, r.sender),
                count: `${r.got}/${r.sent}`,
              })}
            </span>
          </Fragment>
        );
      })}
    </>
  );
}

/**
 * Whose the loss is, as a verdict line under the runs: "path" (one sender lost, the other got through) clears the port and
 * names the sender and its region, or says its way is filtered; "node" (both lost) puts it on the port. Nothing when both
 * runs were clean.
 */
function Blame({
  blame,
  runs,
  kind,
  regionOf,
  filtered,
}: {
  blame: string;
  runs: readonly PortRun[];
  kind: StatusKind;
  regionOf: (sender: string) => string;
  filtered: ReadonlySet<string>;
}) {
  const t = useT();
  let line: { kind: StatusKind; text: string } | null = null;
  if (blame === "path") {
    const lossy = runs.find((r) => r.loss) ?? runs[0]!;
    const sender = senderLabel(t, lossy.sender);
    const region = regionWord(t, regionOf(lossy.sender));
    const text = filtered.has(lossy.sender)
      ? t("ports.blame.filtered", { sender })
      : region
        ? t("ports.blame.path", { sender, region })
        : t("ports.blame.pathPlain", { sender });
    line = { kind: "ok", text };
  } else if (blame === "node") line = { kind, text: t("ports.blame.node") };
  if (!line) return null;
  return <VerdictLine id={blame} kind={line.kind} text={line.text} />;
}

/** The verdict line under a row's counts: a tick when the port lets people in, else the warning mark, in the line's tone. */
function VerdictLine({
  id,
  kind,
  text,
}: {
  id: string;
  kind: StatusKind;
  text: string;
}) {
  return (
    <span
      data-blame={id}
      className={`tone-${kind} tone-text mt-1 flex items-start gap-1.5 font-semibold`}
    >
      <Icon
        name={kind === "ok" ? "check" : "warn"}
        size={12}
        aria-hidden
        className="mt-0.5 flex-none"
      />
      {text}
    </span>
  );
}

/** A caption under the UDP rows about the way to the node (a filter): the shield in the warning tone, the words quiet. */
function Caption({ id, children }: { id: string; children: ReactNode }) {
  return (
    <li data-testid={id} className="wy-cap flex items-start gap-2 text-pretty">
      <Icon
        name="shield"
        size={12}
        aria-hidden
        className="tone-warn tone-text mt-0.5 flex-none"
      />
      {children}
    </li>
  );
}

/** The renewal line when all is well: quiet, with the green dot. */
function RenewalLine({ text }: { text: string }) {
  return (
    <p className="flex items-start gap-2 text-xs leading-snug text-pretty text-muted">
      <span className="mt-[5px] flex">
        <StatusDot kind="ok" />
      </span>
      {text}
    </p>
  );
}
