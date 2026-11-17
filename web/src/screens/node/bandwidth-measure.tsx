import { useMutation } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { useEffect, useState, type CSSProperties } from "react";
import { Scramble } from "@/components/fx/scramble-react";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { useT } from "@/i18n";
import { nodes as nodesApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { Spin } from "./parts";
import { capacityOf, roundMbps, runsLine } from "./bandwidth";

/**
 * "Measure" next to the capacity field. The node downloads from the nearest public speed server (Ookla, else Cloudflare and
 * others) for about half a minute, in three runs; the answer, and how many runs worked, is
 * shown, and "Use" puts the rounded slower direction into the field (it is saved with the rest of the form, never by the
 * measurement itself). Owner only, like the API call.
 *
 * The call answers once, with the best run: while it works the three runs of the board are drawn as a guess of where the node is
 * (about ten seconds each), and when it answers the tiles show what came back: the download, the upload and the figure for the field.
 */
export function useBandwidthMeasure(nodeId: string) {
  const measure = useMutation({ mutationFn: () => nodesApi.measureBandwidth({ nodeId }) });
  const r = measure.data;
  const value = r && !r.errorCode && r.downMbps > 0 ? roundMbps(capacityOf(r.downMbps, r.upMbps)) : 0;
  return { measure, r, value };
}
export type Measure = ReturnType<typeof useBandwidthMeasure>;

/** The button at the field's end. */
export function MeasureButton({ m }: { m: Measure }) {
  const t = useT();
  const done = !!m.value;
  return (
    <Button type="button" variant="secondary" size="lg" disabled={m.measure.isPending} aria-busy={m.measure.isPending} onClick={() => m.measure.mutate()}>
      {m.measure.isPending && <Spin />}
      {m.measure.isPending ? t("node.settings.bandwidthMeasuring") : done ? t("np.sv.ch.again") : t("node.settings.bandwidthMeasure")}
    </Button>
  );
}

/** The run in progress: one of three, a second apart from the last one by about ten seconds. */
function useRun(on: boolean) {
  const [run, setRun] = useState(0);
  useEffect(() => {
    if (!on) return;
    const id = setInterval(() => setRun((x) => Math.min(2, x + 1)), 10_000);
    return () => {
      clearInterval(id);
      setRun(0);
    };
  }, [on]);
  return run;
}

/** What the measurement says: the three tiles, the sentences under them, "Use", and what went wrong. Nothing before the first press. */
export function MeasureResult({ m, current, onUse }: { m: Measure; current: string; onUse: (value: string) => void }) {
  const t = useT();
  const { measure, r, value } = m;
  const run = useRun(measure.isPending);

  // what people already on the node carried during the test (it is inside the figures above, not added to them)
  const people = !r || !value ? "" : r.peopleDownMbps > 0 && r.peopleUpMbps > 0
    ? t("node.settings.bandwidthPeopleBoth", { down: r.peopleDownMbps, up: r.peopleUpMbps })
    : r.peopleDownMbps > 0
      ? t("node.settings.bandwidthPeopleDown", { down: r.peopleDownMbps })
      : r.peopleUpMbps > 0
        ? t("node.settings.bandwidthPeopleUp", { up: r.peopleUpMbps })
        : "";

  const failure = (() => {
    if (measure.error) {
      // the node did not answer within the panel's wait: our own sentence, not the generic "network" one
      return ConnectError.from(measure.error).code === Code.DeadlineExceeded ? t("node.settings.bandwidthErr.noAnswer") : errorText(measure.error, t);
    }
    switch (r?.errorCode) {
      case undefined:
      case "":
        return "";
      case "busy":
        return t("node.settings.bandwidthErr.busy");
      case "unreachable":
        return t("node.settings.bandwidthErr.unreachable");
      case "unsupported":
        return t("node.settings.bandwidthErr.unsupported");
      default:
        return t("node.settings.bandwidthErr.failed");
    }
  })();

  const mbps = (n: number) => t("np.sv.ch.mbps", { n });
  const tiles = measure.isPending
    ? [0, 1, 2].map((i) => ({ cls: i < run ? "done" : i === run ? "now" : "", label: t("np.sv.ch.run", { n: i + 1 }), v: i < run ? "…" : "—", w: i < run ? "100%" : "0%", note: i === run ? t("np.sv.ch.going") : "", key: String(i) }))
    : value > 0 && r
      ? [
          { cls: "done", label: t("np.sv.ch.down"), v: mbps(r.downMbps), w: "100%", note: "", key: "d" },
          { cls: "done", label: t("np.sv.ch.up"), v: r.upMbps > 0 ? mbps(r.upMbps) : "—", w: r.upMbps > 0 ? `${Math.min(100, Math.round((r.upMbps / Math.max(r.downMbps, r.upMbps)) * 100))}%` : "0%", note: "", key: "u" },
          { cls: "done best", label: t("np.sv.ch.field"), v: mbps(value), w: `${Math.min(100, Math.round((value / Math.max(r.downMbps, r.upMbps)) * 100))}%`, note: t("np.sv.ch.best"), key: "f" },
        ]
      : [];

  return (
    <>
      {failure && (
        <div className="px-6 pb-4 max-[720px]:px-4">
          <Notice tone="danger">{failure}</Notice>
        </div>
      )}
      {tiles.length > 0 && (
        <div className="sv-runs" role="status">
          {tiles.map((x) => (
            <div key={x.key} className={x.cls}>
              <span>{x.label}</span>
              <b className="mono">{x.v === "…" || x.v === "—" ? x.v : <Scramble value={x.v} once />}</b>
              <i style={{ "--w": x.w } as CSSProperties} aria-hidden />
              <em>{x.note}</em>
            </div>
          ))}
        </div>
      )}
      {value > 0 && r && (
        <div className="sv-body line !pt-3.5">
          <span className="text-[13px] font-medium">
            {r.upMbps > 0 ? t("node.settings.bandwidthMeasured", { down: r.downMbps, up: r.upMbps }) : t("node.settings.bandwidthMeasuredNoUp", { down: r.downMbps })}
          </span>
          {people && <span className="text-[13px] leading-[18px] text-pretty">{people}</span>}
          <span className="text-[13px] leading-[18px] text-pretty text-muted">{t("node.settings.bandwidthMeasuredNote", { runs: runsLine(t, r) })}</span>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
            <Button type="button" variant="primary" size="md" disabled={current.trim() === String(value)} onClick={() => onUse(String(value))}>
              {t("node.settings.bandwidthUse", { value })}
            </Button>
            <span className="min-w-[200px] flex-1 text-[13px] leading-[18px] text-pretty text-muted">{t("node.settings.bandwidthUseNote")}</span>
          </div>
        </div>
      )}
      <p className={cx("sv-note")}>{measure.isPending ? t("node.settings.bandwidthMeasureBusy") : t("node.settings.bandwidthMeasureHint")}</p>
    </>
  );
}
