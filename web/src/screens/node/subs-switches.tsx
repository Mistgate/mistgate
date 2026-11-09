import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { halo } from "@/components/fx/halo";
import { Block } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import type { Inbound } from "@/gen/mistgate/admin/v1/common_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { profiles as profilesApi } from "@/lib/api";
import { errorText } from "@/lib/errors";
import type { Plain } from "@/lib/plain";
import { protocolShort } from "@/lib/series";
import { useIsOwner } from "@/lib/updates";
import { useInboundRefresh } from "./inbound-check";

// "In subscriptions" on the Ways in tab: a switch per server of the Direct or the CDN lane. Off keeps the inbound running on
// the node (relays and the CDN forward to it) and leaves its plain server out of every subscription; its twins through
// relays and over IPv6 stay. Turning one off asks the server first (validate_only): when it would leave people no way to the
// server, the owner confirms with the reason in words.

type In = Plain<Inbound>;

const reasonKeys: Record<string, MessageKey> = {
  awg: "ways.subs.reason.awg",
  acme_ip: "ways.subs.reason.acme_ip",
  no_usable_relay: "ways.subs.reason.no_usable_relay",
};

const reasonText = (t: T, code: string) => (reasonKeys[code] ? t(reasonKeys[code]) : code);

/**
 * The In subscriptions switch of one server, shared by this list and the graph's wire menu: turning one off asks the
 * server first and, when it would leave people no way to the server, a confirmation dialog (`dialog`, to be rendered).
 */
export function useInSubscriptions(nodeId: string, nodeName: string) {
  const t = useT();
  const toast = useToast();
  const refresh = useInboundRefresh(nodeId);
  const [ask, setAsk] = useState<{ inbound: In; reason: string; report?: string } | null>(null);
  const [error, setError] = useState("");
  const set = useMutation({
    mutationFn: ({ id, on }: { id: string; on: boolean; report?: string }) => profilesApi.updateInbound({ inboundId: id, directInSubscriptions: on }),
    onSuccess: (_r, v) => {
      setAsk(null);
      // a change the graph made says what it did and offers the way back
      if (v.report) toast(v.report, { undo: () => set.mutate({ id: v.id, on: !v.on }) });
      else {
        toast(t("common.saved"));
        halo("save");
      }
    },
    onError: (e) => setError(errorText(e, t)),
    onSettled: refresh,
  });
  const check = useMutation({
    mutationFn: (i: In) => profilesApi.updateInbound({ inboundId: i.id, directInSubscriptions: false, validateOnly: true }),
  });

  /** `report`: the sentence of the toast with Undo that follows (the graph's wires); without it a plain Saved. */
  const toggle = async (i: In, on: boolean, report?: string) => {
    setError("");
    if (!on) {
      try {
        const r = await check.mutateAsync(i);
        const w = r.warnings.find((x) => x.code === "inbound_no_way_in");
        if (w) {
          setAsk({ inbound: i, reason: w.params.reason ?? "", report });
          return;
        }
      } catch (e) {
        setError(errorText(e, t));
        return;
      }
    }
    set.mutate({ id: i.id, on, report });
  };

  const dialog = (
    <Modal
      open={!!ask}
      onOpenChange={(o) => {
        if (!o && !set.isPending) {
          setAsk(null);
          setError("");
        }
      }}
      title={t("ways.subs.offTitle", { name: ask?.inbound.profileName ?? "" })}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={set.isPending} onClick={() => setAsk(null)}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" size="md" disabled={set.isPending} onClick={() => ask && set.mutate({ id: ask.inbound.id, on: false, report: ask.report })}>
            {t("ways.subs.hide")}
          </Button>
        </>
      }
    >
      <p className="text-[13px] leading-normal text-pretty">
        {t("ways.subs.offBody", { name: ask?.inbound.profileName ?? "", node: nodeName, reason: reasonText(t, ask?.reason ?? "") })}
      </p>
      {error && <Notice tone="danger">{error}</Notice>}
    </Modal>
  );
  return { toggle, busy: set.isPending || check.isPending, error, asking: !!ask, dialog };
}

export function InSubscriptions({ nodeId, nodeName, address, inbounds, cdn }: { nodeId: string; nodeName: string; address: string; inbounds: In[]; cdn?: boolean }) {
  const t = useT();
  const owner = useIsOwner();
  const { toggle, busy, error, asking, dialog } = useInSubscriptions(nodeId, nodeName);
  return (
    <Block title={t("ways.subs.title")}>
      <p className="text-xs leading-snug text-pretty text-muted">{cdn ? t("ways.subs.note.cdn") : t("ways.subs.note.direct", { address })}</p>
      <ul className="flex flex-col">
        {inbounds.map((i) => (
          <li key={i.id}>
            <label className="flex min-h-11 items-center gap-3 py-1">
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate text-[13px] leading-5 font-bold">{i.profileName}</span>
                <span className="truncate font-mono text-[11px] text-muted">
                  {protocolShort(i.protocol)} · {i.port}
                </span>
              </span>
              <Switch
                checked={i.directInSubscriptions}
                disabled={!owner || busy}
                onCheckedChange={(on) => void toggle(i, on)}
                aria-label={t("ways.subs.switch", { name: i.profileName })}
              />
            </label>
          </li>
        ))}
      </ul>
      {error && !asking && <Notice tone="danger">{error}</Notice>}
      {dialog}
    </Block>
  );
}
