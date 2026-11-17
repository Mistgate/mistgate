import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { profiles as profilesApi } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { inboundErrorKind } from "@/lib/inbound-error";
import { portNotes, withNotes } from "@/lib/port-check";
import { portOk, sniPlaceholder, useInboundCheck, useInboundRefresh } from "@/screens/node/inbound-check";
import { LossyNotice, lossyRefusal } from "@/screens/node/port-lossy";
import { useTx } from "@/screens/users/t";
import type { ServerRow } from "./profiles-model";

/**
 * A server's port and domain, edited in its own row (the board's .nq-ed.pp-ed): the same call and the same check as the node's own window
 * (UpdateInbound, validate-only first), so a port that is taken or loses packets, or a domain Let's Encrypt cannot use, says so under its
 * field before the click. `focus` is where the cursor starts (the failed server's port, or its domain).
 */
export function ServerEdit({ row, nodeAddress, focus, onClose }: { row: ServerRow; nodeAddress: string; focus?: "port" | "sni"; onClose: () => void }) {
  const t = useTx();
  const tt = useT();
  const toast = useToast();
  const refresh = useInboundRefresh(row.nodeId);
  const i = row.inbound;
  const isAwg = i.protocol === "awg";
  // a VLESS server has a name of its own to set only where it has a certificate (as the node's own window decides)
  const named = !isAwg && (i.protocol !== "vless" || !!i.tlsServerName || !!i.tlsServerNameOverride || (!!i.lastError && inboundErrorKind(i.lastError) === "cert"));
  const was = { port: i.port ? String(i.port) : "", sni: i.tlsServerName };
  const [port, setPort] = useState(was.port);
  const [sni, setSni] = useState(was.sni);
  const [moved, setMoved] = useState(false);
  const touched = { port: port !== was.port, sni: sni !== was.sni };
  const check = useInboundCheck(portOk(port) ? { kind: "update", inboundId: i.id, port: touched.port ? port : undefined, sni: touched.sni ? sni.trim() : undefined } : null);
  // "Change the port": the field opens on a port that is free on this node (the server's picker), once
  const free = check.state === "ok" ? check.freePort : check.state === "refused" && check.code === "port_taken" ? Number(check.vars.free) : 0;
  if (focus === "port" && !moved && free > 0 && !touched.port) {
    setMoved(true);
    setPort(String(free));
  }
  const save = useMutation({
    mutationFn: (allowLossy: boolean) =>
      profilesApi.updateInbound({
        inboundId: i.id,
        portOverride: touched.port ? (port ? Number(port) : 0) : undefined,
        tlsServerNameOverride: touched.sni ? sni.trim() : undefined,
        ...(allowLossy && { allowLossyPort: true }),
      }),
    onSuccess: (r) => {
      onClose();
      const notes = portNotes(tt, r?.warnings);
      toast(withNotes(t("common.saved"), notes), notes.length > 0 ? { ms: 8000 } : undefined);
      refresh();
    },
  });
  const refused = check.state === "refused" ? check : null;
  const lossy = refused?.code === "port_lossy" ? refused.vars : lossyRefusal(save.error);
  const fieldError = (codes: string[]) => (refused && codes.includes(refused.code) ? tt(`err.${refused.code}`, refused.vars) : undefined);
  const portError = !portOk(port) ? t("node.profiles.portError") : fieldError(["port_taken", "port_in_hop", "hop_taken"]);
  const sniError = fieldError(["sni_needs_domain", "sni_invalid", "acme_needs_domain", "reality_needs_domain", "mtproto_needs_domain"]);
  const effective = check.state === "ok" ? check.inbound : undefined;
  const dirty = touched.port || touched.sni;
  return (
    <form
      className="nq-ed pp-ed"
      onSubmit={(e) => {
        e.preventDefault();
        if (dirty && portOk(port) && !refused) save.mutate(false);
      }}
    >
      <div className="pf-two pf-eq">
        <label className={`an-f${portError ? " bad" : ""}`}>
          <span>{t("ps.ed.port", { node: row.nodeName })}</span>
          <input
            className="an-i mono"
            type="text"
            inputMode="numeric"
            value={port}
            autoFocus={focus !== "sni"}
            placeholder={effective?.port && !port ? t("node.profiles.portFrom", { port: effective.port }) : tt("node.profiles.fromProfile")}
            aria-invalid={!!portError}
            onChange={(e) => {
              setPort(e.target.value.replace(/\D/g, "").slice(0, 5));
              save.reset();
            }}
          />
          <small>{portError ?? t("ps.ed.portHint") + (isAwg ? t("ps.ed.awgKeys") : "")}</small>
        </label>
        {named && (
          <label className={`an-f${sniError ? " bad" : ""}`}>
            <span>{t("ps.ed.sni", { node: row.nodeName })}</span>
            <input
              className="an-i mono"
              type="text"
              value={sni}
              autoFocus={focus === "sni"}
              autoCapitalize="off"
              spellCheck={false}
              placeholder={sniPlaceholder(tt, effective?.tlsServerName, nodeAddress)}
              aria-invalid={!!sniError}
              onChange={(e) => setSni(e.target.value)}
            />
            <small>{sniError ?? t("ps.ed.sniHint")}</small>
          </label>
        )}
      </div>
      {refused?.code === "port_taken" && Number(refused.vars.free) > 0 && (
        <div>
          <Button size="md" onClick={() => setPort(refused.vars.free!)}>
            {tt("node.check.port.take", { port: refused.vars.free! })}
          </Button>
        </div>
      )}
      {lossy && <LossyNotice vars={lossy} busy={save.isPending} anywayLabel={tt("ports.anyway.save")} onTake={(p) => (setPort(String(p)), save.reset())} onAnyway={() => save.mutate(true)} />}
      {save.isError && !lossyRefusal(save.error) && (
        <Notice tone="danger" className="items-start">
          {errorText(save.error, tt)}
        </Notice>
      )}
      <div>
        <Button size="md" onClick={onClose}>
          {t("common.cancel")}
        </Button>
        <Button type="submit" variant="primary" size="md" disabled={!dirty || !portOk(port) || !!refused || save.isPending}>
          {save.isPending && touched.port && port ? tt("ports.saving", { port }) : t("common.save")}
        </Button>
      </div>
    </form>
  );
}
