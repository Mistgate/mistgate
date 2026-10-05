import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useMemo, useState, type FormEvent } from "react";
import { useAddNode } from "@/components/add-node";
import { DangerZone, SectionLabel } from "@/components/ui/bits";
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
import { CodeBlock } from "@/screens/integrations/parts";
import { AwgBackendCard } from "./awg-backend";
import { nodeDnsMode, nodeDnsResolvers, type NodeDnsMode } from "./dns";
import { SSHAccessCard, useServerAccess } from "./ssh-access";
import { WarpCard } from "./warp";

// Defaults the panel uses when a timeout is 0 (node.proto, NodeTimeouts).
const timeoutDefs = {
  liveness: { def: 90, min: 30, max: 600, step: 10 },
  apply: { def: 120, min: 10, max: 900, step: 10 },
  dial: { def: 15, min: 5, max: 120, step: 5 },
} as const;
type TimeoutKey = keyof typeof timeoutDefs;

const namePattern = /^[a-z0-9-]{2,24}$/;
const addressPattern = /^[A-Za-z0-9.:-]{1,253}$/;
const resolverPattern = /^[0-9a-fA-F.:]{2,45}$/;
const none = "none";

const parseResolvers = (s: string) => s.split(/[\s,]+/).filter(Boolean);

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

export function SettingsTab({ data }: { data: Plain<GetNodeResponse> }) {
  const node = data.node!;
  // keyed by the node: switching nodes starts from that node's values
  return <SettingsForm key={node.id} data={data} />;
}

function SettingsForm({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const qc = useQueryClient();
  const node = data.node!;
  const retired = node.status === NodeStatus.RETIRED;
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  // retired while offline: shown here, not in the retire dialog, which goes once the node reads as retired
  const [unreached, setUnreached] = useState(false);

  // initialised once per node (the parent is keyed by it): a poll must not overwrite what is being typed
  const [start] = useState(() => ({
      name: node.name,
      address: node.address,
      country: node.countryCode || none,
      location: node.location,
      provider: node.provider,
      bandwidth: node.bandwidthMbps ? String(node.bandwidthMbps) : "",
      notes: data.notes,
      dns: data.dnsResolvers.join(", "),
      liveness: data.timeouts?.livenessTimeoutS || timeoutDefs.liveness.def,
      apply: data.timeouts?.applyTimeoutS || timeoutDefs.apply.def,
      dial: data.timeouts?.dialTimeoutS || timeoutDefs.dial.def,
      torrentBlockerEnabled: node.torrentBlockerEnabled,
  }));
  const [f, setF] = useState(start);
  const [dnsMode, setDnsMode] = useState<NodeDnsMode>(() => nodeDnsMode(data.dnsResolvers));
  const set = <K extends keyof typeof start>(k: K, v: (typeof start)[K]) => setF((x) => ({ ...x, [k]: v }));

  const countries = useMemo(() => {
    const codes = new Set<string>(countryCodes);
    if (start.country !== none) codes.add(start.country); // a country set through the API stays selectable
    return [
      { value: none, label: t("node.add.countryNone") },
      ...[...codes]
        .sort((a, b) => fmt.country(a).localeCompare(fmt.country(b), fmt.lang))
        .map((c) => ({ value: c, label: `${c} · ${fmt.country(c)}` })),
    ];
  }, [fmt, t, start.country]);

  const resolvers = parseResolvers(f.dns);
  const errors = {
    name: namePattern.test(f.name) ? undefined : t("node.add.nameError"),
    address: addressPattern.test(f.address.trim()) ? undefined : t("node.add.addressError"),
    dns: dnsMode !== "custom" || (resolvers.length > 0 && resolvers.every((r) => resolverPattern.test(r))) ? undefined : t("node.settings.dnsError"),
    bandwidth: f.bandwidth.trim() === "" || (/^\d+$/.test(f.bandwidth.trim()) && Number(f.bandwidth) <= 1_000_000)
      ? undefined
      : t("node.settings.bandwidthError"),
  };
  const dirty = (Object.keys(start) as (keyof typeof start)[]).some((k) => f[k] !== start[k]);

  const save = useMutation({
    mutationFn: () => {
      const changed = <K extends keyof typeof start>(k: K) => f[k] !== start[k];
      const timeoutsChanged = changed("liveness") || changed("apply") || changed("dial");
      const tv = (k: TimeoutKey) => (f[k] === timeoutDefs[k].def ? 0 : f[k]); // the default is stored as "0 = default"
      return nodesApi.updateNode({
        nodeId: node.id,
        name: changed("name") ? f.name : undefined,
        address: changed("address") ? f.address.trim() : undefined,
        countryCode: changed("country") ? (f.country === none ? "" : f.country) : undefined,
        location: changed("location") ? f.location.trim() : undefined,
        provider: changed("provider") ? f.provider.trim() : undefined,
        bandwidthMbps: changed("bandwidth") ? Number(f.bandwidth || "0") : undefined,
        notes: changed("notes") ? f.notes : undefined,
        dnsResolvers: changed("dns") ? { values: resolvers } : undefined,
        timeouts: timeoutsChanged
          ? { livenessTimeoutS: tv("liveness"), applyTimeoutS: tv("apply"), dialTimeoutS: tv("dial") }
          : undefined,
        torrentBlockerEnabled: changed("torrentBlockerEnabled") ? f.torrentBlockerEnabled : undefined,
      });
    },
    onSuccess: () => {
      toast(t("common.saved"));
      void qc.invalidateQueries({ queryKey: ["node", node.id] });
      void qc.invalidateQueries({ queryKey: ["nodes"] });
      void qc.invalidateQueries({ queryKey: ["overview"] });
    },
    onError: (e) => toast.error(errorText(e, t)),
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    if (dirty && !errors.name && !errors.address && !errors.dns && !errors.bandwidth) save.mutate();
  }

  const steppers: { key: TimeoutKey; label: string; hint: string }[] = [
    { key: "liveness", label: t("node.settings.liveness"), hint: t("node.settings.livenessHint") },
    { key: "apply", label: t("node.settings.apply"), hint: t("node.settings.applyHint") },
    { key: "dial", label: t("node.settings.dial"), hint: t("node.settings.dialHint") },
  ];
  const dnsModes: { value: NodeDnsMode; label: string; hint: string }[] = [
    { value: "system", label: t("node.settings.dnsMode.system"), hint: t("node.settings.dnsMode.systemHint") },
    { value: "yandex", label: t("node.settings.dnsMode.yandex"), hint: "77.88.8.8 · 77.88.8.1" },
    { value: "cloudflareGoogle", label: t("node.settings.dnsMode.cloudflareGoogle"), hint: "1.1.1.1 · 8.8.8.8" },
    { value: "custom", label: t("node.settings.dnsMode.custom"), hint: t("node.settings.dnsMode.customHint") },
  ];

  function chooseDnsMode(value: string) {
    const mode = value as NodeDnsMode;
    setDnsMode(mode);
    if (mode !== "custom") set("dns", nodeDnsResolvers(mode).join(", "));
  }

  return (
    <div className="flex max-w-[640px] flex-col gap-3.5">
      {/* first: every "Open WARP" of the panel (doctor, alerts, a profile's exit) lands here */}
      {!retired && <WarpCard nodeId={node.id} nodeName={node.name} retired={retired} />}
      <form onSubmit={submit} className="flex flex-col gap-3.5">
        <TextField icon="tag" tone="lavender" label={t("node.add.name")} value={f.name} onChange={(e) => set("name", e.target.value.toLowerCase())} mono maxLength={24} error={errors.name} autoCapitalize="off" spellCheck={false} disabled={retired} />
        <TextField icon="network" tone="sky" label={t("node.add.address")} value={f.address} onChange={(e) => set("address", e.target.value)} mono maxLength={253} error={errors.address} hint={t("node.settings.addressHint")} autoCapitalize="off" spellCheck={false} disabled={retired} />
        <div className="grid gap-3.5 md:grid-cols-2">
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
        <TextField icon="text" tone="sand" label={t("node.settings.notes")} value={f.notes} onChange={(e) => set("notes", e.target.value)} placeholder={t("node.settings.notesPh")} maxLength={500} />

        <div className="rounded-card border border-line bg-surface px-3.5 py-1">
          {steppers.map((s, i) => {
            const d = timeoutDefs[s.key];
            return (
              <div key={s.key} className={`flex min-h-14 items-center gap-3 ${i ? "border-t border-line" : ""}`}>
                <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                  <span className="text-[13px] font-bold">{s.label}</span>
                  <span className="text-xs text-muted">{s.hint}</span>
                </div>
                <div className="w-[130px]">
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
              </div>
            );
          })}
          <div className="flex min-h-14 flex-col justify-center gap-1.5 border-t border-line py-2.5">
            <SectionLabel as="span" icon="dns" tone="sky">
              {t("node.settings.dns")}
            </SectionLabel>
            <Select
              aria-label={t("node.settings.dns")}
              value={dnsMode}
              onValueChange={chooseDnsMode}
              options={dnsModes}
            />
            <span className="text-xs leading-snug text-muted">{t("node.settings.dnsHint")}</span>
            {dnsMode === "custom" && (
              <TextField
                tone="sky"
                label={t("node.settings.dnsCustom")}
                value={f.dns}
                onChange={(e) => {
                  set("dns", e.target.value);
                  setDnsMode("custom");
                }}
                placeholder={t("node.settings.dnsPh")}
                mono
                error={errors.dns}
                autoCapitalize="off"
                spellCheck={false}
              />
            )}
          </div>
        </div>

        <div className="rounded-card border border-line bg-surface px-3.5 py-3">
          <label className={`flex items-start gap-3 ${retired || !node.torrentBlockerSupported ? "cursor-not-allowed opacity-60" : "cursor-pointer"}`}>
            <input
              type="checkbox"
              className="mt-0.5 size-4 accent-accent"
              checked={f.torrentBlockerEnabled}
              onChange={(e) => set("torrentBlockerEnabled", e.target.checked)}
              disabled={retired || !node.torrentBlockerSupported}
            />
            <span className="flex min-w-0 flex-col gap-1">
              <span className="text-[13px] font-bold">{t("node.settings.torrentBlocker")}</span>
              <span className="text-xs leading-snug text-muted">{t("node.settings.torrentBlockerHint")}</span>
              {!node.torrentBlockerSupported && (
                <span className="text-xs leading-snug text-muted">{t("node.settings.torrentBlockerUnsupported")}</span>
              )}
            </span>
          </label>
        </div>

        <div className="flex justify-end">
          <Button type="submit" variant="primary" size="md" disabled={!dirty || !!errors.name || !!errors.address || !!errors.dns || !!errors.bandwidth || save.isPending}>
            {t("common.save")}
          </Button>
        </div>
      </form>

      <SSHAccessCard nodeId={node.id} />

      {!retired && <AwgBackendCard data={data} />}

      {!retired && owner && <ReinstallRow nodeId={node.id} name={node.name} />}

      {!retired && <RetireZone nodeId={node.id} name={node.name} onUnreached={() => setUnreached(true)} />}
      {unreached && <UnreachedModal name={node.name} />}
    </div>
  );
}

/** A fresh install command for a node that was enrolled before: a lost certificate or a reinstalled server. Owner only, like the API. */
function ReinstallRow({ nodeId, name }: { nodeId: string; name: string }) {
  const t = useT();
  const addNode = useAddNode();
  return (
    <section className="flex flex-wrap items-center gap-3 rounded-card border border-line bg-surface px-3.5 py-3">
      <p className="min-w-[220px] flex-1 text-[13px] leading-snug text-pretty">{t("node.add.reenrollBody")}</p>
      <Button variant="secondary" size="md" onClick={() => addNode({ id: nodeId, name })}>
        {t("node.banner.newCommand")}
      </Button>
    </section>
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
