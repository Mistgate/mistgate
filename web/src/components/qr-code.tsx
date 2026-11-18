import { Code, ConnectError } from "@connectrpc/connect";
import { useEffect, useMemo, useState, type HTMLAttributes, type ReactNode } from "react";
import { Logo } from "@/components/brand";
import type { QrCode as QrMessage } from "@/gen/mistgate/admin/v1/common_pb";
import { instance } from "@/lib/api";
import { useBrand } from "@/lib/brand";
import { useT } from "@/i18n";
import { monoBadgeSrc } from "@/lib/qr-mark";
import { qrDrawing, type QrBits } from "@/lib/qr-path";
import { cx } from "@/lib/cx";

// The board's QR card (.u-qr of the person's page): a white square, the code in rounded modules with the three eyes as rounded
// squares, and the mark of the instance in a clear square in the middle. `size` is the side of the card in px: the padding is
// 12 of 136 (18 of 216) and the corners 20 (24 from 216 up), as the board has them. The server encodes (EncodeQr, internal/qr);
// the card only draws the modules it is told, with DOM calls, never as markup.
const padOf = (size: number) => Math.round(size * 0.085);
const radiusOf = (size: number) => (size >= 216 ? 24 : 20);

function Box({ size, className, children, ...rest }: { size: number; className?: string; children?: ReactNode } & HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cx("qr-card relative grid flex-none place-items-center", className)} style={{ width: size, height: size, padding: padOf(size), borderRadius: radiusOf(size) }} {...rest}>
      {children}
    </div>
  );
}

/**
 * A code the server sent, drawn. A code that is not one (a wrong side or byte count) shows the error line instead of a
 * half-drawn picture; a window the code could not spare for the logo (0) leaves the middle to the modules.
 */
export function QrCard({ code, label, size = 136 }: { code: QrBits | undefined | null; label: string; size?: number }) {
  const t = useT();
  const { logo } = useBrand();
  const drawing = useMemo(() => (code ? qrDrawing(code) : null), [code]);
  if (!drawing) {
    return (
      <Box size={size} className="bg-surface-2 text-center text-xs leading-snug text-muted">
        {t("qr.error")}
      </Box>
    );
  }
  const side = size - 2 * padOf(size);
  const mark = Math.round(((side * drawing.window) / drawing.n) * 100) / 100;
  return (
    <Box size={size} className="bg-white text-[#0c0c0e]">
      <svg role="img" aria-label={label} viewBox={`0 0 ${drawing.n} ${drawing.n}`} className="block size-full fill-current">
        <path fillRule="evenodd" d={drawing.d} />
      </svg>
      {mark > 0 && (
        <span className="absolute top-1/2 left-1/2 grid -translate-x-1/2 -translate-y-1/2 place-items-center" style={{ width: mark, height: mark }}>
          {logo === null ? <img src={monoBadgeSrc()} alt="" draggable={false} width={mark} height={mark} className="block size-full" /> : <Logo size={mark} />}
        </span>
      )}
    </Box>
  );
}

/** The card's place while the code is on its way: a skeleton of the card's size, so the layout does not jump. */
export function QrPending({ size = 136 }: { size?: number }) {
  const t = useT();
  return <Box size={size} role="img" aria-busy="true" aria-label={t("common.loading")} className="animate-[mg-skp_1.4s_ease-in-out_infinite] bg-step" />;
}

type Encoded = { state: "loading" } | { state: "ready"; code: QrMessage } | { state: "none" } | { state: "error" };

/**
 * The QR code of a text (a subscription link, a device's config, a proxy link): the server encodes it (InstanceService.EncodeQr)
 * and the card draws what comes back. While it is on its way a skeleton of the card's size stands in, so the layout does not
 * jump. A text too long for any QR code calls `onFail` and shows the error line, so the caller can offer the file instead; a
 * failed request shows the line and "Try again".
 */
export function QrCode({ value, label, size = 136, onFail }: { value: string; label: string; size?: number; onFail?: () => void }) {
  const t = useT();
  const [res, setRes] = useState<Encoded>({ state: "loading" });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const ctl = new AbortController();
    setRes({ state: "loading" });
    instance
      .encodeQr({ text: value }, { signal: ctl.signal })
      .then((r) => {
        if (ctl.signal.aborted) return;
        setRes(r.qr ? { state: "ready", code: r.qr } : { state: "none" });
        if (!r.qr) onFail?.();
      })
      .catch((e: unknown) => {
        if (ctl.signal.aborted) return;
        // the server refuses a text over its limit: too long for a code, as one it could not fit
        const tooLong = e instanceof ConnectError && e.code === Code.InvalidArgument;
        setRes({ state: tooLong ? "none" : "error" });
        if (tooLong) onFail?.();
      });
    return () => ctl.abort();
    // onFail is a notification, not an input
    // oxlint-disable-next-line react-hooks/exhaustive-deps
  }, [value, attempt]);

  if (res.state === "ready") return <QrCard code={res.code} label={label} size={size} />;
  if (res.state === "loading") return <QrPending size={size} />;
  return (
    <Box size={size} className="bg-surface-2 text-center text-xs leading-snug text-muted">
      <div className="flex flex-col items-center gap-2">
        <span>{t("qr.error")}</span>
        {res.state === "error" && (
          <button type="button" className="font-bold text-accent-text hover:underline" onClick={() => setAttempt((n) => n + 1)}>
            {t("common.retry")}
          </button>
        )}
      </div>
    </Box>
  );
}
