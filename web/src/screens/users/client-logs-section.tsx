import { useEffect } from "react";
import { Button } from "@/components/ui/button";
import { logView, type ClientLog, type LogView } from "@/lib/client-debug";
import { cx } from "@/lib/cx";
import { useFmt, type Fmt } from "@/lib/format";
import { appText, deviceLabel, type LogsCtl } from "./client-logs";
import { RowMenu, deviceName } from "./link-devices";
import type { DeviceN } from "./model";
import { useTx, type Tx } from "./t";
import { P, SetHead } from "./user-bits";

/** The quiet line under a row: when, how much and from which app version; for an open request, who asked and until when. */
function lineOf(l: ClientLog, t: Tx, fmt: Fmt): string {
  const view = logView(l);
  if (view === "open") return [fmt.stamp(l.requestedUnix), t("clientlogs.askedBy", { who: l.actor || "—" })].join(" · ");
  if (view === "received" || view === "unasked") return [fmt.stamp(l.closedUnix), t.n("clientlogs.lines", l.records), appText(l.app)].filter(Boolean).join(" · ");
  return fmt.stamp(view === "expired" ? l.expiresUnix : l.closedUnix || l.requestedUnix);
}

// the board's three tones of a log's state (a dot and a word, never colour alone)
const tone: Record<LogView, "ok" | "warn" | "crit" | "idle"> = { open: "warn", received: "ok", unasked: "ok", declined: "crit", cancelled: "idle", expired: "idle" };

/**
 * "Logs for support", the Logs tab of the person: one row per request or bundle (the newest first, what the panel keeps), the device, a
 * status (a dot and a word), when, how many lines, the app's version. Open and Download for logs that came, Cancel for a request still
 * waiting, Delete for the rest (asks first). A device of the Mistgate app that was never asked has its own row with "Request". Drawn only
 * where `ctl` is (the owner, the VPS edition); the others read why it is not there.
 */
export function ClientLogsSection({ ctl, devices = [] }: { ctl?: LogsCtl; devices?: DeviceN[] }) {
  const t = useTx();
  const fmt = useFmt();
  const logs = ctl?.logs ?? [];
  // an event of the feed leads here with #client-logs: the section is drawn once the list has come, so the scroll waits for it
  useEffect(() => {
    if (ctl && window.location.hash === "#client-logs") document.getElementById("client-logs")?.scrollIntoView({ block: "center" });
  }, [ctl]);
  const asked = new Set(logs.map((l) => l.deviceId));
  const quiet = ctl ? devices.filter((d) => d.enrolled && !asked.has(d.id)) : [];
  return (
    <section id="client-logs" className="up-card" data-shot="client-logs-list">
      <SetHead title={t("clientlogs.title")} line={t("up.logsLine")} />
      {!ctl && <p className="up-empty">{t("up.logsNone")}</p>}
      {ctl && logs.length === 0 && quiet.length === 0 && <p className="up-empty">{t("up.logsEmpty")}</p>}
      {logs.map((l) => {
        const view = logView(l);
        const here = view === "received" || view === "unasked";
        const name = deviceLabel(l);
        const menu = [
          ...(here ? [{ label: t("clientlogs.download"), onClick: () => ctl!.download(l.id), icon: P.copy }] : []),
          ...(view === "open" ? [] : [{ label: t("clientlogs.delete"), onClick: () => ctl!.remove(l), danger: true, icon: P.trash }]),
        ];
        return (
          <div key={l.id} className="up-lg">
            <div>
              <b>{name}</b>
              <span>{lineOf(l, t, fmt)}</span>
            </div>
            <span className={cx("up-gs", tone[view])}>
              <i aria-hidden />
              {t(`clientlogs.state.${view}`)}
            </span>
            {here ? (
              <Button size="md" aria-label={t("clientlogs.open")} onClick={() => ctl!.open(l)}>
                {t("clientlogs.open")}
              </Button>
            ) : view === "open" ? (
              <Button size="md" aria-label={t("clientlogs.cancelAsk")} onClick={() => ctl!.cancel(l.id)}>
                {t("clientlogs.cancelAsk")}
              </Button>
            ) : (
              <span aria-hidden />
            )}
            {menu.length > 0 ? <RowMenu label={t("clientlogs.menu", { name })} items={menu} /> : <span aria-hidden />}
          </div>
        );
      })}
      {quiet.map((d) => {
        const name = deviceName(d, t);
        return (
          <div key={d.id} className="up-lg">
            <div>
              <b>{name}</b>
              <span>{t("up.logsNeverAsked")}</span>
            </div>
            <span className="up-gs idle">
              <i aria-hidden />
              {t("up.logsNo")}
            </span>
            <Button size="md" onClick={() => ctl!.ask({ id: d.id, name })} aria-label={`${t("clientlogs.ask")}: ${name}`}>
              {t("clientlogs.ask")}
            </Button>
            <span aria-hidden />
          </div>
        );
      })}
    </section>
  );
}
