import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Card } from "@/components/ui/bits";
import { Icon } from "@/components/ui/icons";
import { Pending, QueryError } from "@/components/ui/query-error";
import { StatusPill } from "@/components/ui/status";
import { cx } from "@/lib/cx";
import { ClientCompatState, outdatedDevicesQuery, pageSizes, stateKind, stateWord, type CompatDevices } from "@/lib/clientcompat";
import { clampPage } from "@/lib/paging";
import { useTx } from "@/screens/users/t";
import { MinimumsCard } from "./clients-rules";
import { DevicesTable } from "./clients-table";
import { Lead } from "./ui";

type Pick = "attention" | "ok" | "below" | "unsupported" | "unknown";
const picks: Pick[] = ["attention", "ok", "below", "unsupported", "unknown"];
const stateOf: Record<Pick, ClientCompatState> = {
  attention: ClientCompatState.UNSPECIFIED,
  ok: ClientCompatState.OK,
  below: ClientCompatState.BELOW_MINIMUM,
  unsupported: ClientCompatState.UNSUPPORTED,
  unknown: ClientCompatState.UNKNOWN,
};

/** How many devices and people the answer counts in a state (the counts do not follow the filters). */
const countOf = (data: CompatDevices, state: ClientCompatState) => data.counts.find((c) => c.state === state) ?? { devices: 0, people: 0 };

/**
 * "App versions": the devices that get REALITY servers, each with a verdict from the known versions of the apps that send a
 * post-quantum key share, and the table of the minimum versions below. It changes nothing for anybody yet. `userId` keeps the
 * list to one person (?user=); `rulesOpen` unfolds the table of minimum versions at once.
 */
export function ClientsTab({ userId = "", onClearUser, rulesOpen }: { userId?: string; onClearUser?: () => void; rulesOpen?: boolean }) {
  const t = useTx();
  const [pick, setPick] = useState<Pick>("attention");
  const [page, setPage] = useState(1);
  const [size, setSize] = useState<number>(50);
  const list = useQuery(outdatedDevicesQuery({ state: stateOf[pick], userId, page, size }));
  const data = list.data;
  const total = data?.total ?? 0;
  const shown = clampPage(page, total, size);
  // rows went away and the page is past the end now: step back to the last one
  if (data && shown !== page) setPage(shown);

  const choose = (next: Pick) => {
    setPick(next);
    setPage(1);
  };
  const pager = { total, page: shown, size, sizes: pageSizes, onPage: setPage, onSize: (n: number) => (setSize(n), setPage(1)), busy: list.isPlaceholderData };
  const anyDevices = !!data && data.counts.some((c) => c.devices > 0);

  return (
    <div className="@container flex flex-col gap-3.5">
      <Lead>{t("cc.lead")}</Lead>
      {list.isPending ? (
        <Pending />
      ) : !data ? (
        <QueryError error={list.error} onRetry={() => void list.refetch()} />
      ) : !anyDevices ? (
        <Card lg className="border-dashed">
          <p className="p-8 text-center text-[13px] text-muted">{t("cc.empty.none")}</p>
        </Card>
      ) : (
        <>
          <Summary data={data} pick={pick} onPick={choose} />
          {(userId || data.fingerprints.length > 0) && (
            <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
              {userId && (
                <button
                  type="button"
                  aria-label={t("cc.userClear")}
                  onClick={onClearUser}
                  className="flex h-[30px] items-center gap-1.5 rounded-[15px] border border-accent-line bg-accent-soft px-2.5 text-xs font-bold"
                >
                  <span className="truncate">{t("cc.userChip", { name: data.devices[0]?.userName || userId })}</span>
                  <Icon name="x" size={12} />
                </button>
              )}
              {data.fingerprints.length > 0 && <p className="min-w-0 text-xs text-muted">{t("cc.fingerprints", { list: data.fingerprints.join(", ") })}</p>}
            </div>
          )}
          {list.isError && <QueryError compact error={list.error} onRetry={() => void list.refetch()} />}
          {data.devices.length > 0 ? (
            <DevicesTable rows={data.devices} pager={pager} />
          ) : (
            <Card lg className="flex flex-col items-center gap-2 p-8 text-center">
              {pick === "attention" ? (
                <StatusPill kind="ok" label={t("cc.empty.ok")} />
              ) : (
                <p className="text-[13px] text-muted">{t("cc.empty.state")}</p>
              )}
            </Card>
          )}
        </>
      )}
      <MinimumsCard initialOpen={rulesOpen} />
    </div>
  );
}

/**
 * The four states as cards with the count, and "Need attention" (everything but OK) first. They are the filter: one is
 * chosen at a time, a second tap on it goes back to "Need attention".
 */
function Summary({ data, pick, onPick }: { data: CompatDevices; pick: Pick; onPick: (p: Pick) => void }) {
  const t = useTx();
  const attention = [ClientCompatState.BELOW_MINIMUM, ClientCompatState.UNSUPPORTED, ClientCompatState.UNKNOWN].reduce((n, s) => n + countOf(data, s).devices, 0);
  return (
    <RadioGroup aria-label={t("cc.sum.label")} value={pick} onValueChange={(v) => onPick(v as Pick)} className="grid grid-cols-2 gap-2 @min-[640px]:grid-cols-5">
      {picks.map((p) => {
        const state = stateOf[p];
        const c = p === "attention" ? { devices: attention, people: 0 } : countOf(data, state);
        return (
          <Radio.Root
            key={p}
            value={p}
            onClick={() => pick === p && p !== "attention" && onPick("attention")}
            className={cx(
              "group flex min-h-[92px] min-w-0 cursor-pointer flex-col items-start justify-between gap-2 rounded-card-lg border border-line bg-surface p-3.5 text-start transition-colors duration-200 hover:bg-surface-2 data-checked:border-accent-line data-checked:bg-accent-soft",
              p === "attention" && "col-span-2 @max-[639px]:min-h-0 @max-[639px]:flex-row @max-[639px]:items-center @max-[639px]:justify-between @min-[640px]:col-span-1",
            )}
          >
            {p === "attention" ? <span className="flex h-[22px] items-center text-xs font-bold">{t(stateWord[state])}</span> : <StatusPill sm kind={stateKind(state)} label={t(stateWord[state])} />}
            <span className={cx("flex flex-col gap-1", p === "attention" && "@max-[639px]:flex-row @max-[639px]:items-baseline @max-[639px]:gap-2")}>
              <span className={cx("text-[26px] leading-none font-extrabold tracking-[-0.03em] tabular-nums", c.devices === 0 && "text-faint")}>{c.devices}</span>
              <span className="text-[11px] text-muted">{p === "attention" ? t("cc.sum.attentionSub") : t.n("cc.people", c.people)}</span>
            </span>
          </Radio.Root>
        );
      })}
    </RadioGroup>
  );
}
