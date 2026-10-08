import { ConnectError } from "@connectrpc/connect";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { useT } from "@/i18n";
import { errorCode, errorVars } from "@/lib/errors";
import { lossyVars } from "@/lib/port-check";

// The port_lossy refusal of CreateInbound / UpdateInbound / UpdateProfile ("port=8443&node=de1&sent=300&got=190&at=…&sender=de2&
// free=2053") in a dialog: what the check found, the clean port to take instead (the one the same run proved, as port_taken
// offers its free one), and the way to go on anyway. One place for the add dialog, the edit dialog and the profile editor.

/** The values of a port_lossy refusal, or null when `error` is something else. */
export function lossyRefusal(error: unknown): Record<string, string> | null {
  if (!error) return null;
  const msg = ConnectError.from(error).rawMessage;
  return errorCode(msg) === "port_lossy" ? errorVars(msg) : null;
}

/** The port to offer instead (0 = none found). */
export const lossyFree = (vars: Record<string, string>) => Number(vars.free) || 0;

export function LossyNotice({
  vars,
  onTake,
  onAnyway,
  anywayLabel,
  busy,
}: {
  vars: Record<string, string>;
  /** Puts the clean port into the form. */
  onTake?: (port: number) => void;
  /** Sends the same request again with allow_lossy_port. */
  onAnyway: () => void;
  anywayLabel: string;
  busy?: boolean;
}) {
  const t = useT();
  const free = lossyFree(vars);
  return (
    <Notice className="items-start">
      <span className="flex flex-col items-start gap-2">
        {t("err.port_lossy", lossyVars(t, vars))}
        <span className="flex flex-wrap items-center gap-2">
          {free > 0 && onTake && (
            <Button variant="secondary" size="sm" onClick={() => onTake(free)}>
              {t("node.check.port.take", { port: free })}
            </Button>
          )}
          {free === 0 && <span className="text-muted">{t("node.check.port.none")}</span>}
          <Button variant="ghost" size="sm" disabled={busy} onClick={onAnyway}>
            {anywayLabel}
          </Button>
        </span>
      </span>
    </Notice>
  );
}
