import { describe, expect, it } from "vitest";
import { fill, type Lang, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { itemDetail, itemParams } from "./doctor-detail";
import { makeFmt } from "./format";

const dict = { en, ru };
const tOf = (lang: Lang) => Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(dict[lang][key], vars), { n: () => "" }) as unknown as T;

/** The text the SPA writes for a code with these params, in a language. */
function render(lang: Lang, detailCode: string, params: Record<string, string>) {
  const t = tOf(lang);
  const item = { detailCode, params };
  return itemDetail(t, item, itemParams(t, makeFmt(lang, t), item));
}

// What the agent sends per code (internal/node/doctor/codes.go), enough to fill every placeholder of its sentence.
const samples: Record<string, Record<string, string>> = {
  "disk_space.usage": { mount: "/", used_pct: "50", free_mb: "4700", inode_pct: "21" },
  "disk_space.unreadable": {},
  "journald_size.size": { journal_mb: "400", cap_mb: "200" },
  "journald_size.none": {},
  "dstate_tasks.none": { stuck: "0" },
  "dstate_tasks.stuck": { stuck: "1", tasks: "apt-get(812)" },
  "dstate_tasks.load_idle": { stuck: "0", load_idle: "1" },
  "dstate_tasks.stuck_load_idle": { stuck: "1", tasks: "apt-get(812)", load_idle: "1" },
  "dstate_tasks.no_proc": {},
  "dstate_tasks.interrupted": {},
  "time_sync.offset": { offset_s: "-1", ntp_synced: "yes" },
  "resolver.ok": { domains: "3", failed_count: "0", median_ms: "12" },
  "resolver.failed": { domains: "3", failed_count: "1", failed: "www.google.com", median_ms: "12" },
  "ipv6.none": { ipv6: "no" },
  "ipv6.none_warp": { ipv6: "no", warp_inbounds: "inb_1" },
  "ipv6.none_warp_ipv4": { ipv6: "no", warp_inbounds: "inb_1" }, // the panel's own code (health/doctor.go reassess)
  "ipv6.ok": { ipv6: "yes", connect: "ok" },
  "ipv6.connect_failed": { ipv6: "yes", connect: "failed" },
  "foreign_vpn.none": {},
  "foreign_vpn.found": { names: "unit:x-ui,docker:amnezia", count: "2" },
  "foreign_vpn.clash": { names: "proc:xray", count: "1", on_our_ports: "xray:udp/443" },
  "foreign_nft.clean": {},
  "foreign_nft.found": { tables: "ip nat", table_count: "1", rules: "0" },
  "foreign_nft.nat": { tables: "ip nat", table_count: "1", rules: "0", nat_tables: "ip nat" },
  "foreign_nft.hits_ports": { tables: "ip legacy", table_count: "1", rules: "1", ports: "443" },
  "foreign_nft.no_nft": {},
  "foreign_nft.unreadable": {},
  "port_conflicts.ok": { checked: "2" },
  "port_conflicts.none": {},
  "port_conflicts.hop": { inbound_id: "inb_1", hop_from: "20000", hop_to: "20100", hop_holders: "ntpd:20050" },
  "port_conflicts.tls": { inbound_id: "inb_1", port: "443", network: "tcp", process: "nginx(50)" },
  "port_conflicts.bind_failed": { inbound_id: "inb_1", port: "443", network: "udp" },
  "port_conflicts.held": { inbound_id: "inb_1", port: "443", network: "udp", process: "caddy(812)" },
  "port_conflicts.no_table": {},
  "net_baseline.ok": {},
  "net_baseline.ok_notes": { notes: "container,no_systemd" },
  "net_baseline.differs": { differs: "default_qdisc,journald_file", notes: "no_bbr" },
  "cert_expiry.ok": { checked: "2" },
  "cert_expiry.agent": { reason: "expiring", days_left: "3", subject: "agent" },
  "cert_expiry.inbound": { reason: "expiring", days_left: "3", inbound_id: "inb_1", server_name: "example.com" },
  "cert_expiry.unreadable": { inbounds: "inb_1" },
  "cert_expiry.none": {},
  "memory_pressure.usage": { avail_pct: "50", swap_pct: "0", oom_kills: "unknown" },
  "memory_pressure.no_meminfo": {},
  "cpu_softirq.load": { softirq_pct: "12", cpu_pct: "30", samples: "60" },
  "cpu_softirq.collecting": { samples: "5", need: "30" },
  "kernel_headers.no_awg": {},
  "kernel_headers.container": { virt: "lxc" },
  "kernel_headers.ready": { kernel: "6.8.0-142-generic" },
  "kernel_headers.userspace": { kernel: "6.8.0-142-generic", missing: "dkms,make,gcc", mode: "auto", backend: "userspace" },
  "kernel_headers.optional": { kernel: "6.8.0-142-generic", missing: "headers", mode: "auto" },
  "kernel_headers.missing": { kernel: "6.8.0-142-generic", missing: "headers,dkms", mode: "kernel" },
  "awg_backend.none": {},
  "awg_backend.no_engine": {},
  "awg_backend.running": { backend: "userspace", version: "amneziawg-go v3", mode: "auto" },
  "awg_backend.forward_drop": { backend: "kernel", version: "v1", mode: "auto", hint: "docker_forward_drop" },
  "awg_backend.unavailable": { mode: "kernel", reason: "the amneziawg kernel module is not loaded", hint: "no_module" },
  "warp_path.no_manager": {},
  "warp_path.unused": {},
  "warp_path.not_configured": { hint: "not_configured", inbounds: "inb_h" },
  "warp_path.host_clash": { hint: "table_in_use" },
  "warp_path.up": { state: "up", backend: "kernel", colo: "FRA" },
  "warp_path.starting": { state: "starting" },
  "warp_path.no_backend": { state: "unavailable", hint: "no_backend" },
  "warp_path.paused_used": { state: "disabled", hint: "paused", inbounds: "inb_h" },
  "warp_path.paused": { state: "disabled" },
  "warp_path.down": { state: "down", error: "probe_other_failed" },
  "warp_path.unknown_state": { state: "weird" },
  "skip.unsupported": { reason: "not a Linux host" },
  "skip.timeout": {},
  "skip.internal": {},
  "skip.unknown_check": {},
};

describe("the node's fact lines", () => {
  it("has wording in both languages for every code, with every placeholder filled", () => {
    for (const lang of ["en", "ru"] as const) {
      for (const [code, params] of Object.entries(samples)) {
        const text = render(lang, code, params);
        expect(text, `${lang} ${code}`).toBeTruthy();
        expect(text, `${lang} ${code}`).not.toMatch(/[{}]|undefined|NaN/);
        expect(text, `${lang} ${code}`).not.toMatch(/[:,;] *$|\(\)|, ,/); // a placeholder that came out empty
      }
    }
    // no wording for a code that is not in the table: the table is the agent's list
    const worded = Object.keys(en).filter((k) => k.startsWith("doctor.detail.")).map((k) => k.slice("doctor.detail.".length));
    expect(worded.sort()).toEqual(Object.keys(samples).sort());
  });

  it("writes the owner's kernel_headers line in Russian: userspace works, kernel mode needs the tools", () => {
    expect(render("ru", "kernel_headers.userspace", { kernel: "6.8.0-142-generic", missing: "dkms,make,gcc", mode: "auto", backend: "userspace" })).toBe(
      "AmneziaWG работает в userspace; для режима ядра нужны dkms, make, gcc",
    );
    expect(render("en", "kernel_headers.userspace", { missing: "headers,dkms" })).toBe("AmneziaWG runs in userspace; kernel mode needs kernel headers, dkms");
  });

  it("formats sizes, percentages and the clock offset for the language", () => {
    expect(render("ru", "disk_space.usage", { mount: "/", used_pct: "50", free_mb: "4700", inode_pct: "21" })).toBe("/: занято 50%, свободно 4,6 ГБ, inodes 21%");
    expect(render("en", "disk_space.usage", { mount: "/", used_pct: "50", free_mb: "4700", inode_pct: "21" })).toBe("/: 50% used, 4.6 GB free, inodes 21%");
    expect(render("ru", "journald_size.size", { journal_mb: "400", cap_mb: "200" })).toBe("Журнал занимает 400 МБ (лимит 200 МБ)");
    expect(render("ru", "time_sync.offset", { offset_s: "-1", ntp_synced: "yes" })).toBe("Часы расходятся с панелью на 1 с, синхронизация NTP: да");
    expect(render("ru", "time_sync.offset", { offset_s: "0", ntp_synced: "no" })).toContain("NTP: нет");
  });

  it("says how long a certificate has left, and nothing about it once it has expired", () => {
    expect(render("ru", "cert_expiry.inbound", { reason: "expiring", days_left: "3", inbound_id: "inb_1", server_name: "example.com" })).toBe(
      "«inb_1», example.com: сертификат скоро истекает, осталось 3 дн.",
    );
    expect(render("en", "cert_expiry.agent", { reason: "expired", days_left: "0" })).toBe("The agent’s certificate has expired");
  });

  it("builds the optional pieces of the WARP line only when the agent sent them", () => {
    expect(render("en", "warp_path.up", { state: "up", backend: "kernel", colo: "FRA" })).toBe("WARP is up via kernel, FRA");
    expect(render("en", "warp_path.up", { state: "up" })).toBe("WARP is up");
    expect(render("ru", "warp_path.down", { state: "down" })).toBe("WARP не работает: нет рукопожатия");
    // the reason goes through the same words as the WARP card, with the recovery step the node is on
    expect(render("ru", "warp_path.down", { state: "down", error: "probe_cloudflare_failed; ladder: reassert" })).toBe(
      "WARP не работает: Проверка через WARP до Cloudflare завершилась ошибкой. Пробую восстановить: переподключение.",
    );
    expect(render("en", "warp_path.down", { state: "down", error: "handshake_never" })).toBe("WARP is down: There has been no handshake with WARP yet.");
    expect(render("en", "warp_path.down", { state: "down", error: "brand_new_failure" })).toBe("WARP is down: brand_new_failure");
    expect(render("ru", "warp_path.host_clash", { hint: "table_in_use" })).toBe("WARP не может работать на этом хосте: её таблица маршрутов занята другим инструментом");
  });

  it("returns null, so the agent's English line is shown, for a missing or unknown code", () => {
    expect(render("ru", "", {})).toBeNull();
    expect(render("ru", "disk_space.brand_new", { mount: "/" })).toBeNull();
    expect(render("ru", "brand_new_check.usage", {})).toBeNull();
  });

  it("names profiles, never inb_ ids, when the panel sent the names; an unnamed id stays readable", () => {
    expect(render("ru", "port_conflicts.held", { inbound_id: "inb_1", profile: "hy2 · 443", port: "443", network: "udp", process: "caddy(812)" })).toBe("udp/443 у «hy2 · 443» занят: caddy(812)");
    expect(render("en", "cert_expiry.inbound", { inbound_id: "inb_1", profile: "hy2 · 443", reason: "expiring", days_left: "3", server_name: "a.example" })).toBe(
      "“hy2 · 443”, a.example: the certificate expires soon, 3 d left",
    );
    expect(render("ru", "warp_path.paused_used", { state: "disabled", hint: "paused", inbounds: "inb_1,inb_2", profiles: "hy2 · WARP, awg · WARP" })).toBe(
      "На паузе; профили с WARP не пропускают трафик: «hy2 · WARP», «awg · WARP»",
    );
    expect(render("en", "port_conflicts.bind_failed", { inbound_id: "inb_7", port: "8443", network: "udp" })).toBe("“inb_7” could not take udp/8443, the port is free now");
  });

  it("says a host without IPv6 is fine for WARP (its endpoint is IPv4), with no ForceIPv4 advice", () => {
    expect(render("ru", "ipv6.none_warp_ipv4", {})).toBe("IPv6 нет; WARP ходит через IPv4-адрес — это нормально");
  });

  it("keeps the raw params for the why texts and words the tokens in them", () => {
    const t = tOf("ru");
    const p = itemParams(t, makeFmt("ru", t), { params: { missing: "headers,dkms", free_mb: "1500", ntp_synced: "no" } });
    expect(p.missing).toBe("заголовки ядра, dkms");
    expect(p.free_mb).toBe("1500");
    expect(p.free).toBe("1,5 ГБ");
    expect(p.ntp_synced).toBe("нет");
  });
});
