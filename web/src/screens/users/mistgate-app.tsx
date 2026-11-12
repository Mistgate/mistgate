import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { DefaultMark } from "@/components/brand";
import { Icon } from "@/components/ui/icons";
import { Switch } from "@/components/ui/switch";
import { clientApiQuery } from "@/lib/clientapi";
import { cx } from "@/lib/cx";
import { useTx } from "./t";

/** The panel's own mark on a quiet tile: the Mistgate app's face in a person's list of apps and on the owner's card in Subscriptions. */
export function MistgateTile({ dim }: { dim?: boolean }) {
  return (
    <span aria-hidden className={cx("grid size-9 flex-none place-items-center rounded-xl bg-accent-soft transition-opacity", dim && "opacity-60")}>
      <DefaultMark size={26} />
    </span>
  );
}

/**
 * The Mistgate app in the list of a person's apps (the user card and the new-user form): the panel's own mark on a quiet tile, the
 * name, one line of what it gives, the switch. It rides on the subscription link, so while that is off the row is locked and says
 * so; while the owner has the app off for the whole panel a small pill under the line says it and leads to that setting (the person's
 * switch still shows what the person has chosen, since it is theirs to keep). `checked` is the person's own switch.
 */
export function MistgateAppRow({
  checked,
  linkOn,
  onCheckedChange,
  className,
}: {
  checked: boolean;
  /** The subscription link switch of the same person: the app needs it. */
  linkOn: boolean;
  onCheckedChange: (on: boolean) => void;
  className?: string;
}) {
  const t = useTx();
  const api = useQuery(clientApiQuery);
  const globalOff = api.data ? !api.data.enabled : false;
  const shown = checked && linkOn;
  const hint = !linkOn ? t("users.mistgateNeedsLink") : !checked ? t("users.mistgateOffHint") : t("users.mistgateHint");
  return (
    <div data-app="mistgate" data-state={!linkOn ? "locked" : checked ? "on" : "off"} className={cx("flex flex-col border-t border-line", className)}>
      <label className={cx("flex min-h-[52px] items-center gap-3 py-1", linkOn ? "cursor-pointer" : "cursor-default")}>
        <MistgateTile dim={!shown} />
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="text-[13px] font-bold">{t("users.mistgate")}</span>
          <span className="text-[11px] leading-snug text-muted">{hint}</span>
        </span>
        <Switch checked={shown} disabled={!linkOn} onCheckedChange={onCheckedChange} />
      </label>
      {globalOff && (
        <Link
          to="/subscriptions"
          search={{ tab: "rules" }}
          hash="client-api"
          title={t("users.mistgateGlobalOffTitle")}
          className="mb-2 ms-12 inline-flex max-w-full items-center gap-1.5 self-start rounded-[13px] border border-line px-2.5 py-1 text-[11px] font-semibold text-muted transition-colors hover:border-faint hover:text-fg"
        >
          <Icon name="info" size={12} className="flex-none" />
          <span className="min-w-0 truncate">{t("users.mistgateGlobalOff")}</span>
          <Icon name="chevronRight" size={10} className="flex-none rtl:rotate-180" />
        </Link>
      )}
    </div>
  );
}
