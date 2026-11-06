import { Code, ConnectError } from "@connectrpc/connect";
import { errorFromJson } from "@connectrpc/connect/protocol-connect";

// An encrypted backup of this panel saved to the owner's computer (ADR 0012): a POST that streams the archive. The server
// answers a missing step-up the way the Connect API does, so the admin's step-up guard asks for it and runs this again.

const lastKey = (version: string) => `mistgate.backup.downloaded.${version}`;

const connectCodes = new Set(["canceled", "unknown", "invalid_argument", "deadline_exceeded", "not_found", "already_exists", "permission_denied", "resource_exhausted", "failed_precondition", "aborted", "out_of_range", "unimplemented", "internal", "unavailable", "data_loss", "unauthenticated"]);

/** The download's own refusal: its code (backup_busy, backup_create_failed, passphrase_too_short, ...) and message. */
export class BackupDownloadError extends Error {
  constructor(
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

/** When this browser last saved a backup of this panel version (unix seconds, 0 = never). */
export function lastDownload(version: string): number {
  const v = Number(localStorage.getItem(lastKey(version)) ?? 0);
  return Number.isFinite(v) ? v : 0;
}

/** The name the server gives the file: mistgate-backup-<panel>-<version>-<UTC yyyy-mm-dd-hhmm>.tar.gz.age. */
export function backupFileName(panel: string, version: string, at: Date): string {
  const slug = (s: string, dots = false) =>
    s
      .toLowerCase()
      .replace(dots ? /[^a-z0-9.-]+/g : /[^a-z0-9-]+/g, "-")
      .replace(/-+/g, "-")
      .replace(/^-|-$/g, "");
  const p = (n: number) => String(n).padStart(2, "0");
  const stamp = `${at.getUTCFullYear()}-${p(at.getUTCMonth() + 1)}-${p(at.getUTCDate())}-${p(at.getUTCHours())}${p(at.getUTCMinutes())}`;
  return `mistgate-backup-${slug(panel) || "panel"}-${slug(version, true) || "dev"}-${stamp}.tar.gz.age`;
}

/** Downloads the archive and saves it under the server's name; the passphrase only when there is no recovery key. */
export async function downloadBackup(version: string, passphrase?: string): Promise<string> {
  const res = await fetch(new URL("api/backup/download", document.baseURI), {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", "Connect-Protocol-Version": "1" },
    body: JSON.stringify(passphrase ? { passphrase } : {}),
  });
  if (!res.ok) {
    const text = await res.text();
    let json: unknown = null;
    try {
      json = JSON.parse(text);
    } catch {
      // not the Connect error shape: the status speaks
    }
    const code = json && typeof json === "object" && "code" in json ? String((json as { code: unknown }).code) : "";
    // Connect's own codes (a step-up the guard asks for, a lost session) as the RPCs' errors; the download's own
    // (backup_busy, backup_create_failed, ...) as a BackupDownloadError the window words
    if (connectCodes.has(code)) throw errorFromJson(json as never, res.headers, new ConnectError(text, Code.Unknown));
    throw new BackupDownloadError(code || `http_${res.status}`, text || res.statusText);
  }
  const name = res.headers.get("X-Mistgate-Filename") || backupFileName(location.hostname, version, new Date());
  const url = URL.createObjectURL(await res.blob());
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
  localStorage.setItem(lastKey(version), String(Math.floor(Date.now() / 1000)));
  return name;
}
