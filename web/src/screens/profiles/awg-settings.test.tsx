import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { GenerateMode } from "@/gen/mistgate/admin/v1/awg_pb";
import { AwgSettings } from "./awg-settings";
import { allFields, parseSchema, parseSettings, type Field, type Settings } from "./schema";
import * as fixture from "./schema.fixtures";

// AmneziaWG's three cards of the profile page (the board's "Основное", "Маскировка и обход", "Сеть") made of the panel's real schema: where each field
// lands, what the controls do, that nothing the schema offers is lost, and the score, the folds and the .conf box.

const listMimicryPresets = vi.fn();
const generateObfuscation = vi.fn();
vi.mock("@/lib/api", () => ({ awg: { listMimicryPresets: (...a: unknown[]) => listMimicryPresets(...a), generateObfuscation: (...a: unknown[]) => generateObfuscation(...a) } }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const preset = (id: string, over: Record<string, unknown> = {}) => ({ id, name: id.toUpperCase(), versions: ["3.1", "2.0"], usesDomain: false, naturalPorts: [], variesPerDevice: true, ...over });
beforeEach(() => {
  listMimicryPresets.mockResolvedValue({ presets: [preset("dns", { usesDomain: true, naturalPorts: [53] }), preset("quic", { usesDomain: true, naturalPorts: [443] }), preset("stun", { variesPerDevice: false }), preset("custom", { variesPerDevice: false })], domains: ["a.example.org", "b.example.org"] });
  generateObfuscation.mockImplementation(async (req: { mode: GenerateMode; preset: string }) => ({ obfuscationJson: JSON.stringify(req.mode === GenerateMode.SIGNATURE ? { preset: req.preset, i1: `<b 0x${req.preset}>` } : { preset: req.preset, jc: 9, s1: 40, h1: "1000-2000" }) }));
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  listMimicryPresets.mockReset();
  generateObfuscation.mockReset();
});

const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};

const start: Settings = parseSettings(
  JSON.stringify({
    version: "3.1",
    port: 47213,
    mtu: 1280,
    egress: "direct",
    subnet4: "10.66.4.0/22",
    subnet6: "fd66:66:0:1::/64",
    obfuscation: { preset: "dns", domain: "", jc: 5, jmin: 24, jmax: 71, s1: 21, h1: "1", i1: "<b 0x1a2b>", per_device_signature: true, signature_seed: "9f2c", header_protection_key: "••••", random_trailers: true, persistent_keepalive: "31" },
  }),
);

type Over = { schema?: string; locked?: boolean; scene?: string; score?: boolean; settings?: Settings };
let changes: Settings[] = [];
let goes: string[] = [];
let twins = 0;

async function mount(over: Over = {}) {
  const fields: Field[] = allFields(parseSchema(over.schema ?? fixture.awg));
  changes = [];
  goes = [];
  twins = 0;
  function Host() {
    const [settings, setSettings] = useState<Settings>(over.settings ?? start);
    const edit = (n: Settings) => {
      changes.push(n);
      setSettings(n);
    };
    return (
      <AwgSettings
        fields={fields}
        settings={settings}
        base={start}
        problems={[]}
        onChange={edit}
        onRow={edit}
        lead={<div className="pp-row" data-k="name">name</div>}
        locked={over.locked ?? true}
        egressNote={() => null}
        twin={{ word: "Make a copy via WARP…", dirty: false, open: () => void twins++ }}
        score={over.score === false ? undefined : { value: 88, base: 95, tier: "header_protection", items: [{ code: "per_device_signature", delta: 3, pointer: "/obfuscation/per_device_signature", params: {} }, { code: "keepalive_over_nat", delta: -5, pointer: "/obfuscation/persistent_keepalive", params: {} }] } as never}
        onGo={(p) => void goes.push(p)}
        scene={over.scene ? new Set(over.scene.split(".")) : undefined}
      />
    );
  }
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <Host />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
  return host;
}
const $ = <T extends HTMLElement = HTMLElement>(sel: string) => host!.querySelector<T>(sel);
const all = (sel: string) => [...host!.querySelectorAll<HTMLElement>(sel)];
const click = async (e: HTMLElement | null) => {
  await act(async () => e!.click());
  await settle();
};
const titles = () => all("section.pp-sec:not(.pp-scb) .pp-sh h3").map((h) => h.textContent);
const byText = (sel: string, text: string) => all(sel).find((b) => b.textContent?.includes(text))!;

describe("the three cards", () => {
  it("are Basics, Masquerade and bypass and Network, in that order, with the board's rows in each", async () => {
    await mount();
    expect(titles()).toEqual(["Basics", "Masquerade and bypass", "Network"]);
    const rows = (i: number) => all("section.pp-sec")[i]!.querySelectorAll<HTMLElement>(":scope > .pp-rows > .pp-row, :scope > .pp-rows > .pp-adv");
    expect([...rows(0)].map((r) => r.dataset.k)).toEqual(["name", "version", "port"]);
    expect([...rows(1)].map((r) => r.dataset.k ?? "fold")).toEqual(["obfuscation.preset", "obfuscation.domain", "obfuscation.per_device_signature", "fold"]);
    expect([...rows(2)].map((r) => r.dataset.k ?? "fold")).toEqual(["egress", "mtu", "fold"]);
  });

  it("say it in the board's words: a hint under each row, the folds with their counts", async () => {
    await mount();
    expect($("[data-k=port] .pp-lb")?.textContent).toContain("The node listens here");
    expect($("[data-k='obfuscation.preset'] .pp-lb")?.textContent).toContain("How the handshake looks from outside");
    expect($("[data-k=egress] .pp-lb")?.textContent).toContain("Where the server goes out to the internet from");
    expect($("[data-k=mtu] .pp-lb")?.textContent).toContain("Desktop AmneziaVPN sets its own for vpn://");
    const folds = all(".pp-advh").map((b) => b.textContent);
    expect(folds[0]).toContain("Advanced: junk, S, H, I1–I5, key, timers (26)");
    expect(folds[0]).toContain("critical");
    // the client networks are kept: the fold says so and holds nothing that could break a key
    expect(folds[1]).toContain("Advanced: keepalive, client networks (pinned)");
    expect(folds[1]).not.toContain("critical");
  });

  it("keep every field of the schema: each one is in a row or a card, once", async () => {
    await mount({ scene: "adv.net.show" });
    const ids = allFields(parseSchema(fixture.awg)).map((f) => f.id);
    for (const id of ids) {
      if (id === "version") expect($("[data-k=version]"), id).not.toBeNull();
      else expect(all(`[data-k="${id}"]`).length, id).toBe(1);
    }
  });

  it("put a field the board has no row for in the card it fits: a block field into the fold of the look, any other into Basics", async () => {
    const extra = JSON.parse(fixture.awg);
    extra.properties.dscp = { type: "integer", title: "DSCP", "x-group": "basics", "x-order": 90 };
    extra.properties.obfuscation.properties.decoy_count = { type: "integer", title: "Decoys", "x-order": 300 };
    await mount({ schema: JSON.stringify(extra), scene: "adv.show" });
    expect(all("section.pp-sec")[0]!.querySelector("[data-k=dscp]")).not.toBeNull();
    const look = all("section.pp-sec")[1]!;
    expect(look.querySelector("[data-k='obfuscation.decoy_count']")).not.toBeNull();
    expect(look.querySelector(".pp-advh")?.textContent).toContain("(27)");
  });
});

describe("the version", () => {
  it("is two cards; picking the other rebuilds the block for it (mode FULL)", async () => {
    await mount();
    expect(all("[data-k=version] [role=radio]").map((r) => r.getAttribute("aria-checked"))).toEqual(["true", "false"]);
    expect($("[data-k=version] .pp-lb")?.textContent).toContain("critical");
    await click(all("[data-k=version] [role=radio]")[1]!);
    expect(generateObfuscation.mock.calls[0]![0]).toMatchObject({ version: "2.0", mode: GenerateMode.FULL });
    expect(changes.at(-1)).toMatchObject({ version: "2.0" });
    expect($("[data-k=version] .pp-rmk.part")?.textContent).toContain("only for old clients");
  });
});

describe("the look of the first packets", () => {
  it("is a row of chips with the usual port under it; a look that carries a domain brings the domain's row", async () => {
    await mount();
    expect(all("[data-k='obfuscation.preset'] [role=radio]").map((b) => b.textContent)).toEqual(["DNS", "QUIC", "STUN", "Custom"]);
    expect($("[data-k='obfuscation.preset'] .pp-pcn")?.textContent).toBe("Usually port 53");
    expect($("[data-k='obfuscation.domain']")).not.toBeNull();
    await click(byText("[data-k='obfuscation.preset'] [role=radio]", "STUN"));
    expect(generateObfuscation.mock.calls[0]![0]).toMatchObject({ preset: "stun", mode: GenerateMode.SIGNATURE });
    expect($("[data-k='obfuscation.domain']")).toBeNull();
    expect($("[data-k='obfuscation.preset'] .pp-pcn")).toBeNull();
    await click(byText("[data-k='obfuscation.preset'] [role=radio]", "QUIC"));
    expect($("[data-k='obfuscation.preset'] .pp-pcn")?.textContent).toBe("The config doesn’t fit into a QR code");
    await click(byText("[data-k='obfuscation.preset'] [role=radio]", "Custom"));
    expect($("[data-k='obfuscation.preset'] .pp-pcn")?.textContent).toContain("in “Advanced”");
  });

  it("takes a domain typed or drawn, and fetches the packets again with it", async () => {
    await mount();
    await click(byText("[data-k='obfuscation.domain'] button", "Random"));
    expect(generateObfuscation.mock.calls[0]![0]).toMatchObject({ preset: "dns", mode: GenerateMode.SIGNATURE });
    expect(["a.example.org", "b.example.org"]).toContain(generateObfuscation.mock.calls[0]![0].domain);
    expect($("[data-k='obfuscation.domain'] input")?.getAttribute("list")).toBeTruthy();
    expect($("[data-k='obfuscation.domain'] input")?.getAttribute("placeholder")).toBe("each device has its own");
  });

  it("has a signature of its own for every device as a switch that says what it does; no switch where it changes nothing", async () => {
    await mount();
    const sw = () => $<HTMLButtonElement>("[data-k='obfuscation.per_device_signature'] [role=switch]")!;
    expect(sw().getAttribute("aria-checked")).toBe("true");
    expect($("[data-k='obfuscation.per_device_signature'] .pp-lb")?.textContent).toContain("Each device has its own signature packets");
    await click(sw());
    expect((changes.at(-1)!.obfuscation as { per_device_signature: boolean }).per_device_signature).toBe(false);
    expect($("[data-k='obfuscation.per_device_signature'] .pp-lb")?.textContent).toContain("All devices share one set of signature packets");
    await click(byText("[data-k='obfuscation.preset'] [role=radio]", "STUN"));
    expect(sw().disabled).toBe(true);
    expect($("[data-k='obfuscation.per_device_signature'] .pp-lb")?.textContent).toContain("the switch changes nothing");
  });
});

describe("the score", () => {
  it("is a pill in the card's head that opens the card of what it is made of, each reason with its sign and the way to its field", async () => {
    await mount();
    expect($(".pp-score")?.textContent).toBe("Disguise 88/100");
    expect($(".pp-score")?.className).toContain("ok");
    expect($(".pp-scb")).toBeNull();
    await click($(".pp-score"));
    expect($(".pp-scb h3")?.textContent).toBe("What the score is made of");
    const rows = all(".pp-scb .pp-sci").map((r) => r.textContent);
    expect(rows[0]).toContain("95");
    expect(rows[0]).toContain("Base: headers are sealed with the protection key");
    expect(rows[1]).toContain("+3");
    expect(rows[2]).toContain("−5");
    await click(all(".pp-scb .pp-sci button")[2]!);
    expect(goes).toEqual(["/obfuscation/persistent_keepalive"]);
    await click($(".pp-scb .ic"));
    expect($(".pp-scb")).toBeNull();
  });
  it("is not there without the server's preview", async () => {
    await mount({ score: false });
    expect($(".pp-score")).toBeNull();
  });
});

describe("the fold of the block", () => {
  it("is closed; open it and the values are dots until Show values, then they are boxes that take what is typed", async () => {
    await mount();
    expect($(".pp-hgr")).toBeNull();
    await click(all(".pp-advh")[0]!);
    expect($(".pp-advt")?.textContent).toContain("Values are hidden");
    expect(all(".pp-hgr .rw-sl").map((s) => s.textContent)).toEqual(["Junk", "Sizes", "Headers", "Signature", "Timers 3.1"]);
    expect($("[data-k='obfuscation.jc'] b")?.textContent).toBe("••••");
    expect($("[data-k='obfuscation.jc'] input")).toBeNull();
    // the key the panel keeps says so
    expect($("[data-k='obfuscation.header_protection_key'] b")?.textContent).toBe("set, not shown");
    await click($(".pp-show"));
    expect($(".pp-show")?.textContent).toBe("Hide values");
    const jc = $<HTMLInputElement>("[data-k='obfuscation.jc'] input")!;
    expect(jc.value).toBe("5");
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      set.call(jc, "7");
      jc.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect((changes.at(-1)!.obfuscation as { jc: number }).jc).toBe(7);
    expect($("[data-k='obfuscation.jc']")?.className).toContain("ch");
    // a flag says on or off and flips
    await click($("[data-k='obfuscation.random_trailers'] [role=switch]"));
    expect((changes.at(-1)!.obfuscation as { random_trailers: boolean }).random_trailers).toBe(false);
  });

  it("holds the new values for the whole block and the .conf box, which says keys are not taken", async () => {
    await mount({ scene: "adv" });
    const gen = byText(".pp-advf button", "New values for the whole block");
    expect(gen.querySelector(".pp-crit")?.textContent).toBe("critical");
    await click(gen);
    expect(generateObfuscation.mock.calls[0]![0]).toMatchObject({ version: "3.1", mode: GenerateMode.FULL });
    expect((changes.at(-1)!.obfuscation as { jc: number }).jc).toBe(9);
    expect($(".pf-conf")).toBeNull();
    await click(byText(".pp-advf button", "Fill from .conf"));
    expect($(".pf-conf small")?.textContent).toBe("Keys aren’t taken from the file");
    await act(async () => {
      const ta = $<HTMLTextAreaElement>(".pf-conf textarea")!;
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(ta, "[Interface]\nJc = 12\nMTU = 1200\n");
      ta.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(byText(".pf-conf button", "Fill in"));
    expect(changes.at(-1)).toMatchObject({ mtu: 1200 });
    expect((changes.at(-1)!.obfuscation as { jc: number }).jc).toBe(12);
    expect($(".pf-conf")).toBeNull();
    expect($(".pf-conf-r")?.textContent).toContain("Filled");
  });

  it("opens itself for a field that has a problem", async () => {
    const fields = allFields(parseSchema(fixture.awg));
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const qc = new QueryClient();
    await act(async () =>
      root!.render(
        <QueryClientProvider client={qc}>
          <ToastProvider>
            <AwgSettings fields={fields} settings={start} base={start} problems={[{ pointer: "/obfuscation/jc", message: "Junk packets must be 0–128" }]} onChange={() => {}} onRow={() => {}} lead={null} locked egressNote={() => null} twin={null} onGo={() => {}} />
          </ToastProvider>
        </QueryClientProvider>,
      ),
    );
    await settle();
    expect($(".pp-advh")?.getAttribute("aria-expanded")).toBe("true");
    expect($(".pp-hgr .pp-ferr")?.textContent).toContain("Junk packets must be 0–128");
    expect($("[data-k='obfuscation.jc']")?.className).toContain("err");
  });
});

describe("the network", () => {
  it("is the exit as two parts, a link to a copy through WARP, the MTU in bytes", async () => {
    await mount();
    expect(all("[data-k=egress] .pp-seg label, [data-k=egress] .pp-seg [role=radio]").map((b) => b.textContent)).toEqual(["From the node’s address", "Via WARP"]);
    await click($("[data-k=egress] .pp-twin"));
    expect(twins).toBe(1);
    expect($("[data-k=mtu] em")?.textContent).toBe("bytes");
  });

  it("keeps the client networks of a profile on a node as locked values; a profile on none has them typed", async () => {
    await mount({ scene: "net" });
    expect(all("[data-k=subnets] .pf-lock").map((x) => x.textContent)).toEqual(["10.66.4.0/22", "fd66:66:0:1::/64"]);
    expect($("[data-k=subnets] .pp-lb")?.textContent).toContain("Locked: the profile is already on a node");
    expect($("[data-k=obfuscation\\.persistent_keepalive] em")?.textContent).toBe("s");
    act(() => root?.unmount());
    host?.remove();
    await mount({ scene: "net", locked: false });
    expect(all("[data-k=subnets] input").map((i) => (i as HTMLInputElement).value)).toEqual(["10.66.4.0/22", "fd66:66:0:1::/64"]);
    expect($("[data-k=subnets] .pp-lb")?.textContent).toContain("Leave blank to assign a free client network");
    expect(all(".pp-advh")[1]!.textContent).toContain("Advanced: keepalive, client networks");
    expect(all(".pp-advh")[1]!.textContent).not.toContain("pinned");
    // typing a network goes through
    const v4 = $<HTMLInputElement>("[data-k=subnets] input")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(v4, "10.70.0.0/22");
      v4.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(changes.at(-1)).toMatchObject({ subnet4: "10.70.0.0/22" });
  });

  it("reveals the fold of a field when asked to, before the page scrolls to it", async () => {
    const fields = allFields(parseSchema(fixture.awg));
    const reveal: { current: ((p: string) => boolean) | null } = { current: null };
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const qc = new QueryClient();
    await act(async () =>
      root!.render(
        <QueryClientProvider client={qc}>
          <ToastProvider>
            <AwgSettings fields={fields} settings={start} base={start} problems={[]} onChange={() => {}} onRow={() => {}} lead={null} locked egressNote={() => null} twin={null} onGo={() => {}} register={(fn) => (reveal.current = fn)} />
          </ToastProvider>
        </QueryClientProvider>,
      ),
    );
    await settle();
    expect($(".pp-hgr")).toBeNull();
    let waits = false;
    await act(async () => void (waits = reveal.current!("/obfuscation/jc")));
    expect(waits).toBe(true);
    expect($(".pp-hgr")).not.toBeNull();
    // a field outside the folds needs no waiting
    expect(reveal.current!("/port")).toBe(false);
  });
});
