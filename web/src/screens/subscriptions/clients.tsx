import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Pending, QueryError } from "@/components/ui/query-error";
import { cx } from "@/lib/cx";
import { ClientCompatActual, ClientCompatState, outdatedDevicesQuery, pageSizes, stateWord, windowDays, type CompatDevices } from "@/lib/clientcompat";
import { clampPage } from "@/lib/paging";
import { useTx } from "@/screens/users/t";
import { ActualBar } from "./clients-actual";
import { MinimumsCard } from "./clients-rules";
import { DevicesTable } from "./clients-table";
import { path, Svg } from "./parts";
import { sbDemo } from "./demo-sb";

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
 * list to one person (?user=); `rulesOpen` unfolds the table of minimum versions at once; `initialActual` starts on a class of what the
 * nodes saw (the dev kit).
 */
export function ClientsTab({ userId = "", onClearUser, rulesOpen, initialActual = ClientCompatActual.UNSPECIFIED }: { userId?: string; onClearUser?: () => void; rulesOpen?: boolean; initialActual?: ClientCompatActual }) {
  const t = useTx();
  const [pick, setPick] = useState<Pick>(() => sbDemo.pick ?? "attention");
  const [actual, setActual] = useState(initialActual);
  const [page, setPage] = useState(1);
  const [size, setSize] = useState<number>(50);
  const list = useQuery(outdatedDevicesQuery({ state: stateOf[pick], actual, userId, page, size }));
  const data = list.data;
  const total = data?.total ?? 0;
  const shown = clampPage(page, total, size);
  // rows went away and the page is past the end now: step back to the last one
  if (data && shown !== page) setPage(shown);

  // "Need attention" is the default view: choosing it again drops the class too. A class asked for under it takes in devices of every
  // state (the panel does so), which is why the card is not shown as chosen then.
  const choose = (next: Pick) => {
    setPick(next);
    if (next === "attention") setActual(ClientCompatActual.UNSPECIFIED);
    setPage(1);
  };
  const chooseActual = (next: ClientCompatActual) => {
    setActual(next);
    setPage(1);
  };
  const pager = { total, page: shown, size, sizes: pageSizes, onPage: setPage, onSize: (n: number) => (setSize(n), setPage(1)), busy: list.isPlaceholderData };
  const anyDevices = !!data && data.counts.some((c) => c.devices > 0);

  return (
    <div data-shot="subs-clients">
      <p className="sb-intro">
        {t("cc.lead")} {data && t("cc.leadActual", { days: windowDays(data.helloWindowHours) })}
      </p>
      {list.isPending ? (
        <Pending />
      ) : !data ? (
        <QueryError error={list.error} onRetry={() => void list.refetch()} />
      ) : !anyDevices ? (
        <section className="card">
          <div className="sb-nf">
            <p>{t("cc.empty.none")}</p>
          </div>
        </section>
      ) : (
        <>
          <Summary data={data} pick={pick === "attention" && actual !== ClientCompatActual.UNSPECIFIED ? null : pick} onPick={choose} />
          <ActualBar data={data} actual={actual} onPick={chooseActual} />
          {userId && (
            <p className="sb-vf">
              <button type="button" className="sb-who" aria-label={t("cc.userClear")} onClick={onClearUser}>
                <span className="truncate">{t("cc.userChip", { name: data.devices[0]?.userName || userId })}</span>
                <Svg d={path.x} />
              </button>
            </p>
          )}
          {list.isError && <QueryError compact error={list.error} onRetry={() => void list.refetch()} />}
          <DevicesTable rows={data.devices} windowHours={data.helloWindowHours} pager={pager} fingerprints={data.fingerprints} empty={pick === "attention" && actual === ClientCompatActual.UNSPECIFIED ? "ok" : "state"} />
        </>
      )}
      <MinimumsCard initialOpen={rulesOpen ?? sbDemo.minOpen} />
    </div>
  );
}

/**
 * The four states as cards with the count, and "Need attention" (everything but OK) first. They are the filter: one is
 * chosen at a time, a second tap on it goes back to "Need attention".
 */
function Summary({ data, pick, onPick }: { data: CompatDevices; pick: Pick | null; onPick: (p: Pick) => void }) {
  const t = useTx();
  const attention = [ClientCompatState.BELOW_MINIMUM, ClientCompatState.UNSUPPORTED, ClientCompatState.UNKNOWN].reduce((n, s) => n + countOf(data, s).devices, 0);
  return (
    <RadioGroup aria-label={t("cc.sum.label")} value={pick ?? ""} onValueChange={(v) => onPick(v as Pick)} className="sb-tiles">
      {picks.map((p) => {
        const state = stateOf[p];
        const c = p === "attention" ? { devices: attention, people: 0 } : countOf(data, state);
        return (
          <Radio.Root key={p} value={p} onClick={() => pick === p && p !== "attention" && onPick("attention")} className={cx(p === "attention" && "warn")}>
            <span>{t(stateWord[state])}</span>
            <b className="mono">{c.devices}</b>
            <em>{p === "attention" ? t("cc.sum.attentionSub") : t.n("cc.people", c.people)}</em>
          </Radio.Root>
        );
      })}
    </RadioGroup>
  );
}
