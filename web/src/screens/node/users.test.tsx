import { describe, expect, it } from "vitest";
import { onlinePeople } from "./users";

const conn = (userId: string, protocol: string, deviceModel = "", downBps = 11e6) => ({ userId, userName: userId, protocol, deviceModel, connectedAtUnix: 0, downBps });

describe("onlinePeople", () => {
  it("lists a person once however many connections the node reports", () => {
    const rows = onlinePeople([...Array.from({ length: 6 }, () => conn("vadik", "hysteria2")), conn("amet", "awg", "iPhone 13"), conn("alina", "awg", "iPhone", 0)]);
    expect(rows.map((r) => [r.userId, r.connections, r.downBps])).toEqual([
      ["vadik", 6, 11e6],
      ["amet", 1, 11e6],
      ["alina", 1, 0],
    ]);
  });

  it("merges the protocols and device models of one person without repeats", () => {
    const [p] = onlinePeople([conn("max", "hysteria2"), conn("max", "awg", "Pixel 8"), conn("max", "awg", "Pixel 8"), conn("max", "vless", "iPad")]);
    expect(p).toMatchObject({ protocols: ["hysteria2", "awg", "vless"], models: ["Pixel 8", "iPad"], connections: 4 });
  });
});
