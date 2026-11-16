import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { halo } from "@/components/fx/halo";
import { Button } from "@/components/ui/button";
import { Chip } from "@/components/ui/bits";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { StatusDot, StatusPill, type StatusKind } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { relays as relaysApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { msText } from "@/lib/health";
import { nodesQuery } from "@/lib/queries";
import type { RelayMsg, Stage } from "@/lib/relay";
import {
  activeOf,
  blockedOf,
  blockedText,
  bridgeErrorText,
  bridgeFailed,
  bridgeLoad,
  bridgeMaxPool,
  directStreakGoal,
  modeOf,
  modes,
  reasonText,
  speedKind,
  speedOk,
  speedReason,
  type Mode,
} from "@/lib/relay-bridge";

// The way a relay works, in the rows of the relay lists (relay.proto Relay.mode, active_mode, mode_reason, bridge): direct, or
// through a bridge; why; the bridge's line and load; the choice of the way; the speed check of the bridge's line. Owner's, like
// every relay screen: a reader sees the state without the buttons.

const link = "font-bold underline decoration-dotted underline-offset-2 hover:text-accent-text";

/** The way line of a relay row: the chip of the way it stands in, why, the bridge, its load, the speed checks. */
export function WayLine({ r, nowS, readOnly, onChange, onSpeed }: { r: RelayMsg; nowS: number; readOnly?: boolean; onChange: () => void; onSpeed: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const active = activeOf(r);
  const mode = modeOf(r);
  const reverse = active === "reverse";
  const n = { entry: r.viaNodeName, exit: r.exitNodeName };
  const blocked = blockedOf(r);
  // a UDP server stays direct and says so in the choice of the way, not on every row of the list
  const why = r.modeReason === "udp" ? "" : reasonText(t, r);
  const b = r.bridge;
  const load = bridgeLoad(b);
  const ago = (unix: number) => fmt.ago(unix, nowS * 1000);
  const meta = [mode === "auto" ? t("relay.way.auto") : t("relay.way.owner"), r.modeChangedUnix > 0 ? t("relay.way.since", { time: fmt.stamp(r.modeChangedUnix) }) : ""].filter(Boolean).join(" · ");
  const port = r.check?.reverseReach?.port ?? 0;

  // the bridge, in a line: held (with what it carries), not held (and why), or still coming up
  const held = reverse && b?.connected;
  const bridgeError = b ? bridgeErrorText(t, b.error, n) : "";
  const parts = b
    ? [
        b.pool > 0 ? t("relay.bridge.conns", { conns: b.conns, pool: b.pool }) : t.n("relay.bridge.connsNoPool", b.conns),
        b.rttMs > 0 ? msText(t, fmt, b.rttMs) : "",
        b.sinceUnix > 0 ? t("relay.bridge.since", { time: fmt.duration(Math.max(60, nowS - b.sinceUnix)) }) : "",
        port > 0 ? t("relay.bridge.port", { port, entry: r.viaNodeName }) : "",
      ].filter(Boolean)
    : [];
  const speed = r.check?.reverseSpeed;
  const direct = r.check?.directSpeed;
  const askSpeed = !readOnly && blocked === "";
  // a bridge that is not held is the row's red line already (the stored check says it): the way says it once
  const failed = bridgeFailed(r);

  return (
    <div data-testid="relay-way" data-active={active} className={cx("flex flex-col gap-1.5", (reverse || !!why) && "rounded-field border border-line bg-surface-2/50 px-3 py-2.5")}>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5">
        {/* how the line is held, said once where the way is named */}
        <Chip className="gap-1.5" title={t(`relay.way.how.${active}`, n)}>
          <Icon name={reverse ? "link" : "share"} size={12} />
          {t(`relay.way.opt.${active}`)}
        </Chip>
        <span className="text-xs text-muted">{meta}</span>
        {!readOnly && (
          <div className="grid basis-full gap-1.5 sm:ms-auto sm:flex sm:basis-auto sm:flex-wrap sm:items-center">
            <Button variant="secondary" size="sm" onClick={onChange}>
              {t("relay.way.change")}
            </Button>
            {askSpeed && (
              <Button variant="secondary" size="sm" onClick={onSpeed}>
                {t("relay.speed.button")}
              </Button>
            )}
          </div>
        )}
      </div>
      {(reverse || why) && (
        <div className="flex flex-col gap-1 text-xs leading-snug text-pretty">
          {why && (
            <p className="text-muted">
              {why}
              {!readOnly && (blocked === "entry_agent_old" || blocked === "exit_agent_old") && (
                <>
                  {" "}
                  <Link to="/updates" className={link}>
                    {t("relay.openUpdates")}
                  </Link>
                </>
              )}
            </p>
          )}
          {reverse && mode === "auto" && (
            <p data-testid="relay-streak" className="text-muted">
              {t("relay.way.streak", { k: Math.min(r.directStreak, directStreakGoal) })}
            </p>
          )}
        </div>
      )}
      {reverse && (
        <div data-testid="relay-bridge" className="flex flex-col gap-1 text-xs leading-snug">
          {held ? (
            <p className="flex min-w-0 items-start gap-1.5">
              <span className="mt-[4px] flex">
                <StatusDot kind="ok" />
              </span>
              <span className="text-muted">{[t("relay.bridge.up"), ...parts].join(" · ")}</span>
            </p>
          ) : failed ? null : b && (b.error || !b.connected) && r.enabled ? (
            <p className="flex min-w-0 items-start gap-1.5">
              <span className="mt-[4px] flex">
                <StatusDot kind="bad" />
              </span>
              <span className="font-semibold text-danger-text">{t("relay.bridge.down", { reason: bridgeError || bridgeErrorText(t, "disconnected", n) })}</span>
            </p>
          ) : r.enabled ? (
            <p className="flex min-w-0 items-start gap-1.5">
              <span className="mt-[4px] flex">
                <StatusDot kind="busy" />
              </span>
              <span className="text-muted">{t("relay.bridge.starting")}</span>
            </p>
          ) : null}
          {held && bridgeError && !load?.atLimit && <p className="ps-3.5 text-muted">{t("relay.bridge.note", { reason: bridgeError })}</p>}
          {load && held && (
            <div data-testid="relay-load" className="flex flex-wrap items-center gap-x-2 gap-y-1 ps-3.5">
              <span aria-hidden className="flex gap-0.5">
                {Array.from({ length: 10 }, (_, i) => (
                  <i key={i} className={cx("block h-1.5 w-3 rounded-[2px]", i < load.segments ? (load.nearFull || load.atLimit ? "bg-warn" : "bg-accent") : "bg-surface-2")} />
                ))}
              </span>
              <span className="text-muted">{t("relay.bridge.load", { clients: load.clients, capacity: load.capacity })}</span>
              {load.atLimit ? <StatusPill sm kind="warn" label={t("relay.bridge.atLimit")} /> : load.nearFull ? <StatusPill sm kind="warn" label={t("relay.bridge.nearFull")} /> : null}
            </div>
          )}
          {load?.atLimit && <p className="ps-3.5 text-warn-text">{t("relay.bridge.atLimit.text")}</p>}
          {load && (load.atLimit || load.nearFull) && load.pool >= bridgeMaxPool && (
            <p className="ps-3.5 text-muted">
              {t("relay.bridge.moreRoom")}
              {!readOnly && (
                <>
                  {" "}
                  <Link to="/nodes/$id" params={{ id: r.exitNodeId }} search={{ tab: "ways", add: "1" }} className={link}>
                    {t("relay.bridge.another", { exit: r.exitNodeName })}
                  </Link>
                </>
              )}
            </p>
          )}
        </div>
      )}
      {(speed?.status || (reverse && direct?.status)) && (
        <ul data-testid="relay-speeds" className="flex flex-col gap-1 text-xs leading-snug">
          {speed?.status && <SpeedItem label={t("relay.stage.reverseSpeed", n)} s={speed} n={n} ago={ago} />}
          {reverse && direct?.status && <SpeedItem label={t("relay.stage.directSpeed", n)} s={direct} n={n} ago={ago} />}
        </ul>
      )}
    </div>
  );
}

/** One speed test in its list: the label, the rate it showed or why it did not pass, and when. */
function SpeedItem({ label, s, n, ago }: { label: string; s: Stage; n: { entry: string; exit: string }; ago: (unix: number) => string }) {
  const t = useT();
  const fmt = useFmt();
  const kind: StatusKind = speedKind(s);
  return (
    <li className="flex min-w-0 items-start gap-1.5">
      <span className="mt-[4px] flex">
        <StatusDot kind={kind} />
      </span>
      <span className="min-w-0">
        <span className="text-muted">{label}: </span>
        <span className={cx("font-semibold", s.status === "fail" ? "text-danger-text" : s.status === "ok" ? "text-fg" : "text-muted")}>
          {s.status === "ok" ? speedOk(t, fmt, s) : speedReason(t, s, n)}
        </span>
        {s.atUnix > 0 && <span className="text-muted"> · {ago(s.atUnix)}</span>}
      </span>
    </li>
  );
}

// ---- the choice of the way ----

/**
 * The three ways with a line each: auto, direct, via bridge. The bridge is greyed with its reason when the pair cannot do it
 * (`blocked` is the sentence: UDP, or the agent to update). `entry` fills the lines.
 */
export function WayChoice({ value, onChange, blocked, entry, label }: { value: Mode; onChange: (m: Mode) => void; blocked: string; entry: string; label: string }) {
  const t = useT();
  return (
    <div role="radiogroup" aria-label={label} data-testid="way-choice" className="flex flex-col gap-2">
      {modes.map((m) => {
        const off = m === "reverse" && blocked !== "";
        return (
          <label
            key={m}
            data-off={off || undefined}
            className={cx(
              "flex items-start gap-2.5 rounded-field border border-line px-3 py-2.5 transition-colors duration-200 has-checked:border-accent-line has-checked:bg-accent-soft",
              off ? "cursor-not-allowed" : "cursor-pointer hover:bg-surface-2 has-checked:hover:bg-accent-soft",
            )}
          >
            <input type="radio" name="relay-way" className="mt-0.5 size-4 flex-none accent-accent" value={m} checked={value === m} disabled={off} onChange={() => onChange(m)} />
            <span className="flex min-w-0 flex-col gap-0.5">
              <span className={cx("text-[13px] font-bold", off && "text-muted")}>{t(`relay.way.opt.${m}`)}</span>
              <span className="text-xs leading-snug text-muted">{t(`relay.way.opt.${m}.text`, { entry })}</span>
              {off && <span className="text-xs leading-snug font-semibold text-warn-text">{blocked}</span>}
            </span>
          </label>
        );
      })}
    </div>
  );
}

/** "Change" of a relay row: the way, through the same UpdateRelay the switch and the port use. */
export function ChangeWay({ r, onClose }: { r: RelayMsg; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [value, setValue] = useState<Mode>(modeOf(r));
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  const n = { entry: r.viaNodeName, exit: r.exitNodeName };
  const save = async () => {
    setPending(true);
    setError(null);
    try {
      await relaysApi.updateRelay({ id: r.id, mode: value });
      onClose();
      toast(t("relay.way.saved"));
      halo("save");
    } catch (e) {
      setError(e);
    } finally {
      setPending(false);
      void qc.invalidateQueries({ queryKey: ["relays"] });
    }
  };
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !pending && onClose()}
      title={t("relay.way.dlg.title", n)}
      description={t("relay.way.dlg.intro", n)}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={pending} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="md" disabled={pending || value === modeOf(r)} onClick={() => void save()}>
            {t("relay.way.dlg.save")}
          </Button>
        </>
      }
    >
      <WayChoice value={value} onChange={setValue} blocked={blockedText(t, blockedOf(r), n)} entry={r.viaNodeName} label={t("relay.way.title")} />
      {r.enabled && value !== modeOf(r) && <Notice tone="quiet">{t("relay.way.dlg.drop")}</Notice>}
      {error != null && (
        <Notice tone="danger" className="items-start">
          {errorText(error, t)}
        </Notice>
      )}
    </Modal>
  );
}

// ---- the speed check ----

/**
 * The bridge's line, by speed: the exit sends 8 MB to the entry (CheckRelayRequest.reverse_speed). It moves traffic between
 * two nodes and counts toward their daily limits (the panel holds them), so it asks first and says so.
 */
export function SpeedCheck({ r, onClose }: { r: RelayMsg; onClose: () => void }) {
  const t = useT();
  const qc = useQueryClient();
  const [error, setError] = useState<unknown>(null);
  const [running, setRunning] = useState(false);
  const n = { entry: r.viaNodeName, exit: r.exitNodeName };
  const run = async () => {
    setRunning(true);
    setError(null);
    try {
      await relaysApi.checkRelay({ id: r.id, reverseSpeed: true });
      await qc.invalidateQueries({ queryKey: ["relays"] });
      onClose();
    } catch (e) {
      setError(e);
      setRunning(false);
    }
  };
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !running && onClose()}
      title={t("relay.speed.title", n)}
      description={t("relay.speed.body", n)}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={running} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="md" disabled={running} onClick={() => void run()}>
            {t("relay.speed.run")}
          </Button>
        </>
      }
    >
      <p className="text-xs leading-snug text-pretty text-muted">{t("relay.speed.note", n)}</p>
      {running && (
        <p role="status" className="flex items-center gap-2 text-xs font-semibold">
          <StatusDot kind="busy" />
          {t("relay.speed.running", n)}
        </p>
      )}
      {error != null && (
        <Notice tone="danger" className="items-start">
          {errorText(error, t)}
        </Notice>
      )}
    </Modal>
  );
}

// ---- the exit's card ----

/** "Link to the panel: through msk1": the agent of this node reaches the panel through an entry's bridge (Node.link_via_node_id). */
export function LinkViaLine({ viaNodeId }: { viaNodeId: string }) {
  const t = useT();
  const nodes = useQuery(nodesQuery).data?.nodes ?? [];
  if (!viaNodeId) return null;
  const entry = nodes.find((x) => x.id === viaNodeId)?.name ?? viaNodeId;
  return (
    <Notice tone="quiet" title={t("node.linkVia", { entry })}>
      {t("node.linkVia.text")}
    </Notice>
  );
}
