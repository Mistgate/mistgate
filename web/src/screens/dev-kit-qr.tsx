import type { ReactNode } from "react";
import { QrCard, QrCode } from "@/components/qr-code";
import { TotpEnroll } from "@/components/auth-parts";
import { instance } from "@/lib/api";
import { demoQr } from "@/lib/qr-demo";
import { QrDialog } from "@/screens/users/user-bits";

// Development only, /dev-kit?only=qr&state=…: the QR codes of the panel, drawn from bits (components/qr-code.tsx) as the server sends
// them. There is no server here, so EncodeQr is answered by this file for every scene of the kit (the link window, the device's
// config, the Telegram and TrustTunnel dialogs):
//   &qr=slow    the answer never comes: the skeleton
//   &qr=long    "no code": the text is too long for a QR code
//   &qr=err     the request fails: the line and "Try again"
//   (none)      the made-up 41-module code
// Scenes (state=): cards (the sizes of the three places side by side, and every state), dialog (the key's QR window, 216 px).
// Open the page at 1440 and at 390 px, in both themes, next to the board's .u-qr.

const mode = new URLSearchParams(location.search).get("qr") ?? "";

Object.assign(instance, {
  encodeQr: async () => {
    if (mode === "slow") return new Promise<never>(() => {});
    if (mode === "err") throw new Error("down");
    return mode === "long" ? {} : { qr: demoQr };
  },
});

const caption = "text-xs text-muted";

function Tile({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="flex flex-col items-start gap-2">
      <span className={caption}>{title}</span>
      {children}
    </div>
  );
}

export function QrKit() {
  const state = new URLSearchParams(location.search).get("state") ?? "cards";
  if (state === "dialog") {
    return <QrDialog open onOpenChange={() => {}} title="QR code · de1" text="Point the phone's camera at it, and the key opens in the app." value="tt://?AAEBAQ9kZTEuZXhhbXBsZS5jb20" label="QR code of the tt:// link to de1" copyLabel="Copy link" />;
  }
  const bad = { n: 22, bits: new Uint8Array(3), window: 0 };
  return (
    <section data-shot="qr-cards" className="flex flex-wrap items-start gap-6 p-4">
      <Tile title="136 · TOTP card of the sign-in">
        <TotpEnroll qr={demoQr} secret="JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP" caption="Scan it with an authenticator app" />
      </Tile>
      <Tile title="232 · subscription link">
        <QrCode value="https://vpn.example.com/y4pt5Qm8ZrLw3VcXnB7d2Ke9HsAf" label="Subscription link" size={232} />
      </Tile>
      <Tile title="216 · key or proxy link (big)">
        <QrCode value="tg://proxy?server=de1.example.com&port=443&secret=ee0123" label="Proxy link" size={216} />
      </Tile>
      <Tile title="252 · AmneziaWG config">
        <QrCode value="[Interface]" label="Device config" size={252} />
      </Tile>
      <Tile title="no hole (the code cannot spare it)">
        <QrCard code={{ ...demoQr, window: 0 }} label="No logo" />
      </Tile>
      <Tile title="not a code: the error line, the same box">
        <QrCard code={bad} label="Bad" />
      </Tile>
    </section>
  );
}
