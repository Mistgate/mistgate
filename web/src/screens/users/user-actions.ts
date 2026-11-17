import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { QuotaReset } from "@/gen/mistgate/admin/v1/user_pb";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { useToast } from "@/components/ui/toast";
import { users } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { meQuery } from "@/lib/session";
import { big, type UserN as User } from "./model";
import { useTx } from "./t";

/** A change to a user in plain numbers; `write` turns the 64-bit ones into bigints for the wire. */
export type Patch = Partial<{
  name: string;
  subscriptionName: string;
  groupId: string;
  quotaBytes: number;
  quotaReset: QuotaReset;
  expiresUnix: number;
  deviceLimit: number;
  speedLimitBps: number;
  deviceSpeedLimitBps: number;
  dnsPresetId: string;
  apps: { happ: boolean; amnezia: boolean; telegram: boolean; mistgate?: boolean; trustTunnel?: boolean };
  nodes: { all: boolean; nodeIds: string[] };
}>;
export const write = ({ quotaBytes, expiresUnix, speedLimitBps, deviceSpeedLimitBps, ...rest }: Patch) => ({
  ...rest,
  ...(quotaBytes !== undefined && { quotaBytes: big(quotaBytes) }),
  ...(expiresUnix !== undefined && { expiresUnix: big(expiresUnix) }),
  ...(speedLimitBps !== undefined && { speedLimitBps: big(speedLimitBps) }),
  ...(deviceSpeedLimitBps !== undefined && { deviceSpeedLimitBps: big(deviceSpeedLimitBps) }),
});

/** Everything that changes a user goes through here: one call, then every cached list and detail refreshes. */
export function useUserActions(user: User) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const refresh = () => qc.invalidateQueries({ queryKey: ["users"] });
  /** A save that shows the server's reason on failure and rejects, so a draft control can drop its unsaved value. */
  async function update(patch: Patch) {
    try {
      await users.updateUser({ userId: user.id, ...write(patch) });
      await refresh();
    } catch (e) {
      toast.error(errorText(e, t));
      throw e;
    }
  }
  async function run(fn: () => Promise<unknown>, done?: string) {
    try {
      await fn();
      await refresh();
      if (done) toast(done);
    } catch (e) {
      toast.error(errorText(e, t));
    }
  }
  return {
    update,
    refresh,
    // disabling cuts the person off at once, so it can be undone for 8 s, as in the list's bulk bar
    setEnabled: async (enabled: boolean) => {
      try {
        await users.setUsersEnabled({ userIds: [user.id], enabled });
        await refresh();
        if (enabled) toast(t("users.enabledToast", { names: user.name }));
        else toast(t("users.disabledOne", { name: user.name }), { undo: () => void users.setUsersEnabled({ userIds: [user.id], enabled: true }).then(refresh) });
      } catch (e) {
        toast.error(errorText(e, t));
      }
    },
    extend: () => run(() => users.extendUsers({ userIds: [user.id], days: 30 }), t("users.extended", { names: user.name })),
    extendBy: (days: number) => run(() => users.extendUsers({ userIds: [user.id], days }), t("users.extended", { names: user.name })),
    revoke: async (deviceId: string, done?: string) => {
      try {
        await users.revokeDevice({ deviceId });
        await refresh();
        toast(done ?? t("users.revoked"));
      } catch (e) {
        toast.error(errorText(e, t));
        throw e;
      }
    },
    resetTraffic: async () => {
      try {
        await users.resetUserTraffic({ userIds: [user.id] });
        await refresh();
        toast(t("users.resetTrafficDone", { name: user.name }));
      } catch (e) {
        toast.error(errorText(e, t));
        throw e;
      }
    },
    remove: async () => {
      try {
        await guard(() => users.deleteUsers({ userIds: [user.id] }));
        await refresh();
        toast(t("users.deleted", { name: user.name }));
      } catch (e) {
        if (!isStepUpCancelled(e)) toast.error(errorText(e, t));
        throw e;
      }
    },
  };
}

export type Actions = ReturnType<typeof useUserActions>;

/** An observer reads the page and changes nothing: the write controls stay out (the server would refuse them anyway). Role unknown (still loading) is a writer. */
export function useReadonly(): boolean {
  return useQuery(meQuery).data?.admin?.role === Role.READONLY;
}
