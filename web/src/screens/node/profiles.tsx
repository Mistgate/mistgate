import { Scramble } from "@/components/fx/scramble-react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useId, useState, type ReactNode } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { IconButton } from "@/components/ui/icon-button";
import { ProtocolTag } from "@/components/ui/protocol-tag";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { kindTextClass, StatusDot, type StatusKind } from "@/components/ui/status";
import { Switch } from "@/components/ui/switch";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { InboundState, NodeStatus, type Inbound } from "@/gen/mistgate/admin/v1/common_pb";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import type { Plain } from "@/lib/plain";
import { nodes as nodesApi, profiles as profilesApi } from "@/lib/api";
import { inboundConnParts, nodeConnectionsQuery, type InboundConn } from "@/lib/connections";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { errorText } from "@/lib/errors";
import { useFmt, type Fmt } from "@/lib/format";
import { cellLook, checksQuery, doctorQuery } from "@/lib/health";
import { inboundErrorKind, inboundErrorText, nameFixes } from "@/lib/inbound-error";
import { portNotes, withNotes } from "@/lib/port-check";
import { agentLinked } from "@/lib/node-status";
import { protocolName } from "@/lib/series";
import { useNow } from "@/lib/time";
import { meQuery } from "@/lib/session";
import { useIsOwner } from "@/lib/updates";
import { networkOf } from "@/lib/ways-graph";
import { profileListQuery } from "@/screens/users/rpc";
import { PartsLine } from "@/screens/users/user-connection";
import { AddInbound } from "./add-inbound";
import { AwgStatusLine } from "./awg-status";
import { DecoyNotice, portOk, sniPlaceholder, useInboundCheck, useInboundRefresh, WarpWarnings } from "./inbound-check";
import { LossyNotice, lossyRefusal } from "./port-lossy";
import { SniScanCard, sniDemo } from "./sni-scan";
import { tlsHint, useRealityTargetEdit } from "./reality-target";
import { ShareFix, ShareNotes } from "./share-fix";
import { SharedPortCards } from "./shared-port";
import { isShareCode } from "@/lib/tcpshare";
import { tcpPill, verdictPill } from "./ports";
import { Fresh } from "@/components/fx/fresh";
import { alsoRemovesText, useRelaysOf } from "./relays";
import { nodeTicks } from "@/screens/overview-checks";

export const states: Record<InboundState, { kind: StatusKind; word: MessageKey }> = {
  [InboundState.UNSPECIFIED]: { kind: "off", word: "node.inbound.off" },
  [InboundState.PENDING]: { kind: "busy", word: "node.inbound.pending" },
  [InboundState.ACTIVE]: { kind: "ok", word: "node.inbound.active" },
  [InboundState.FAILED]: { kind: "bad", word: "node.inbound.failed" },
  [InboundState.DISABLED]: { kind: "off", word: "node.inbound.off" },
};

type Cell = { label: string; value: string; mono?: boolean; title?: string; tone?: "warn" | "bad"; dot?: { kind: StatusKind; label: string } };

/** The certificate cell: its date, amber under 14 days left and red under 3; "none" for a profile that is not running. */
function certCell(t: T, fmt: Fmt, i: Plain<Inbound>, now: number): Cell {
  const label = t("node.profiles.cert");
  if (i.certNotAfterUnix) {
    const left = Math.ceil((i.certNotAfterUnix - now) / 86400);
    if (left < 14) return { label, value: t("node.profiles.certLeft", { date: fmt.date(i.certNotAfterUnix), n: Math.max(0, left) }), tone: left < 3 ? "bad" : "warn" };
    return { label, value: t("node.profiles.certUntil", { date: fmt.date(i.certNotAfterUnix) }) };
  }
  if (i.certPinSha256) return { label, value: t("node.profiles.pinned") };
  return { label, value: i.state === InboundState.ACTIVE ? "" : t("node.profiles.certNone") };
}

/**
 * The facts a row shows: port and egress for every protocol, then a TLS protocol's domain and certificate, or a
 * WireGuard-family one's devices and backend (the backend's version is its tooltip). Always four, so the columns of
 * every row line up. Anything that is not AmneziaWG reads as TLS.
 */
function cells(t: T, fmt: Fmt, i: Plain<Inbound>, now: number, dot?: Cell["dot"]): Cell[] {
  const port: Cell = { label: t("node.profiles.port"), value: i.port ? `${networkOf(i.protocol) ?? "udp"}/${i.port}` : "", mono: true, dot };
  const egress: Cell = { label: t("node.profiles.egress"), value: i.egress ? t(i.egress === "warp" ? "node.profiles.egress.warp" : "node.profiles.egress.direct") : "" };
  if (i.protocol === "awg") {
    const a = i.awg;
    return [
      port,
      egress,
      { label: t("node.profiles.devices"), value: a ? t("node.profiles.devicesOnline", { n: a.peers, online: a.peersOnline }) : "" },
      { label: t("node.profiles.backend"), value: a?.backend ?? "", title: a?.backendVersion },
    ];
  }
  return [port, egress, { label: t("node.profiles.tls"), value: i.tlsServerName, mono: true }, certCell(t, fmt, i, now)];
}

const toneText = { warn: "text-warn-text", bad: "text-danger-text" } as const;

/** The last port check of a profile's port, as a dot: its verdict on hover and to a screen reader, the check a click away. */
function PortDot({ nodeId, dot }: { nodeId: string; dot: NonNullable<Cell["dot"]> }) {
  const t = useT();
  const label = t("ways.portDot", { verdict: dot.label });
  return (
    <Link to="/nodes/$id" params={{ id: nodeId }} search={{ tab: "ways", way: "direct" }} aria-label={label} title={label} className="flex size-4 flex-none items-center justify-center rounded-full hover:bg-surface-2">
      <StatusDot kind={dot.kind} />
    </Link>
  );
}

/** The process that holds a profile's port, by inbound id, as the node's doctor saw it ("caddy(812)" -> "caddy"). */
export function usePortHolders(nodeId: string, enabled = true): Record<string, string> {
  const q = useQuery({ ...doctorQuery(nodeId), enabled });
  const out: Record<string, string> = {};
  for (const item of q.data?.nodes.find((n) => n.nodeId === nodeId)?.items ?? []) {
    const { inbound_id: id, process } = item.params;
    if (item.id === "port_conflicts" && item.status === DoctorStatus.FAIL && id && process) out[id] = process.replace(/\(\d+\)$/, "");
  }
  return out;
}

/** Why a profile did not start, in words, with the agent's own text under it. Also on the profile page ("Where it runs"). */
export function InboundErrorNote({ inbound, process }: { inbound: Pick<Plain<Inbound>, "lastError" | "port" | "protocol">; process?: string }) {
  const t = useT();
  return (
    <div className="flex flex-col gap-0.5 rounded-field border border-[color-mix(in_oklch,var(--danger)_45%,var(--border))] bg-danger-soft px-3 py-2">
      <p className="text-[13px] leading-snug font-semibold text-danger-text">{inboundErrorText(t, inbound.lastError, { port: inbound.port, protocol: inbound.protocol, process })}</p>
      <p className="font-mono text-[11px] leading-snug break-words text-muted">{inbound.lastError}</p>
    </div>
  );
}

type Editing = { inbound: Plain<Inbound>; focus?: "port" | "sni" };

/** The node's inbounds: a profile deployed here. Add, edit the port / domain / switch, restart one, remove. */
export function ProfilesTab({ data, addProfile, onAddClosed }: { data: Plain<GetNodeResponse>; addProfile?: string; onAddClosed?: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const now = Math.floor(useNow(60_000) / 1000);
  // addProfile: the profile page sent us here to add that profile: the dialog opens with it chosen
  const owner = useIsOwner(); // policy.go: a server on a node (CreateInbound, UpdateInbound, DeleteInbound) is the owner's
  const canRestart = useQuery(meQuery).data?.admin?.role === Role.HELPER || owner; // RestartInbounds is the helper's too
  const edition = useEdition();
  const [adding, setAddingOpen] = useState(Boolean(addProfile));
  const setAdding = (open: boolean) => {
    setAddingOpen(open);
    if (!open) onAddClosed?.();
  };
  // /dev-kit: the edit window of a server opens by itself (sni=edit)
  const [editing, setEditing] = useState<Editing | null>(() => {
    const inbound = sniDemo.current?.edit ? data.inbounds.find((i) => i.id === sniDemo.current?.edit) : undefined;
    return inbound ? { inbound } : null;
  });
  const [removing, setRemoving] = useState<Plain<Inbound> | null>(null);
  const [restarting, setRestarting] = useState<Plain<Inbound> | null>(null);
  const node = data.node!;
  const linked = agentLinked(node.status);
  const portTrouble = data.inbounds.some((i) => i.lastError && inboundErrorKind(i.lastError) === "port");
  const holders = usePortHolders(node.id, portTrouble);
  // what the node counted of connection attempts per profile (lib/connections.ts); nothing while it cannot be read
  const conn = useQuery({ ...nodeConnectionsQuery(node.id), enabled: data.inbounds.length > 0 && node.status !== NodeStatus.RETIRED }).data;
  const connOf = new Map((conn?.inbounds ?? []).map((x) => [x.inboundId, x]));
  // relays are the owner's (RelayService): removing a server says which relays go with it

  // the checks as a client sees them: a day of hourly cells and the time of each profile (the same round the Overview reads)
  const checks = useQuery({ ...checksQuery(node.id), enabled: data.inbounds.length > 0 && node.status !== NodeStatus.RETIRED });
  const checkRow = checks.data?.rows.find((r) => r.nodeId === node.id);
  const dead = node.status === NodeStatus.DOWN;
  // "Sites near the node": the node scans its own subnet (ProbeService, the VPS panel only) for the target of a REALITY server, the owner's call
  const sniCard = owner && !edition.off("sni_scan") && node.status !== NodeStatus.RETIRED && data.inbounds.some((i) => i.protocol === "vless");

  const add = owner ? (
    <Button variant="primary" size="lg" onClick={() => setAdding(true)}>
      <Icon name="plus" size={16} />
      {t("node.profiles.add")}
    </Button>
  ) : undefined;

  return (
    <div className="flex flex-col gap-4">
      {data.inbounds.length === 0 ? (
        <section className="flex flex-col items-center gap-2 rounded-card border border-line bg-surface px-6 py-12 text-center">
          <span aria-hidden className="mb-1 grid size-12 place-items-center rounded-[14px] bg-step text-sec">
            <Icon name="sliders" size={24} />
          </span>
          <b className="text-base font-medium">{t("node.profiles.none", { name: node.name })}</b>
          <span className="max-w-[420px] text-[13px] text-sec">{t(owner ? "node.profiles.noneBody" : "node.profiles.ownerOnly")}</span>
          {add && <div className="mt-3">{add}</div>}
        </section>
      ) : (
        <>
          <div className="mt-2 flex items-center gap-4 max-[720px]:flex-wrap">
            <h2 className="me-auto text-xl leading-7 font-medium tracking-[-0.01em]">{t.n("node.profiles.count", data.inbounds.length)}</h2>
            <span className="text-[13px] text-muted max-[720px]:hidden">{t("np.pf.every")}</span>
            {add}
          </div>
          {conn && !conn.supported && linked && (
            <p data-testid="conn-unsupported" className="text-xs leading-normal text-pretty text-muted">
              {t("conn.node.unsupported")}
            </p>
          )}
          {!owner && <Notice tone="quiet">{t("node.profiles.ownerOnly")}</Notice>}
        </>
      )}

      <SharedPortCards data={data} />

      <div className="flex flex-col gap-3">
        {data.inbounds.map((i) => {
          const s = states[i.state] ?? states[InboundState.UNSPECIFIED];
          // the last port check of this profile's port: a dot that leads to it in the ways in
          const tcp = networkOf(i.protocol) === "tcp";
          const checked = tcp ? data.tcpChecks?.find((c) => c.port === i.port && c.verdict) : data.portChecks?.find((c) => c.port === i.port && c.verdict);
          const cs = cells(t, fmt, i, now, checked && (tcp ? tcpPill(t, checked.verdict) : verdictPill(t, checked as never)));
          const kind = i.lastError ? inboundErrorKind(i.lastError) : null;
          const cell = checkRow?.cells.find((c) => c.deployed && c.inboundId === i.id);
          const look = cell && checkRow ? cellLook(t, fmt, cell, checkRow.nodeStatus) : undefined;
          const ticks = nodeTicks(cell ? { cells: [cell] } : undefined);
          const st = cell?.stats24h;
          const okN = cell?.history.reduce((n, h) => n + h.ok, 0) ?? 0;
          const failN = cell?.history.reduce((n, h) => n + h.failed, 0) ?? 0;
          const stats: { v: string; k: string; cls?: string }[] = cell
            ? [
                { v: fmt.num(okN, 0), k: t("np.pf.ok") },
                { v: fmt.num(failN, 0), k: t("np.pf.failed"), cls: failN > 0 ? "text-danger-text" : undefined },
                ...(st && st.responseP50Ms > 0 ? [{ v: `${st.responseP50Ms} ${t("hl.ms")}`, k: t("np.pf.response") }] : []),
                ...(st && st.connectP50Ms > 0 ? [{ v: `${st.connectP50Ms} ${t("hl.ms")}`, k: t("np.pf.connect") }] : []),
              ]
            : [];
          const fix: ReactNode = !owner ? null : kind === "port" ? (
              <Button variant="primary" size="md" onClick={() => setEditing({ inbound: i, focus: "port" })}>
                {t("node.profiles.changePort")}
              </Button>
            ) : nameFixes(kind) && i.protocol !== "awg" ? (
              <Button variant="primary" size="md" onClick={() => setEditing({ inbound: i, focus: "sni" })}>
                {t("node.profiles.setDomain")}
              </Button>
            ) : kind === "share" ? (
              <Link to="/updates" className={buttonClass("primary", "md")}>
                {t("share.openUpdates")}
              </Link>
            ) : kind === "warp" ? (
              <Link to="/nodes/$id" params={{ id: node.id }} search={{ tab: "exit" }} hash="warp" className={buttonClass("primary", "md")}>
                {t("warp.open")}
              </Link>
            ) : null;
          return (
            <Fresh key={i.id} kind="inbound" id={i.id} className="flex min-w-0 flex-col rounded-card border border-line bg-surface transition-colors duration-200 ease-out hover:border-[color-mix(in_srgb,var(--text)_18%,transparent)]">
              <div className="grid grid-cols-[28px_minmax(160px,220px)_minmax(120px,170px)_minmax(0,1fr)_88px_auto] items-center gap-4 py-4 ps-6 pe-4 max-[1200px]:grid-cols-[28px_minmax(0,1fr)_auto_auto] max-[1200px]:gap-y-3 max-[720px]:gap-3 max-[720px]:px-4 max-[720px]:py-3.5">
                <ProtocolTag protocol={i.protocol} compact className="w-7 justify-center px-0" />
                <div className="flex min-w-0 flex-col">
                  <Link to="/profiles/$id" params={{ id: i.profileId }} className="truncate font-medium hover:text-accent-text hover:underline">
                    {i.profileName}
                  </Link>
                  <span className="font-mono text-[11px] font-normal text-muted">{i.port ? `${networkOf(i.protocol) ?? "udp"}/${i.port}` : protocolName(i.protocol)}</span>
                </div>
                <span className={cx("inline-flex items-center gap-2 text-[13px] whitespace-nowrap", kindTextClass[s.kind])}>
                  <StatusDot kind={s.kind} />
                  {t(s.word)}
                </span>
                <span role="img" aria-label={t("ov.tick.aria")} className={cx("nd-tk max-[1200px]:order-5 max-[1200px]:col-[1/4]", dead && "opacity-40")}>
                  {ticks.map((k, n) => (
                    <i
                      key={n}
                      className={k.state === "ok" ? undefined : k.state}
                      data-t={k.from ? `${fmt.clock(k.from)}–${fmt.clock(k.to)}` : t("ov.tick.none")}
                      data-s={k.state === "none" ? (k.from ? t("ov.tick.none") : "") : k.state === "ok" ? t("ov.tick.ok", { ms: k.ms || "–" }) : k.state === "warn" ? t("ov.tick.part", { failed: k.failed, total: k.ok + k.failed }) : t("ov.tick.fail")}
                    />
                  ))}
                </span>
                <span className={cx("text-end font-mono text-[13px] font-medium whitespace-nowrap tabular-nums max-[1200px]:order-6", look?.kind === "warn" && "text-warn-text", look?.kind === "bad" && "text-danger-text")}>{look && cell && look.label ? <Scramble value={look.label} once /> : ""}</span>
                <div className="flex items-center gap-1.5 max-[1200px]:order-7 max-[1200px]:col-[1/-1] max-[720px]:[&>*]:flex-1">
                  {fix}
                  {owner && (
                    <Button variant="secondary" size="md" onClick={() => setEditing({ inbound: i })}>
                      {t("common.edit")}
                    </Button>
                  )}
                  {canRestart && linked && i.state !== InboundState.DISABLED && (
                    <Button variant="secondary" size="md" onClick={() => setRestarting(i)}>
                      {t("node.profiles.restart")}
                    </Button>
                  )}
                  {owner && (
                    <IconButton aria-label={t("node.profiles.remove")} title={t("node.profiles.remove")} className="hover:text-danger-text" onClick={() => setRemoving(i)}>
                      <Icon name="trash" size={16} />
                    </IconButton>
                  )}
                </div>
              </div>
              {(i.awg || conn?.supported || i.lastError) && (
                <div className="flex flex-col gap-2.5 px-6 pb-4 max-[720px]:px-4">
                  {i.awg && <AwgStatusLine awg={i.awg} />}
                  {conn?.supported && <ConnLine x={connOf.get(i.id)} />}
                  {i.lastError && <InboundErrorNote inbound={i} process={holders[i.id]} />}
                </div>
              )}
              <dl className="m-0 grid grid-cols-4 gap-4 border-t border-line px-6 py-3.5 max-[1200px]:grid-cols-2 max-[720px]:px-4">
                {cs.map((c) => (
                  <div key={c.label} className="flex min-w-0 flex-col gap-0.5">
                    <dt className="text-xs text-muted">{c.label}</dt>
                    <dd title={c.title} className={cx("m-0 flex min-w-0 items-center gap-1.5 truncate", c.mono && "font-mono text-[13px] font-normal", c.tone && toneText[c.tone])}>
                      {c.value || "—"}
                      {c.dot && <PortDot nodeId={node.id} dot={c.dot} />}
                    </dd>
                  </div>
                ))}
              </dl>
              {stats.length > 0 && (
                <div className={cx("flex flex-wrap items-center gap-x-5 gap-y-1.5 rounded-b-[15px] border-t border-line bg-step px-6 pt-3 pb-3.5 max-[720px]:px-4", dead && "opacity-40")}>
                  <span className="text-[13px] text-muted">{t("np.pf.day")}</span>
                  {stats.map((x) => (
                    <span key={x.k} className="inline-flex items-baseline gap-1.5 text-[13px] text-sec">
                      <b className={cx("font-mono text-[13px] font-medium text-fg", x.cls)}>
                        <Scramble value={x.v} once />
                      </b>
                      {x.k}
                    </span>
                  ))}
                </div>
              )}
            </Fresh>
          );
        })}
      </div>

      {sniCard && <SniScanCard nodeId={node.id} nodeName={node.name} linked={linked} servers={data.inbounds.filter((i) => i.protocol === "vless" && !!i.realityTarget && i.state !== InboundState.DISABLED)} />}

      {owner && <AddInbound open={adding} onOpenChange={setAdding} data={data} initialProfile={addProfile} />}
      {editing && (
        <EditInbound key={editing.inbound.id} inbound={editing.inbound} focus={editing.focus} nodeId={node.id} nodeAddress={node.address} onClose={() => setEditing(null)} />
      )}
      {removing && <RemoveInbound key={removing.id} inbound={removing} nodeId={node.id} nodeName={node.name} owner={owner} onClose={() => setRemoving(null)} />}
      {restarting && <RestartInbound key={restarting.id} inbound={restarting} nodeId={node.id} nodeName={node.name} onClose={() => setRestarting(null)} />}
    </div>
  );
}

function EditInbound({ inbound, focus, nodeId, nodeAddress, onClose }: { inbound: Plain<Inbound>; focus?: "port" | "sni"; nodeId: string; nodeAddress: string; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const refresh = useInboundRefresh(nodeId);
  const isAwg = inbound.protocol === "awg";
  // the site this node shows instead of its profile's target (REALITY): its field, and its name is no domain of the node
  const target = useRealityTargetEdit(inbound, onClose);
  // VLESS has a name of its own to set only where it has a certificate: the node's own site behind REALITY, or TLS (the
  // server resolved one, or the admin set one, or it could not for want of a domain); REALITY with another site has none
  const named = (inbound.protocol !== "vless" || !!inbound.tlsServerName || !!inbound.tlsServerNameOverride || (!!inbound.lastError && nameFixes(inboundErrorKind(inbound.lastError)))) && !target.own;
  const was = { port: inbound.port ? String(inbound.port) : "", sni: inbound.tlsServerName, enabled: inbound.state !== InboundState.DISABLED };
  const [port, setPort] = useState(was.port);
  const [sni, setSni] = useState(was.sni);
  const [enabled, setEnabled] = useState(was.enabled);
  // the port "Change port" put in, and why (another profile holds the old one, or a program outside the panel does)
  const [moved, setMoved] = useState<{ from: string; to: string; profile?: string } | null>(null);
  const sniId = useId();

  const touched = { port: port !== was.port, sni: sni !== was.sni, enabled: enabled !== was.enabled };
  const check = useInboundCheck(
    portOk(port)
      ? { kind: "update", inboundId: inbound.id, port: touched.port ? port : undefined, sni: touched.sni ? sni.trim() : undefined, enabled: touched.enabled ? enabled : undefined }
      : null,
  );
  // "Change port": the field opens on a port that is free on this node (the server's picker), once
  const free = check.state === "ok" ? check.freePort : check.state === "refused" && check.code === "port_taken" ? Number(check.vars.free) : 0;
  if (focus === "port" && !moved && free > 0 && !touched.port) {
    setMoved({ from: was.port, to: String(free), profile: check.state === "refused" ? check.vars.profile : undefined });
    setPort(String(free));
  }

  const save = useMutation({
    // only what was touched goes out: an untouched field keeps whatever the server has (profile default or override)
    mutationFn: (allowLossy: boolean) =>
      profilesApi.updateInbound({
        inboundId: inbound.id,
        portOverride: touched.port ? (port ? Number(port) : 0) : undefined,
        tlsServerNameOverride: touched.sni ? sni.trim() : undefined,
        enabled: touched.enabled ? enabled : undefined,
        realityTargetOverride: target.save,
        ...(allowLossy && { allowLossyPort: true }),
      }),
    onSuccess: (r) => {
      onClose();
      // the UDP check could not run (port_unchecked), or the port loses packets and was kept (port_lossy)
      const notes = portNotes(t, r?.warnings);
      toast(withNotes(t("common.saved"), notes), notes.length > 0 ? { ms: 8000 } : undefined);
      refresh();
    },
  });

  const refused = check.state === "refused" ? check : null;
  const share = refused && isShareCode(refused.code) ? { code: refused.code, vars: refused.vars } : null;
  // the stored checks (before the click) or the real run (the click) found the port losing packets
  const lossy = refused?.code === "port_lossy" ? refused.vars : lossyRefusal(save.error);
  const fieldError = (codes: string[]) => (refused && codes.includes(refused.code) ? t(`err.${refused.code}`, refused.vars) : undefined);
  const portError = !portOk(port) ? t("node.profiles.portError") : fieldError(["port_taken", "port_in_hop", "hop_taken"]);
  const sniError = fieldError(["sni_needs_domain", "sni_invalid", "acme_needs_domain", "reality_needs_domain", "mtproto_needs_domain"]);
  const effective = check.state === "ok" ? check.inbound : undefined;
  const dirty = touched.port || touched.sni || touched.enabled || target.touched;
  // a refusal no field above words (the Save button is off for it): a notice at the foot says why, so the button is never off in silence
  const unexplained = refused && !share && !lossy && !portError && !sniError ? refused : null;
  return (
    <Modal open onOpenChange={(o) => !o && onClose()} title={t("node.profiles.editTitle", { name: inbound.profileName })} description={t("node.profiles.editBody")}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (dirty && portOk(port) && !refused && !target.blocked) save.mutate(false);
        }}
        className="flex flex-col gap-3.5"
      >
        <TextField
          label={t("node.profiles.portOverride")}
          value={port}
          onChange={(e) => {
            setPort(e.target.value.replace(/\D/g, "").slice(0, 5));
            save.reset();
          }}
          inputMode="numeric"
          autoFocus={focus === "port"}
          placeholder={effective?.port && !port ? t("node.profiles.portFrom", { port: effective.port }) : t("node.profiles.fromProfile")}
          hint={
            moved && port === moved.to
              ? moved.profile
                ? t("node.check.port.auto", { from: moved.from, profile: moved.profile, to: moved.to })
                : t("node.check.port.moved", { from: moved.from, to: moved.to })
              : undefined
          }
          mono
          error={portError}
        />
        {refused?.code === "port_taken" && Number(refused.vars.free) > 0 && (
          <div className="-mt-2 flex">
            <Button variant="secondary" size="sm" onClick={() => setPort(refused.vars.free!)}>
              {t("node.check.port.take", { port: refused.vars.free! })}
            </Button>
          </div>
        )}
        {share && (
          <ShareFix
            code={share.code}
            vars={share.vars}
            node={{ name: inbound.nodeName, address: nodeAddress }}
            onLeave={onClose}
            onPut={(name) => {
              setSni(name);
              save.reset();
            }}
            onTake={(p) => {
              setPort(String(p));
              save.reset();
            }}
          />
        )}
        {lossy && (
          <LossyNotice
            vars={lossy}
            busy={save.isPending}
            anywayLabel={t("ports.anyway.save")}
            onTake={(p) => {
              setPort(String(p));
              save.reset();
            }}
            onAnyway={() => save.mutate(true)}
          />
        )}
        {!isAwg && named && (
          <TextField
            id={sniId}
            label={t("node.profiles.tlsOverride")}
            value={sni}
            onChange={(e) => setSni(e.target.value)}
            autoFocus={focus === "sni"}
            placeholder={sniPlaceholder(t, effective?.tlsServerName, nodeAddress)}
            hint={tlsHint(t, inbound.protocol)}
            error={sniError}
            mono
            autoCapitalize="off"
            spellCheck={false}
          />
        )}
        {target.field}
        <label className="flex items-center gap-3">
          <span className="flex min-w-0 flex-1 flex-col gap-0.5">
            <span className="text-[13px] font-bold">{t("node.profiles.enabled")}</span>
            <span className="text-xs text-muted">{t("node.profiles.enabledHint")}</span>
          </span>
          <Switch checked={enabled} onCheckedChange={setEnabled} />
        </label>
        <WarpWarnings check={check} nodeId={nodeId} onLeave={onClose} />
        <DecoyNotice check={check} />
        {!target.touched && <ShareNotes check={check} node={{ name: inbound.nodeName, address: nodeAddress }} />}
        {unexplained && (
          <Notice tone="danger" className="items-start">
            {t(`err.${unexplained.code}`, unexplained.vars)}
          </Notice>
        )}
        {save.isError && !lossyRefusal(save.error) && (
          <Notice tone="danger" className="items-start">
            {errorText(save.error, t)}
          </Notice>
        )}
        <div className="flex justify-end gap-2 pt-1">
          <Button variant="ghost" size="md" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" variant="primary" size="md" disabled={!dirty || !portOk(port) || !!refused || target.blocked || save.isPending}>
            {save.isPending && touched.port && port ? t("ports.saving", { port }) : t("common.save")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function RemoveInbound({ inbound, nodeId, nodeName, owner, onClose }: { inbound: Plain<Inbound>; nodeId: string; nodeName: string; owner: boolean; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const refresh = useInboundRefresh(nodeId);
  const list = useQuery(profileListQuery);
  const users = list.data?.find((p) => p.id === inbound.profileId)?.userCount ?? 0;
  const devices = inbound.awg?.peers ?? 0;
  // the relays that pass this server on go with it (relay.proto)
  const also = alsoRemovesText(t, useRelaysOf(nodeId, owner).filter((r) => r.inboundId === inbound.id));
  const remove = useMutation({
    mutationFn: () => profilesApi.deleteInbound({ inboundId: inbound.id }),
    onSuccess: () => {
      onClose();
      toast(t("node.profiles.removed", { name: inbound.profileName }));
      refresh();
    },
  });
  // who notices: a subscription loses the node at its next update; an AmneziaVPN key of this node stops (and works again
  // when the profile comes back: the server key is kept for the pair)
  const impact =
    inbound.protocol === "awg"
      ? devices > 0
        ? t.n("node.profiles.removeDevices", devices, { node: nodeName })
        : t("node.profiles.removeNobody", { node: nodeName })
      : users > 0
        ? t.n("node.profiles.removeUsers", users, { node: nodeName })
        : t("node.profiles.removeNobody", { node: nodeName });
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t("node.profiles.removeTitle", { profile: inbound.profileName, node: nodeName })}
      description={list.isPending && inbound.protocol !== "awg" ? t("common.loading") : also ? `${impact} ${also}` : impact}
      footer={
        <>
          <Button variant="ghost" size="md" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" size="md" disabled={remove.isPending} onClick={() => remove.mutate()}>
            {t("node.profiles.remove")}
          </Button>
        </>
      }
    >
      {remove.isError && (
        <Notice tone="danger" className="items-start">
          {errorText(remove.error, t)}
        </Notice>
      )}
    </Modal>
  );
}

/** Restart one profile on the node: only its connections drop. */
function RestartInbound({ inbound, nodeId, nodeName, onClose }: { inbound: Plain<Inbound>; nodeId: string; nodeName: string; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const refresh = useInboundRefresh(nodeId);
  const restart = useMutation({
    mutationFn: () => nodesApi.restartInbounds({ nodeId, inboundId: inbound.id }),
    onSuccess: () => {
      onClose();
      toast(t("node.profiles.restarted", { profile: inbound.profileName }));
      refresh();
    },
  });
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t("node.profiles.restartTitle", { profile: inbound.profileName, node: nodeName })}
      description={t("node.profiles.restartBody")}
      footer={
        <>
          <Button variant="ghost" size="md" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="md" disabled={restart.isPending} onClick={() => restart.mutate()}>
            {t("node.profiles.restart")}
          </Button>
        </>
      }
    >
      {restart.isError && (
        <Notice tone="danger" className="items-start">
          {errorText(restart.error, t)}
        </Notice>
      )}
    </Modal>
  );
}

/** What the node counted of attempts on a profile in the last 24 hours, or that there were none. */
function ConnLine({ x }: { x?: InboundConn }) {
  const t = useT();
  const fmt = useFmt();
  const parts = x ? inboundConnParts(t, fmt, x) : [];
  return (
    <div data-testid="conn-line" className="flex items-baseline gap-2">
      <span className="text-[11px] font-bold text-muted">{t("conn.node.label")}</span>
      {parts.length > 0 ? <PartsLine parts={parts} /> : <span className="text-xs text-muted">{t("conn.node.quiet")}</span>}
    </div>
  );
}
