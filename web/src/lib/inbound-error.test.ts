import { describe, expect, it } from "vitest";
import { fill, type T } from "@/i18n";
import { ru } from "@/i18n/ru";
import { inboundErrorKind, inboundErrorPort, inboundErrorText } from "./inbound-error";

const t = Object.assign((key: keyof typeof ru, vars?: Record<string, string | number>) => fill(ru[key], vars), { n: () => "" }) as unknown as T;

describe("why a profile did not start, in words", () => {
  it("sorts the agent's texts into classes, and leaves an unknown one as 'other'", () => {
    expect(inboundErrorKind("listen udp :8443: bind: address already in use")).toBe("port");
    expect(inboundErrorKind("certs: acme_domain needs a DNS name, got 203.0.113.10")).toBe("cert");
    expect(inboundErrorKind("cannot build inbound: a host name is required for a Let's Encrypt certificate: the node address is an IP, set an SNI")).toBe("cert");
    expect(inboundErrorKind("egress warp: no account on this node")).toBe("warp");
    expect(inboundErrorKind("open /etc/mistgate/x: permission denied")).toBe("permission");
    expect(inboundErrorKind("awgnl: the amneziawg kernel module is not loaded")).toBe("awg");
    expect(inboundErrorKind("something nobody has seen")).toBe("other");
  });

  it("finds the port in the text when the row does not give one", () => {
    expect(inboundErrorPort("listen udp :8443: bind: address already in use")).toBe(8443);
    expect(inboundErrorPort("listen udp [::]:443: bind: address already in use")).toBe(443);
    expect(inboundErrorPort("port udp/2053 is held")).toBe(2053);
    expect(inboundErrorPort("no port here")).toBeUndefined();
  });

  it("says the port and, when the doctor knows it, the program that holds it", () => {
    const e = "listen udp :8443: bind: address already in use";
    expect(inboundErrorText(t, e)).toBe("Порт udp/8443 занят другой программой");
    expect(inboundErrorText(t, e, { port: 8443, process: "xray" })).toBe("Порт udp/8443 занят другой программой (xray)");
    expect(inboundErrorText(t, "bind: address already in use")).toBe("Порт занят другой программой");
    expect(inboundErrorText(t, "needs a DNS name")).toContain("нужен домен с A-записью");
    expect(inboundErrorText(t, "x")).toBe("Профиль не запустился");
  });
});
