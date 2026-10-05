import { useMutation } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { useT } from "@/i18n";
import { nodes as nodesApi } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { capacityOf, roundMbps } from "./bandwidth";

/**
 * "Measure" next to the capacity field. The node downloads from a public speed server for about ten seconds; the answer is
 * shown, and "Use" puts the rounded slower direction into the field (it is saved with the rest of the form, never by the
 * measurement itself). Owner only, like the API call.
 */
export function BandwidthMeasure({ nodeId, current, onUse }: { nodeId: string; current: string; onUse: (value: string) => void }) {
  const t = useT();
  const measure = useMutation({ mutationFn: () => nodesApi.measureBandwidth({ nodeId }) });
  const r = measure.data;
  const value = r && !r.errorCode && r.downMbps > 0 ? roundMbps(capacityOf(r.downMbps, r.upMbps)) : 0;

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

  return (
    <div className="flex flex-col gap-2" aria-busy={measure.isPending}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <Button type="button" variant="secondary" size="sm" disabled={measure.isPending} onClick={() => measure.mutate()}>
          {measure.isPending ? t("node.settings.bandwidthMeasuring") : t("node.settings.bandwidthMeasure")}
        </Button>
        <span className="min-w-[200px] flex-1 text-xs leading-snug text-pretty text-muted">
          {measure.isPending ? t("node.settings.bandwidthMeasureBusy") : t("node.settings.bandwidthMeasureHint")}
        </span>
      </div>
      {failure && <Notice tone="danger">{failure}</Notice>}
      {value > 0 && r && (
        <div className="flex flex-col gap-2 rounded-field border border-line bg-surface px-3 py-2.5" role="status">
          <span className="text-[13px] font-bold">
            {r.upMbps > 0
              ? t("node.settings.bandwidthMeasured", { down: r.downMbps, up: r.upMbps })
              : t("node.settings.bandwidthMeasuredNoUp", { down: r.downMbps })}
          </span>
          {people && <span className="text-xs leading-snug text-pretty">{people}</span>}
          <span className="text-xs leading-snug text-pretty text-muted">{t("node.settings.bandwidthMeasuredNote", { server: r.server, runs: Math.max(r.runs, 1) })}</span>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
            <Button type="button" variant="primary" size="sm" disabled={current.trim() === String(value)} onClick={() => onUse(String(value))}>
              {t("node.settings.bandwidthUse", { value })}
            </Button>
            <span className="min-w-[200px] flex-1 text-xs leading-snug text-pretty text-muted">{t("node.settings.bandwidthUseNote")}</span>
          </div>
        </div>
      )}
    </div>
  );
}
