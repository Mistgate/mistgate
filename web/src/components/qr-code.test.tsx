import { Code, ConnectError } from "@connectrpc/connect";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { setBrand } from "@/lib/brand";
import { demoQr } from "@/lib/qr-demo";
import { QrCard, QrCode } from "./qr-code";

const encodeQr = vi.fn();
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<object>()), instance: { encodeQr: (...a: unknown[]) => encodeQr(...a) } }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => setBrand({ logoSvg: null }));
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  encodeQr.mockReset();
});

async function mount(ui: ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(ui));
}
async function remount(ui: ReactElement) {
  act(() => root?.unmount());
  host?.remove();
  await mount(ui);
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const card = () => host!.querySelector<HTMLElement>(".qr-card")!;
const pathOf = () => host!.querySelector("svg path")!.getAttribute("d")!;

describe("QrCard: the board's card, drawn from the bits the server sent", () => {
  it("draws the path the Go renderer draws for the same code (the bits, their order, the eyes, the hole, the corners)", async () => {
    // demoQr is Packed() of the "link" sample of internal/qr/testdata/path_golden.json, with its window
    const golden = JSON.parse(readFileSync(resolve(process.cwd(), "../internal/qr/testdata/path_golden.json"), "utf8")) as { name: string; window: number; d_sha256: string }[];
    const link = golden.find((g) => g.name === "link")!;
    expect(demoQr.window).toBe(link.window);
    await mount(<QrCard code={demoQr} label="QR" />);
    expect(createHash("sha256").update(pathOf()).digest("hex")).toBe(link.d_sha256);
    expect(host!.querySelector("svg")!.getAttribute("viewBox")).toBe("0 0 41 41");
    expect(host!.querySelector("path")!.getAttribute("fill-rule")).toBe("evenodd");
  });

  it("is white with the board's padding and corners: 12 and 20 at 136 px, 18 and 24 at 216 px", async () => {
    await mount(<QrCard code={demoQr} label="QR" />);
    expect(card().style.width).toBe("136px");
    expect([card().style.padding, card().style.borderRadius]).toEqual(["12px", "20px"]);
    expect(card().className).toContain("bg-white");
    await act(async () => root!.render(<QrCard code={demoQr} label="QR" size={216} />));
    expect([card().style.width, card().style.padding, card().style.borderRadius]).toEqual(["216px", "18px", "24px"]);
  });

  it("puts the built-in mark in the hole in tonal grey, and an instance's own logo as it is", async () => {
    await mount(<QrCard code={demoQr} label="QR" />);
    const img = host!.querySelector("img")!;
    const svg = decodeURIComponent(img.getAttribute("src")!.replace("data:image/svg+xml,", ""));
    // tonal grey: the disc and the cat in the ink of the code, greys between, the eye white; the clip and the mask stay
    for (const v of ["--mg-ink:#0c0c0e", "--mg-deep:#8a8a93", "--mg-a:#c9c9d0", "--mg-paper:#f4f4f6", "--mg-mist:#fff", "--mg-eye:#fff"]) expect(svg).toContain(v);
    expect(svg).toContain('clip-path="url(#mg-clip)"');
    expect(svg).toContain('mask="url(#mg-gap)"');
    expect(svg).not.toContain("oklch");
    expect(img.width).toBeCloseTo((136 - 24) * (demoQr.window / 41), 0); // the hole's share of the code
    expect(card().querySelectorAll("img")).toHaveLength(1);

    setBrand({ logoSvg: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><circle cx="5" cy="5" r="4" fill="#cba5fa"/></svg>' });
    await act(async () => root!.render(<QrCard code={demoQr} label="QR" />));
    expect(host!.querySelector("img")).toBeNull();
    expect(card().querySelector("span svg circle")).not.toBeNull();
  });

  it("leaves no logo where the server cut no hole", async () => {
    await mount(<QrCard code={{ ...demoQr, window: 0 }} label="QR" />);
    expect(host!.querySelector("img")).toBeNull();
    expect(pathOf().length).toBeGreaterThan(0);
  });

  it("is the error line, not a half-drawn picture, when the data is no code", async () => {
    const bad = [
      undefined,
      null,
      { ...demoQr, n: 22 },
      { ...demoQr, n: 41.5 },
      { ...demoQr, n: 181 },
      { ...demoQr, bits: demoQr.bits.slice(1) },
      { ...demoQr, bits: new Uint8Array(demoQr.bits.length + 1) },
      { ...demoQr, bits: "AAAA" as unknown as Uint8Array },
    ];
    for (const code of bad) {
      await remount(<QrCard code={code} label="QR" />);
      expect(host!.querySelector("svg"), JSON.stringify(code)).toBeNull();
      expect(card().textContent).toBe("Could not draw the QR code.");
      expect(card().style.width).toBe("136px"); // the same box, the layout does not move
    }
  });

  it("drops a window that is nonsense and keeps the code", async () => {
    for (const window of [-1, NaN, Infinity, 21]) {
      await remount(<QrCard code={{ ...demoQr, window }} label="QR" />);
      expect(host!.querySelector("svg path"), String(window)).not.toBeNull();
      expect(host!.querySelector("img")).toBeNull();
    }
  });

  it("takes the label as text of an attribute, never as markup", async () => {
    await mount(<QrCard code={demoQr} label="QR <img src=x onerror=alert(1)>" />);
    expect(host!.querySelector("svg")!.getAttribute("aria-label")).toBe("QR <img src=x onerror=alert(1)>");
    expect(host!.querySelectorAll("img")).toHaveLength(1); // the mark, not the label's
    expect(host!.querySelector("[onerror]")).toBeNull();
  });
});

describe("QrCode: the server encodes the text", () => {
  it("shows a skeleton of the card's size, then the code; the text is sent once, as the request says", async () => {
    let done!: (v: unknown) => void;
    encodeQr.mockReturnValue(new Promise((r) => (done = r)));
    await mount(<QrCode value="https://example.com/s/abc" label="QR" size={216} />);
    expect(card().getAttribute("aria-busy")).toBe("true");
    expect([card().style.width, card().style.height]).toEqual(["216px", "216px"]);
    expect(host!.querySelector("svg")).toBeNull();
    await act(async () => done({ qr: demoQr }));
    expect(host!.querySelector("svg path")).not.toBeNull();
    expect([card().style.width, card().style.height]).toEqual(["216px", "216px"]); // no jump
    expect(encodeQr).toHaveBeenCalledTimes(1);
    expect(encodeQr.mock.calls[0]![0]).toEqual({ text: "https://example.com/s/abc" });
  });

  it("asks again for another text, and not for the same one", async () => {
    encodeQr.mockResolvedValue({ qr: demoQr });
    await mount(<QrCode value="a" label="QR" />);
    await act(async () => root!.render(<QrCode value="a" label="QR" />));
    expect(encodeQr).toHaveBeenCalledTimes(1);
    await act(async () => root!.render(<QrCode value="b" label="QR" />));
    expect(encodeQr).toHaveBeenCalledTimes(2);
  });

  it("calls onFail, and says so, for a text too long for any QR code (no code, or a refusal for the length)", async () => {
    const onFail = vi.fn();
    encodeQr.mockResolvedValue({});
    await mount(<QrCode value={"x".repeat(2000)} label="QR" onFail={onFail} />);
    await settle();
    expect(onFail).toHaveBeenCalledTimes(1);
    expect(card().textContent).toBe("Could not draw the QR code.");
    expect(card().querySelector("button")).toBeNull();

    encodeQr.mockRejectedValue(new ConnectError("the text must be 1 to 4096 bytes", Code.InvalidArgument));
    await act(async () => root!.render(<QrCode value={"y".repeat(5000)} label="QR" onFail={onFail} />));
    await settle();
    expect(onFail).toHaveBeenCalledTimes(2);
    expect(card().querySelector("button")).toBeNull();
  });

  it("says a failed request, offers to try again, and draws the code when it works", async () => {
    const onFail = vi.fn();
    encodeQr.mockRejectedValueOnce(new ConnectError("down", Code.Unavailable)).mockResolvedValueOnce({ qr: demoQr });
    await mount(<QrCode value="a" label="QR" onFail={onFail} />);
    await settle();
    expect(card().textContent).toContain("Could not draw the QR code.");
    expect(onFail).not.toHaveBeenCalled(); // this is not a text that cannot be drawn
    const retry = card().querySelector("button")!;
    expect(retry.textContent).toBe("Try again");
    await act(async () => retry.click());
    await settle();
    expect(host!.querySelector("svg path")).not.toBeNull();
    expect(encodeQr).toHaveBeenCalledTimes(2);
  });

  it("does not draw an answer that comes after the text changed", async () => {
    let first!: (v: unknown) => void;
    encodeQr.mockReturnValueOnce(new Promise((r) => (first = r))).mockResolvedValueOnce({ qr: { ...demoQr, window: 0 } });
    await mount(<QrCode value="a" label="QR" />);
    await act(async () => root!.render(<QrCode value="b" label="QR" />));
    await settle();
    await act(async () => first({ qr: demoQr })); // the answer for "a" is late
    expect(host!.querySelector("img")).toBeNull(); // still the code for "b", which has no window
  });

  it("keeps nothing of the text on the page", async () => {
    encodeQr.mockResolvedValue({ qr: demoQr });
    await mount(<QrCode value="https://example.com/s3cr3t/token-xyz" label="QR" />);
    expect(host!.innerHTML).not.toContain("s3cr3t");
    expect(JSON.stringify(Object.keys(localStorage)) + JSON.stringify(Object.keys(sessionStorage))).not.toContain("s3cr3t");
  });
});
