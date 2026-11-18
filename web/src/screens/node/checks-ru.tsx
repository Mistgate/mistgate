import { Link } from "@tanstack/react-router";
import { Fragment, useEffect, useRef, useState } from "react";
import { Flag } from "@/components/ui/flag";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEffectsEnabled } from "@/lib/effects";
import { useFmt } from "@/lib/format";
import { countryIn, countryLook, type CountryCheck, type CountryLook } from "@/lib/health";
import { CountryTip, OwnPoints, PartialBreakdown } from "@/screens/health/country";

type Kind = CountryLook["kind"] | "none" | "down";

/** The words of a state: the board's "From Russia the node is …" and the states the board has no word for. */
const titleKey = {
  ok: "np.ru.ok",
  partial: "np.ru.partial",
  warn: "np.ru.tcp",
  bad: "np.ru.bad",
  listed: "np.ru.listed",
  unknown: "np.ru.unknown",
  none: "np.ru.none",
  down: "np.ru.down",
} as const;

/**
 * What the board's checks card ends with (`.nd-ru`): the flag of the country in a 28 px square, "From Russia the node is reachable"
 * and a line under it (the networks, the probe node, the registry, when). A partial block and the owner's own probes lay their lines
 * under it, in the column of the text. `now` is the round running (a radar off the flag), a result that has just come is `fresh`
 * (the words come up). Both are the reveal animation: off with its switch and with the system's "reduce motion".
 */
export function NodeRu({ check, code, status, now = false }: { check: CountryCheck | undefined; code: string; status?: NodeStatus; now?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const fx = useEffectsEnabled();
  const look = check ? countryLook(t, fmt, check) : null;
  // a result newer than the one on the screen: the words come up for a moment
  const at = check?.atUnix ?? 0;
  const seen = useRef(at);
  const [fresh, setFresh] = useState(false);
  useEffect(() => {
    const was = seen.current;
    seen.current = at;
    if (!was || at <= was) return;
    setFresh(true);
    const off = setTimeout(() => setFresh(false), 2000);
    return () => clearTimeout(off);
  }, [at]);

  const kind: Kind = status === NodeStatus.DOWN ? "down" : (look?.kind ?? "none");
  const title = t(titleKey[kind], { country: countryIn(t, code) });
  const node = check ? check.tspuVantageName || check.tspuVantageId : "";
  const probe = node && look ? t("cr.line.probe", { node }) : "";
  const row = look?.row ?? [];
  const breakdown = look?.breakdown;
  const more = breakdown || (look && look.ownPoints.length > 0);

  const head = (
    <div tabIndex={look ? 0 : undefined} className="min-w-0 rounded-sm">
      <b className="block font-medium break-words">
        {/* the title leads to the Reachability page, where the sources and the week are */}
        <Link to="/reachability" title={t("rc.open")} className="underline-offset-2 hover:underline">
          {title}
        </Link>
      </b>
      {row.length > 0 && (
        <span className="text-[13px] text-sec">
          {row.map((bit, i) => (
            <Fragment key={i}>
              {i > 0 && " · "}
              {i === row.length - 1 ? (
                <span className="whitespace-nowrap">{bit}</span>
              ) : probe && bit === probe ? (
                <>
                  {bit.slice(0, bit.indexOf(node))}
                  <span className="font-mono text-xs font-medium tabular-nums">{node}</span>
                  {bit.slice(bit.indexOf(node) + node.length)}
                </>
              ) : (
                bit
              )}
            </Fragment>
          ))}
        </span>
      )}
    </div>
  );

  return (
    <div
      data-testid="node-ru"
      data-kind={kind}
      className={cx("nd-ru grid grid-cols-[28px_minmax(0,1fr)] items-center gap-x-3 border-t border-line px-6 pt-3.5 pb-4 max-[720px]:px-4", fx && now && "now", fx && fresh && "fresh")}
    >
      <span aria-hidden className="g relative grid size-7 place-items-center">
        <Flag code={code} size={16} />
      </span>
      {look ? <CountryTip look={look}>{head}</CountryTip> : head}
      {more && (
        <div className="col-start-2 mt-2.5 flex min-w-0 flex-col gap-2.5">
          {breakdown ? <PartialBreakdown breakdown={breakdown} /> : look && <OwnPoints points={look.ownPoints} className="text-[13px]" />}
        </div>
      )}
    </div>
  );
}
