import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { channelRank, channels, channelTone, type Channel } from "@/lib/channels";
import { cx } from "@/lib/cx";

// The release channel on the Updates page (ADR 0011): a chip next to the panel's version, and for the owner a window with
// the three channels. Moving to an earlier channel offers its newest release at the next check; moving to a later one
// never downgrades: the panel stays on what it runs until the channel has something newer.

const nameKey: Record<Channel, MessageKey> = { stable: "up.ch.stable", testing: "up.ch.testing", dev: "up.ch.dev" };
const bodyKey: Record<Channel, MessageKey> = { stable: "up.ch.stable.body", testing: "up.ch.testing.body", dev: "up.ch.dev.body" };

/** The channel's dot and name; a button with a caret when it opens the window. */
export function ChannelChip({ channel, onClick }: { channel: Channel; onClick?: () => void }) {
  const t = useT();
  const inner = (
    <>
      <span aria-hidden className={cx("size-1.5 flex-none rounded-full bg-(--c)", channelTone[channel])} />
      {t(nameKey[channel])}
      {onClick && <Icon name="chevronDown" size={11} className="text-muted" aria-hidden />}
    </>
  );
  const cls = "inline-flex h-[22px] flex-none items-center gap-1.5 rounded-[11px] border border-line bg-surface-2 px-2 text-[11px] font-bold whitespace-nowrap";
  if (!onClick) return <span className={cls}>{inner}</span>;
  return (
    <button type="button" onClick={onClick} aria-label={t("up.ch.change", { channel: t(nameKey[channel]) })} className={cx(cls, "cursor-pointer transition-colors hover:border-accent-line hover:bg-surface")}>
      {inner}
    </button>
  );
}

/** The owner's window: the three channels as cards, and what a switch does to this panel. */
export function ChannelDialog({
  open,
  onOpenChange,
  current,
  running,
  onPick,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  current: Channel;
  /** The running version and its channel: a switch to a more stable channel keeps it. */
  running: { version: string; channel: Channel };
  onPick: (c: Channel) => Promise<boolean>;
}) {
  const t = useT();
  const [picked, setPicked] = useState<Channel>(current);
  const [busy, setBusy] = useState(false);
  const choice = open ? picked : current;
  // to a more stable channel than the build that runs: nothing is installed backwards
  const stays = channelRank[choice] < channelRank[running.channel];
  const early = choice !== "stable" && choice !== current;
  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        if (busy) return;
        if (o) setPicked(current);
        onOpenChange(o);
      }}
      title={t("up.ch.title")}
      description={t("up.ch.desc")}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={busy} onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="primary"
            size="md"
            disabled={busy || choice === current}
            onClick={async () => {
              setBusy(true);
              const ok = await onPick(choice);
              setBusy(false);
              if (ok) onOpenChange(false);
            }}
          >
            {t("up.ch.switch")}
          </Button>
        </>
      }
    >
      <div role="radiogroup" aria-label={t("up.ch.title")} className="flex flex-col gap-2">
        {channels.map((c) => {
          const on = c === choice;
          return (
            <button
              key={c}
              type="button"
              role="radio"
              aria-checked={on}
              onClick={() => setPicked(c)}
              className={cx(
                "flex w-full cursor-pointer items-start gap-3 rounded-field border p-3.5 text-start transition-colors",
                on ? "border-accent-line bg-accent-soft" : "border-line bg-surface-2 hover:border-accent-line",
              )}
            >
              <span aria-hidden className={cx("mt-0.5 grid size-4 flex-none place-items-center rounded-full border-2", on ? "border-accent" : "border-line")}>
                {on && <span className="size-2 rounded-full bg-accent" />}
              </span>
              <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="flex flex-wrap items-center gap-2">
                  <span aria-hidden className={cx("size-1.5 rounded-full bg-(--c)", channelTone[c])} />
                  <span className="text-[13px] font-bold">{t(nameKey[c])}</span>
                  {c === "stable" && <span className="rounded-md bg-surface px-1.5 py-px text-[10px] font-bold text-muted">{t("up.ch.recommended")}</span>}
                  {c === current && <span className="text-[11px] text-muted">· {t("up.ch.now")}</span>}
                </span>
                <span className="text-xs leading-snug text-pretty text-muted">{t(bodyKey[c])}</span>
              </span>
            </button>
          );
        })}
      </div>
      {stays && (
        <Notice tone="quiet">{t("up.ch.stays", { version: running.version, channel: t(nameKey[choice]) })}</Notice>
      )}
      {early && !stays && <Notice tone="warn">{t("up.ch.early", { channel: t(nameKey[choice]) })}</Notice>}
    </Modal>
  );
}

export const channelName = (t: ReturnType<typeof useT>, c: Channel) => t(nameKey[c]);
