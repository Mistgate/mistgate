import type { Device, GetUserResponse, ListUsersResponse, NodeAccess, ProfileRef, User } from "@/gen/mistgate/admin/v1/user_pb";

// protobuf-es gives every 64-bit field as a bigint. Counters and Unix times here stay far below 2^53, so the
// screens work with plain numbers: the conversion happens once, where a response comes in. (The other way,
// a write, uses BigInt(Math.round(x)) at the call.)
type Int64 = "usedBytes" | "quotaBytes" | "nextResetUnix" | "expiresUnix" | "lastSeenUnix" | "speedLimitBps" | "createdUnix";
export type UserN = Omit<User, Int64> & Record<Int64, number>;
export type DeviceN = Omit<Device, "firstSeenUnix" | "lastSeenUnix" | "lastHandshakeUnix"> & { firstSeenUnix: number; lastSeenUnix: number; lastHandshakeUnix: number };
export type DetailN = {
  user: UserN;
  devices: DeviceN[];
  dailyTraffic: { dayUnix: number; bytes: number }[];
  nodeTraffic: { nodeId: string; nodeName: string; protocol: string; bytes: number }[];
  profiles: ProfileRef[];
  nodeAccess: NodeAccess[];
};

export const userN = (u: User): UserN => ({
  ...u,
  usedBytes: Number(u.usedBytes),
  quotaBytes: Number(u.quotaBytes),
  nextResetUnix: Number(u.nextResetUnix),
  expiresUnix: Number(u.expiresUnix),
  lastSeenUnix: Number(u.lastSeenUnix),
  speedLimitBps: Number(u.speedLimitBps),
  createdUnix: Number(u.createdUnix),
});

export const usersPageN = (r: ListUsersResponse) => ({ users: r.users.map(userN), nextPageToken: r.nextPageToken, counts: r.counts });

export function detailN(r: GetUserResponse): DetailN {
  return {
    user: userN(r.user!),
    devices: r.devices.map((d) => ({ ...d, firstSeenUnix: Number(d.firstSeenUnix), lastSeenUnix: Number(d.lastSeenUnix), lastHandshakeUnix: Number(d.lastHandshakeUnix) })),
    dailyTraffic: r.dailyTraffic.map((d) => ({ dayUnix: Number(d.dayUnix), bytes: Number(d.bytes) })),
    nodeTraffic: r.nodeTraffic.map((n) => ({ nodeId: n.nodeId, nodeName: n.nodeName, protocol: n.protocol, bytes: Number(n.bytes) })),
    profiles: r.profiles,
    nodeAccess: r.nodeAccess,
  };
}

export const big = (n: number) => BigInt(Math.round(n));
