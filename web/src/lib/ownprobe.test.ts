import { describe, expect, it } from "vitest";
import { AlertKind, AlertSeverity, CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { dictionaries, fill, langs, pickForm, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { makeFmt } from "./format";
import { alertWhy, countryLook, probeNamesIn, probePointName, probeWhy, type Alert, type CountryCheck } from "./health";
import {
  hostOf,
  isOutdated,
  probeLook,
  probesState,
  sortProbes,
  uninstallCommand,
  unfilteredProbes,
  updateCommand,
  validFallback,
  type OwnProbeRow,
} from "./ownprobe";

const tOf = (lang: (typeof langs)[number]) => {
  const dict = dictionaries[lang] as Record<string, string>;
  return Object.assign((key: string, vars?: Record<string, string | number>) => fill(dict[key]!, vars), {
    n: (key: string, count: number, vars?: Record<string, string | number>) => fill(pickForm(dict[key]!, count, lang), { n: count, ...vars }),
  }) as unknown as T;
};
const t = tOf("en");
const fmt = makeFmt("en", t);

const NOW = 1_760_500_000;
const min = 60;
const probe = (over: Partial<OwnProbeRow> = {}): OwnProbeRow => ({
  id: "prb_0123456789abcdef01234567", name: "dacha", city: "Springfield", kind: "home", state: "active", asn: 64496, operator: "ExampleNet", pinnedOperator: "ExampleNet",
  version: "0a1b2c3d4e5f", lastSeenUnix: NOW - 4 * min, lastResultUnix: NOW - 12 * min, checksDay: 24, tokenExpiresUnix: 0, ...over,
});
const none = new Set<string>();

describe("what a probe is now", () => {
  it("is waiting for its command until it is heard from, and expired once the command's day is over", () => {
    expect(probeLook(probe({ state: "pending", tokenExpiresUnix: NOW + 3600 }), NOW, none)).toBe("pending");
    expect(probeLook(probe({ state: "pending", tokenExpiresUnix: NOW - 1 }), NOW, none)).toBe("expired");
    expect(probeLook(probe({ state: "pending", tokenExpiresUnix: 0 }), NOW, none)).toBe("pending");
  });

  it("is working while it is heard from within the hour, and silent after", () => {
    expect(probeLook(probe(), NOW, none)).toBe("online");
    expect(probeLook(probe({ lastSeenUnix: NOW - 3599 }), NOW, none)).toBe("online");
    expect(probeLook(probe({ lastSeenUnix: NOW - 3601 }), NOW, none)).toBe("offline");
  });

  it("says no filtering when its points say it, but only while it works", () => {
    const id = "prb_0123456789abcdef01234567";
    expect(probeLook(probe(), NOW, new Set([id]))).toBe("noFilter");
    expect(probeLook(probe({ lastSeenUnix: NOW - 7200 }), NOW, new Set([id]))).toBe("offline");
    expect(probeLook(probe(), NOW, new Set(["prb_other"]))).toBe("online");
  });

  it("holds a changed network and a revoked probe as the panel says, whatever the clock", () => {
    expect(probeLook(probe({ state: "network_changed" }), NOW, none)).toBe("netChanged");
    expect(probeLook(probe({ state: "network_changed", lastSeenUnix: 1 }), NOW, none)).toBe("netChanged");
    expect(probeLook(probe({ state: "revoked", lastSeenUnix: 1 }), NOW, none)).toBe("revoked");
  });

  it("finds the probes that see no filtering in the points of the checks, by their ids, and only those with that reason", () => {
    const check = (vantages: object[]) => ({ vantages }) as unknown as CountryCheck;
    const ids = unfilteredProbes([
      check([{ source: "probes", key: "probe:prb_a", why: "no_filter" }, { source: "probes", key: "probe:prb_b", why: "tls_reset" }, { source: "globalping", key: "probe:prb_c", why: "no_filter" }]),
      check([{ source: "probes", key: "probe:prb_d", why: "no_filter" }]),
    ]);
    expect([...ids].sort()).toEqual(["prb_a", "prb_d"]);
  });
});

describe("the state of the card", () => {
  it.each([
    ["off", { enabled: false }, [probe()], "off"],
    ["no probes", { enabled: true }, [], "none"],
    ["only a revoked one", { enabled: true }, [probe({ state: "revoked" })], "none"],
    ["waiting", { enabled: true }, [probe({ state: "pending" })], "pending"],
    ["waiting, one with an expired command", { enabled: true }, [probe({ state: "pending", tokenExpiresUnix: 1 })], "pending"],
    ["silent", { enabled: true }, [probe({ lastSeenUnix: NOW - 5 * 3600 })], "offline"],
    ["a network to confirm and nothing else working", { enabled: true }, [probe({ state: "network_changed" })], "netChanged"],
    ["working", { enabled: true }, [probe()], "works"],
  ] as const)("%s", (_, settings, probes, kind) => {
    expect(probesState(settings, probes, NOW, none).kind).toBe(kind);
  });

  it("counts N of M over the probes that are not revoked, with the newest result, the changed networks and the unfiltered ones", () => {
    const s = probesState(
      { enabled: true },
      [probe(), probe({ id: "prb_b", lastResultUnix: NOW - 3 * min }), probe({ id: "prb_c", state: "network_changed" }), probe({ id: "prb_d", state: "revoked" })],
      NOW,
      new Set(["prb_b"]),
    );
    expect(s).toEqual({ kind: "works", n: 2, m: 3, lastAt: NOW - 3 * min, changed: 1, unfiltered: 1 });
  });

  it("names how long the silence is, by the newest sign of life", () => {
    const s = probesState({ enabled: true }, [probe({ lastSeenUnix: NOW - 5 * 3600 }), probe({ id: "prb_b", lastSeenUnix: NOW - 9 * 3600 })], NOW, none);
    expect(s).toEqual({ kind: "offline", since: NOW - 5 * 3600 });
  });

  it("lists what wants a look first: a changed network, then silence, then the rest, a revoked one last", () => {
    const order = sortProbes(
      [
        probe({ id: "prb_ok", name: "b-ok" }),
        probe({ id: "prb_dead", name: "z-dead", state: "revoked" }),
        probe({ id: "prb_wait", name: "wait", state: "pending" }),
        probe({ id: "prb_net", name: "net", state: "network_changed" }),
        probe({ id: "prb_off", name: "off", lastSeenUnix: 1 }),
        probe({ id: "prb_ok2", name: "a-ok" }),
      ],
      NOW,
      none,
    ).map((p) => p.id);
    expect(order).toEqual(["prb_net", "prb_off", "prb_wait", "prb_ok2", "prb_ok", "prb_dead"]);
  });
});

describe("a probe is Outdated", () => {
  it("only when a newer script is known, and the probe has one", () => {
    expect(isOutdated(probe({ version: "111111111111" }), "0a1b2c3d4e5f")).toBe(true);
    expect(isOutdated(probe({ version: "0a1b2c3d4e5f" }), "0a1b2c3d4e5f")).toBe(false);
    // the whole hash of Create is as good as its first twelve
    expect(isOutdated(probe({ version: "0a1b2c3d4e5f" }), "0a1b2c3d4e5f".padEnd(64, "0"))).toBe(false);
    expect(isOutdated(probe({ version: "111111111111" }), undefined)).toBe(false);
    expect(isOutdated(probe({ version: "" }), "0a1b2c3d4e5f")).toBe(false);
    expect(isOutdated(probe({ state: "pending", version: "111111111111" }), "0a1b2c3d4e5f")).toBe(false);
    expect(isOutdated(probe({ state: "revoked", version: "111111111111" }), "0a1b2c3d4e5f")).toBe(false);
  });
});

describe("the commands for the device", () => {
  it("update from the probe's own configuration: nothing secret in it, the system's own paths", () => {
    expect(updateCommand("keenetic")).toBe(`cd /tmp && curl -fsS --proto '=https' -o mgp.sh "$(sed -n 's/^url=//p' /opt/etc/mistgate-probe/conf)/probe.sh" && sh mgp.sh upgrade`);
    expect(updateCommand("server")).toContain("/etc/mistgate-probe/conf");
    expect(updateCommand("server")).not.toContain("/opt/");
    expect(uninstallCommand("keenetic")).toBe("sh /opt/sbin/mistgate-probe uninstall");
    expect(uninstallCommand("server")).toBe("sh /usr/local/sbin/mistgate-probe uninstall");
  });

  it("go to a router for a home or mobile probe and to a server for a data centre one", () => {
    expect([hostOf("home"), hostOf("mobile"), hostOf("dc"), hostOf("")]).toEqual(["keenetic", "keenetic", "server", "keenetic"]);
  });

  it("take a spare address that is empty or an absolute https URL, as the panel does", () => {
    for (const ok of ["", "  ", "https://spare.example.com", "https://spare.example.com:8443/p"]) expect(validFallback(ok), ok).toBe(true);
    for (const bad of ["http://spare.example.com", "spare.example.com", "https://", "https://user:pw@spare.example.com", "https://spare.example.com/#x", "ftp://x.example.com"]) expect(validFallback(bad), bad).toBe(false);
  });
});

describe("the reasons of a point", () => {
  const reasons = ["tcp_timeout", "tcp_reset", "tls_reset", "tls_timeout", "freeze", "no_filter", "no_control"];

  it("have words in every language, none the same as the 'unclear' one, and an unknown code says it is not clear", () => {
    for (const lang of langs) {
      const tt = tOf(lang);
      const unclear = probeWhy(tt, "some_new_code");
      expect(unclear, lang).toBe(probeWhy(tt, ""));
      for (const why of reasons) {
        expect(probeWhy(tt, why), `${lang} ${why}`).not.toBe(unclear);
        expect(probeWhy(tt, why).length, `${lang} ${why}`).toBeGreaterThan(3);
      }
    }
    expect(probeWhy(t, "tls_reset")).toBe("TLS is reset");
    expect(probeWhy(t, "freeze")).toBe("freezes after ~16 KB");
  });

  it("are the words of every state a probe or the card can show, in every language", () => {
    const keys = [
      "pr.state.pending", "pr.state.expired", "pr.state.active", "pr.state.offline", "pr.state.netChanged", "pr.state.noFilter", "pr.state.revoked",
      "pr.state.off.detail", "pr.state.none", "pr.state.pending.detail", "pr.state.works", "pr.state.offlineAll", "pr.state.offlineAll.detail", "pr.state.netChanged.detail",
      "pr.state.edition", "pr.outdated", "pr.checkNow.note", "pr.revoke", "pr.cmd.note",
    ] as const;
    for (const lang of langs) for (const k of keys) expect((dictionaries[lang] as Record<string, string>)[k], `${lang} ${k}`).toBeTruthy();
    expect(en["pr.state.offline"]).toBe("Not heard from for {age}");
    expect(en["pr.cmd.note"]).toBe("One-time command, valid for 24 hours.");
  });
});

describe("the points of the probes in a node's verdict", () => {
  const point = (over: object) => ({ source: "probes", key: "probe:prb_a", network: "ExampleNet · Springfield · probe dacha", region: "Springfield", asn: "64496", kind: "home", state: "dark", atUnix: NOW - 5 * min, dpiHop: 0, icmpOk: false, tcpOk: true, why: "tls_reset", ...over });
  const check = (vantages: object[], over: object = {}) =>
    ({
      nodeId: "nod_1", country: "RU", verdict: CountryVerdict.PARTIAL, atUnix: NOW - 5 * min, probes: [], reason: "", eyeball: true, confidence: "eyeball", listedBy: [], cdnProvider: "", cdnRegion: "",
      sources: ["probes"], globalpingVerdict: CountryVerdict.UNSPECIFIED, tspuVerdict: CountryVerdict.UNSPECIFIED, tspuPorts: [], dpiHop: 0, tspuVantageId: "", tspuVantageName: "", tspuAtUnix: 0,
      noProbe: [], vantages, sourceVerdicts: [], ...over,
    }) as unknown as CountryCheck;
  const looks = (lang: (typeof langs)[number], c: CountryCheck) => countryLook(tOf(lang), makeFmt(lang, tOf(lang)), c);

  it("name the probe the way the owner reads it, without an address: operator, city, the probe, what it saw and when", () => {
    const look = countryLook(t, { ...fmt, ago: () => "5 min ago" }, check([point({})]));
    expect(look.ownPoints).toEqual([{ key: "probe:prb_a", kind: "dark", name: "ExampleNet · Springfield — probe “dacha”", what: "TLS is reset", at: "5 min ago" }]);
    // the cell's note and the tooltip's line say it in the admin's words as well
    expect(look.note).toBe("ExampleNet · Springfield — probe “dacha”");
    expect(look.dark).not.toContain(" · probe ");
    // the dark point is in the breakdown too, with the reason in place of its source
    expect(look.breakdown?.dark).toEqual([{ name: "ExampleNet · Springfield — probe “dacha”", tag: "AS64496 · home · TLS is reset" }]);
  });

  it("say what a point that reaches the node says, and a point that proves nothing says why", () => {
    const ok = looks("en", check([point({ state: "reach", why: "" })], { verdict: CountryVerdict.OK })).ownPoints[0]!;
    expect([ok.kind, ok.what]).toEqual(["ok", "reaches the node"]);
    const open = looks("en", check([point({ state: "uncertain", why: "no_filter" })], { verdict: CountryVerdict.OK })).ownPoints[0]!;
    expect([open.kind, open.what]).toEqual(["unsure", "no filtering on its path, so “reachable” proves nothing"]);
    const noControl = looks("en", check([point({ state: "uncertain", why: "no_control" })], { verdict: CountryVerdict.UNKNOWN })).ownPoints[0]!;
    expect(noControl.what).toBe("its own network is down: nothing proved");
  });

  it("have no breakdown of their own when nothing is dark: the points are the lines", () => {
    const look = looks("en", check([point({ state: "reach", why: "" })], { verdict: CountryVerdict.OK }));
    expect(look.breakdown).toBeNull();
    expect(look.ownPoints).toHaveLength(1);
  });

  it("read in every language with the probe's name in guillemets or quotes, and never the panel's English word", () => {
    for (const lang of langs) {
      const look = looks(lang, check([point({})]));
      expect(look.ownPoints[0]!.name, lang).toContain("dacha");
      expect(look.ownPoints[0]!.name, lang).not.toContain(" · probe ");
    }
    expect(looks("ru", check([point({})])).ownPoints[0]!.name).toBe("ExampleNet · Springfield — зонд «dacha»");
  });

  it("leave a name of another shape as the panel wrote it", () => {
    expect(probePointName(t, "ExampleNet")).toBe("ExampleNet");
    expect(probePointName(t, "ExampleNet · Springfield · probe office")).toBe("ExampleNet · Springfield — probe “office”");
  });

  it("turn every such name inside the networks of an alert", () => {
    expect(probeNamesIn(t, "ExampleNet · Springfield · probe dacha (AS64496, Springfield), Demo Telecom · Shelbyville · probe office")).toBe(
      "ExampleNet · Springfield — probe “dacha” (AS64496, Springfield), Demo Telecom · Shelbyville — probe “office”",
    );
    expect(probeNamesIn(t, "MTS (AS8359, Moscow), Tele2")).toBe("MTS (AS8359, Moscow), Tele2");
  });
});

describe("the alert of a block that only own probes saw", () => {
  const alert = (over: Partial<Alert> = {}): Alert =>
    ({
      id: "alt_1", severity: AlertSeverity.WARNING, kind: AlertKind.COUNTRY_BLOCKED, nodeId: "nod_1", nodeName: "de1", subject: "partial", titleKey: "health.alert.country_blocked.title",
      whyKey: "health.alert.country_blocked.why.partial", firstSeenUnix: 1, openedUnix: 1, lastSeenUnix: 1, resolvedAtUnix: 0, resolution: "", mutedUntilUnix: 0, actions: [],
      params: { country: "RU", verdict: "partial", sources: "probes", networks: "ExampleNet · Springfield · probe dacha" }, ...over,
    }) as Alert;

  it("says that it is a hint from one narrow point, with the probe named, in every language", () => {
    for (const lang of langs) {
      const tt = tOf(lang);
      const why = alertWhy(tt, makeFmt(lang, tt), alert());
      expect(why, lang).toContain("dacha");
      expect(why, lang).not.toContain("{");
      expect(why, lang).not.toBe(alertWhy(tt, makeFmt(lang, tt), alert({ params: { country: "RU", verdict: "partial", sources: "globalping", networks: "MTS" } })));
    }
    expect(alertWhy(t, fmt, alert())).toBe(
      "Your own probe on ExampleNet · Springfield — probe “dacha” gets no answer from this node. It is one narrow point, so this is a hint, not proof: the other networks of Russia were not looked at. Two checks in a row saw it. It closes once the probe reaches the node again.",
    );
  });

  it("keeps the common sentence when other sources saw the block as well", () => {
    const why = alertWhy(t, fmt, alert({ params: { country: "RU", verdict: "partial", sources: "globalping,probes", networks: "MTS (AS8359, Moscow)" } }));
    expect(why).toContain("Home or mobile probes on MTS (AS8359, Moscow) get no answer");
  });
});
