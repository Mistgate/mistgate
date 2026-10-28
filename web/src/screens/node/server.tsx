import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMemo, useState, type FormEvent, type ReactNode } from "react";
import { useAddNode } from "@/components/add-node";
import { ActionBar, Block, DangerZone, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Select } from "@/components/ui/select";
import { Stepper } from "@/components/ui/stepper";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import type { Plain } from "@/lib/plain";
import { nodes as nodesApi } from "@/lib/api";
import { countryCodes } from "@/lib/countries";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { meQuery } from "@/lib/session";
import { useOlderNodes } from "@/lib/updates";
import { CodeBlock } from "@/screens/integrations/parts";
import { Panel, SettingRow } from "@/screens/users/ui";
import { AwgBackendCard } from "./awg-backend";
import { BandwidthMeasure } from "./bandwidth-measure";
import { DecoyCard } from "./decoy";
import { useNodeForm, useSaveNode } from "./node-form";
import { SSHAccessCard, useServerAccess } from "./ssh-access";

// Defaults the panel uses when a timeout is 0 (node.proto, NodeTimeouts).
const timeoutDefs = {
  liveness: { def: 90, min: 30, max: 600, step: 10 },
  apply: { def: 120, min: 10, max: 900, step: 10 },
  dial: { def: 15, min: 5, max: 120, step: 5 },
} as const;
type TimeoutKey = keyof typeof timeoutDefs;

const namePattern = /^[a-z0-9-]{2,24}$/;
const addressPattern = /^[A-Za-z0-9.:-]{1,253}$/;
const none = "none";

/**
 * What to run as root on a node that was offline when it was retired and so never got the order (docs: Add a node, Remove
 * a node): stop the agent (its stop runs `mistgate-node cleanup-net`), undo what it set up, remove its files.
 */
export const retiredCleanupCommand = [
  "systemctl disable --now mistgate-node",
  "nft delete table inet mistgate_node",
  "rm -f /etc/sysctl.d/90-mistgate.conf /etc/systemd/journald.conf.d/90-mistgate.conf",
  "systemctl restart systemd-journald",
  "rm -f /etc/systemd/system/mistgate-node.service",
  "systemctl daemon-reload",
  "rm -f /usr/local/bin/mistgate-node /usr/local/bin/mistgate-node.prev /usr/local/bin/mistgate-node.new /root/mistgate-node",
  "rm -rf /var/lib/mistgate-node",
].join("\n");

/**
 * Server: what this machine is. From xl two columns: on the left the node's passport, capacity and agent link as one form
 * with one Save at its end, and the agent under it; on the right SSH access, the decoy site, the AmneziaWG backend and
 * the host facts. The danger zone comes last, full width. One column below xl in the same order, the form first.
 */
export function ServerTab({ data }: { data: Plain<GetNodeResponse> }) {
  const node = data.node!;
  const retired = node.status === NodeStatus.RETIRED;
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  // retired while offline: shown here, not in the retire dialog, which goes once the node reads as retired
  const [unreached, setUnreached] = useState(false);
  return (
    <div className="flex flex-col gap-3.5">
      <div className="grid items-start gap-3.5 xl:grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)]">
        <div className="flex min-w-0 flex-col gap-3.5">
          {/* keyed by the node: switching nodes starts from that node's values */}
          <NodeForm key={node.id} data={data} owner={owner} />
          <AgentPanel data={data} owner={owner} />
        </div>
        <div className="flex min-w-0 flex-col gap-3.5">
          <SSHAccessCard nodeId={node.id} />
          {!retired && <DecoyCard node={node} />}
          {!retired && <AwgBackendCard data={data} />}
          <HostPanel data={data} />
        </div>
      </div>
      {!retired && <RetireZone nodeId={node.id} name={node.name} onUnreached={() => setUnreached(true)} />}
      {unreached && <UnreachedModal name={node.name} />}
    </div>
  );
}

/** Node (name, address, country, place, hoster, notes), capacity and the agent link: one form, one Save at its end. */
function NodeForm({ data, owner }: { data: Plain<GetNodeResponse>; owner: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const node = data.node!;
  const retired = node.status === NodeStatus.RETIRED;
  const startCountry = node.countryCode || none;
  const { f, set, changed, dirty } = useNodeForm(() => ({
    name: node.name,
    address: node.address,
    country: startCountry,
    location: node.location,
    provider: node.provider,
    bandwidth: node.bandwidthMbps ? String(node.bandwidthMbps) : "",
    notes: data.notes,
    liveness: data.timeouts?.livenessTimeoutS || timeoutDefs.liveness.def,
    apply: data.timeouts?.applyTimeoutS || timeoutDefs.apply.def,
    dial: data.timeouts?.dialTimeoutS || timeoutDefs.dial.def,
  }));

  const countries = useMemo(() => {
    const codes = new Set<string>(countryCodes);
    if (startCountry !== none) codes.add(startCountry); // a country set through the API stays selectable
    return [
      { value: none, label: t("node.add.countryNone") },
      ...[...codes]
        .sort((a, b) => fmt.country(a).localeCompare(fmt.country(b), fmt.locale))
        .map((c) => ({ value: c, label: `${c} · ${fmt.country(c)}` })),
    ];
  }, [fmt, t, startCountry]);

  const errors = {
    name: namePattern.test(f.name) ? undefined : t("node.add.nameError"),
    address: addressPattern.test(f.address.trim()) ? undefined : t("node.add.addressError"),
    bandwidth: f.bandwidth.trim() === "" || (/^\d+$/.test(f.bandwidth.trim()) && Number(f.bandwidth) <= 1_000_000)
      ? undefined
      : t("node.settings.bandwidthError"),
  };
  const invalid = !!errors.name || !!errors.address || !!errors.bandwidth;

  const save = useSaveNode(node.id, () => {
    const timeoutsChanged = changed("liveness") || changed("apply") || changed("dial");
    const tv = (k: TimeoutKey) => (f[k] === timeoutDefs[k].def ? 0 : f[k]); // the default is stored as "0 = default"
    return {
      name: changed("name") ? f.name : undefined,
      address: changed("address") ? f.address.trim() : undefined,
      countryCode: changed("country") ? (f.country === none ? "" : f.country) : undefined,
      location: changed("location") ? f.location.trim() : undefined,
      provider: changed("provider") ? f.provider.trim() : undefined,
      bandwidthMbps: changed("bandwidth") ? Number(f.bandwidth || "0") : undefined,
      notes: changed("notes") ? f.notes : undefined,
      timeouts: timeoutsChanged ? { livenessTimeoutS: tv("liveness"), applyTimeoutS: tv("apply"), dialTimeoutS: tv("dial") } : undefined,
    };
  });
  function submit(e: FormEvent) {
    e.preventDefault();
    if (dirty && !invalid) save.mutate();
  }

  const steppers: { key: TimeoutKey; label: string; hint: string }[] = [
    { key: "liveness", label: t("node.settings.liveness"), hint: t("node.settings.livenessHint") },
    { key: "apply", label: t("node.settings.apply"), hint: t("node.settings.applyHint") },
    { key: "dial", label: t("node.settings.dial"), hint: t("node.settings.dialHint") },
  ];

  return (
    <form onSubmit={submit} className="flex min-w-0 flex-col rounded-card-lg border border-line bg-surface">
      <SectionLabel as="h2" icon="server" tone="sky" className="px-4 pt-4 pb-3.5 md:px-5">
        {t("node.server.node")}
      </SectionLabel>
      <Block>
        <TextField icon="tag" tone="lavender" label={t("node.add.name")} value={f.name} onChange={(e) => set("name", e.target.value.toLowerCase())} mono maxLength={24} error={errors.name} autoCapitalize="off" spellCheck={false} disabled={retired} />
        <TextField icon="network" tone="sky" label={t("node.add.address")} value={f.address} onChange={(e) => set("address", e.target.value)} mono maxLength={253} error={errors.address} hint={t("node.settings.addressHint")} autoCapitalize="off" spellCheck={false} disabled={retired} />
        <div className="grid gap-3 md:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <SectionLabel as="span" icon="globe" tone="sand">
              {t("node.add.country")}
            </SectionLabel>
            <Select value={f.country} onValueChange={(v) => set("country", v)} options={countries} aria-label={t("node.add.country")} />
            <span className="text-xs leading-snug text-pretty text-muted">{t("node.settings.countryHint")}</span>
          </div>
          <TextField icon="map" tone="sand" label={t("node.settings.location")} value={f.location} onChange={(e) => set("location", e.target.value)} placeholder={fmt.country(f.country === none ? "" : f.country)} maxLength={100} disabled={retired} />
        </div>
        <TextField icon="server" tone="sky" label={t("node.settings.provider")} value={f.provider} onChange={(e) => set("provider", e.target.value)} maxLength={100} disabled={retired} />
        <TextField icon="text" tone="sand" label={t("node.settings.notes")} value={f.notes} onChange={(e) => set("notes", e.target.value)} placeholder={t("node.settings.notesPh")} maxLength={500} />
      </Block>
      <Block title={t("node.server.capacity")}>
        <TextField
          icon="network"
          tone="sky"
          label={t("node.settings.bandwidth")}
          hint={t("node.settings.bandwidthHint")}
          value={f.bandwidth}
          onChange={(e) => set("bandwidth", e.target.value)}
          type="number"
          min={0}
          max={1_000_000}
          step={1}
          placeholder="0"
          inputMode="numeric"
          error={errors.bandwidth}
          disabled={retired}
        />
        {!retired && owner && <BandwidthMeasure nodeId={node.id} current={f.bandwidth} onUse={(v) => set("bandwidth", v)} />}
      </Block>
      <Block title={t("node.server.agentLink")} className="gap-1 pb-1">
        {steppers.map((s) => {
          const d = timeoutDefs[s.key];
          return (
            <SettingRow key={s.key} label={s.label} hint={s.hint}>
              <div className="w-[130px] flex-none">
                <Stepper
                  decrementLabel={t("common.decrease", { what: s.label })}
                  incrementLabel={t("common.increase", { what: s.label })}
                  decrementDisabled={f[s.key] <= d.min}
                  incrementDisabled={f[s.key] >= d.max}
                  onDecrement={() => set(s.key, Math.max(d.min, f[s.key] - d.step))}
                  onIncrement={() => set(s.key, Math.min(d.max, f[s.key] + d.step))}
                >
                  {f[s.key]} {t("unit.s")}
                </Stepper>
              </div>
            </SettingRow>
          );
        })}
      </Block>
      <ActionBar>
        <Button type="submit" variant="primary" size="md" className="ml-auto" disabled={!dirty || invalid || save.isPending}>
          {t("common.save")}
        </Button>
      </ActionBar>
    </form>
  );
}

/** The agent: its version (and the way to the update when it is behind), what an old one cannot do, a new install command. */
function AgentPanel({ data, owner }: { data: Plain<GetNodeResponse>; owner: boolean }) {
  const t = useT();
  const addNode = useAddNode();
  const node = data.node!;
  const retired = node.status === NodeStatus.RETIRED;
  const older = useOlderNodes().has(node.id);
  return (
    <Panel title={t("node.server.agent")} icon="cpu" tone="lavender" className="pb-1.5">
      <div className="flex flex-col">
        <SettingRow
          label={t("node.server.version")}
          hint={
            older ? (
              <Link to="/updates" className="font-semibold text-warn-text underline decoration-dotted underline-offset-2">
                {t("up.node.outdated")}
              </Link>
            ) : undefined
          }
        >
          <span className="font-mono text-xs font-semibold">{node.agentVersion || "—"}</span>
        </SettingRow>
        {/* an agent without "user-rate-limit/1" holds every device to the smaller of the person and device limits */}
        {!retired && node.lastSeenUnix > 0 && !node.userRateLimitSupported && (
          <p className="border-t border-line py-3 text-xs leading-snug text-pretty text-muted">{t("node.settings.userRateLimitUnsupported")}</p>
        )}
        {/* a lost certificate or a reinstalled server; owner only, like the API */}
        {!retired && owner && (
          <SettingRow label={t("node.server.reinstall")} hint={t("node.add.reenrollBody")} className="py-2.5">
            <Button variant="secondary" size="md" onClick={() => addNode({ id: node.id, name: node.name })}>
              {t("node.banner.newCommand")}
            </Button>
          </SettingRow>
        )}
      </div>
    </Panel>
  );
}

/** What the agent reported about the machine. */
function HostPanel({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const fmt = useFmt();
  const f = data.facts;
  const fact = (k: string, v: ReactNode) => ({ k, v });
  const facts = f
    ? [
        fact(t("node.f.hostname"), f.hostname),
        fact(t("node.f.os"), f.os),
        fact(t("node.f.kernel"), f.kernel),
        fact(t("node.f.arch"), f.arch),
        fact(t("node.f.cpu"), f.cpuCount ? String(f.cpuCount) : ""),
        fact(t("node.f.ram"), f.ramTotalBytes ? fmt.bytes(f.ramTotalBytes, 1024) : ""),
        fact(t("node.f.disk"), f.diskTotalBytes ? fmt.bytes(f.diskTotalBytes, 1024) : ""),
        fact(t("node.f.virt"), f.virt),
        fact("IPv6", f.hasIpv6 ? t("common.yes") : t("common.no")),
        fact(t("node.f.booted"), f.bootUnix ? fmt.dateTime(f.bootUnix) : ""),
        fact(
          t("node.f.engines"),
          f.engines.length ? (
            <span className="flex flex-col items-end gap-0.5">
              {f.engines.map((e) => (
                <span key={e}>{e}</span>
              ))}
            </span>
          ) : (
            ""
          ),
        ),
      ]
    : [];
  return (
    <Panel title={t("node.server.host")} icon="info" tone="sand" className="pb-1.5">
      <div className="flex flex-col">
        {facts.length === 0 && <p className="py-2 text-[13px] text-muted">{t("node.factsNone")}</p>}
        {facts
          .filter(({ v }) => v !== "")
          .map(({ k, v }, i) => (
            <div key={k} className={`flex min-h-[38px] items-center gap-2.5 py-1.5 text-[13px] ${i ? "border-t border-line" : ""}`}>
              <span className="flex-1 text-muted">{k}</span>
              <span className="min-w-0 text-right font-mono text-xs font-semibold break-all">{v}</span>
            </div>
          ))}
      </div>
    </Panel>
  );
}

function RetireZone({ nodeId, name, onUnreached }: { nodeId: string; name: string; onUnreached: () => void }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  return (
    <DangerZone title={t("node.danger")}>
      <div className="flex flex-wrap items-center gap-3">
        <p className="min-w-[220px] flex-1 text-[13px] leading-snug text-pretty">{t("node.retireText")}</p>
        <Button variant="danger" size="md" onClick={() => setOpen(true)}>
          {t("node.retire")}
        </Button>
      </div>
      {open && <RetireModal nodeId={nodeId} name={name} onClose={() => setOpen(false)} onUnreached={onUnreached} />}
    </DangerZone>
  );
}

/** "Retire from fleet": the node's name has to be typed. */
function RetireModal({ nodeId, name, onClose, onUnreached }: { nodeId: string; name: string; onClose: () => void; onUnreached: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [typed, setTyped] = useState("");
  const generatedPassword = useServerAccess(nodeId).data?.passwordGenerated ?? false;
  const retire = useMutation({
    mutationFn: () => nodesApi.retireNode({ nodeId, confirmName: typed.trim() }),
    onSuccess: (r) => {
      onClose();
      toast(t("node.retired", { name }));
      void qc.invalidateQueries({ queryKey: ["nodes"] });
      void qc.invalidateQueries({ queryKey: ["node-server-access", nodeId] });
      void qc.invalidateQueries({ queryKey: ["overview"] });
      // an agent that was not on the line keeps serving: the admin has to clean the server, so stay and say how
      if (r.agentNotified) void navigate({ to: "/nodes" });
      else onUnreached();
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
  const ok = typed.trim() === name;
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t("node.retireTitle", { name })}
      footer={
        <>
          <Button variant="ghost" size="md" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" size="md" disabled={!ok || retire.isPending} onClick={() => retire.mutate()}>
            {t("node.retire")}
          </Button>
        </>
      }
    >
      <ul className="flex flex-col gap-2">
        {(["node.retireList1", "node.retireList2", "node.retireList3"] as const).map((k) => (
          <li key={k} className="flex gap-2 text-[13px] leading-snug">
            <span aria-hidden className="font-extrabold text-danger">
              ·
            </span>
            <span className="text-pretty">{t(k)}</span>
          </li>
        ))}
      </ul>
      {generatedPassword && <Notice>{t("node.retireGeneratedPassword")}</Notice>}
      <p className="text-xs text-muted">{t("node.retireConfirm", { name })}</p>
      <TextField
        aria-label={t("node.retireConfirm", { name })}
        value={typed}
        onChange={(e) => setTyped(e.target.value)}
        placeholder={name}
        mono
        autoFocus
        autoComplete="off"
        autoCapitalize="off"
        spellCheck={false}
      />
    </Modal>
  );
}

/** The node was retired but its agent was offline and never got the order: it keeps serving until the server is cleaned. */
function UnreachedModal({ name }: { name: string }) {
  const t = useT();
  const navigate = useNavigate();
  const leave = () => void navigate({ to: "/nodes" });
  return (
    <Modal
      open
      onOpenChange={(o) => !o && leave()}
      title={t("node.retiredUnreached", { name })}
      description={t("node.retiredUnreachedBody")}
      footer={
        <Button variant="primary" size="md" onClick={leave}>
          {t("common.done")}
        </Button>
      }
    >
      <CodeBlock text={retiredCleanupCommand} lang="sh" />
      <p className="text-xs leading-normal text-pretty text-muted">{t("node.retiredUnreachedResolver")}</p>
    </Modal>
  );
}
