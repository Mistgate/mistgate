import { useQuery } from "@tanstack/react-query";
import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { CopyButton } from "@/components/copy-button";
import { Step } from "@/components/add-node";
import { Chip } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Callout } from "@/components/ui/notice";
import { Segmented } from "@/components/ui/segmented";
import { StatusPill, type StatusKind } from "@/components/ui/status";
import { TextField } from "@/components/ui/text-field";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import {
  hostOf,
  isOutdated,
  ownProbeQuery,
  probeKinds,
  probeLook,
  sortProbes,
  uninstallCommand,
  updateCommand,
  useLatestScript,
  type Issued,
  type OwnProbeRow,
  type ProbeHost,
  type ProbeKind,
  type ProbeLook,
  type useOwnProbeActions,
} from "@/lib/ownprobe";

// The windows and the list of the "Own probes" card (sources.tsx has the card itself): the probes with their states and actions, the
// window that makes one and shows its one-time install command, the confirmation of a revoke, the update command. Nothing here
// holds a command longer than its window: the window is mounted while it is open and its state goes with it.

type Actions = ReturnType<typeof useOwnProbeActions>;

const lookPill: Record<ProbeLook, { kind: StatusKind; word: MessageKey }> = {
  pending: { kind: "blip", word: "pr.state.pending" },
  expired: { kind: "warn", word: "pr.state.expired" },
  online: { kind: "ok", word: "pr.state.active" },
  offline: { kind: "warn", word: "pr.state.offline" },
  netChanged: { kind: "warn", word: "pr.state.netChanged" },
  noFilter: { kind: "blip", word: "pr.state.noFilter" },
  revoked: { kind: "off", word: "pr.state.revoked" },
};

const kindWord = (t: T, kind: string) => (kind === "home" || kind === "mobile" || kind === "dc" ? t(`pr.kind.${kind}`) : kind);

/** The probes: a row each, the ones that want a look first. `now` and `unfiltered` come from the card, which reads the checks. */
export function ProbeList({ probes, now, unfiltered, actions }: { probes: readonly OwnProbeRow[]; now: number; unfiltered: ReadonlySet<string>; actions: Actions }) {
  const t = useT();
  const latest = useLatestScript();
  const [revoking, setRevoking] = useState<OwnProbeRow | null>(null);
  const [updating, setUpdating] = useState<OwnProbeRow | null>(null);
  // the probe that was just asked to check: its row says when it will
  const [asked, setAsked] = useState<string | null>(null);
  return (
    <>
      <ul aria-label={t("pr.block.probes")} className="flex flex-col divide-y divide-line rounded-field border border-line bg-canvas">
        {sortProbes(probes, now, unfiltered).map((p) => (
          <ProbeRow
            key={p.id}
            p={p}
            now={now}
            look={probeLook(p, now, unfiltered)}
            outdated={isOutdated(p, latest)}
            asked={asked === p.id}
            busy={actions.checkNow.isPending || actions.confirmNetwork.isPending}
            onCheck={() => actions.checkNow.mutate(p.id, { onSuccess: () => setAsked(p.id) })}
            onConfirm={() => actions.confirmNetwork.mutate(p.id)}
            onUpdate={() => setUpdating(p)}
            onRevoke={() => setRevoking(p)}
          />
        ))}
      </ul>
      {revoking && (
        <RevokeModal
          probe={revoking}
          busy={actions.revoke.isPending}
          onClose={() => setRevoking(null)}
          onConfirm={() => actions.revoke.mutate(revoking.id, { onSuccess: () => setRevoking(null) })}
        />
      )}
      {updating && <UpdateModal probe={updating} onClose={() => setUpdating(null)} />}
    </>
  );
}

function ProbeRow(p: {
  p: OwnProbeRow;
  now: number;
  look: ProbeLook;
  outdated: boolean;
  asked: boolean;
  busy: boolean;
  onCheck: () => void;
  onConfirm: () => void;
  onUpdate: () => void;
  onRevoke: () => void;
}) {
  const { p: probe, now, look } = p;
  const t = useT();
  const fmt = useFmt();
  const pill = lookPill[look];
  const word = look === "offline" ? t(pill.word, { age: fmt.duration(now - probe.lastSeenUnix) }) : t(pill.word);
  const live = look === "online" || look === "noFilter" || look === "offline";
  const where = [probe.operator || probe.pinnedOperator, probe.city].filter(Boolean).join(" · ");
  // a probe that is not heard from says how long in its pill: "in touch 7 h ago" would say the opposite
  const quiet = look === "revoked" || look === "pending" || look === "expired" || look === "offline";
  const meta: ReactNode[] = [
    where,
    quiet ? "" : probe.lastSeenUnix > 0 ? t("pr.seen", { ago: fmt.ago(probe.lastSeenUnix) }) : t("pr.seen.never"),
    live ? t.n("pr.checks", probe.checksDay) : "",
    probe.version && !quiet ? <span key="v" className="font-mono text-[11px]">{t("pr.version", { v: probe.version })}</span> : "",
  ].filter(Boolean);
  return (
    <li data-testid="probe" data-look={look} className={cx("flex flex-col gap-2 px-3 py-2.5", look === "revoked" && "opacity-60")}>
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
        <span className="min-w-0 text-[13px] font-bold break-words">{probe.name}</span>
        <Chip>{kindWord(t, probe.kind)}</Chip>
        <StatusPill sm kind={pill.kind} label={word} />
        {p.outdated && (
          <span title={t("pr.outdated.hint")} data-testid="probe-outdated">
            <StatusPill sm kind="warn" label={t("pr.outdated")} />
          </span>
        )}
      </div>
      <p className="flex flex-wrap items-baseline gap-x-1.5 gap-y-0.5 text-xs leading-snug text-muted">
        {meta.map((m, i) => (
          <span key={i} className="whitespace-nowrap">
            {m}
            {i < meta.length - 1 && <span aria-hidden> ·</span>}
          </span>
        ))}
      </p>
      {look === "netChanged" && (
        <Callout
          kind="warn"
          action={
            <Button variant="secondary" size="sm" disabled={p.busy} onClick={p.onConfirm}>
              {t("pr.confirmNetwork")}
            </Button>
          }
        >
          {t("pr.confirmNetwork.note", { now: probe.operator || `AS${probe.asn}`, was: probe.pinnedOperator || "?" })}
        </Callout>
      )}
      {look === "pending" && <p className="text-xs leading-snug text-pretty text-muted">{[t("pr.expires", { time: fmt.stamp(probe.tokenExpiresUnix) }), t("pr.pending.note")].join(" ")}</p>}
      {look === "expired" && <p className="text-xs leading-snug text-muted">{t("pr.expired")}</p>}
      {look === "noFilter" && <p className="text-xs leading-snug text-pretty text-muted">{t("pr.noFilter.note")}</p>}
      {look !== "revoked" && (
        <div className="flex flex-wrap items-center gap-2">
          {live && (
            <Button variant="secondary" size="sm" disabled={p.busy} onClick={p.onCheck}>
              {t("pr.checkNow")}
            </Button>
          )}
          {live && (
            <Button variant="secondary" size="sm" onClick={p.onUpdate}>
              {t("pr.update")}
            </Button>
          )}
          <Button variant="ghostDanger" size="sm" className="ms-auto" onClick={p.onRevoke}>
            {t("pr.revoke")}
          </Button>
        </div>
      )}
      {p.asked && <p className="text-xs leading-snug text-muted">{t("pr.checkNow.note")}</p>}
    </li>
  );
}

/** A command in a box of its own: it wraps, never widens the window, and selects whole on a click. */
const commandCls = "rounded-field border border-line bg-canvas p-3.5 font-mono text-[13px] leading-relaxed break-all whitespace-pre-wrap text-fg select-all";

/** Revoke asks first: it cannot be undone. The window also says how to take the script off the device itself. */
export function RevokeModal({ probe, busy, onClose, onConfirm }: { probe: OwnProbeRow; busy: boolean; onClose: () => void; onConfirm: () => void }) {
  const t = useT();
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={t("pr.revoke.title", { name: probe.name })}
      description={t("pr.revoke.body")}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" size="md" disabled={busy} onClick={onConfirm}>
            {t("pr.revoke")}
          </Button>
        </>
      }
    >
      {probe.state !== "pending" && (
        <div className="flex flex-col gap-2">
          <p className="text-xs leading-snug text-muted">{t("pr.revoke.remove")}</p>
          <pre dir="ltr" className={commandCls}>
            {uninstallCommand(hostOf(probe.kind))}
          </pre>
        </div>
      )}
    </Modal>
  );
}

/** The command that updates a probe already standing, for the system it is on (a router is the default for a home or a mobile probe). */
export function UpdateModal({ probe, onClose }: { probe: OwnProbeRow; onClose: () => void }) {
  const t = useT();
  const [host, setHost] = useState<ProbeHost>(hostOf(probe.kind));
  const command = updateCommand(host);
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t("pr.update.title", { name: probe.name })}
      description={t("pr.update.body")}
      closeLabel={t("common.close")}
      footer={
        <Button variant="secondary" size="md" onClick={onClose}>
          {t("common.close")}
        </Button>
      }
    >
      <div className="flex flex-col gap-1.5">
        <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("pr.update.on")}</span>
        <Segmented
          aria-label={t("pr.update.on")}
          variant="inset"
          className="w-full [&>*]:flex-1"
          wrap
          value={host}
          onValueChange={setHost}
          options={[
            { value: "keenetic", label: t("pr.update.keenetic") },
            { value: "server", label: t("pr.update.server") },
          ]}
        />
      </div>
      <div className="flex flex-col gap-2.5">
        <pre tabIndex={0} dir="ltr" data-testid="update-command" className={commandCls}>
          {command}
        </pre>
        <CopyButton value={command} label={t("pr.cmd.copy")} variant="primary" size="md" full />
      </div>
    </Modal>
  );
}

const hasControl = (v: string) => [...v].some((c) => c.charCodeAt(0) < 0x20 || c.charCodeAt(0) === 0x7f);
const labelOk = (v: string) => v.trim().length > 0 && v.trim().length <= 80 && !hasControl(v);

/**
 * "Add a probe": its name, its city (the owner's own words: the panel keeps no address) and where it stands; then the one-time
 * command with the three steps. The command lives in this window's state and nowhere else (not in the query cache, not in the
 * storage, not in a log): the card unmounts the window on close and it is gone. It is said before it goes, and "create again"
 * revokes the probe that waits and makes the same one with a new command. `preset` opens it on the command screen (the dev kit).
 */
export function AddProbeModal({ actions, onClose, preset }: { actions: Actions; onClose: () => void; preset?: Issued }) {
  const t = useT();
  const [name, setName] = useState("");
  const [city, setCity] = useState("");
  const [kind, setKind] = useState<ProbeKind>("home");
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [issued, setIssued] = useState<Issued | null>(preset ?? null);

  const nameError = !labelOk(name) ? t("pr.add.err.name") : undefined;
  const cityError = !labelOk(city) ? t("pr.add.err.city") : undefined;

  async function run(make: () => Promise<Issued>) {
    setBusy(true);
    try {
      setIssued(await make());
    } catch (e) {
      actions.fail(e);
    } finally {
      setBusy(false);
    }
  }
  const request = () => ({ name: name.trim(), city: city.trim(), kind });
  function submit(e: FormEvent) {
    e.preventDefault();
    setTouched(true);
    if (nameError || cityError || busy) return;
    void run(() => actions.create(request()));
  }

  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      wide={!!issued}
      shot={issued ? "probe-command" : "probe-add"}
      title={issued ? t("pr.cmd.title", { name: issued.probe.name }) : t("pr.add.title")}
      description={issued ? t("pr.cmd.body") : t("pr.add.body")}
      closeLabel={t("common.close")}
      footer={
        issued ? (
          <Button variant="primary" size="md" onClick={onClose}>
            {t("pr.cmd.done")}
          </Button>
        ) : (
          <>
            <Button variant="ghost" size="md" disabled={busy} onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button variant="primary" size="md" disabled={busy} type="submit" form="probe-add-form">
              {busy ? t("pr.add.creating") : t("pr.add.create")}
            </Button>
          </>
        )
      }
    >
      {issued ? (
        <CommandSteps issued={issued} busy={busy} onAgain={() => void run(() => actions.recreate(issued, request()))} />
      ) : (
        <form id="probe-add-form" onSubmit={submit} noValidate className="flex flex-col gap-4">
          <TextField label={t("pr.add.name")} hint={t("pr.add.name.hint")} error={touched ? nameError : undefined} value={name} onChange={(e) => setName(e.target.value)} maxLength={80} autoComplete="off" autoFocus />
          <TextField label={t("pr.add.city")} hint={t("pr.add.city.hint")} error={touched ? cityError : undefined} value={city} onChange={(e) => setCity(e.target.value)} maxLength={80} autoComplete="off" />
          <div className="flex flex-col gap-1.5">
            <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("pr.add.kind")}</span>
            <Segmented
              aria-label={t("pr.add.kind")}
              variant="inset"
              value={kind}
              onValueChange={setKind}
              className="w-full [&>*]:flex-1"
              options={probeKinds.map((k) => ({ value: k, label: t(`pr.kind.${k}`) }))}
            />
            <p className="text-xs leading-snug text-muted">{t(`pr.kind.${kind}.hint`)}</p>
          </div>
        </form>
      )}
    </Modal>
  );
}

/** The command, its warning, the three steps (the third one watches the probe come online) and what the person's provider sees. */
function CommandSteps({ issued, busy, onAgain }: { issued: Issued; busy: boolean; onAgain: () => void }) {
  const t = useT();
  const pre = useRef<HTMLPreElement>(null);
  const live = useQuery({ ...ownProbeQuery, refetchInterval: 3_000 });
  const seen = live.data?.probes.find((p) => p.id === issued.probe.id);
  const connected = !!seen && seen.state !== "pending" && seen.lastSeenUnix > 0;

  // the browser refused the clipboard: the text is selected, so Ctrl+C is one key away
  const select = () => {
    const el = pre.current;
    if (!el) return;
    const range = document.createRange();
    range.selectNodeContents(el);
    const sel = window.getSelection();
    sel?.removeAllRanges();
    sel?.addRange(range);
  };

  return (
    <div className="flex flex-col gap-4">
      <Callout kind="warn">{t("pr.cmd.noRepeat")}</Callout>
      <ol className="flex flex-col gap-4">
        <Step n={1} title={t("pr.cmd.step1")}>
          <p className="text-xs leading-snug text-pretty text-muted">{t("pr.cmd.step1.hint")}</p>
        </Step>
        <Step n={2} title={t("pr.cmd.step2")}>
          <pre ref={pre} tabIndex={0} dir="ltr" data-testid="probe-command" className={commandCls}>
            {issued.command}
          </pre>
          <CopyButton value={issued.command} label={t("pr.cmd.copy")} variant="primary" size="md" full onFail={select} />
          <p className="text-xs leading-snug text-pretty text-muted">
            <b className="font-bold text-fg">{t("pr.cmd.note")}</b> {t("pr.cmd.for")}
          </p>
          {issued.probe.kind === "dc" && <p className="text-xs leading-snug text-pretty text-muted">{t("pr.cmd.forServer")}</p>}
          <button
            type="button"
            disabled={busy}
            onClick={onAgain}
            className="w-fit rounded-sm text-xs font-semibold text-muted underline decoration-dotted underline-offset-2 hover:text-fg disabled:opacity-50"
          >
            {busy ? t("pr.cmd.againBusy") : t("pr.cmd.again")}
          </button>
        </Step>
        <Step n={3} title={t("pr.cmd.step3")} done={connected}>
          <div role="status" aria-live="polite" className="flex items-center gap-2.5 rounded-field border border-line bg-surface-2 px-3 py-2.5">
            <StatusPill sm kind={connected ? "ok" : "busy"} label={connected ? t("pr.cmd.connected") : t("pr.state.pending")} />
            <span className="text-xs leading-snug text-pretty text-muted">
              {connected ? [[seen.operator, seen.city].filter(Boolean).join(" · "), t("pr.cmd.connectedBody")].filter(Boolean).join(". ") : t("pr.cmd.waiting")}
            </span>
          </div>
        </Step>
      </ol>
      <WhatTheProviderSees />
      <a
        href={t("pr.cmd.docsUrl")}
        target="_blank"
        rel="noopener noreferrer"
        className="inline-flex w-fit items-center gap-1.5 text-xs font-bold text-accent-text underline decoration-dotted underline-offset-2"
      >
        {t("pr.cmd.docs")}
        <Icon name="externalLink" size={12} aria-hidden />
      </a>
    </div>
  );
}

/** For the person who agrees to host a probe: what their provider will see, in plain words (plan §7). The owner shows it to them. */
export function WhatTheProviderSees() {
  const t = useT();
  return (
    <section aria-labelledby="probe-see" data-testid="probe-see" className="flex flex-col gap-2 rounded-field border border-line bg-surface-2 px-3.5 py-3">
      <h4 id="probe-see" className="flex items-center gap-2 text-[13px] font-bold">
        <Icon name="eye" size={14} aria-hidden className="text-muted" />
        {t("pr.see.title")}
      </h4>
      <ul className="flex list-disc flex-col gap-1.5 ps-4 text-xs leading-normal text-pretty text-muted marker:text-faint">
        {(["pr.see.1", "pr.see.2", "pr.see.3", "pr.see.4"] as const).map((k) => (
          <li key={k}>{t(k)}</li>
        ))}
      </ul>
    </section>
  );
}
