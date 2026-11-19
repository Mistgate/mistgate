import { useMutation, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Select } from "@/components/ui/select";
import { useToast } from "@/components/ui/toast";
import type { Inbound } from "@/gen/mistgate/admin/v1/common_pb";
import { useT } from "@/i18n";
import { profiles as profilesApi } from "@/lib/api";
import { errorText } from "@/lib/errors";
import type { Plain } from "@/lib/plain";
import { profileListQuery } from "@/screens/users/rpc";
import { DecoyNotice, useInboundRefresh } from "./inbound-check";
import { asInboundCheck, hostOf, useRefusalText, useTargetCheck } from "./reality-target";

/**
 * "Apply…" on a neighbour of the "Sites near the node" card: show that site on a REALITY server of this node. The window says what strangers
 * see now and after, which server restarts and who has to refresh their subscription (the new name reaches the apps only with it), asks the
 * panel first (UpdateInbound with validate_only: the refusals and the warnings of the real call), and saves only on the click. The site is
 * text from another server's certificate: it is shown as text, never as a link.
 */

/** A neighbour's REALITY target: its name on the port the node scanned. */
export const targetOf = (site: string) => `${site}:443`;

export function ApplyDialog({ site, nodeId, nodeName, servers, onClose }: { site: string; nodeId: string; nodeName: string; servers: readonly Plain<Inbound>[]; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const refresh = useInboundRefresh(nodeId);
  const refusalText = useRefusalText();
  const [picked, setPicked] = useState(servers[0]?.id ?? "");
  const server = servers.find((s) => s.id === picked) ?? servers[0];
  const target = targetOf(site);
  const same = server?.realityTargetOverride === target;
  const check = useTargetCheck(server && !same ? { inboundId: server.id, target } : null);
  const users = useQuery(profileListQuery).data?.find((p) => p.id === server?.profileId)?.userCount ?? 0;
  const save = useMutation({
    mutationFn: () => profilesApi.updateInbound({ inboundId: server!.id, realityTargetOverride: target }),
    onSuccess: () => {
      onClose();
      toast(t("sni.apply.done", { site, node: nodeName }));
      refresh();
    },
  });
  if (!server) return null;
  const now = server.realityTarget === "self" ? t("sni.apply.own") : hostOf(server.realityTarget);
  const refused = check.state === "refused" ? check : null;
  const after = check.state === "ok" ? hostOf(check.inbound?.realityTarget ?? target) || site : site;
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t("sni.apply.title", { site, node: nodeName })}
      shot="sni-apply"
      footer={
        <>
          <Button type="button" variant="secondary" size="md" autoFocus onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button type="button" variant="primary" size="md" disabled={same || check.state !== "ok" || save.isPending} onClick={() => save.mutate()}>
            {check.state === "idle" && !same ? t("sni.apply.checking") : t("sni.apply.go")}
          </Button>
        </>
      }
    >
      <div className="sa" data-testid="sni-apply">
        {servers.length > 1 && (
          <div className="sa-pick">
            <span>{t("sni.apply.pick")}</span>
            <Select
              aria-label={t("sni.apply.pick")}
              value={server.id}
              onValueChange={(v) => {
                setPicked(v);
                save.reset();
              }}
              options={servers.map((s) => ({ value: s.id, label: s.profileName, hint: `tcp/${s.port}` }))}
            />
          </div>
        )}
        <div className="sa-change">
          <div className="sa-side">
            <small>{t("sni.apply.now")}</small>
            <b className="mono" dir="ltr" translate="no">
              {now}
            </b>
          </div>
          <Icon name="chevronRight" size={16} className="sa-arrow" aria-hidden />
          <div className="sa-side sa-new">
            <small>{t("sni.apply.after")}</small>
            <b className="mono" dir="ltr" translate="no">
              {after}
            </b>
          </div>
        </div>
        {same ? (
          <Notice tone="quiet">{t("sni.apply.same")}</Notice>
        ) : refused ? (
          <Notice tone="danger">{refusalText(check)}</Notice>
        ) : (
          <ul className="sa-facts">
            <li>{t("sni.apply.restart", { profile: server.profileName })}</li>
            <li>{t("sni.apply.clients")}</li>
            {users > 0 && <li>{t.n("sni.apply.users", users)}</li>}
          </ul>
        )}
        <DecoyNotice check={asInboundCheck(check)} />
        {save.isError && <Notice tone="danger">{errorText(save.error, t)}</Notice>}
      </div>
    </Modal>
  );
}
