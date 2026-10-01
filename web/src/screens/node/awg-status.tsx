import { StatusDot } from "@/components/ui/status";
import type { AwgInboundStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { Plain } from "@/lib/plain";
import { useNow } from "@/lib/time";
import { useTx } from "@/screens/users/t";

/** What the node last reported about an AmneziaWG inbound: peers, handshakes, and a hint when nobody gets through. */
export function AwgStatusLine({ awg }: { awg: Plain<AwgInboundStatus> }) {
  const t = useTx();
  const now = Math.floor(useNow() / 1000);
  const never = Math.max(0, awg.peers - awg.peersHandshaken);
  const newest = awg.newestHandshakeUnix > 0 ? Math.max(0, now - awg.newestHandshakeUnix) : -1;
  // the numbers say where it breaks: no datagrams = the hoster or a firewall; datagrams without a handshake = the obfuscation
  const hint = !awg.ifaceUp
    ? t("awg.node.down")
    : awg.peers > 0 && awg.peersHandshaken === 0
      ? awg.udpRxPackets === 0
        ? t("awg.node.noPackets")
        : t("awg.node.noHandshake")
      : "";
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted">
        <span className="flex items-center gap-1.5">
          <StatusDot kind={!awg.ifaceUp ? "bad" : awg.peersOnline > 0 ? "ok" : "off"} />
          <b className="text-fg">{awg.backend}</b>
        </span>
        <span>{t.n("awg.node.peers", awg.peers)}</span>
        <span>{t("awg.node.online", { n: awg.peersOnline })}</span>
        {never > 0 && <span>{t("awg.node.never", { n: never })}</span>}
        {newest >= 0 && <span>{newest < 60 ? t("awg.node.handshakeNow") : t("awg.node.handshake", { s: newest < 3600 ? t("users.ago.m", { n: Math.floor(newest / 60) }) : newest < 86400 ? t("users.ago.h", { n: Math.floor(newest / 3600) }) : t("users.ago.d", { n: Math.floor(newest / 86400) }) })}</span>}
      </div>
      {hint && <p className="text-xs leading-snug text-warn-text">{hint}</p>}
    </div>
  );
}
