import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState, type ReactNode, type RefObject } from "react";
import { originOf, playWave } from "@/components/fx/wave";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { StatusDot } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { health } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { quoteNames } from "@/lib/doctor-detail";
import { fixErrorText, fixLabel, lookup, restartConsequence, useCan, type DoctorItem, type FixRequest } from "@/lib/health";
import { plain, type Plain } from "@/lib/plain";
import type { FixPlan } from "@/gen/mistgate/admin/v1/health_pb";
import "./alert-body.css";

// The two-step fix (health.proto ApplyFix): ask the node what it would do (dry run), show that, and only a click on
// "Apply" performs it with the one-time plan id. While it runs the row says "Fixing…", afterwards "Fixed".

export type FixTarget = FixRequest & {
  /** Identifies the row that shows the progress (node + check + inbound). */
  key: string;
  nodeId: string;
  nodeName: string;
  /** The doctor item this fix belongs to, so a list can go on showing the row as "Fixed" after the item left the report. */
  item?: DoctorItem;
  /** The profile a restart_inbound restarts, by name: the button and the dialog say it. */
  profile?: string;
};

type Phase = { phase: "busy" | "done"; target: FixTarget };
type Dialog =
  | { target: FixTarget; state: "asking" }
  | { target: FixTarget; state: "ready"; plan: Plain<FixPlan>; planId: string }
  | { target: FixTarget; state: "error"; error: string };

const doneFor = 30_000; // "Fixed" stays on the row until the re-check report replaces it

/** What a scene of the dev kit starts the flow with: the window asking (its plan in), the fixes that run, what a failed one said. A real page starts with none. */
export type FixSeed = { dlg?: { target: FixTarget; plan: Plain<FixPlan>; planId: string }; busy?: FixTarget[]; errors?: Record<string, string> };

/**
 * What the board says of a fix while it runs ("Сжимаю журнал на de1…") and when it is done ("Журнал на de1 сжат"): a line of the fix's own where there is one
 * (the two the board draws), else null and the caller says the general words.
 */
export function fixLine(t: ReturnType<typeof useT>, kind: "run" | "ran", target: Pick<FixTarget, "fixId" | "nodeName" | "profile">): string | null {
  return lookup(t, `hl.fix.${kind}.${target.fixId}`, { node: target.nodeName, profile: target.profile ? quoteNames(t, target.profile) : "" });
}

export type FixFlow = {
  start: (target: FixTarget) => void;
  phase: (key: string) => "idle" | "busy" | "done";
  /** How many times the fix of this key has failed since the page opened (a card shakes when it grows). */
  failures: (key: string) => number;
  /** What the node said when the fix of this key failed last, until it is tried again (the Health page shows it in the row). */
  error: (key: string) => string;
  /** Fixes that are running or finished a moment ago. */
  pending: () => FixTarget[];
  modal: ReactNode;
};

/**
 * `inline`: the page shows a failure in the row itself (the board's "Could not do it" box with "Try again"), so no toast says it twice.
 * The node's own page leaves it off: its toast is the only word.
 */
export function useFixFlow({ inline = false, seed }: { inline?: boolean; seed?: FixSeed } = {}): FixFlow {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [dlg, setDlg] = useState<Dialog | null>(() => (seed?.dlg ? { state: "ready", ...seed.dlg } : null));
  const [phases, setPhases] = useState<Record<string, Phase>>(() => Object.fromEntries((seed?.busy ?? []).map((target) => [target.key, { phase: "busy" as const, target }])));
  const [failed, setFailed] = useState<Record<string, number>>({});
  const [errors, setErrors] = useState<Record<string, string>>(seed?.errors ?? {});
  const seq = useRef(0); // a dry run that comes back after the dialog was closed is ignored
  const timers = useRef<number[]>([]);
  useEffect(() => () => timers.current.forEach(clearTimeout), []);

  const setPhase = useCallback((key: string, p: Phase | null) => {
    setPhases((prev) => {
      const next = { ...prev };
      if (p) next[key] = p;
      else delete next[key];
      return next;
    });
  }, []);

  const refresh = useCallback(() => {
    for (const k of ["health", "overview", "nodes", "node"]) void qc.invalidateQueries({ queryKey: [k] });
  }, [qc]);

  const start = useCallback(
    (target: FixTarget) => {
      const mine = ++seq.current;
      setErrors((e) => (target.key in e ? Object.fromEntries(Object.entries(e).filter(([k]) => k !== target.key)) : e));
      setDlg({ target, state: "asking" });
      health
        .applyFix({ nodeId: target.nodeId, fixId: target.fixId, params: target.params, dryRun: true })
        .then((r) => {
          const res = plain(r);
          if (mine !== seq.current || !res.plan) return;
          setDlg({ target, state: "ready", plan: res.plan, planId: res.planId });
        })
        .catch((e) => {
          if (mine === seq.current) setDlg({ target, state: "error", error: errorText(e, t) });
        });
    },
    [t],
  );

  const close = useCallback(() => {
    seq.current++;
    setDlg(null);
  }, []);

  const confirm = async (d: Extract<Dialog, { state: "ready" }>) => {
    const { target } = d;
    close();
    setPhase(target.key, { phase: "busy", target });
    const label = fixLabel(t, target.fixId, target.profile);
    try {
      const r = plain(await health.applyFix({ nodeId: target.nodeId, fixId: target.fixId, params: target.params, dryRun: false, planId: d.planId }));
      if (!r.applied) {
        setPhase(target.key, null);
        setFailed((f) => ({ ...f, [target.key]: (f[target.key] ?? 0) + 1 }));
        const said = t("hl.fix.failed", { error: fixErrorText(t, r.error) });
        setErrors((e) => ({ ...e, [target.key]: said }));
        if (!inline) toast.error(said);
        return;
      }
      setPhase(target.key, { phase: "done", target });
      timers.current.push(window.setTimeout(() => setPhase(target.key, null), doneFor));
      toast(r.resultParams.noop === "1" ? t("hl.fix.doneNothing", { node: target.nodeName }) : (fixLine(t, "ran", target) ?? t("hl.fix.done", { node: target.nodeName, label })));
    } catch (e) {
      setPhase(target.key, null);
      setFailed((f) => ({ ...f, [target.key]: (f[target.key] ?? 0) + 1 }));
      const said = errorText(e, t);
      setErrors((x) => ({ ...x, [target.key]: said }));
      if (!inline) toast.error(said);
    } finally {
      refresh();
    }
  };

  const phase = useCallback((key: string) => phases[key]?.phase ?? "idle", [phases]);
  const failures = useCallback((key: string) => failed[key] ?? 0, [failed]);
  const error = useCallback((key: string) => errors[key] ?? "", [errors]);
  const pending = () => Object.values(phases).map((p) => p.target);
  return { start, phase, failures, error, pending, modal: <FixDialog dlg={dlg} onClose={close} onConfirm={confirm} /> };
}

/**
 * The wave across the window when a fix of this key has just worked: from the middle of `ref` (the open row of the alert, the card of
 * the finding), as the board plays it. With the effects off or with reduced motion nothing plays (components/fx/wave.ts).
 */
export function useFixWave(flow: FixFlow, key: string, ref: RefObject<Element | null>) {
  const phase = key ? flow.phase(key) : "idle";
  const was = useRef(phase);
  useEffect(() => {
    if (was.current === "busy" && phase === "done") playWave(originOf(ref.current), { light: document.documentElement.dataset.theme === "light" });
    was.current = phase;
  }, [phase, ref]);
}

function FixDialog({ dlg, onClose, onConfirm }: { dlg: Dialog | null; onClose: () => void; onConfirm: (d: Extract<Dialog, { state: "ready" }>) => void }) {
  const t = useT();
  // the last dialog stays rendered while the modal fades out
  const [last, setLast] = useState<Dialog | null>(dlg);
  if (dlg && dlg !== last) setLast(dlg);
  const d = dlg ?? last;
  if (!d) return null;
  const label = fixLabel(t, d.target.fixId);
  const plan = d.state === "ready" ? d.plan : null;
  const nothing = plan?.params.noop === "1";
  // a restart is asked as a question with the profiles by name and who drops; the node's own words go under "Details"
  const restart = d.target.fixId === "restart_inbound";
  const names = plan?.params.profiles || d.target.profile || "";
  const many = names.includes(",");
  const title =
    restart && names ? t("hl.restart.title", { profiles: quoteNames(t, names), node: d.target.nodeName }) : t("hl.fix.title", { label, node: d.target.nodeName });
  const params = plan ? { ...plan.params, profiles: quoteNames(t, plan.params.profiles || plan.params.inbounds || "") } : {};
  // a fix the SPA has no words for says what the node says, in the open
  const worded = plan ? lookup(t, plan.titleKey, params) : null;
  const sentence = plan && !restart ? (worded ?? plan.detail) : "";
  const consequence = plan
    ? restart
      ? (restartConsequence(t, many ? "many" : "one", plan.params.online) ?? t("hl.fix.disruptive"))
      : plan.disruptive
        ? t(
            d.target.fixId === "reconnect_warp"
              ? "hl.fix.disruptiveWarp"
              : d.target.fixId === "refresh_unit"
                ? "hl.fix.disruptiveUnit"
                : d.target.fixId === "awg_module_repair"
                  ? "hl.fix.disruptiveAwg"
                  : "hl.fix.disruptive",
          )
        : ""
    : "";

  return (
    <Modal
      open={!!dlg}
      onOpenChange={(o) => !o && onClose()}
      title={title}
      // the board's window of a fix (.hl-dlg): 480 wide, its words 14/24 in the second grey, "Cancel" and the action at the end
      closeLabel={t("common.close")}
      className="md:max-w-[480px]!"
      footer={
        <>
          <Button variant="secondary" size="lg" onClick={onClose}>
            {d.state === "ready" && !nothing ? t("common.cancel") : t("common.close")}
          </Button>
          {d.state === "ready" && !nothing && (
            <Button variant="primary" size="lg" onClick={() => onConfirm(d)}>
              {restart ? t("hl.restart.do") : t("hl.fix.apply")}
            </Button>
          )}
        </>
      }
    >
      <div className="hl-dlgb">
        {d.state === "asking" && <p>{t("hl.fix.asking")}</p>}
        {d.state === "error" && (
          <Notice tone="danger" className="items-start">
            {d.error}
          </Notice>
        )}
        {plan &&
          (nothing ? (
            <p>{t("hl.fix.nothing")}</p>
          ) : (
            <>
              {sentence && <p>{sentence}</p>}
              {plan.disruptive ? <p>{consequence}</p> : <p>{t(d.target.fixId === "enable_security_updates" ? "hl.fix.securityUpdates" : d.target.fixId === "install_nftables" ? "hl.fix.nftables" : "hl.fix.safe")}</p>}
              {plan.detail && sentence !== plan.detail && (
                <details className="group hl-dl-more">
                  <summary className="flex w-fit cursor-pointer list-none items-center gap-1 font-medium text-muted select-none hover:text-fg [&::-webkit-details-marker]:hidden">
                    {t("hl.fix.details")}
                    <span aria-hidden className="transition-transform duration-200 group-open:rotate-90">
                      ›
                    </span>
                  </summary>
                  <p className="hl-dl-det">{plan.detail}</p>
                </details>
              )}
            </>
          ))}
      </div>
    </Modal>
  );
}

/** The fix button of a row or card, or what replaces it while the fix runs and right after. Nothing for a role that cannot fix. */
export function FixControl({ flow, target, variant = "secondary" }: { flow: FixFlow; target: FixTarget; variant?: "primary" | "secondary" }) {
  const t = useT();
  const can = useCan();
  const phase = flow.phase(target.key);
  if (phase === "busy")
    return (
      <span className="flex items-center gap-2 text-xs font-medium text-muted" role="status">
        <StatusDot kind="busy" />
        {t("hl.fix.fixing")}
      </span>
    );
  if (phase === "done") return <span className="text-xs font-medium text-muted">{t("hl.fix.fixed")}</span>;
  if (!can.fix) return null;
  return (
    <Button variant={variant} size="md" onClick={() => flow.start(target)}>
      {fixLabel(t, target.fixId, target.profile)}
    </Button>
  );
}
