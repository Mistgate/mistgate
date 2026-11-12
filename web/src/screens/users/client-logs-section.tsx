import { useEffect } from "react";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { logView, type ClientLog } from "@/lib/client-debug";
import { useFmt, type Fmt } from "@/lib/format";
import { appText, deviceLabel, LogStatePill, type LogsCtl } from "./client-logs";
import { DeviceRow, RowMenu, Secondary, SectionHead, Tile, platformGlyph } from "./link-devices";
import { useTx, type Tx } from "./t";

/** The quiet line under a row: when, how much and from which app version; for an open request, who asked and until when. */
function lineOf(l: ClientLog, t: Tx, fmt: Fmt): string {
  const view = logView(l);
  if (view === "open") return [fmt.stamp(l.requestedUnix), t("clientlogs.askedBy", { who: l.actor || "—" })].join(" · ");
  if (view === "received" || view === "unasked") return [fmt.stamp(l.closedUnix), t.n("clientlogs.lines", l.records), appText(l.app)].filter(Boolean).join(" · ");
  return fmt.stamp(view === "expired" ? l.expiresUnix : l.closedUnix || l.requestedUnix);
}

/**
 * "Logs for support" in the person's devices: one row per request or bundle (the newest first, what the panel keeps), the device,
 * a coloured pill for what became of it, when, how many lines, the app's version. Open and Download for logs that came, Cancel for
 * a request still waiting, Delete for the rest (asks first). Drawn only when there is something to show.
 */
export function ClientLogsSection({ ctl }: { ctl: LogsCtl }) {
  const t = useTx();
  const fmt = useFmt();
  const has = ctl.logs.length > 0;
  // an event of the feed leads here with #client-logs: the section is drawn once the list has come, so the scroll waits for it
  useEffect(() => {
    if (has && window.location.hash === "#client-logs") document.getElementById("client-logs")?.scrollIntoView({ block: "center" });
  }, [has]);
  if (!has) return null;
  return (
    <section id="client-logs" className="flex flex-col" data-shot="client-logs-list">
      <SectionHead title={t("clientlogs.title")} hint={t("clientlogs.hint")} />
      <ul className="flex flex-col">
        {ctl.logs.map((l) => {
          const view = logView(l);
          const here = view === "received" || view === "unasked";
          const name = deviceLabel(l);
          const menu = [
            ...(here ? [{ label: t("clientlogs.download"), onClick: () => ctl.download(l.id) }] : []),
            ...(view === "open" ? [] : [{ label: t("clientlogs.delete"), onClick: () => ctl.remove(l), danger: true }]),
          ];
          return (
            <DeviceRow
              key={l.id}
              tile={<Tile icon={platformGlyph({ platform: l.devicePlatform, model: l.deviceModel })} tone="sky" muted={!here && view !== "open"} />}
              name={name}
              line={
                <span className="flex min-w-0 items-center gap-2">
                  <LogStatePill view={view} />
                  <Secondary text={lineOf(l, t, fmt)} />
                </span>
              }
              right={
                here ? (
                  <Button variant="secondary" size="sm" aria-label={t("clientlogs.open")} onClick={() => ctl.open(l)}>
                    <Icon name="file" size={12} />
                    <span className="@max-[400px]:sr-only">{t("clientlogs.open")}</span>
                  </Button>
                ) : view === "open" ? (
                  <Button variant="secondary" size="sm" aria-label={t("clientlogs.cancelAsk")} onClick={() => ctl.cancel(l.id)}>
                    <Icon name="x" size={12} />
                    <span className="@max-[400px]:sr-only">{t("clientlogs.cancelAsk")}</span>
                  </Button>
                ) : undefined
              }
              menu={menu.length > 0 ? <RowMenu label={t("clientlogs.menu", { name })} items={menu} /> : null}
            />
          );
        })}
      </ul>
    </section>
  );
}
