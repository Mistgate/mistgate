import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useId, useState, type ReactNode } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon, IconChip } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { EmptyState, Notice } from "@/components/ui/notice";
import { StatusPill, type StatusKind } from "@/components/ui/status";
import { Switch } from "@/components/ui/switch";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { InboundState, type Inbound } from "@/gen/mistgate/admin/v1/common_pb";
import { DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import type { Plain } from "@/lib/plain";
import { nodes as nodesApi, profiles as profilesApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt, type Fmt } from "@/lib/format";
import { doctorQuery } from "@/lib/health";
import { inboundErrorKind, inboundErrorText } from "@/lib/inbound-error";
import { agentLinked } from "@/lib/node-status";
import { protocolName } from "@/lib/series";
import { useNow } from "@/lib/time";
import { profileListQuery } from "@/screens/users/rpc";
import { AddInbound } from "./add-inbound";
import { AwgStatusLine } from "./awg-status";
import { portOk, sniPlaceholder, useInboundCheck, useInboundRefresh, WarpWarnings } from "./inbound-check";

export const states: Record<InboundState, { kind: StatusKind; word: MessageKey }> = {
  [InboundState.UNSPECIFIED]: { kind: "off", word: "node.inbound.off" },
  [InboundState.PENDING]: { kind: "busy", word: "node.inbound.pending" },
  [InboundState.ACTIVE]: { kind: "ok", word: "node.inbound.active" },
  [InboundState.FAILED]: { kind: "bad", word: "node.inbound.failed" },
  [InboundState.DISABLED]: { kind: "off", word: "node.inbound.off" },
};

type Cell = { label: string; value: string; mono?: boolean; title?: string; tone?: "warn" | "bad" };

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
function cells(t: T, fmt: Fmt, i: Plain<Inbound>, now: number): Cell[] {
  const port: Cell = { label: t("node.profiles.port"), value: i.port ? `udp/${i.port}` : "", mono: true };
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

// The last column fits the longest pill ("Не запустился").
const gridClass = "md:grid-cols-[minmax(0,1.4fr)_92px_minmax(0,0.7fr)_minmax(0,1.3fr)_minmax(0,1fr)_124px]";

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
export function InboundErrorNote({ inbound, process }: { inbound: Pick<Plain<Inbound>, "lastError" | "port">; process?: string }) {
  const t = useT();
  return (
    <div className="flex flex-col gap-0.5 rounded-field border border-[color-mix(in_oklch,var(--danger)_45%,var(--border))] bg-danger-soft px-3 py-2">
      <p className="text-[13px] leading-snug font-semibold text-danger-text">{inboundErrorText(t, inbound.lastError, { port: inbound.port, process })}</p>
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
  const [adding, setAddingOpen] = useState(Boolean(addProfile));
  const setAdding = (open: boolean) => {
    setAddingOpen(open);
    if (!open) onAddClosed?.();
  };
  const [editing, setEditing] = useState<Editing | null>(null);
  const [removing, setRemoving] = useState<Plain<Inbound> | null>(null);
  const [restarting, setRestarting] = useState<Plain<Inbound> | null>(null);
  const node = data.node!;
  const linked = agentLinked(node.status);
  const portTrouble = data.inbounds.some((i) => i.lastError && inboundErrorKind(i.lastError) === "port");
  const holders = usePortHolders(node.id, portTrouble);

  const add = (
    <Button variant="primary" size="md" onClick={() => setAdding(true)}>
      <Icon name="plus" size={14} />
      {t("node.profiles.add")}
    </Button>
  );

  return (
    <div className="flex flex-col gap-2">
      {data.inbounds.length === 0 ? (
        <div className="rounded-card-lg border border-dashed border-line">
          <EmptyState title={t("node.profiles.none", { name: node.name })} action={add}>
            {t("node.profiles.noneBody")}
          </EmptyState>
        </div>
      ) : (
        <div className="flex items-center gap-2 pb-1">
          <span className="flex-1 text-xs text-muted">{t.n("node.profiles.count", data.inbounds.length)}</span>
          <Button variant="primary" size="md" onClick={() => setAdding(true)}>
            <Icon name="plus" size={14} />
            {t("node.profiles.add")}
          </Button>
        </div>
      )}

      {data.inbounds.map((i) => {
        const s = states[i.state] ?? states[InboundState.UNSPECIFIED];
        const cs = cells(t, fmt, i, now);
        const kind = i.lastError ? inboundErrorKind(i.lastError) : null;
        const fix: ReactNode =
          kind === "port" ? (
            <Button variant="primary" size="sm" onClick={() => setEditing({ inbound: i, focus: "port" })}>
              {t("node.profiles.changePort")}
            </Button>
          ) : kind === "cert" && i.protocol !== "awg" ? (
            <Button variant="primary" size="sm" onClick={() => setEditing({ inbound: i, focus: "sni" })}>
              {t("node.profiles.setDomain")}
            </Button>
          ) : kind === "warp" ? (
            <Link to="/nodes/$id" params={{ id: node.id }} search={{ tab: "settings" }} hash="warp" className={buttonClass("primary", "sm")}>
              {t("warp.open")}
            </Link>
          ) : null;
        return (
          <div key={i.id} className="flex flex-col gap-2.5 rounded-card border border-line bg-surface px-4 py-3.5">
            <div className={`grid items-center gap-3 max-md:grid-cols-[minmax(0,1fr)_auto] ${gridClass}`}>
              <div className="flex min-w-0 items-center gap-2.5">
                <IconChip icon="sliders" tone="sage" size={28} />
                <div className="flex min-w-0 flex-col gap-[3px]">
                  {/* the name is read, never cut: it wraps */}
                  <Link to="/profiles/$id" params={{ id: i.profileId }} className="text-sm leading-snug font-bold break-words hover:text-accent-text hover:underline">
                    {i.profileName}
                  </Link>
                  <span className="text-xs text-muted">{protocolName(i.protocol)}</span>
                </div>
              </div>
              {cs.map((c) => (
                <div key={c.label} className="min-w-0 max-md:hidden">
                  <div className="flex min-w-0 flex-col gap-[3px]">
                    <span className="text-[11px] text-muted">{c.label}</span>
                    <span title={c.title} className={cx(c.mono ? "font-mono text-[13px] break-all" : "text-xs font-semibold break-words", c.tone && toneText[c.tone])}>
                      {c.value || "—"}
                    </span>
                  </div>
                </div>
              ))}
              <div className="flex justify-end">
                <StatusPill kind={s.kind} label={t(s.word)} sm />
              </div>
            </div>
            {/* the phone has no columns: each fact with its name */}
            <dl className="flex flex-wrap gap-x-4 gap-y-1 text-[11px] md:hidden">
              {cs.map((c) => (
                <div key={c.label} className="flex min-w-0 gap-1.5">
                  <dt className="text-muted">{c.label}</dt>
                  <dd className={cx("min-w-0 font-semibold", c.mono ? "font-mono break-all" : "break-words", c.tone && toneText[c.tone])}>{c.value || "—"}</dd>
                </div>
              ))}
            </dl>
            {i.awg && <AwgStatusLine awg={i.awg} />}
            {i.lastError && <InboundErrorNote inbound={i} process={holders[i.id]} />}
            <div className="flex flex-wrap items-center gap-2 border-t border-line pt-2.5">
              {fix}
              <Button variant="secondary" size="sm" onClick={() => setEditing({ inbound: i })}>
                {t("common.edit")}
              </Button>
              {linked && i.state !== InboundState.DISABLED && (
                <Button variant="secondary" size="sm" onClick={() => setRestarting(i)}>
                  {t("node.profiles.restart")}
                </Button>
              )}
              <Button variant="ghostDanger" size="sm" className="ml-auto" onClick={() => setRemoving(i)}>
                {t("node.profiles.remove")}
              </Button>
            </div>
          </div>
        );
      })}

      <AddInbound open={adding} onOpenChange={setAdding} data={data} initialProfile={addProfile} />
      {editing && (
        <EditInbound key={editing.inbound.id} inbound={editing.inbound} focus={editing.focus} nodeId={node.id} nodeAddress={node.address} onClose={() => setEditing(null)} />
      )}
      {removing && <RemoveInbound key={removing.id} inbound={removing} nodeId={node.id} nodeName={node.name} onClose={() => setRemoving(null)} />}
      {restarting && <RestartInbound key={restarting.id} inbound={restarting} nodeId={node.id} nodeName={node.name} onClose={() => setRestarting(null)} />}
    </div>
  );
}

function EditInbound({ inbound, focus, nodeId, nodeAddress, onClose }: { inbound: Plain<Inbound>; focus?: "port" | "sni"; nodeId: string; nodeAddress: string; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const refresh = useInboundRefresh(nodeId);
  const isAwg = inbound.protocol === "awg";
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
    mutationFn: () =>
      profilesApi.updateInbound({
        inboundId: inbound.id,
        portOverride: touched.port ? (port ? Number(port) : 0) : undefined,
        tlsServerNameOverride: touched.sni ? sni.trim() : undefined,
        enabled: touched.enabled ? enabled : undefined,
      }),
    onSuccess: () => {
      onClose();
      toast(t("common.saved"));
      refresh();
    },
  });

  const refused = check.state === "refused" ? check : null;
  const fieldError = (codes: string[]) => (refused && codes.includes(refused.code) ? t(`err.${refused.code}`, refused.vars) : undefined);
  const portError = !portOk(port) ? t("node.profiles.portError") : fieldError(["port_taken", "port_in_hop", "hop_taken"]);
  const sniError = fieldError(["sni_needs_domain", "sni_invalid", "acme_needs_domain"]);
  const effective = check.state === "ok" ? check.inbound : undefined;
  const dirty = touched.port || touched.sni || touched.enabled;
  return (
    <Modal open onOpenChange={(o) => !o && onClose()} title={t("node.profiles.editTitle", { name: inbound.profileName })} description={t("node.profiles.editBody")}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (dirty && portOk(port) && !refused) save.mutate();
        }}
        className="flex flex-col gap-3.5"
      >
        <TextField
          label={t("node.profiles.portOverride")}
          value={port}
          onChange={(e) => setPort(e.target.value.replace(/\D/g, "").slice(0, 5))}
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
        {!isAwg && (
          <TextField
            id={sniId}
            label={t("node.profiles.tlsOverride")}
            value={sni}
            onChange={(e) => setSni(e.target.value)}
            autoFocus={focus === "sni"}
            placeholder={sniPlaceholder(t, effective?.tlsServerName, nodeAddress)}
            hint={t("node.profiles.tlsHint")}
            error={sniError}
            mono
            autoCapitalize="off"
            spellCheck={false}
          />
        )}
        <label className="flex items-center gap-3">
          <span className="flex min-w-0 flex-1 flex-col gap-0.5">
            <span className="text-[13px] font-bold">{t("node.profiles.enabled")}</span>
            <span className="text-xs text-muted">{t("node.profiles.enabledHint")}</span>
          </span>
          <Switch checked={enabled} onCheckedChange={setEnabled} />
        </label>
        <WarpWarnings check={check} nodeId={nodeId} onLeave={onClose} />
        {save.isError && (
          <Notice tone="danger" className="items-start">
            {errorText(save.error, t)}
          </Notice>
        )}
        <div className="flex justify-end gap-2 pt-1">
          <Button variant="ghost" size="md" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" variant="primary" size="md" disabled={!dirty || !portOk(port) || !!refused || save.isPending}>
            {t("common.save")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function RemoveInbound({ inbound, nodeId, nodeName, onClose }: { inbound: Plain<Inbound>; nodeId: string; nodeName: string; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const refresh = useInboundRefresh(nodeId);
  const list = useQuery(profileListQuery);
  const users = list.data?.find((p) => p.id === inbound.profileId)?.userCount ?? 0;
  const devices = inbound.awg?.peers ?? 0;
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
      description={list.isPending && inbound.protocol !== "awg" ? t("common.loading") : impact}
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
