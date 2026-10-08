import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { EventSeverity } from "@/gen/mistgate/admin/v1/fleet_pb";
import { fill, type Lang, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { describeAudit } from "./audit";
import { errorText } from "./errors";
import { describeEvent, eventKind } from "./events";
import { bestDelivered, lossPct, lossyVars, portNotes, reasons, reasonText } from "./port-check";

const dict = { en, ru };
const tOf = (lang: Lang) => Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(dict[lang][key], vars), { n: () => "" }) as unknown as T;
const t = tOf("en");
const at = Math.floor(Date.now() / 1000) - 5 * 60;
const lossy = `port_lossy: port=8443&node=de1&sent=300&got=189&at=${at}&sender=de2&free=2053`;

describe("the share of lost packets", () => {
  it("rounds sent / got to whole percent and never leaves 0..100", () => {
    expect(lossPct(300, 189)).toBe(37);
    expect(lossPct(300, 300)).toBe(0);
    expect(lossPct(300, 0)).toBe(100);
    expect(lossPct(300, 310)).toBe(0);
    expect(lossPct(0, 0)).toBe(0);
    expect(bestDelivered([{ sent: 300, got: 120 }, { sent: 300, got: 150 }])).toBe(50);
    expect(bestDelivered([])).toBe(0);
  });
});

describe("port_lossy as an error", () => {
  it("says the loss, the node, who checked and how long ago, in both languages", () => {
    const e = new ConnectError(lossy, Code.FailedPrecondition);
    expect(errorText(e, t)).toBe("Port 8443 loses 37 % of UDP packets on de1 (checked from de2, 5 min ago).");
    expect(errorText(e, tOf("ru"))).toBe("Порт 8443 теряет 37 % UDP-пакетов на de1 (проверка с de2, 5 мин назад).");
  });
  it("names the panel when the panel sent, and no_clean_port has a sentence of its own", () => {
    expect(errorText(new ConnectError(`port_lossy: port=8443&node=de1&sent=300&got=150&at=${at}&sender=panel&free=0`, Code.FailedPrecondition), t)).toContain("checked from the panel");
    expect(errorText(new ConnectError("no_clean_port", Code.FailedPrecondition), t)).toBe(en["err.no_clean_port"]);
  });
  it("lossyVars survives a refusal without the time", () => {
    expect(lossyVars(t, { sent: "300", got: "300", sender: "n1" }).when).toBe("earlier");
  });
});

describe("what a saved inbound owes the admin", () => {
  it("words port_unchecked with its reason and port_lossy like the refusal, and nothing for other warnings", () => {
    const notes = portNotes(t, [
      { code: "warp_missing", params: { state: "none" } },
      { code: "port_unchecked", params: { node: "de1", port: "8443", reason: "no_sender" } },
      { code: "port_lossy", params: { node: "de1", port: "8443", sent: "300", got: "270", at: String(at), sender: "panel" } },
    ]);
    expect(notes).toEqual([
      "Port 8443 on de1 was not checked for UDP loss. No other node can send the test packets. A second node can check this one.",
      "Port 8443 loses 10 % of UDP packets on de1 (checked from the panel, 5 min ago).",
    ]);
    expect(portNotes(t, undefined)).toEqual([]);
  });
  it("has a one-line explanation of every reason in both languages, and 'failed' for an unknown one", () => {
    for (const r of reasons) {
      expect(reasonText(tOf("en"), r, { best: 41 }), r).not.toBe("");
      expect(reasonText(tOf("ru"), r, { best: 41 }), r).not.toBe(reasonText(tOf("en"), r, { best: 41 }));
    }
    expect(reasonText(t, "inconclusive", { best: 41 })).toContain("only 41 %");
    expect(reasonText(t, "from_the_future")).toBe(en["ports.why.failed"]);
  });
});

describe("the event and the audit rows", () => {
  it("words port_lossy like the other events, with the loss and the sender", () => {
    const e = { code: "port_lossy", params: { inbound: "inb_1", profile: "hy2 · WARP · 8443", port: "8443", sent: "300", got: "189", sender: "de2" } };
    expect(describeEvent(t, e).text).toBe("UDP to port 8443 of “hy2 · WARP · 8443” loses 37 % of packets (checked from de2)");
    expect(describeEvent(tOf("ru"), { ...e, params: { ...e.params, sender: "panel" } }).text).toBe("UDP до порта 8443 профиля «hy2 · WARP · 8443» теряет 37 % пакетов (проверка с панели)");
    // the profile name the list joined in wins over the one in the params
    expect(describeEvent(t, { ...e, profileName: "Main" }).text).toContain("“Main”");
    expect(eventKind({ code: "port_lossy", severity: EventSeverity.WARNING })).toBe("warn");
  });
  it("words the check and the 'save anyway' choice", () => {
    expect(describeAudit(t, { action: "node.ports_check", paramsJson: JSON.stringify({ node: "de1", ports: "443,8443" }) })).toBe("checked UDP delivery on de1: ports 443,8443");
    expect(describeAudit(t, { action: "port_lossy_override", paramsJson: JSON.stringify({ node: "de1", port: 8443 }) })).toBe("saved the port 8443 on de1 although it loses UDP packets");
  });
});
