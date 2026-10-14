import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { dictionaries, setLang, type Lang } from "@/i18n";
import { SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { useTx } from "@/screens/users/t";
import { dropsOf, FormatDrops, FormatsLine } from "./formats";

type F = { format: SubFormat; ok: boolean; note: string };
const order = [SubFormat.BASE64_URIS, SubFormat.MIHOMO_YAML, SubFormat.XRAY_JSON, SubFormat.SINGBOX_JSON];
/** The four formats in the order of the API, each [ok, note]. */
const four = (...rows: [boolean, string][]): F[] => rows.map(([ok, note], i) => ({ format: order[i]!, ok, note }));

const allOk = four([true, ""], [true, ""], [true, ""], [true, ""]);
const xhttp = four([true, ""], [true, ""], [true, ""], [false, "xhttp"]);
const selfSigned = four([true, ""], [true, ""], [true, ""], [false, "self_signed"]);
const awg = four([false, "awg"], [true, ""], [false, "awg"], [false, "awg"]);
const hop = four([true, "hop_single_port"], [true, ""], [true, "hop_single_port"], [true, ""]);

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  act(() => setLang("en"));
});

function Line({ formats, drops }: { formats: F[]; drops?: boolean }) {
  const t = useTx();
  return drops ? <FormatDrops formats={formats} t={t} /> : <FormatsLine formats={formats} t={t} />;
}
function render(formats: F[], lang: Lang = "en", drops = false) {
  act(() => setLang(lang));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<Line formats={formats} drops={drops} />));
}
// the quiet formats, and the marks that take focus (drops and dotted ones) with their accessible names
const quiet = () => host!.querySelector("[role=group] > span")?.textContent;
const marks = () => [...host!.querySelectorAll<HTMLElement>("[tabindex='0']")].map((m) => ({ text: m.textContent, label: m.getAttribute("aria-label") }));

describe("the formats line of a profile", () => {
  it("lists all four quietly when the profile works everywhere, with nothing to point at", () => {
    render(allOk);
    expect(quiet()).toBe("Link list · Mihomo · Xray JSON · sing-box");
    expect(marks()).toEqual([]);
  });

  it("drops sing-box for XHTTP with a chip that says why, the full sentence in its name", () => {
    render(xhttp);
    expect(quiet()).toBe("Link list · Mihomo · Xray JSON");
    expect(marks()).toEqual([{ text: "not in sing-box: XHTTP", label: "not in sing-box: XHTTP. sing-box does not support XHTTP." }]);
  });

  it("puts the three formats AmneziaWG is not in under one chip", () => {
    render(awg);
    expect(quiet()).toBe("Mihomo");
    expect(marks()).toEqual([{ text: "not in Link list, Xray JSON, sing-box: AmneziaWG", label: "not in Link list, Xray JSON, sing-box: AmneziaWG. AmneziaWG: AmneziaVPN keys and Mihomo only." }]);
  });

  it("dots the formats that write a port-hopping range as one port, without a drop", () => {
    render(hop);
    expect(quiet()).toBe("Link list · Mihomo · Xray JSON · sing-box");
    expect(marks()).toEqual([
      { text: "Link list", label: "Link list: one port. The port range is written as one port." },
      { text: "Xray JSON", label: "Xray JSON: one port. The port range is written as one port." },
    ]);
  });

  it("shows nothing when the formats are unknown", () => {
    render([]);
    expect(host!.innerHTML).toBe("");
    act(() => root!.render(<Line formats={[]} drops />));
    expect(host!.innerHTML).toBe("");
  });

  it("speaks Russian and Chinese in their own words and marks", () => {
    render(selfSigned, "ru");
    expect(quiet()).toBe("Список · Mihomo · Xray JSON");
    expect(marks()[0]!.text).toBe("не sing-box: самоподписанный");
    act(() => setLang("zh"));
    act(() => root!.render(<Line formats={awg} />));
    expect(marks()[0]!.text).toBe("链接列表、Xray JSON、sing-box：不可用（AmneziaWG）");
    expect(marks()[0]!.label).toContain("AmneziaWG 仅用于 AmneziaVPN 密钥和 Mihomo。");
  });

  it("gives the user card only the drops", () => {
    render(xhttp, "en", true);
    expect(host!.textContent).toBe("not in sing-box: XHTTP");
    act(() => root!.render(<Line formats={allOk} drops />));
    expect(host!.innerHTML).toBe("");
  });

  it("names a drop that came without a code as unsupported", () => {
    expect(dropsOf(four([true, ""], [true, ""], [false, ""], [false, "xhttp"]))).toEqual([
      { note: "unsupported", formats: [SubFormat.XRAY_JSON] },
      { note: "xhttp", formats: [SubFormat.SINGBOX_JSON] },
    ]);
  });
});

describe("the texts of the formats line", () => {
  const notes = ["xhttp", "self_signed", "gecko", "awg", "unsupported", "hop_single_port"];
  const dict = (lang: Lang) => dictionaries[lang] as Record<string, string>;

  it("have a short form and a full sentence for every note in every language", () => {
    for (const lang of ["en", "ru", "zh"] as const)
      for (const note of notes) {
        expect(dict(lang)[`profiles.fmt.why.${note}`], `${lang} why ${note}`).toBeTruthy();
        expect(dict(lang)[`profiles.fmt.tip.${note}`], `${lang} tip ${note}`).toBeTruthy();
      }
    for (const note of notes) {
      expect(dict("ru")[`profiles.fmt.tip.${note}`]).toMatch(/[а-яё]/i);
      expect(dict("ru")[`profiles.fmt.tip.${note}`]).toMatch(/\.$/);
      expect(dict("zh")[`profiles.fmt.tip.${note}`]).toMatch(/[一-鿿]/);
      expect(dict("zh")[`profiles.fmt.tip.${note}`]).toMatch(/。$/);
    }
  });

  it("keep the Chinese style: no 您, full-width marks, a space between Han and Latin", () => {
    const zh = Object.entries(dict("zh")).filter(([k]) => k.startsWith("profiles.fmt."));
    for (const [k, v] of zh) {
      expect(v, k).not.toMatch(/您|[":;()]/);
      expect(v, k).not.toMatch(/[一-鿿][A-Za-z0-9{]|[A-Za-z0-9}][一-鿿]/);
    }
  });
});
