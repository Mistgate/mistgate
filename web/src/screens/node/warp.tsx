import { Code, ConnectError } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Flourish } from "@/components/fx/flourish";
import { Button } from "@/components/ui/button";
import { Chip } from "@/components/ui/bits";
import { QueryError } from "@/components/ui/query-error";
import { StatusPill } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { useT } from "@/i18n";
import { warp as warpApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { effectsEnabled, prefersReducedMotion } from "@/lib/effects";
import { useFmt } from "@/lib/format";
import { useNow } from "@/lib/time";
import { ageParts, probeLook, safeHttpUrl, sourceWord, warpPill, warpTrouble, type ProbeLook } from "@/lib/warp";
import { attentionWords, describeWarpError, warpCheckFailed } from "@/lib/warp-error";
import { Glyph, glyphs } from "@/screens/overview-parts";
import { useTx } from "@/screens/users/t";
import { Facts } from "./server-parts";
import { Wa } from "./parts";
import { warpError, warpQuery, type WarpData } from "./warp-model";
import { DeleteWarpDialog, ImportWarpDialog, LicenseWarpDialog, PauseWarpDialog, RegisterWarpDialog } from "./warp-dialogs";
import "./exit.css";

// The WARP card (the board's .nx-warp): a head with the account and the state, then one of three bodies: nothing connected yet, the
// connection being made (four steps and a wave of dots), or the account as three rows (the tunnel, the traffic, the check of the
// exit), what can be done with it, the details and the plain statement. The panel talks to Cloudflare only when a button here is
// pressed; the card says so and links the terms.

const cloud = "M7 18a4 4 0 0 1-.6-7.96A5.5 5.5 0 0 1 17 9.5a4.25 4.25 0 0 1 .5 8.5H7z";
const iconTunnel = "M4 12h5l2-6 3 12 2-6h4";
const iconTraffic = "M8 4v15M4 15l4 4 4-4M16 20V5M12 9l4-4 4 4";
const iconCheck = "M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6zM9 12l2 2 4-4";
const iconRestart = "M20 12a8 8 0 1 1-2.3-5.6M20 4v4h-4";
const iconPause = "M9 5v14M15 5v14";
const iconPlay = "M8 5l11 7-11 7z";
const iconChevron = "M7 10l5 5 5-5";
const iconInfo = "M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17zM12 11v5M12 8v.01";

export function useAge() {
  const t = useTx();
  const now = Math.floor(useNow(5_000) / 1000);
  return (unix: number) => {
    const a = ageParts(unix, now);
    if (!a) return t("warp.never");
    if (a.unit === "s") return t("users.now");
    return t(`users.ago.${a.unit}` as "users.ago.m", { n: a.n });
  };
}

/** "4 s ago" for a check (the checks come every 5-30 s, so "now" would say nothing), minutes and more as everywhere else. */
function useCheckedAgo() {
  const t = useTx();
  const age = useAge();
  const now = Math.floor(useNow(5_000) / 1000);
  return (unix: number) => {
    const a = ageParts(unix, now);
    return a?.unit === "s" ? t("warp.secAgo", { n: a.n }) : age(unix);
  };
}

/** How long the card glows after a link brought the owner to it (#warp). */
const glowMs = 2000;

/**
 * WARP of a node: the account (registered here or imported), its state and probes, and what the owner can do with
 * it. `down`: the node does not answer, what the card shows is the last it heard. `profiles`: how many profiles the node has.
 */
export function WarpCard(props: { nodeId: string; nodeName: string; retired: boolean; down?: boolean; profiles?: number }) {
  const ref = useRef<HTMLDivElement>(null);
  // every "Open WARP" links here (?tab=exit#warp): bring the card into view and light it up for a moment
  const [glow, setGlow] = useState(() => window.location.hash === "#warp");
  useEffect(() => {
    if (window.location.hash !== "#warp") return;
    ref.current?.scrollIntoView?.({ block: "start", behavior: "smooth" });
    const id = window.setTimeout(() => setGlow(false), glowMs);
    return () => window.clearTimeout(id);
  }, []);
  return (
    <div
      id="warp"
      ref={ref}
      data-glow={glow || undefined}
      className={cx("min-w-0 scroll-mt-4 rounded-card transition-shadow duration-500", glow && "shadow-[0_0_0_2px_var(--accent),0_0_0_6px_var(--accent-soft)]")}
    >
      <WarpCardBody {...props} />
    </div>
  );
}

type WarpDialog = "register" | "again" | "import" | "license" | "delete" | "pause" | null;
/** The four steps of a registration, as the board draws them; the call itself is one request, the steps are where it is likely to be. */
const stepMs = 1350;
const steps = ["account", "keys", "tunnel", "check"] as const;

function WarpCardBody({ nodeId, nodeName, retired, down, profiles }: { nodeId: string; nodeName: string; retired: boolean; down?: boolean; profiles?: number }) {
  const t = useTx();
  const toast = useToast();
  const guard = useStepUp();
  const qc = useQueryClient();
  const card = useRef<HTMLElement>(null);
  const q = useQuery(warpQuery(nodeId));
  const [dialog, setDialog] = useState<WarpDialog>(null);
  // a registration under way: the step it is likely at (4 = the call is back), and a note for the dialog if it failed
  const [reg, setReg] = useState<number | null>(null);
  const [regError, setRegError] = useState<{ text: string; rateLimited: boolean } | null>(null);
  const [just, setJust] = useState(false);
  const stepTimer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearInterval(stepTimer.current), []);
  const d = q.data;

  async function register(again: boolean, tosUrl: string) {
    setRegError(null);
    try {
      await guard(async () => {
        // past the confirmation: the dialog gives way to the progress
        setDialog(null);
        setReg(0);
        window.clearInterval(stepTimer.current);
        stepTimer.current = window.setInterval(() => setReg((k) => (k === null ? k : Math.min(3, k + 1))), stepMs);
        await warpApi.registerWarp({ nodeId, acceptTos: true, tosUrlShown: tosUrl, replaceExisting: again });
      });
      window.clearInterval(stepTimer.current);
      setReg(4);
      await Promise.all([qc.invalidateQueries({ queryKey: ["warp", nodeId] }), qc.invalidateQueries({ queryKey: ["node", nodeId] }), qc.invalidateQueries({ queryKey: ["nodes"] })]);
      window.setTimeout(() => {
        setReg(null);
        setJust(true);
        window.setTimeout(() => setJust(false), 3000);
      }, 750);
      toast(t(again ? "warp.reregistered" : "warp.registered"));
    } catch (e) {
      window.clearInterval(stepTimer.current);
      setReg(null);
      if (isStepUpCancelled(e)) return;
      // the window opens again with the reason above its buttons
      setRegError({ text: warpError(e, t), rateLimited: ConnectError.from(e).code === Code.ResourceExhausted });
      setDialog(again ? "again" : "register");
    }
  }

  const frame = "nx-warp relative rounded-card border border-line bg-surface";
  // a poll that failed keeps the card it had: only a card with nothing to show says so
  if (!d)
    return (
      <section className={frame} aria-label="WARP">
        <Head title={t("np.wp.title")} sub={q.isError ? "" : t("common.loading")} />
        {q.isError && (
          <div className="px-6 pb-5 max-[720px]:px-4">
            <QueryError error={q.error} onRetry={() => void q.refetch()} />
          </div>
        )}
      </section>
    );
  const close = (o: boolean) => !o && setDialog(null);
  const account = d.account;
  const state = account ? (!account.enabled ? WarpState.DISABLED : (d.health?.state ?? WarpState.UNKNOWN)) : WarpState.NOT_CONFIGURED;
  const h = d.health;
  const failing = !!account && account.enabled && !!h && (warpCheckFailed(h.lastError) || h.probeCloudflare?.ok === false || h.probeOther?.ok === false);
  const pill = warpPill(state, failing);
  const via = (profiles ?? 0) > 0 ? t.n("np.wp.via", d.inbounds.length, { m: profiles ?? 0 }) : "";
  const plus = account?.accountType === "plus";
  const sub =
    reg !== null ? (
      t("np.wp.sub.reg")
    ) : !account ? (
      t("np.wp.sub.none")
    ) : (
      <>
        {[t(sourceWord[account.source]), plus ? "" : account.accountType, via].filter(Boolean).join(" · ")}
        {plus && (
          <Chip tone="lavender" className="ms-1.5 !h-5 align-middle" title={t("warp.plus.badgeTitle")}>
            {t("warp.plus.badge")}
          </Chip>
        )}
      </>
    );

  return (
    <section ref={card} className={cx(frame, just && "just")} aria-label="WARP" aria-busy={reg !== null}>
      {just && <Flourish x={43} y={39} />}
      <Head
        title={t("np.wp.title")}
        sub={sub}
        pill={
          reg !== null ? (
            <StatusPill kind="busy" label={t("np.wp.state.reg")} />
          ) : !account ? (
            <StatusPill kind="off" label={t("warp.state.none")} />
          ) : down ? (
            <StatusPill kind="bad" label={t("np.wp.state.dead")} />
          ) : (
            <StatusPill kind={pill.kind} label={t(pill.word)} />
          )
        }
      />
      {reg !== null ? (
        <RegProgress step={reg} />
      ) : account ? (
        <Account d={d} nodeId={nodeId} retired={retired} down={!!down} onDialog={setDialog} />
      ) : (
        <Empty d={d} retired={retired} onRegister={() => setDialog("register")} onImport={() => setDialog("import")} />
      )}
      <Honest tosUrl={d.tosUrl} />
      <RegisterWarpDialog open={dialog === "register" || dialog === "again"} again={dialog === "again"} onOpenChange={(o) => (close(o), !o && setRegError(null))} tosUrl={d.tosUrl} onImport={() => setDialog("import")} onRegister={() => void register(dialog === "again", d.tosUrl)} error={regError} />
      <ImportWarpDialog open={dialog === "import"} onOpenChange={close} nodeId={nodeId} />
      <LicenseWarpDialog open={dialog === "license"} onOpenChange={close} nodeId={nodeId} hasLicense={!!d.account?.hasLicense} />
      <PauseWarpDialog open={dialog === "pause"} onOpenChange={close} nodeId={nodeId} nodeName={nodeName} inbounds={d.inbounds} />
      <DeleteWarpDialog open={dialog === "delete"} onOpenChange={close} nodeId={nodeId} nodeName={nodeName} hasToken={!!d.account?.hasToken} />
    </section>
  );
}

/** The cloud of the board's logo box, drawn plain (no brand mark: the project keeps none of it). */
export function CloudMark({ className }: { className?: string }) {
  return (
    <span className={cx("cf-m", className)} aria-hidden>
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="size-full">
        <path d={cloud} />
      </svg>
    </span>
  );
}

/** The head: the logo box, the title with what the card is about under it, and the state at the end. */
function Head({ title, sub, pill }: { title: ReactNode; sub: ReactNode; pill?: ReactNode }) {
  return (
    <div className="nx-ch">
      <span className="nd-wi nx-cf" aria-hidden>
        <CloudMark />
      </span>
      <div className="nd-wt">
        <h3>{title}</h3>
        <span>{sub}</span>
      </div>
      {pill && <span className="pillw">{pill}</span>}
    </div>
  );
}

function Empty({ d, retired, onRegister, onImport }: { d: WarpData; retired: boolean; onRegister: () => void; onImport: () => void }) {
  const t = useT();
  const tx = useTx();
  return (
    <>
      <div className="nx-empty">
        <span className="nx-sh" aria-hidden>
          <CloudMark />
        </span>
        <div>
          <b>{t("np.wp.empty.title")}</b>
          <span>{tx("warp.empty")}</span>
          {!d.agentSupports && (
            <span className="!text-warn">
              {tx("warp.agentOld")}{" "}
              <Link to="/updates" className="underline decoration-dotted underline-offset-2">
                {tx("warp.openUpdates")}
              </Link>
            </span>
          )}
          <div className="nx-row">
            <Button variant="primary" size="lg" disabled={retired || !d.agentSupports} onClick={onRegister}>
              {tx("warp.enable")}
            </Button>
            <Button variant="secondary" size="lg" disabled={retired || !d.agentSupports} onClick={onImport}>
              {tx("warp.import")}
            </Button>
          </div>
        </div>
      </div>
    </>
  );
}

/** The registration under way: the line of the step rolls, a wave of dots runs to the finish, the four steps fill in. */
function RegProgress({ step }: { step: number }) {
  const t = useT();
  const now = Math.min(3, step);
  return (
    <div className="nx-reg" role="status" aria-live="polite">
      <div className="nx-rh">
        <span className="nx-sh" aria-hidden>
          <CloudMark />
        </span>
        <div className="nx-rt">
          <div className="nx-roll">
            {steps.map((s, i) => (
              <b key={s} className={i === now ? undefined : i < now ? "prev" : "next"} aria-hidden={i !== now}>
                {t(`np.wp.reg.say.${s}`)}
              </b>
            ))}
          </div>
          <span>{t("np.wp.reg.sub")}</span>
        </div>
        <span className="nx-rn">{t("np.wp.reg.of", { n: Math.min(4, step + 1) })}</span>
      </div>
      <WaveCanvas done={step >= 4} />
      <ol className="nx-st2">
        {steps.map((s, i) => (
          <li key={s} className={step > i ? "done" : step === i ? "now" : undefined}>
            <span className="m" aria-hidden>
              <Glyph d={glyphs.check} size={12} stroke={3} />
            </span>
            <b>{t(`np.wp.reg.step.${s}`)}</b>
          </li>
        ))}
      </ol>
    </div>
  );
}

/**
 * The wave of dots of the board: rows of dots, filled from the start to the finish as the steps pass, a swell at the front, a few sparks
 * past it. Drawn on a canvas; with the effects off (or reduced motion) nothing is drawn, the four steps say it all.
 */
function WaveCanvas({ done }: { done: boolean }) {
  const ref = useRef<HTMLCanvasElement>(null);
  const finished = useRef(done);
  useEffect(() => {
    finished.current = done;
  }, [done]);
  useEffect(() => {
    const cv = ref.current;
    if (!cv || !effectsEnabled() || prefersReducedMotion() || !cv.getContext) return;
    const g = cv.getContext("2d");
    if (!g) return;
    const t0 = performance.now();
    let shown = 0;
    let raf = 0;
    const hex = (h: string, fallback: string) => {
      let s = (/^#[0-9a-f]{3,6}$/i.test(h.trim()) ? h.trim() : fallback).replace("#", "");
      if (s.length === 3) s = s.split("").map((c) => c + c).join("");
      return [parseInt(s.slice(0, 2), 16), parseInt(s.slice(2, 4), 16), parseInt(s.slice(4, 6), 16)] as const;
    };
    const ss = (e0: number, e1: number, v: number) => {
      const q = Math.max(0, Math.min(1, (v - e0) / (e1 - e0)));
      return q * q * (3 - 2 * q);
    };
    const draw = (now: number) => {
      const dpr = Math.min(2, window.devicePixelRatio || 1);
      const W = cv.clientWidth;
      const H = cv.clientHeight;
      if (W === 0 || H === 0) {
        raf = requestAnimationFrame(draw);
        return;
      }
      if (cv.width !== Math.round(W * dpr)) {
        cv.width = Math.round(W * dpr);
        cv.height = Math.round(H * dpr);
      }
      const cs = getComputedStyle(cv);
      const accHex = cs.getPropertyValue("--accent").trim() || "#7dd3a0";
      const A = hex(accHex, "#7dd3a0");
      const I = hex(cs.getPropertyValue("--text"), "#ffffff");
      const t = (now - t0) / 1000;
      const lin = Math.min(1, (now - t0) / (stepMs * 4));
      const target = finished.current ? 1 : lin < 1 ? lin * 0.97 : 0.97;
      shown += (target - shown) * 0.045;
      const p = shown;
      const G = 10;
      const rows = Math.max(3, Math.floor(H / G) - 1);
      const cols = Math.floor(W / G);
      const ox = (W - (cols - 1) * G) / 2;
      const oy = (H - (rows - 1) * G) / 2;
      const mid = (rows - 1) / 2;
      const calm = 1 - ss(0.93, 1, p);
      g.setTransform(dpr, 0, 0, dpr, 0, 0);
      g.clearRect(0, 0, W, H);
      for (let c = 0; c < cols; c++) {
        const x = ox + c * G;
        const u = x / W;
        const dd = p - u;
        const fill = ss(-0.035, 0.035, dd);
        const near = Math.exp(-Math.pow(dd * 7, 2)) * calm;
        const ph = x * 0.038 - t * 2.3;
        const wv = Math.sin(ph);
        const wv2 = Math.sin(ph * 0.5 + 1.3);
        for (let r = 0; r < rows; r++) {
          const k = (r - mid) / (mid || 1);
          const lens = 1 - Math.abs(k) * 0.55;
          const amp = (0.9 * fill + near * 6.5) * calm;
          const y = oy + r * G + wv * amp + k * near * 5 * wv2;
          const rad = 1.05 + fill * 0.5 + near * 1.5 * lens + fill * Math.max(0, wv) * 0.25 * calm;
          const tr = fill * (0.62 - Math.min(0.3, Math.max(0, dd) * 0.5)) + near * 0.5 * lens;
          const al = 0.14 + tr + (1 - calm) * fill * 0.25;
          const hot = near * near * lens * 0.55;
          const m = Math.min(1, fill + near);
          const R = Math.round(I[0] + (A[0] - I[0]) * m);
          const Gc = Math.round(I[1] + (A[1] - I[1]) * m);
          const B = Math.round(I[2] + (A[2] - I[2]) * m);
          g.globalAlpha = Math.min(1, al);
          g.fillStyle = `rgb(${Math.round(R + (255 - R) * hot)},${Math.round(Gc + (255 - Gc) * hot)},${Math.round(B + (255 - B) * hot)})`;
          g.beginPath();
          g.arc(x, y, rad, 0, 6.283);
          g.fill();
        }
      }
      for (let k = 0; k < 7; k++) {
        const life = (t * 0.55 + k * 0.143) % 1;
        const ez = 1 - Math.pow(1 - life, 2);
        const px = p * W + 6 + ez * 84;
        const py = H / 2 + Math.sin(k * 2.1 + 0.6) * (4 + ez * (H / 2 - 10));
        if (px < W - 2) {
          g.globalAlpha = Math.pow(1 - life, 1.6) * 0.85 * calm;
          g.fillStyle = `rgb(${A[0]},${A[1]},${A[2]})`;
          g.beginPath();
          g.arc(px, py, 0.6 + (1 - life) * 1.3, 0, 6.283);
          g.fill();
        }
      }
      g.globalAlpha = 1;
      raf = requestAnimationFrame(draw);
    };
    raf = requestAnimationFrame(draw);
    return () => cancelAnimationFrame(raf);
  }, []);
  return <canvas ref={ref} className="nx-cv" aria-hidden />;
}

function Account({ d, nodeId, retired, down, onDialog }: { d: WarpData; nodeId: string; retired: boolean; down: boolean; onDialog: (dialog: WarpDialog) => void }) {
  const t = useTx();
  const np = useT();
  const fmt = useFmt();
  const age = useAge();
  const checkedAgo = useCheckedAgo();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const a = d.account!;
  const h = d.health;
  const [busy, setBusy] = useState<"" | "refresh" | "resume" | "restart">("");
  const [more, setMore] = useState(false);
  const now = Math.floor(useNow(30_000) / 1000);
  // the account decides "paused" (the node may not have reported it yet); the node's own state decides the rest
  const state = !a.enabled ? WarpState.DISABLED : (h?.state ?? WarpState.UNKNOWN);
  const trouble = warpTrouble(d);
  const refreshAll = () => Promise.all([qc.invalidateQueries({ queryKey: ["warp", nodeId] }), qc.invalidateQueries({ queryKey: ["node", nodeId] })]);

  async function run(what: "refresh" | "resume" | "restart", call: () => Promise<unknown>, done: (r: unknown) => string) {
    setBusy(what);
    try {
      const r = await guard(call);
      await refreshAll();
      toast(done(r));
    } catch (e) {
      if (!isStepUpCancelled(e)) toast.error(warpError(e, t));
    } finally {
      setBusy("");
    }
  }

  const reported = h && h.reportedUnix > 0;
  // What the LATEST check found (the agent clears last_error when a check passes): never the last success, never a stale failure.
  // A paused account has no checks; an agent that predates the per-probe results gives only last_error and the two flags.
  const live = !!h && a.enabled;
  const checked = !!h && h.checkedUnix > 0;
  const failing = live && (warpCheckFailed(h.lastError) || h.probeCloudflare?.ok === false || h.probeOther?.ok === false);
  const words = live ? describeWarpError(t, h.lastError) : null;
  const attention = d.needsAttention ? attentionWords(t, d.attentionReason) : null;
  const checkedUnix = checked ? h.checkedUnix : reported ? h.reportedUnix : 0;
  // the tunnel is down and the account is alive: say for how long, in one line, above the agent's own words
  // ("0 min" says nothing: under a minute it is just "failing")
  const downFor = trouble?.kind === "down" ? now - trouble.since : 0;
  const downLine =
    trouble?.kind === "down"
      ? [
          trouble.since <= 0 ? t("warp.down.never") : downFor < 60 ? t("warp.down.now") : t("warp.down.since", { duration: fmt.duration(downFor) }),
          trouble.checks > 0 ? `(${t.n("warp.down.checks", trouble.checks)})` : "",
        ]
          .filter(Boolean)
          .join(" ")
      : "";

  const probes = [
    { label: t("warp.probe.a"), look: reported ? probeLook(h.probeCloudflare, h.probeCloudflareOk, checked) : probeLook(undefined, undefined, false) },
    { label: t("warp.probe.b"), look: reported ? probeLook(h.probeOther, h.probeOtherOk, checked) : probeLook(undefined, undefined, false) },
  ];
  const probeText = (look: ProbeLook) =>
    [look.word ? t(look.word) : "—", look.failure ? (look.httpStatus !== null ? t(look.failure, { status: look.httpStatus }) : t(look.failure)) : look.ms !== null ? `${fmt.num(look.ms / 1000, 1)} ${t("warp.sec")}` : ""].filter(Boolean).join(" · ");
  const bad = probes.some((p) => p.look.kind === "bad" || p.look.kind === "warn");
  const allOk = probes.every((p) => p.look.kind === "ok");

  // row 1: the tunnel
  const tunnelTone = down ? "crit" : state === WarpState.UP ? "ok" : state === WarpState.DISABLED ? "warn" : state === WarpState.STARTING || state === WarpState.UNKNOWN ? "idle" : "crit";
  const tunnelWord = down ? np("np.wp.tun.nodata") : state === WarpState.UP ? np("np.wp.tun.up") : state === WarpState.DISABLED ? np("np.wp.tun.stopped") : state === WarpState.STARTING ? t("warp.state.starting") : state === WarpState.UNKNOWN ? t("warp.state.unknown") : np("np.wp.tun.fail");
  const tunnelNote = down ? np("np.wp.tun.silent") : a.enabled && reported && h.lastHandshakeUnix > 0 ? np("np.wp.tun.shake", { ago: age(h.lastHandshakeUnix) }) : a.enabled ? np("np.wp.tun.noShake") : np("np.wp.tun.paused");
  // row 3: the check of the exit
  const live3 = !down && a.enabled && live && checked;
  const checkTone = down || (live3 && (bad || failing)) ? "crit" : !a.enabled ? "idle" : live3 && allOk ? "ok" : "idle";
  const checkWord = down || (live3 && (bad || failing)) ? np("np.wp.chk.fail") : !a.enabled ? np("np.wp.chk.idle") : live3 && allOk ? np("np.wp.chk.ok") : t("warp.state.unknown");
  const checkNote = down ? np("np.wp.chk.noData") : !a.enabled ? np("np.wp.chk.paused") : checkedUnix > 0 ? t("warp.checked", { ago: checkedAgo(checkedUnix) }) : "";
  const traffic = reported ? h.rxBytes + h.txBytes : 0;

  const details: { l: ReactNode; v: ReactNode; mono?: boolean }[] = [
    { l: t("warp.endpoint"), v: h?.endpoint || a.endpointV4 || "—", mono: true },
    { l: t("warp.traffic"), v: reported ? `↓ ${fmt.bytes(h.rxBytes)} · ↑ ${fmt.bytes(h.txBytes)}` : "—", mono: true },
    ...(h?.backend ? [{ l: t("warp.detail.backend"), v: h.backend, mono: true }] : []),
    ...(reported && h.warpFlag ? [{ l: t("warp.detail.flag"), v: `warp=${h.warpFlag}`, mono: true }] : []),
    ...(a.useReserved ? [{ l: t("warp.detail.reserved"), v: t("warp.detail.reservedOn") }] : []),
    { l: np("np.wp.d.account"), v: [a.accountType, t(sourceWord[a.source])].filter(Boolean).join(", ") },
    { l: t("warp.plus.detail"), v: t(a.hasLicense ? "warp.plus.detailSet" : "warp.plus.detailNone") },
  ];

  return (
    <>
      {down && <Wa tone="crit" title={np("np.wp.dead")}>{reported ? np("np.wp.deadBody", { ago: age(h.reportedUnix) }) : undefined}</Wa>}
      {d.needsAttention && !(d.attentionReason === "down_after_ladder" && downLine) && (
        <Wa tone="crit" title={t("warp.attention")}>
          {attention ?? t("warp.attentionBody")}
          {!attention && d.attentionReason && <span className="mt-1 block font-mono text-[11px] break-words text-muted">{d.attentionReason}</span>}
          {/* revoked: what the node saw, small; its own advice would argue with "Register again" */}
          {trouble?.kind === "revoked" && words?.head && <span className="mt-1 block text-muted">{words.head}</span>}
        </Wa>
      )}
      {a.licenseError && (
        <Wa tone="warn" title={t("warp.plus.carryFailed")}>
          {t.opt("warp.err." + a.licenseError) ?? a.licenseError}
        </Wa>
      )}
      {a.hasToken === false && <p className="nx-p !pb-3">{t("warp.noToken")}</p>}
      {d.applyError && <Wa tone="crit" title={d.applyError} />}
      {!d.applyError && d.pendingApply && <Wa tone="warn" title={t("warp.pending")} />}
      {!d.agentSupports && <Wa tone="warn" title={t("warp.agentOld")} />}
      {(words || downLine) && trouble?.kind !== "revoked" && !down && (
        <Wa tone={state === WarpState.DOWN || state === WarpState.UNAVAILABLE ? "crit" : "warn"} title={downLine || words?.head || ""}>
          {downLine && words?.head && <span className="block">{words.head}</span>}
          {words?.more}
          {downLine && d.attentionReason === "down_after_ladder" && <span className="mt-1 block">{t("warp.attentionBody")}</span>}
          {words?.ladder && <span className="mt-1 block text-muted">{words.ladder}</span>}
          {words?.raw && <span className="mt-1 block font-mono text-[11px] break-words text-muted">{words.raw}</span>}
        </Wa>
      )}

      <div className="nx-ls">
        <div className="nx-li">
          <span className="g" aria-hidden>
            <Glyph d={iconTunnel} size={16} stroke={1.5} />
          </span>
          <span className="l">{np("np.wp.row.tunnel")}</span>
          <div className="v">
            <b className={tunnelTone}>{tunnelWord}</b>
            <em>{tunnelNote}</em>
          </div>
          <span className="m font-mono">{[h?.colo, h?.endpoint || a.endpointV4].filter(Boolean).join(" · ") || "—"}</span>
        </div>
        <div className="nx-li">
          <span className="g" aria-hidden>
            <Glyph d={iconTraffic} size={16} stroke={1.5} />
          </span>
          <span className="l">{t("warp.traffic")}</span>
          <div className="v">
            <b className="font-mono">{reported ? fmt.bytes(traffic) : "—"}</b>
            <em>{np("np.wp.allTime")}</em>
          </div>
          <span className="m nx-io font-mono">
            <span>
              <i className="dn" aria-hidden />
              {np("np.wp.rx", { n: reported ? fmt.bytes(h.rxBytes) : "—" })}
            </span>
            <span>
              <i className="up" aria-hidden />
              {np("np.wp.tx", { n: reported ? fmt.bytes(h.txBytes) : "—" })}
            </span>
          </span>
        </div>
        <div className="nx-li">
          <span className="g" aria-hidden>
            <Glyph d={iconCheck} size={16} stroke={1.5} />
          </span>
          <span className="l">{np("np.wp.row.check")}</span>
          <div className="v">
            <b className={checkTone}>{checkWord}</b>
            <em>{checkNote}</em>
          </div>
          <span className="m nx-cks">
            {probes.map((p) => {
              const tone = down || !a.enabled ? "" : p.look.kind === "ok" ? "ok" : p.look.kind === "warn" ? "warn" : p.look.kind === "bad" ? "crit" : "";
              return (
                <span key={p.label} className={tone} title={probeText(p.look)}>
                  <Glyph d={tone === "ok" ? glyphs.check : tone === "crit" ? "M6 6l12 12M18 6L6 18" : tone === "warn" ? glyphs.warn : "M6 12h12"} size={14} stroke={2.5} />
                  {p.label}
                  <span className="sr-only">{probeText(p.look)}</span>
                </span>
              );
            })}
          </span>
        </div>
      </div>

      <div className="nx-act">
        {/* Always offered while there is an account: a slow but "up" exit needs them too, and without them the only way to a fresh device
            was Delete + Register, which leaves the node without WARP in between. The trouble that calls for one makes it the primary button. */}
        {trouble?.kind === "revoked" && (
          <Button variant="primary" size="lg" className="!h-9 !px-3 !text-[13px]" disabled={!!busy || retired} onClick={() => onDialog("again")}>
            <Glyph d={iconRestart} size={16} stroke={1.5} />
            {t("warp.reregister")}
          </Button>
        )}
        {a.enabled && trouble?.kind !== "revoked" && (
          <Button
            variant={trouble?.kind === "down" ? "primary" : "secondary"}
            size="lg"
            className="!h-9 !px-3 !text-[13px]"
            disabled={!!busy || retired}
            onClick={() => void run("restart", () => warpApi.restartWarp({ nodeId }), (r) => t((r as { confirmed?: boolean }).confirmed === false ? "warp.restartedUnconfirmed" : "warp.restarted"))}
          >
            {busy === "restart" ? <span aria-hidden className="size-3.5 flex-none animate-[mg-ui-spin_0.8s_linear_infinite] rounded-full border-2 border-current border-t-transparent" /> : <Glyph d={iconRestart} size={16} stroke={1.5} />}
            {busy === "restart" ? t("warp.restarting") : t("warp.restart")}
          </Button>
        )}
        <Button variant="secondary" size="lg" className="!h-9 !px-3 !text-[13px]" disabled={!!busy || retired || !a.hasToken} title={a.hasToken ? undefined : t("warp.noToken")} onClick={() => void run("refresh", () => warpApi.refreshWarp({ nodeId }), () => t("warp.refreshed"))}>
          {busy === "refresh" && <span aria-hidden className="size-3.5 flex-none animate-[mg-ui-spin_0.8s_linear_infinite] rounded-full border-2 border-current border-t-transparent" />}
          {t("warp.refresh")}
        </Button>
        {a.enabled ? (
          <Button variant="secondary" size="lg" className="!h-9 !px-3 !text-[13px]" disabled={!!busy || retired} onClick={() => onDialog("pause")}>
            <Glyph d={iconPause} size={16} stroke={1.5} />
            {t("warp.pause")}
          </Button>
        ) : (
          <Button variant={trouble ? "secondary" : "primary"} size="lg" className="!h-9 !px-3 !text-[13px]" disabled={!!busy || retired} onClick={() => void run("resume", () => warpApi.setWarpEnabled({ nodeId, enabled: true }), () => t("warp.resumed"))}>
            <Glyph d={iconPlay} size={16} stroke={1.5} />
            {t("warp.resume")}
          </Button>
        )}
        <button type="button" className="nx-more" aria-expanded={more} onClick={() => setMore((x) => !x)}>
          {more ? np("np.wp.hide") : t("warp.details")}
          <Glyph d={iconChevron} size={16} stroke={1.5} />
        </button>
      </div>
      {more && (
        <div className="nx-det">
          <Facts items={details} />
          <div className="nx-row">
            {trouble?.kind !== "revoked" && (
              <Button variant="secondary" size="md" disabled={!!busy || retired} onClick={() => onDialog("again")}>
                {t("warp.reregister")}
              </Button>
            )}
            <Button variant="secondary" size="md" disabled={!!busy || retired || !a.hasToken} title={a.hasToken ? undefined : t("warp.noToken")} onClick={() => onDialog("license")}>
              {t(a.hasLicense ? "warp.plus.change" : "warp.plus.add")}
            </Button>
            <Button variant="secondary" size="md" className="nx-del" disabled={!!busy} onClick={() => onDialog("delete")}>
              {t("warp.delete")}
            </Button>
          </div>
        </div>
      )}
    </>
  );
}

/** The plain statement under the card: whose account this is, what it is shared with and what Cloudflare may do. */
function Honest({ tosUrl }: { tosUrl: string }) {
  const t = useTx();
  const url = safeHttpUrl(tosUrl);
  return (
    <p className="nx-warn">
      <Glyph d={iconInfo} size={16} stroke={1.5} />
      <span>
        {t("warp.honest")}{" "}
        {url && (
          <a href={url} target="_blank" rel="noopener noreferrer">
            {t("warp.tosLink")}
          </a>
        )}
      </span>
    </p>
  );
}
