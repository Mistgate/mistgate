import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { nowLook, nowSkip, nowSourceName, type CountryNow } from "@/lib/country-now";
import { useFmt } from "@/lib/format";
import { useNow } from "@/lib/time";

/**
 * What "Check now" asked of the country check for the node, under its "From Russia" line (Overview, Ways tab, Reachability): what is being
 * rechecked or was, what is left out and why ("bsbord: on its schedule only"), what did not work, when the next recheck may be asked, and
 * what Globalping has left for it. Nothing while nobody asked. The radar of the row is the caller's, `now.running` says when.
 */
export function CountryNowNote({ now, className }: { now: CountryNow | undefined; className?: string }) {
  const t = useT();
  const fmt = useFmt();
  const nowSec = Math.floor(useNow(10_000) / 1000);
  const look = nowLook(now, nowSec);
  if (!look || (!look.any && look.nextIn === 0)) return null;
  const names = (list: { source: string }[]) => list.map((s) => nowSourceName(t, s.source)).join(t("cn.sep"));
  const lines: { key: string; text: string }[] = [];
  if (look.asked.length > 0) {
    lines.push(look.running ? { key: "running", text: t("cn.running", { sources: names(look.asked) }) } : { key: "done", text: t("cn.done", { ago: fmt.ago(look.startedUnix), sources: names(look.asked) }) });
  }
  if (look.queued.length > 0) lines.push({ key: "queued", text: t("cn.queued", { sources: names(look.queued) }) });
  if (look.failed.length > 0) lines.push({ key: "failed", text: t("cn.fail", { list: look.failed.map((s) => nowSkip(t, s)).join(t("cn.sep")) }) });
  if (look.skipped.length > 0) lines.push({ key: "skipped", text: t("cn.skip", { list: look.skipped.map((s) => nowSkip(t, s)).join(t("cn.sep")) }) });
  if (look.reread.length > 0) lines.push({ key: "reread", text: t("cn.reread", { sources: names(look.reread) }) });
  if (look.gp) lines.push({ key: "gp", text: t("cn.gp", { left: look.gp.left, cost: look.gp.cost }) });
  if (look.nextIn > 0 && !look.running) lines.push({ key: "next", text: t("cn.next", { when: fmt.duration(look.nextIn) }) });
  return (
    <div data-testid="country-now" data-state={look.running ? "running" : "idle"} className={cx("flex min-w-0 flex-col gap-0.5 text-[13px] leading-snug text-sec", className)}>
      {lines.map((l) => (
        <p key={l.key} data-line={l.key} className={cx("m-0 break-words", l.key === "running" && "text-tone-sky", l.key === "failed" && "text-warn-text")}>
          {l.text}
        </p>
      ))}
    </div>
  );
}
