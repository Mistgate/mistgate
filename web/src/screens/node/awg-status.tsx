import type { AwgInboundStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { cx } from "@/lib/cx";
import type { Plain } from "@/lib/plain";
import { useNow } from "@/lib/time";
import { useTx } from "@/screens/users/t";

/**
 * What the node last reported about an AmneziaWG inbound beyond the row's columns (devices and backend are there):
 * devices that never connected, the newest handshake, and a hint when nobody gets through.
 */
export function AwgStatusLine({ awg }: { awg: Plain<AwgInboundStatus> }) {
  const t = useTx();
  const now = Math.floor(useNow() / 1000);
  const never = Math.max(0, awg.peers - awg.peersHandshaken);
  const newest = awg.newestHandshakeUnix > 0 ? Math.max(0, now - awg.newestHandshakeUnix) : -1;
  // the numbers say where it breaks: datagrams without a handshake = the obfuscation. No datagram at all proves nothing by itself
  // (nobody may have tried the key yet: the node cannot tell), so that line is grey and blames the port only if a device tried.
  const silent = awg.udpRxPackets === 0;
  const hint = !awg.ifaceUp
    ? t("awg.node.down")
    : awg.peers > 0 && awg.peersHandshaken === 0
      ? silent
        ? t("awg.node.noPackets")
        : t("awg.node.noHandshake")
      : "";
  if (never === 0 && newest < 0 && !hint) return null;
  return (
    <div className="flex flex-col gap-1.5">
      {(never > 0 || newest >= 0) && (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted">
          {never > 0 && <span>{t("awg.node.never", { n: never })}</span>}
          {newest >= 0 && <span>{newest < 60 ? t("awg.node.handshakeNow") : t("awg.node.handshake", { s: newest < 3600 ? t("users.ago.m", { n: Math.floor(newest / 60) }) : newest < 86400 ? t("users.ago.h", { n: Math.floor(newest / 3600) }) : t("users.ago.d", { n: Math.floor(newest / 86400) }) })}</span>}
        </div>
      )}
      {hint && <p className={cx("text-xs leading-snug", awg.ifaceUp && silent ? "text-muted" : "text-warn-text")}>{hint}</p>}
    </div>
  );
}
