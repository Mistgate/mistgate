import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMemo, useState, type ReactNode } from "react";
import { halo } from "@/components/fx/halo";
import { useAddNode } from "@/components/add-node";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Select } from "@/components/ui/select";
import { fieldClass, TextArea, TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import type { Plain } from "@/lib/plain";
import { nodes as nodesApi } from "@/lib/api";
import { countryCodes } from "@/lib/countries";
import { cx } from "@/lib/cx";
import { debugListQuery, isLive } from "@/lib/debug-capture";
import { useEdition } from "@/lib/edition";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { osRows, useOsUpdates } from "@/lib/os-updates";
import { meQuery } from "@/lib/session";
import { useIsOwner, useOlderNodes } from "@/lib/updates";
import { CodeBlock } from "@/screens/integrations/parts";
import { Glyph } from "@/screens/overview-parts";
import { AwgBackendCard } from "./awg-backend";
import { MeasureButton, MeasureResult, useBandwidthMeasure } from "./bandwidth-measure";
import { DebugCaptureCard } from "./debug-capture";
import { DecoyCard } from "./decoy";
import { useNodeForm, useSaveNode } from "./node-form";
import { OsCard } from "./os-card";
import { useRelaysOf } from "./relays";
import { Facts, SvCard, SvHead, SvRow, SvSave, SvStep, useSavedFlash } from "./server-parts";
import { SSHAccessCard, useServerAccess } from "./ssh-access";
import { serverSections, type ServerSection } from "./tabs";

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

// the icons of the menu (the board's svNav)
const navIcon: Record<ServerSection, string> = {
  node: "M4 6h16v5H4zM4 13h16v5H4zM7 8.5h.5M7 15.5h.5",
  ch: "M4 17l5-5 4 3 7-8M15 7h5v5",
  ag: "M12 4v10M8 10l4 4 4-4M5 19h14",
  ssh: "M8 15a4 4 0 1 1 3.9-5H21v3h-2v2h-3v-2h-4.1A4 4 0 0 1 8 15z",
  site: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18",
  awg: "M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6z",
  sys: "M5 5h14v10H5zM9 19h6M12 15v4",
  dbg: "M9 6a3 3 0 0 1 6 0M7 9h10v5a5 5 0 0 1-10 0zM3 12h4M17 12h4M5 19l3-2M19 19l-3-2",
  del: "M5 7h14M10 7V5h4v2M7 7l1 12h8l1-12M10 11v5M14 11v5",
};

/**
 * Server: what this machine is. The sections of the board's left menu, one at a time: the node's passport, its channel, the agent
 * and the link to it, SSH access with its keys, the decoy site, the AmneziaWG backend, the operating system with the host, the
 * debug recording, and the end of the node's life. A retired node keeps what is still true of it: the passport (locked), the agent,
 * SSH access (to forget it) and the host. Below 1000 px the menu is a row of pills above the card.
 */
export function ServerTab({ data, section = "node", onSection }: { data: Plain<GetNodeResponse>; section?: ServerSection; onSection?: (s: ServerSection) => void }) {
  return <ServerBody key={data.node!.id} data={data} section={section} onSection={onSection ?? (() => {})} />;
}

function ServerBody({ data, section, onSection }: { data: Plain<GetNodeResponse>; section: ServerSection; onSection: (s: ServerSection) => void }) {
  const t = useT();
  const node = data.node!;
  const retired = node.status === NodeStatus.RETIRED;
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const edition = useEdition();
  // retired while offline: shown here, not in the retire dialog, which goes once the node reads as retired
  const [unreached, setUnreached] = useState(false);
  const form = useServerForm(data);

  // what the menu says about the system and the recording: the same queries the cards read, so no second poll
  const lacking = edition.off("os_updates");
  const os = useOsUpdates(!lacking && !retired);
  const osRow = os.data ? osRows(os.data).find((r) => r.nodeId === node.id) : undefined;
  const recording = useQuery({ ...debugListQuery(node.id), enabled: owner && !retired }).data?.captures.some(isLive) ?? false;
  const sys = osRow?.status && !osRow.status.error ? (osRow.status.rebootRequired ? { b: "!", warn: true } : osRow.status.security > 0 ? { b: String(osRow.status.security), warn: true } : null) : null;

  const shown = serverSections.filter((s) => (retired ? s === "node" || s === "ag" || s === "ssh" || s === "sys" : true));
  const open = shown.includes(section) ? section : "node";
  const badge = (s: ServerSection) => (s === "sys" && sys ? { text: sys.b, cls: "" } : s === "dbg" && recording ? { text: "●", cls: "rec" } : null);

  return (
    <div className="sv">
      <nav className="sv-nav" aria-label={t("np.sv.nav")}>
        {shown.map((s) => {
          const b = badge(s);
          return (
            <button key={s} type="button" aria-pressed={open === s} className={s === "del" ? "danger" : undefined} onClick={() => onSection(s)}>
              <Glyph d={navIcon[s]} size={18} stroke={1.5} />
              <span>{s === "del" ? t("node.danger") : t(`np.sv.nav.${s}`)}</span>
              {b && <em className={b.cls}>{b.text}</em>}
            </button>
          );
        })}
      </nav>
      {/* keyed by the section: it rises as it opens */}
      <div className="sv-main" key={open}>
        {open === "node" && <NodeSection data={data} owner={owner} form={form} />}
        {open === "ch" && <ChannelSection data={data} form={form} />}
        {open === "ag" && <AgentSections data={data} owner={owner} form={form} />}
        {open === "ssh" && <SSHAccessCard nodeId={node.id} attach={retired ? undefined : { name: node.name, address: node.address }} />}
        {open === "site" && !retired && <DecoyCard node={node} />}
        {open === "awg" && !retired && <AwgBackendCard data={data} />}
        {open === "sys" && (
          <>
            {!retired && <OsCard nodeId={node.id} />}
            <HostPanel data={data} />
          </>
        )}
        {open === "dbg" && !retired && <DebugCaptureCard node={node} />}
        {open === "del" && !retired && <RetireZone nodeId={node.id} name={node.name} onUnreached={() => setUnreached(true)} />}
      </div>
      {unreached && <UnreachedModal name={node.name} />}
    </div>
  );
}

// the fields of the node's three forms: what each Save sends and each Cancel puts back
const nodeFields = ["name", "address", "country", "location", "publicLabel", "provider", "notes"] as const;
const channelFields = ["bandwidth"] as const;
const agentFields = ["liveness", "apply", "dial"] as const;

/** The state of the three forms of the Server tab: it lives above the menu, so a half-typed field survives a look at another section. */
function useServerForm(data: Plain<GetNodeResponse>) {
  const node = data.node!;
  const startCountry = node.countryCode || none;
  return useNodeForm(() => ({
    name: node.name,
    address: node.address,
    country: startCountry,
    location: node.location,
    publicLabel: node.publicLabel,
    provider: node.provider,
    bandwidth: node.bandwidthMbps ? String(node.bandwidthMbps) : "",
    notes: data.notes,
    liveness: data.timeouts?.livenessTimeoutS || timeoutDefs.liveness.def,
    apply: data.timeouts?.applyTimeoutS || timeoutDefs.apply.def,
    dial: data.timeouts?.dialTimeoutS || timeoutDefs.dial.def,
  }));
}
type Form = ReturnType<typeof useServerForm>;

/** Save of one of the forms: what changed in its fields goes to UpdateNode; the bar says "Saved" and the page's wave runs. */
function useSaveSection(nodeId: string, form: Form, fields: readonly (keyof Form["f"])[], payload: () => Parameters<typeof useSaveNode>[1] extends () => infer R ? R : never) {
  const [saved, flash] = useSavedFlash();
  const save = useSaveNode(nodeId, payload);
  const dirty = form.dirtyOf(fields);
  return {
    saved,
    dirty,
    busy: save.isPending,
    cancel: () => form.revert(fields),
    save: () =>
      save.mutate(undefined, {
        onSuccess: () => {
          form.commit(fields);
          flash();
          halo("save");
        },
      }),
  };
}

/** The node (name, address, country, place, public label, hoster, notes): one form, one bar at its end. */
function NodeSection({ data, owner, form }: { data: Plain<GetNodeResponse>; owner: boolean; form: Form }) {
  const t = useT();
  const fmt = useFmt();
  const node = data.node!;
  const retired = node.status === NodeStatus.RETIRED;
  const { f, set, changed } = form;
  const startCountry = node.countryCode || none;

  const countries = useMemo(() => {
    const codes = new Set<string>(countryCodes);
    if (startCountry !== none) codes.add(startCountry); // a country set through the API stays selectable
    return [
      { value: none, label: t("node.add.countryNone") },
      ...[...codes]
        .sort((a, b) => fmt.country(a).localeCompare(fmt.country(b), fmt.locale))
        .map((c) => ({ value: c, label: `${c} · ${fmt.country(c)}`, icon: <Flag code={c} size={22} className="h-4 w-[22px] rounded-[4px]" /> })),
    ];
  }, [fmt, t, startCountry]);

  const nameError = namePattern.test(f.name) ? undefined : t("node.add.nameError");
  const addressError = addressPattern.test(f.address.trim()) ? undefined : t("node.add.addressError");
  const sec = useSaveSection(node.id, form, nodeFields, () => ({
    name: changed("name") ? f.name : undefined,
    address: changed("address") ? f.address.trim() : undefined,
    countryCode: changed("country") ? (f.country === none ? "" : f.country) : undefined,
    location: changed("location") ? f.location.trim() : undefined,
    publicLabel: changed("publicLabel") ? f.publicLabel.trim() : undefined,
    provider: changed("provider") ? f.provider.trim() : undefined,
    notes: changed("notes") ? f.notes : undefined,
  }));

  return (
    <SvCard data-shot="node-public-label" aria-label={t("np.sv.nav.node")}>
      <SvHead title={t("np.sv.node.title")} hint={t("np.sv.node.hint")} />
      <div className="sv-form">
        <TextField label={t("node.add.name")} hint={t("np.sv.node.nameHint")} value={f.name} onChange={(e) => set("name", e.target.value.toLowerCase())} mono maxLength={24} error={nameError} autoCapitalize="off" spellCheck={false} disabled={retired} />
        {/* moving a node is the owner's call; the API refuses a helper that changes it */}
        <TextField
          label={t("node.add.address")}
          value={f.address}
          onChange={(e) => set("address", e.target.value)}
          mono
          maxLength={253}
          error={addressError}
          hint={<span className={owner ? "sv-warnhint" : undefined}>{t(owner ? "node.settings.addressHint" : "node.settings.addressOwnerHint")}</span>}
          autoCapitalize="off"
          spellCheck={false}
          disabled={retired || !owner}
        />
        <div className="flex min-w-0 flex-col gap-1.5">
          <span className="text-[13px] text-sec">{t("node.add.country")}</span>
          <Select value={f.country} onValueChange={(v) => set("country", v)} options={countries} aria-label={t("node.add.country")} />
          <span className="text-xs leading-4 text-muted">{t("node.settings.countryHint")}</span>
        </div>
        <TextField label={t("node.settings.location")} value={f.location} onChange={(e) => set("location", e.target.value)} placeholder={fmt.country(f.country === none ? "" : f.country)} maxLength={100} disabled={retired} />
        <TextField
          label={
            <>
              {t("node.settings.publicLabel")}
              <em className="ms-1 font-normal text-muted not-italic">{t("np.sv.optional")}</em>
            </>
          }
          hint={t("node.settings.publicLabelHint")}
          value={f.publicLabel}
          onChange={(e) => set("publicLabel", e.target.value)}
          maxLength={40}
          disabled={retired}
        />
        <TextField label={t("node.settings.provider")} value={f.provider} onChange={(e) => set("provider", e.target.value)} maxLength={100} disabled={retired} />
        <TextArea className="wide" label={t("node.settings.notes")} value={f.notes} onChange={(e) => set("notes", e.target.value)} placeholder={t("node.settings.notesPh")} maxLength={500} rows={2} />
      </div>
      <SvSave dirty={sec.dirty} saved={sec.saved} busy={sec.busy} invalid={!!nameError || !!addressError} onCancel={sec.cancel} onSave={sec.save} />
    </SvCard>
  );
}

/** The channel: the capacity from the plan, which the load people see is counted from, and the measurement of it. */
function ChannelSection({ data, form }: { data: Plain<GetNodeResponse>; form: Form }) {
  const t = useT();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const node = data.node!;
  const retired = node.status === NodeStatus.RETIRED;
  const { f, set, changed } = form;
  const m = useBandwidthMeasure(node.id);
  const error = f.bandwidth.trim() === "" || (/^\d+$/.test(f.bandwidth.trim()) && Number(f.bandwidth) <= 1_000_000) ? undefined : t("node.settings.bandwidthError");
  const sec = useSaveSection(node.id, form, channelFields, () => ({ bandwidthMbps: changed("bandwidth") ? Number(f.bandwidth || "0") : undefined }));
  return (
    <SvCard aria-label={t("np.sv.nav.ch")}>
      <SvHead title={t("np.sv.ch.title")} hint={t("np.sv.ch.hint")} />
      <div className="sv-ch">
        <label className="flex min-w-0 flex-col gap-1.5">
          <span className="text-[13px] text-sec">{t("node.settings.bandwidth")}</span>
          <div className="sv-unit">
            <input
              className={cx(fieldClass, "h-10 px-3 font-mono")}
              dir="ltr"
              type="number"
              min={0}
              max={1_000_000}
              step={1}
              placeholder="0"
              inputMode="numeric"
              value={f.bandwidth}
              onChange={(e) => set("bandwidth", e.target.value)}
              disabled={retired}
              data-invalid={error ? "" : undefined}
            />
            <em>{t("np.sv.ch.unit")}</em>
          </div>
          <small className={cx("text-xs leading-4", error ? "text-danger-text" : "text-muted")}>{error ?? t("node.settings.bandwidthHint")}</small>
        </label>
        {!retired && owner && <MeasureButton m={m} />}
      </div>
      {!retired && owner && <MeasureResult m={m} current={f.bandwidth} onUse={(v) => set("bandwidth", v)} />}
      <SvSave dirty={sec.dirty} saved={sec.saved} busy={sec.busy} invalid={!!error} onCancel={sec.cancel} onSave={sec.save} />
    </SvCard>
  );
}

/** The agent (its version, what an old one cannot do, a new install command) and the link to it (how long the panel waits). */
function AgentSections({ data, owner, form }: { data: Plain<GetNodeResponse>; owner: boolean; form: Form }) {
  const t = useT();
  const addNode = useAddNode();
  const node = data.node!;
  const retired = node.status === NodeStatus.RETIRED;
  const older = useOlderNodes().has(node.id);
  const { f, set } = form;
  const sec = useSaveSection(node.id, form, agentFields, () => {
    const tv = (k: TimeoutKey) => (f[k] === timeoutDefs[k].def ? 0 : f[k]); // the default is stored as "0 = default"
    return { timeouts: { livenessTimeoutS: tv("liveness"), applyTimeoutS: tv("apply"), dialTimeoutS: tv("dial") } };
  });
  const steppers: { key: TimeoutKey; label: string; hint: string }[] = [
    { key: "liveness", label: t("node.settings.liveness"), hint: t("node.settings.livenessHint") },
    { key: "apply", label: t("node.settings.apply"), hint: t("node.settings.applyHint") },
    { key: "dial", label: t("node.settings.dial"), hint: t("node.settings.dialHint") },
  ];
  return (
    <>
      <SvCard aria-label={t("node.server.agent")}>
        <SvHead title={t("node.server.agent")} hint={t("np.sv.ag.hint")} />
        <SvRow
          title={
            <>
              {t("node.server.version")} <span className="mono">{node.agentVersion || "—"}</span>
            </>
          }
          hint={t("np.sv.ag.versionHint")}
        >
          {node.agentVersion &&
            (older ? (
              <Link to="/updates" className="inline-flex items-center gap-1.5 text-xs text-warn underline decoration-dotted underline-offset-2">
                <i aria-hidden className="size-2 rounded-full bg-(--nd-warn)" />
                {t("up.node.outdated")}
              </Link>
            ) : (
              <span className="inline-flex items-center gap-1.5 text-xs text-accent-text">
                <i aria-hidden className="size-2 rounded-full bg-accent-mark" />
                {t("np.sv.ag.current")}
              </span>
            ))}
        </SvRow>
        {/* an agent without "user-rate-limit/1" holds every device to the smaller of the person and device limits */}
        {!retired && node.lastSeenUnix > 0 && !node.userRateLimitSupported && <p className="sv-note !pt-3.5">{t("node.settings.userRateLimitUnsupported")}</p>}
        {/* a lost certificate or a reinstalled server; owner only, like the API */}
        {!retired && owner && (
          <SvRow title={t("node.server.reinstall")} hint={t("node.add.reenrollBody")}>
            <Button variant="secondary" size="lg" onClick={() => addNode({ id: node.id, name: node.name })}>
              {t("node.banner.newCommand")}
            </Button>
          </SvRow>
        )}
      </SvCard>
      <SvCard aria-label={t("node.server.agentLink")}>
        <SvHead title={t("node.server.agentLink")} hint={t("np.sv.ag.linkHint")} />
        {steppers.map((s) => {
          const d = timeoutDefs[s.key];
          return (
            <SvRow key={s.key} title={s.label} hint={s.hint}>
              <SvStep
                decLabel={t("common.decrease", { what: s.label })}
                incLabel={t("common.increase", { what: s.label })}
                decOff={f[s.key] <= d.min}
                incOff={f[s.key] >= d.max}
                onDec={() => set(s.key, Math.max(d.min, f[s.key] - d.step))}
                onInc={() => set(s.key, Math.min(d.max, f[s.key] + d.step))}
                value={`${f[s.key]} ${t("unit.s")}`}
              />
            </SvRow>
          );
        })}
        <SvSave dirty={sec.dirty} saved={sec.saved} busy={sec.busy} onCancel={sec.cancel} onSave={sec.save} />
      </SvCard>
    </>
  );
}

/** What the agent reported about the machine, and the engines it runs. */
function HostPanel({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const fmt = useFmt();
  const f = data.facts;
  const fact = (l: string, v: ReactNode, mono?: boolean) => ({ l, v, mono });
  const facts = f
    ? [
        fact(t("node.f.hostname"), f.hostname, true),
        fact(t("node.f.os"), f.os),
        fact(t("node.f.kernel"), f.kernel, true),
        fact(t("node.f.arch"), f.arch, true),
        fact(t("node.f.cpu"), f.cpuCount ? String(f.cpuCount) : "", true),
        fact(t("node.f.ram"), f.ramTotalBytes ? fmt.bytes(f.ramTotalBytes, 1024) : ""),
        fact(t("node.f.disk"), f.diskTotalBytes ? fmt.bytes(f.diskTotalBytes, 1024) : ""),
        fact(t("node.f.virt"), f.virt, true),
        fact("IPv6", f.hasIpv6 ? t("common.yes") : t("common.no")),
        fact(t("node.f.booted"), f.bootUnix ? fmt.dateTime(f.bootUnix) : ""),
      ].filter((x) => x.v !== "")
    : [];
  return (
    <SvCard aria-label={t("node.server.host")}>
      <SvHead title={t("node.server.host")} hint={t("np.sv.host.hint")} />
      {facts.length === 0 ? <p className="sv-note !border-t-0 !pt-0">{t("node.factsNone")}</p> : <Facts className="sv-facts sv-host" items={facts} />}
      {f && f.engines.length > 0 && (
        <div className="sv-eng">
          <span>{t("node.f.engines")}</span>
          {f.engines.map((e) => (
            <span key={e} className="tag">
              {e}
            </span>
          ))}
        </div>
      )}
    </SvCard>
  );
}

function RetireZone({ nodeId, name, onUnreached }: { nodeId: string; name: string; onUnreached: () => void }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  return (
    <SvCard tone="danger" aria-label={t("node.danger")}>
      <SvHead title={t("node.danger")} hint={t("np.sv.del.hint")} />
      <SvRow title={t("node.retire")} hint={t("node.retireText")}>
        <Button variant="danger" size="lg" onClick={() => setOpen(true)}>
          {t("node.retire")}
        </Button>
      </SvRow>
      {open && <RetireModal nodeId={nodeId} name={name} onClose={() => setOpen(false)} onUnreached={onUnreached} />}
    </SvCard>
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
  // the relays with this node at either end go with it (relay.proto)
  const relays = useRelaysOf(nodeId, useIsOwner()).length;
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
        {[...(["node.retireList1", "node.retireList2", "node.retireList3"] as const).map((k) => t(k)), ...(relays > 0 ? [t.n("relay.retireAlso", relays)] : [])].map((text) => (
          <li key={text} className="flex gap-2 text-[13px] leading-snug">
            <span aria-hidden className="font-extrabold text-danger">
              ·
            </span>
            <span className="text-pretty">{text}</span>
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
