import { ConnectError } from "@connectrpc/connect";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { StatusDot, type StatusKind } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { Ipv6EntryAction } from "@/gen/mistgate/admin/v1/node_pb";
import { useT, type T } from "@/i18n";
import { nodes as nodesApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorCode, errorText } from "@/lib/errors";
import { useFmt, type Fmt } from "@/lib/format";
import { reasonText, senderLabel } from "@/lib/port-check";
import { plain } from "@/lib/plain";
import type { Ipv6Entry } from "@/lib/ways";
import { Progress, tcpPill } from "./ports";

// The IPv6 entry of a node (ipv6 plan §3.10, node.proto NodeIpv6): an extra server "<node> · IPv6" in every subscription, for
// people whose network blocks the node's IPv4 address. The Ways in tab draws it as a lane; these are its card's parts: the
// state in words, the outside check with its ports, the dialog that checks an address and turns the entry on, and the
// confirmation that turns it off. Only the owner acts (UpdateIpv6Entry and CheckIpv6 refuse anyone else); the rest read.

export type Ipv6Check = NonNullable<Ipv6Entry["lastCheck"]>;
type Node = { id: string; name: string; address: string };

const reasons = ["no_ipv6", "ipv6_broken", "not_checked", "agent_old", "main_is_ipv6"] as const;

/** Why the node cannot take an entry (Node.ipv6.unavailable_reason) in a sentence; "" (it can) says that IPv6 works. */
export function entryReason(t: T, reason: string): string {
  switch (reason) {
    case "":
      return t("ways.ipv6.works");
    case "no_ipv6":
      return t("ways.ipv6.none");
    case "ipv6_broken":
      return t("ways.ipv6.broken");
    case "not_checked":
      return t("ways.ipv6.unknown");
    case "agent_old":
    case "main_is_ipv6":
      return t(`ways.ipv6.why.${reason}`);
  }
  return reason;
}

/** A refusal of CheckIpv6 / UpdateIpv6Entry: the reasons the card words, the entry's own two, else the panel's sentence. */
export function entryErrorText(e: unknown, t: T): string {
  const code = errorCode(ConnectError.from(e).rawMessage);
  if ((reasons as readonly string[]).includes(code)) return entryReason(t, code);
  if (code === "ipv6_entry_on" || code === "ipv6_check_stale") return t(`ways.ipv6.why.${code}`);
  return errorText(e, t);
}

/** Who checked: a node's name, "the panel", or Globalping. */
const senderOf = (t: T, sender: string) => (sender === "globalping" ? "Globalping" : senderLabel(t, sender));

/** Why a check could not run (CheckIpv6Response.error_code): its own two codes, else those of the port check. */
const whyNot = (t: T, code: string) => (code === "no_ipv6_verifier" || code === "nothing_to_check" ? t(`ways.ipv6.why.${code}`) : reasonText(t, code));

/** One port's verdict as a pill: tcp as the port check says it (Globalping's "reachable" only connected), udp ok / lossy / broken. */
export function verdictLook(t: T, v: { network: string; verdict: string }): { kind: StatusKind; label: string } {
  if (v.network === "udp") {
    if (v.verdict === "ok") return { kind: "ok", label: t("ports.ok") };
    if (v.verdict === "lossy") return { kind: "warn", label: t("ways.ipv6.v.lossy") };
    if (v.verdict === "broken") return { kind: "bad", label: t("ways.ipv6.v.broken") };
    return { kind: "off", label: t("ports.unchecked") };
  }
  if (v.verdict === "reachable") return { kind: "ok", label: t("ways.ipv6.v.reachable") };
  if (v.verdict === "failed") return { kind: "off", label: t("ways.ipv6.v.failed") };
  return tcpPill(t, v.verdict);
}

const lower = (s: string) => s.charAt(0).toLocaleLowerCase() + s.slice(1);

/** A check in one sentence: it answers (when, from where), the first port that did not answer as the node, or why it did not run. */
export function checkSentence(t: T, fmt: Fmt, c: Ipv6Check): { tone: "ok" | "bad" | "warn"; text: string } {
  const addr = c.address;
  if (c.errorCode && c.errorCode !== "check_failed") return { tone: "warn", text: t("ways.ipv6.unchecked", { addr, why: whyNot(t, c.errorCode) }) };
  if (c.passed) return { tone: "ok", text: t("ways.ipv6.passed", { addr, ago: fmt.ago(c.checkedUnix), sender: senderOf(t, c.sender) }) };
  const bad = c.verdicts.find((v) => verdictLook(t, v).kind !== "ok") ?? c.verdicts[0];
  return {
    tone: "bad",
    text: t("ways.ipv6.fail", { addr, port: bad ? `${bad.network}/${bad.port}` : "", verdict: bad ? lower(verdictLook(t, bad).label) : "" }),
  };
}

const boxTone = {
  ok: "tone-ok tint border-transparent",
  bad: "border-[color-mix(in_oklch,var(--danger)_45%,var(--border))] bg-danger-soft",
  warn: "border-warn-line bg-warn-soft",
} as const;

/** The answer of a check: its sentence, every port's verdict as a small chip, and what a Globalping-only answer proves. */
export function CheckResult({ check, className }: { check: Ipv6Check; className?: string }) {
  const t = useT();
  const fmt = useFmt();
  const s = checkSentence(t, fmt, check);
  return (
    <div data-testid="ipv6-check" data-tone={s.tone} className={cx("flex flex-col gap-2 rounded-field border px-3 py-2.5 text-xs leading-snug", boxTone[s.tone], className)}>
      <p className={cx("text-pretty", s.tone === "bad" ? "font-semibold text-danger-text" : "text-fg")}>{s.text}</p>
      {check.verdicts.length > 0 && (
        <ul aria-label={t("ports.title")} className="flex flex-wrap gap-1.5">
          {check.verdicts.map((v) => {
            const look = verdictLook(t, v);
            return (
              <li key={`${v.network}/${v.port}`} className="inline-flex h-[26px] items-center gap-1.5 rounded-xl border border-line bg-surface px-2.5 whitespace-nowrap">
                <StatusDot kind={look.kind} />
                <span className="font-mono text-[11px] font-bold">
                  {v.network}/{v.port}
                </span>
                <span className="text-muted">{look.label}</span>
              </li>
            );
          })}
        </ul>
      )}
      {/* a pass says when and from where in its sentence; anything else says it here */}
      {s.tone !== "ok" && check.checkedUnix > 0 && (
        <p className="text-muted">{[fmt.ago(check.checkedUnix), check.sender ? t("ports.row.from", { sender: senderOf(t, check.sender) }) : ""].filter(Boolean).join(" · ")}</p>
      )}
      {check.sender === "globalping" && check.passed && <p className="text-pretty text-warn-text">{t("ways.ipv6.globalping")}</p>}
    </div>
  );
}

/**
 * The entry in words, for the card's State block: on (the address, when and by whom it was checked, the name people see), on
 * while the server's IPv6 is down, off (the main address only; whether IPv4 is cut where people are), or why it cannot be had.
 * The last check stays under it while it says something the line above does not: a failure, or a pass of an address not on.
 */
export function EntryState({ node, v6, v4, country }: { node: Node; v6: Ipv6Entry; v4?: "helps" | "ok"; country: string }) {
  const t = useT();
  const fmt = useFmt();
  const c = v6.lastCheck;
  const down = !!v6.entry && (v6.unavailableReason === "no_ipv6" || v6.unavailableReason === "ipv6_broken");
  const ofEntry = !!c && !!v6.entry && c.address === v6.entry && c.passed;
  let dot: StatusKind = "off";
  let line: string;
  if (v6.entry) {
    dot = down ? "bad" : "ok";
    line = ofEntry
      ? t("ways.ipv6.on", { addr: v6.entry, ago: fmt.ago(c.checkedUnix), sender: senderOf(t, c.sender), node: node.name })
      : t("ways.ipv6.onPlain", { addr: v6.entry, node: node.name });
  } else if (v6.entrySupported) {
    line = t("ways.ipv6.off", { address: node.address });
  } else {
    dot = v6.unavailableReason === "ipv6_broken" ? "bad" : "off";
    line = entryReason(t, v6.unavailableReason);
  }
  // §3.8: the card offers the entry whenever the node can take one, and says whether IPv4 is cut where people are
  const v4line = !v6.entry && v6.entrySupported ? v4 : undefined;
  return (
    <div data-testid="ipv6-state" className="flex flex-col gap-2.5">
      <p className="flex items-start gap-2 text-[13px] leading-snug text-pretty">
        <span className="mt-[5px] flex">
          <StatusDot kind={dot} />
        </span>
        <span className="min-w-0 [overflow-wrap:anywhere]">{line}</span>
      </p>
      {ofEntry && c.sender === "globalping" && <p className="pl-4 text-xs leading-snug text-pretty text-warn-text">{t("ways.ipv6.globalping")}</p>}
      {down && <Notice tone="danger">{t("ways.ipv6.down", { node: node.name })}</Notice>}
      {v4line === "helps" && (
        <p className="tone-ok tone-text flex items-start gap-1.5 pl-4 text-xs leading-snug font-semibold text-pretty">
          <Icon name="check" size={13} aria-hidden className="mt-px flex-none" />
          {t("ways.ipv6.helps", { country })}
        </p>
      )}
      {v4line === "ok" && <p className="pl-4 text-xs leading-snug text-pretty text-muted">{t("ways.ipv6.v4ok", { country })}</p>}
      {c && !ofEntry && (v6.entrySupported || !!v6.entry) && <CheckResult check={c} />}
    </div>
  );
}

/**
 * "IPv6 entry on de1": the addresses the node's uplink holds (its own outbound one first chosen, marked as the server's), then
 * "Only check" (CheckIpv6: nothing changes) or "Check and turn on" (UpdateIpv6Entry ENABLE: a check under ten minutes old is
 * used, else one runs; a check that fails changes nothing). The run takes about 15 s and the window waits for it; the answer
 * stays in the window, except a turn-on that worked, which closes it.
 */
export function EntryDialog({ open, onClose, node, v6, awg }: { open: boolean; onClose: () => void; node: Node; v6: Ipv6Entry; awg: boolean }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const group = useId();
  const addrs = v6.addresses.map((a) => a.address);
  const [picked, setPicked] = useState("");
  const addr = picked || (addrs.includes(v6.src) ? v6.src : (addrs[0] ?? ""));
  const [result, setResult] = useState<Ipv6Check | null>(null);
  const [error, setError] = useState("");

  const finish = () => {
    setPicked("");
    setResult(null);
    setError("");
    onClose();
  };
  const run = useMutation({
    mutationFn: async (on: boolean) => {
      if (!on) return { on, check: plain(await nodesApi.checkIpv6({ nodeId: node.id, address: addr })), code: "" };
      const r = plain(await nodesApi.updateIpv6Entry({ nodeId: node.id, action: Ipv6EntryAction.ENABLE, address: addr }));
      return { on, check: r.check, code: r.errorCode };
    },
    onMutate: () => {
      setResult(null);
      setError("");
    },
    onSuccess: (r) => {
      if (r.on && !r.code) {
        toast(t("ways.ipv6.onDone", { node: node.name, addr }));
        finish();
        return;
      }
      setResult(r.check ?? { address: addr, passed: false, errorCode: r.code, verdicts: [], sender: "", checkedUnix: 0 });
    },
    onError: (e) => setError(entryErrorText(e, t)),
    // the check is kept with the node (Node.ipv6.last_check), and the entry is the node's: the card reads both from GetNode
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ["node", node.id] });
      void qc.invalidateQueries({ queryKey: ["nodes"] });
    },
  });
  const busy = run.isPending;

  return (
    <Modal
      open={open}
      onOpenChange={(o) => !o && !busy && finish()}
      closeLabel={t("common.close")}
      title={t("ways.ipv6.dlg.title", { node: node.name })}
      description={t("ways.ipv6.dlg.intro", { node: node.name })}
      footer={
        <>
          <Button variant="ghost" size="md" className="max-md:hidden" disabled={busy} onClick={finish}>
            {t("common.cancel")}
          </Button>
          <Button variant="secondary" size="md" disabled={busy || !addr} onClick={() => run.mutate(false)}>
            {t("relay.dlg.checkOnly")}
          </Button>
          <Button variant="primary" size="md" disabled={busy || !addr} onClick={() => run.mutate(true)}>
            {t("relay.dlg.checkOn")}
          </Button>
        </>
      }
    >
      <section className="flex flex-col gap-2">
        <SectionLabel as="h3">{t("ways.ipv6.dlg.address")}</SectionLabel>
        <div role="radiogroup" aria-label={t("ways.ipv6.dlg.address")} className="flex flex-col gap-1.5">
          {addrs.map((a) => (
            <label
              key={a}
              className={cx(
                "flex min-h-11 items-center gap-3 rounded-field border border-line bg-surface px-3.5 py-2 transition-colors duration-200 has-checked:border-accent-line has-checked:bg-surface-2",
                busy ? "cursor-not-allowed opacity-60" : "cursor-pointer",
              )}
            >
              <input
                type="radio"
                name={group}
                value={a}
                className="size-4 flex-none accent-accent"
                checked={addr === a}
                disabled={busy}
                onChange={() => {
                  setPicked(a);
                  setResult(null);
                }}
              />
              <span className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
                <span className="font-mono text-[13px] font-bold [overflow-wrap:anywhere]">{a}</span>
                {a === v6.src && <span className="text-xs text-muted">{t("ways.ipv6.dlg.src")}</span>}
              </span>
            </label>
          ))}
        </div>
      </section>
      {awg && <p className="text-xs leading-snug text-pretty text-muted">{t("ways.ipv6.dlg.awg", { address: node.address })}</p>}
      {busy && (
        <div data-testid="ipv6-checking" className="flex flex-col gap-2">
          <p className="flex items-center gap-2 text-[13px] font-semibold [overflow-wrap:anywhere]">
            <StatusDot kind="busy" />
            {t("ways.ipv6.checking", { addr })}
          </p>
          <Progress label={t("ways.ipv6.checking", { addr })} ms={15_000} />
          <p className="text-xs leading-snug text-pretty text-muted">{t("ways.ipv6.checkingNote")}</p>
        </div>
      )}
      {result && !busy && <CheckResult check={result} />}
      {error && !busy && <Notice tone="danger">{error}</Notice>}
    </Modal>
  );
}

/** "Turn off the IPv6 entry on de1?": the "· IPv6" servers leave the subscriptions (UpdateIpv6Entry DISABLE; it always works). */
export function TurnOffDialog({ open, onOpenChange, node }: { open: boolean; onOpenChange: (open: boolean) => void; node: Node }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [error, setError] = useState("");
  const call = useMutation({
    mutationFn: async () => plain(await nodesApi.updateIpv6Entry({ nodeId: node.id, action: Ipv6EntryAction.DISABLE })),
    onSuccess: () => {
      onOpenChange(false);
      toast(t("ways.ipv6.offDone", { node: node.name }));
    },
    onError: (e) => setError(entryErrorText(e, t)),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ["node", node.id] });
      void qc.invalidateQueries({ queryKey: ["nodes"] });
    },
  });
  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        if (!call.isPending) onOpenChange(o);
        if (!o) setError("");
      }}
      title={t("ways.ipv6.offTitle", { node: node.name })}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={call.isPending} onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="md" disabled={call.isPending} onClick={() => call.mutate()}>
            {t("ways.ipv6.turnOff")}
          </Button>
        </>
      }
    >
      <p className="text-[13px] leading-normal text-pretty">{t("ways.ipv6.offBody", { node: node.name })}</p>
      {error && <Notice tone="danger">{error}</Notice>}
    </Modal>
  );
}
