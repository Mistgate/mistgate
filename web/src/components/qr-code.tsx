import { useEffect, useState } from "react";

/**
 * QR code as a crisp SVG: dark cells on a white card (scanners need the light quiet zone in both
 * themes). The encoder is loaded on first use, so it stays out of the main bundle. `size` is the side in px
 * (a long text, like an AmneziaWG config, needs room: a code of 60 modules is unreadable at 132 px).
 * A text too long for any QR version calls `onFail` and draws nothing, so the caller can offer the file instead.
 */
export function QrCode({ value, label, size = 132, onFail }: { value: string; label: string; size?: number; onFail?: () => void }) {
  const [cells, setCells] = useState<{ n: number; d: string } | null>(null);

  useEffect(() => {
    let live = true;
    void import("qrcode-generator").then(({ default: qrcode }) => {
      let out: { n: number; d: string } | null = null;
      try {
        const qr = qrcode(0, "M");
        qr.addData(value);
        qr.make();
        const n = qr.getModuleCount();
        let d = "";
        for (let y = 0; y < n; y++) for (let x = 0; x < n; x++) if (qr.isDark(y, x)) d += `M${x} ${y}h1v1h-1z`;
        out = { n, d };
      } catch {
        // too long for any QR version
      }
      if (!live) return;
      setCells(out);
      if (!out) onFail?.();
    });
    return () => {
      live = false;
    };
    // onFail is a notification, not an input
    // oxlint-disable-next-line react-hooks/exhaustive-deps
  }, [value]);

  return (
    <div className="flex-none rounded-[10px] bg-white p-2">
      <svg
        role="img"
        aria-label={label}
        viewBox={`0 0 ${cells?.n ?? 25} ${cells?.n ?? 25}`}
        shapeRendering="crispEdges"
        className="block aspect-square h-auto max-w-full"
        style={{ width: size }}
      >
        {cells && <path d={cells.d} fill="#0c0c0e" />}
      </svg>
    </div>
  );
}
