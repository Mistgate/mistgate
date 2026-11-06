import { Shot } from "./dev-kit-redesign";
import { RestoreDialog, RestoreSection, type RestoreDemo } from "./settings-pages/restore";

// Development only, /dev-kit?only=restore&state=…: restore from a backup (ADR 0013). "section" is the part of Settings ->
// Backups (the last result, the data kept from before a restore, the way on a new server); the other states are the window,
// one per address so it is alone on its page (for screenshots).

const NOW = Math.floor(Date.now() / 1000);
const r2 = [
  { key: "mistgate/2026-11-06T03-00-00Z.tar.gz.age", sizeBytes: 2_411_520n, createdUnix: BigInt(NOW - 9 * 3600) },
  { key: "mistgate/2026-11-05T03-00-00Z.tar.gz.age", sizeBytes: 2_398_208n, createdUnix: BigInt(NOW - 33 * 3600) },
] as never[];
const summary = {
  panelName: "Mistgate",
  madeByVersion: "v0.1.79",
  createdUnix: BigInt(NOW - 33 * 3600),
  backupNodes: 4n,
  backupUsers: 118n,
  backupProfiles: 7n,
  currentNodes: 5n,
  currentUsers: 131n,
  currentProfiles: 7n,
} as never;

const states: Record<string, RestoreDemo> = {
  pick: {},
  "pick-file": { source: "file", fileName: "mistgate-backup-panel-example-com-v0.1.79-2026-11-05-0300.tar.gz.age", keyKind: "passphrase" },
  uploading: { step: "busy", source: "file", fileName: "mistgate-backup-panel-example-com-v0.1.79-2026-11-05-0300.tar.gz.age", keyKind: "passphrase", progress: 0.64 },
  confirm: { step: "confirm", source: "r2", summary },
  "confirm-typed": { step: "confirm", source: "r2", summary, typed: "Mistgate" },
  "wrong-key": { source: "r2", error: "set.backups.rs.err.open" },
  restarting: { step: "restarting" },
  back: { step: "back" },
};

export function RestoreKit() {
  const state = new URLSearchParams(location.search).get("state") ?? "section";
  if (state === "section") {
    return (
      <Shot
        id="restore-section"
        title="Settings → Backups → Restore from a backup"
        seed={[
          [["backups", "restore-result"], { outcome: "restored", source: "upload", fileName: "mistgate-backup-panel-example-com-v0.1.79-2026-11-05-0300.tar.gz.age", madeByVersion: "v0.1.79", completedUnix: BigInt(NOW - 1800), recent: true }],
          [["backups", "kept"], [{ name: "mistgate.before-restore-20261106T143012Z", sizeBytes: 2_621_440n, modifiedUnix: BigInt(NOW - 1800) }]],
        ]}
      >
        <div className="max-w-[860px]">
          <RestoreSection r2={r2 as never} identityFile="./mistgate-recovery.txt" archiveFile="./backup.tar.gz.age" dataDir="/var/lib/mistgate-restored" />
        </div>
      </Shot>
    );
  }
  return (
    <Shot id={`restore-${state}`} title={`Restore window: ${state}`}>
      <RestoreDialog open onOpenChange={() => {}} r2={r2 as never} initialR2={states[state]?.source === "r2" ? "mistgate/2026-11-05T03-00-00Z.tar.gz.age" : undefined} demo={states[state] ?? {}} />
    </Shot>
  );
}
