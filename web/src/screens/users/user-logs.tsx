import { useClientLogs } from "./client-logs";
import { ClientLogsSection } from "./client-logs-section";
import type { DeviceN, UserN } from "./model";

/** The Logs tab: the owner's logs for support from the person's app (requests, bundles; the windows to read and delete them). */
export function LogsTab({ user, devices }: { user: UserN; devices: DeviceN[] }) {
  const { ctl, dialogs } = useClientLogs(user.id);
  return (
    <div className="up-one">
      <ClientLogsSection ctl={ctl} devices={devices} />
      {dialogs}
    </div>
  );
}
