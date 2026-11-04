import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { EventSeverity } from "@/gen/mistgate/admin/v1/fleet_pb";
import { dictionaries as dict, fill, type Lang, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { describeAudit } from "./audit";
import { errorText } from "./errors";
import { describeEvent, eventKind } from "./events";
import { bestDelivered, decoyNote, lossPct, lossyVars, portNotes, portRuns, reasons, reasonText, regionWord, renewalOf, renewalText, testBlocked, udpFilters, udpRegion } from "./port-check";

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

describe("a port checked from two senders", () => {
  const twice = { sender: "minsk1", sent: 300, got: 236, verdict: "ok", senderVerdict: "lossy", confirmSender: "fi1", confirmSent: 300, confirmGot: 299, confirmVerdict: "ok" };
  it("gives both runs, the one that lost packets marked by its verdict, and nothing for a port checked from one sender", () => {
    expect(portRuns(twice)).toEqual([
      { sender: "minsk1", sent: 300, got: 236, loss: "warn" },
      { sender: "fi1", sent: 300, got: 299, loss: "" },
    ]);
    // a row from before the confirmation has no sender verdict: the node's verdict is the first sender's
    expect(portRuns({ ...twice, verdict: "broken", senderVerdict: "", confirmVerdict: "broken" })?.map((r) => r.loss)).toEqual(["bad", "bad"]);
    expect(portRuns({ ...twice, confirmSender: "" })).toBeNull();
  });
  it("puts a sender in the CIS or outside it by its country, and says so", () => {
    expect(["BY", "ru", "KZ", "DE", "FI", ""].map(udpRegion)).toEqual(["cis", "cis", "cis", "other", "other", ""]);
    expect(["cis", "other", ""].map((r) => regionWord(t, r))).toEqual(["CIS", "outside the CIS", ""]);
    expect(regionWord(tOf("ru"), "other")).toBe("вне СНГ");
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
  it("words a port lost from both senders, and this node's packets lost on the way to another node", () => {
    const both = { code: "port_lossy", params: { profile: "hy2 · 443", port: "443", sent: "300", got: "241", sender: "minsk1", confirm_sender: "panel", confirm_sent: "300", confirm_got: "238" } };
    expect(describeEvent(t, both).text).toBe("UDP to port 443 of “hy2 · 443” loses packets from both sides: 20 % from minsk1, 21 % from the panel");
    expect(describeEvent(tOf("ru"), both).text).toBe("UDP до порта 443 профиля «hy2 · 443» теряется с обеих сторон: 20 % с minsk1, 21 % с панели");
    const path = { code: "path_lossy", params: { target: "nl1", target_id: "nod_nl1", ports: "2053, 2083", sent: "300", got: "236", other: "fi1", other_got: "299", region: "cis", other_region: "other" } };
    expect(describeEvent(t, path).text).toBe("UDP to nl1 gets lost on the way: 21 % on ports 2053, 2083, while 299/300 arrive from fi1");
    expect(describeEvent(tOf("ru"), path).text).toBe("UDP до nl1 теряется по пути: 21 % на портах 2053, 2083, а с fi1 доходит 299/300");
    expect(eventKind({ code: "path_lossy", severity: EventSeverity.WARNING })).toBe("warn");
  });
  it("words a filtered path: the ports that got through, not a share lost", () => {
    const filtered = { code: "path_lossy", params: { target: "fi1", target_id: "nod_fi1", ports: "2053, 8443", open: "443", sent: "300", got: "0", other: "msk1", other_got: "300", region: "other", other_region: "cis" } };
    expect(describeEvent(t, filtered).text).toBe("UDP to fi1 is filtered on the way: only 443 gets through, ports 2053, 8443 get nothing, while everything arrives from msk1");
    expect(describeEvent(tOf("ru"), filtered).text).toBe("UDP до fi1 режет фильтр по пути: доходит только 443, на порты 2053, 8443 — ничего, а с msk1 доходит всё");
    expect(describeEvent(t, filtered).text).not.toContain("%");
  });
  it("words the check and the 'save anyway' choice", () => {
    expect(describeAudit(t, { action: "node.ports_check", paramsJson: JSON.stringify({ node: "de1", ports: "443,8443" }) })).toBe("checked UDP delivery on de1: ports 443,8443");
    expect(describeAudit(t, { action: "port_lossy_override", paramsJson: JSON.stringify({ node: "de1", port: 8443 }) })).toBe("saved the port 8443 on de1 although it loses UDP packets");
  });
  it("words the IPv6 entry: the check, and the entry on and off, in three languages", () => {
    const check = (pass: string) => ({ action: "node.ipv6_check", paramsJson: JSON.stringify({ node: "de1", address: "2001:db8::5", pass }) });
    expect(describeAudit(t, check("true"))).toBe("checked IPv6 address 2001:db8::5 of de1 from outside: it answers");
    expect(describeAudit(t, check("false"))).toBe("checked IPv6 address 2001:db8::5 of de1 from outside: it does not answer as the node");
    const entry = (action: string) => ({ action: "node.ipv6_entry", paramsJson: JSON.stringify({ node: "de1", action, address: "2001:db8::5" }) });
    expect(describeAudit(t, entry("on"))).toBe("turned on the IPv6 entry of de1 (2001:db8::5)");
    expect(describeAudit(t, entry("off"))).toBe("turned off the IPv6 entry of de1");
    expect(describeAudit(tOf("ru"), entry("on"))).toBe("включил(а) вход по IPv6 на de1 (2001:db8::5)");
    expect(describeAudit(tOf("zh"), entry("off"))).toBe("关闭了 de1 的 IPv6 入口");
  });
});

describe("a VLESS profile on the Hysteria2 site's TCP port (hy2_decoy_paused)", () => {
  const params = { node: "de1", port: "443", hy2: "Main", vless: "vless 443", via: "site", target: "www.example.org", port80: "unchecked" };

  it("says who takes the port, where visitors go, and how the certificate renews, in both languages", () => {
    expect(decoyNote(t, params)).toBe(
      "TCP 443 on de1 goes to “vless 443”. The site of “Main” is off while it does and comes back when the port is free; visitors of TCP 443 see www.example.org instead (REALITY passes them on). The Hysteria2 certificate now renews only through TCP 80. Check the ports on the node’s Profiles tab to see whether it is open.",
    );
    expect(decoyNote(tOf("ru"), params)).toBe(
      "TCP 443 на de1 отходит «vless 443». Сайт «Main» на это время выключается и вернётся, когда порт освободится; на TCP 443 вместо него откроется www.example.org — REALITY передаёт посетителей туда. Сертификат Hysteria2 теперь обновляется только через TCP 80. Проверь порты на вкладке «Профили» ноды — открыт ли он.",
    );
  });

  it("words each way of sharing and each port 80 state, and leaves the certificate sentence out when port80 is not sent", () => {
    expect(decoyNote(t, { ...params, via: "self", port80: undefined })).toBe(fill(en["node.check.decoy.self"], params));
    expect(decoyNote(t, { ...params, via: "tls", port80: undefined })).toBe(fill(en["node.check.decoy.tls"], params));
    expect(decoyNote(t, { ...params, port80: "closed" })).toContain("will stop renewing. Open TCP 80 at the provider, move “vless 443” to 8443");
    expect(decoyNote(t, { ...params, port80: "open" })).toContain("Port 80 reaches Mistgate here");
    expect(decoyNote(t, { ...params, port80: "busy" })).toContain("Another program on the node holds port 80");
    // a value this page does not know reads as the plain REALITY case
    expect(decoyNote(t, { ...params, via: "later", port80: "maybe" })).toBe(fill(en["node.check.decoy.site"], params));
  });

  it("goes into what a saved inbound owes the admin (the toast, the rows of 'Put on nodes')", () => {
    expect(portNotes(t, [{ code: "hy2_decoy_paused", params }])).toEqual([decoyNote(t, params)]);
  });
});

describe("whether the certificates of a node renew", () => {
  const row = (over: Partial<{ port: number; role: string; alpn: boolean; profile: string; verdict: string }>) => ({ port: 80, role: "acme", alpn: false, profile: "", verdict: "ours", ...over });
  const site443 = row({ port: 443, role: "site", alpn: true, profile: "hy2" });
  const reality443 = row({ port: 443, role: "vless", profile: "VLESS 443" });

  it("is ok80 when port 80 is ours, whatever 443 says", () => {
    expect(renewalOf([row({}), reality443])).toEqual({ kind: "ok80" });
  });

  it("is ok443 when port 80 is not ours and an alpn row on 443 is ours", () => {
    for (const verdict of ["other", "busy", "refused", "timeout"]) expect(renewalOf([row({ verdict }), site443])).toEqual({ kind: "ok443" });
    expect(renewalOf([row({ verdict: "refused" }), row({ port: 8443, alpn: true, role: "site" })])).toMatchObject({ kind: "risk" });
  });

  it("is a risk otherwise, with the VLESS profile that holds 443 with another site's REALITY", () => {
    expect(renewalOf([row({ verdict: "timeout" }), reality443])).toEqual({ kind: "risk", profile: "VLESS 443" });
    expect(renewalOf([row({ verdict: "timeout" }), { ...site443, verdict: "other" }])).toEqual({ kind: "risk", profile: "" });
    expect(renewalOf([row({ verdict: "refused" })])).toEqual({ kind: "risk", profile: "" });
  });

  it("is nothing without a row for port 80 (no ACME name) or with a port 80 that was not probed", () => {
    expect(renewalOf([])).toBeNull();
    expect(renewalOf([site443, reality443])).toBeNull();
    expect(renewalOf([row({ verdict: "" }), site443])).toBeNull();
  });

  it("reads in both languages as the design lists it", () => {
    const tRu = tOf("ru");
    expect(renewalText(t, { kind: "ok80" })).toBe("Port 80 is open: Let’s Encrypt certificates on this node renew.");
    expect(renewalText(tRu, { kind: "ok443" })).toBe("Порт 80 закрыт, но TCP 443 отвечает Let’s Encrypt — сертификаты обновляются.");
    expect(renewalText(t, { kind: "risk", profile: "" })).toBe("Port 80 is closed and TCP 443 does not answer Let’s Encrypt: certificates on this node will stop renewing. Open TCP 80 at the provider, move VLESS to 8443, or make its REALITY target “This node’s site”.");
    expect(renewalText(tRu, { kind: "risk", profile: "VLESS 443" })).toBe("Порт 80 закрыт, а TCP 443 не отвечает Let’s Encrypt (его занимает «VLESS 443» — REALITY с другим сайтом) — сертификаты на этой ноде перестанут обновляться. Открой TCP 80 у провайдера, перенеси VLESS на 8443 или поставь ему цель REALITY «Сайт этой ноды».");
  });
});

describe("a filter on the way, told from a lossy line", () => {
  // the panel's stored rows on the target: first sender de1, confirmed from msk1; blame "path" where one side lost
  const row = (port: number, over: object = {}) => ({ port, verdict: "ok", sender: "de1", sent: 300, got: 300, senderVerdict: "ok", confirmSender: "msk1", confirmSent: 300, confirmGot: 300, confirmVerdict: "ok", blame: "", ...over });
  const dropped = (port: number) => row(port, { got: 0, senderVerdict: "broken", blame: "path" });
  it("names the sender whose other ports arrived whole while the lost ones got nothing, with the open ports sorted", () => {
    expect(udpFilters([dropped(8443), row(8080), dropped(2053), row(443)])).toEqual([{ sender: "de1", open: [443, 8080] }]);
    // at most 1 % through still counts as nothing
    expect(udpFilters([row(443), row(2053, { got: 3, senderVerdict: "broken", blame: "path" })])).toEqual([{ sender: "de1", open: [443] }]);
  });
  it("finds it on the confirming sender's side too", () => {
    const lost = row(2053, { confirmGot: 0, confirmVerdict: "broken", blame: "path" });
    expect(udpFilters([row(443), lost])).toEqual([{ sender: "msk1", open: [443] }]);
  });
  it("says nothing for a lossy line, a clean run, a sender that got nothing anywhere, or a port both sides lost", () => {
    // a share lost on every port: a lossy line
    expect(udpFilters([row(443, { got: 206, senderVerdict: "lossy", blame: "path" }), row(2053, { got: 210, senderVerdict: "lossy", blame: "path" })])).toEqual([]);
    // some got through on a lost port (40 of 300): not dropped outright
    expect(udpFilters([row(443), dropped(2053), row(8443, { got: 40, senderVerdict: "broken", blame: "path" })])).toEqual([]);
    expect(udpFilters([row(443), row(2053)])).toEqual([]);
    expect(udpFilters([dropped(443), dropped(2053)])).toEqual([]);
    // lost from both sides is the node's port, not a path
    expect(udpFilters([row(443), row(2053, { verdict: "broken", got: 0, senderVerdict: "broken", confirmGot: 0, confirmVerdict: "broken", blame: "node" })])).toEqual([]);
    // checked from one sender only: nothing to compare
    expect(udpFilters([row(443, { confirmSender: "" }), row(2053, { confirmSender: "", got: 0, verdict: "broken" })])).toEqual([]);
  });
});

describe("a port whose test is blocked while the protocol works", () => {
  const checked = 1_000_000;
  const row = (over: object = {}) => ({ verdict: "broken", checkedUnix: checked, protocolOkUnix: checked + 60, ...over });
  it("is a lossy or broken port whose panel protocol check passed after the run or at most 15 minutes before it", () => {
    expect(testBlocked(row())).toBe(true);
    expect(testBlocked(row({ verdict: "lossy" }))).toBe(true);
    expect(testBlocked(row({ protocolOkUnix: checked }))).toBe(true);
    // the window is portProbeTrust (internal/panel/health/eval.go): 15 minutes before the run still answers it, a second more does not
    expect(testBlocked(row({ protocolOkUnix: checked - 15 * 60 }))).toBe(true);
    expect(testBlocked(row({ protocolOkUnix: checked - 15 * 60 - 1 }))).toBe(false);
  });
  it("is a real loss when the protocol check failed or never ran, and nothing to say on a clean or unchecked port", () => {
    expect(testBlocked(row({ protocolOkUnix: 0 }))).toBe(false);
    expect(testBlocked(row({ protocolOkUnix: undefined }))).toBe(false);
    expect(testBlocked(row({ verdict: "ok" }))).toBe(false);
    expect(testBlocked(row({ verdict: "", checkedUnix: 0 }))).toBe(false);
  });
});
