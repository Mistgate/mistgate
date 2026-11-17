import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { channelRank, channels, type Channel } from "@/lib/channels";
import { cx } from "@/lib/cx";
import { G } from "@/screens/users/user-bits";
import { D } from "./bits";

// The release channel on the Updates page (ADR 0011): a chip next to the panel's version, and for the owner a window with
// the three channels. Moving to an earlier channel offers its newest release at the next check; moving to a later one
// never downgrades: the panel stays on what it runs until the channel has something newer.

const nameKey: Record<Channel, MessageKey> = { stable: "up.ch.stable", testing: "up.ch.testing", dev: "up.ch.dev" };
const bodyKey: Record<Channel, MessageKey> = { stable: "up.ch.stable.body", testing: "up.ch.testing.body", dev: "up.ch.dev.body" };
const iconOf: Record<Channel, string> = { stable: D.shield, testing: D.flask, dev: D.branch };

/** The channel's dot and name; a button with a caret when it opens the window. */
export function ChannelChip({ channel, onClick }: { channel: Channel; onClick?: () => void }) {
  const t = useT();
  const cls = cx("uq-ch", channel !== "stable" && channel);
  const inner = (
    <>
      <i aria-hidden />
      {t(nameKey[channel])}
      {onClick && <G d={D.down} size={14} stroke={1.75} />}
    </>
  );
  if (!onClick) return <span className={cx(cls, "nobtn")}>{inner}</span>;
  return (
    <button type="button" onClick={onClick} aria-label={t("up.ch.change", { channel: t(nameKey[channel]) })} className={cls}>
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
      closeLabel={t("common.close")}
      className="uq-dlg"
      shot="updates-channel"
      footer={
        <>
          <Button variant="secondary" size="lg" disabled={busy} onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="primary"
            size="lg"
            disabled={busy || choice === current}
            aria-disabled={choice === current}
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
      <div className="uq-db">
        <p>{t("up.ch.desc")}</p>
        <div role="radiogroup" aria-label={t("up.ch.title")} className="uq-opts">
          {channels.map((c) => {
            const on = c === choice;
            return (
              <button key={c} type="button" role="radio" aria-checked={on} onClick={() => setPicked(c)} className={cx("uq-opt uq-cho", c !== "stable" && c)}>
                <span className="uq-oi" aria-hidden>
                  <G d={iconOf[c]} size={18} stroke={1.75} />
                </span>
                <span>
                  <b>
                    {t(nameKey[c])}
                    {c === "stable" && <i className="uq-otg">{t("up.ch.recommended")}</i>}
                    {c === current && <i className="uq-otg now">{t("up.ch.now")}</i>}
                  </b>
                  <em>{t(bodyKey[c])}</em>
                </span>
                <span className="nx-rad" aria-hidden data-checked={on}>
                  <i />
                </span>
              </button>
            );
          })}
        </div>
        {stays && <Notice tone="quiet">{t("up.ch.stays", { version: running.version, channel: t(nameKey[choice]) })}</Notice>}
        {early && !stays && <Notice tone="warn">{t("up.ch.early", { channel: t(nameKey[choice]) })}</Notice>}
        <p className="uq-fine">{t("up.ch.policy")}</p>
      </div>
    </Modal>
  );
}

export const channelName = (t: ReturnType<typeof useT>, c: Channel) => t(nameKey[c]);
