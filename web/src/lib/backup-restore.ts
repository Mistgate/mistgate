import { Code, ConnectError } from "@connectrpc/connect";
import { errorFromJson } from "@connectrpc/connect/protocol-connect";

// Restore from a backup (ADR 0013): the archive goes up to the panel's private stage first (a POST of the raw encrypted
// bytes, owner and fresh step-up, answered like the Connect API so the step-up guard can ask and run it again), then
// BackupService.PrepareRestore decrypts and checks it and ConfirmRestore hands it to the restore helper.

const connectCodes = new Set(["canceled", "unknown", "invalid_argument", "deadline_exceeded", "not_found", "already_exists", "permission_denied", "resource_exhausted", "failed_precondition", "aborted", "out_of_range", "unimplemented", "internal", "unavailable", "data_loss", "unauthenticated"]);

/** Uploads the encrypted archive and returns its upload id; onProgress gets the share sent, 0..1. */
export function uploadBackup(file: Blob & { name?: string }, onProgress?: (share: number) => void, signal?: AbortSignal): Promise<string> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", new URL("api/backup/upload", document.baseURI));
    xhr.withCredentials = true;
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    xhr.setRequestHeader("Connect-Protocol-Version", "1");
    if (file.name) xhr.setRequestHeader("X-Mistgate-Filename", file.name.replace(/[^\x20-\x7e]/g, "_"));
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && e.total > 0) onProgress?.(e.loaded / e.total);
    };
    xhr.onload = () => {
      const text = xhr.responseText;
      let json: unknown = null;
      try {
        json = JSON.parse(text);
      } catch {
        // not JSON: the status speaks
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        const id = json && typeof json === "object" && "upload_id" in json ? String((json as { upload_id: unknown }).upload_id) : "";
        if (id) resolve(id);
        else reject(new ConnectError("the panel did not return an upload id", Code.Internal));
        return;
      }
      const code = json && typeof json === "object" && "code" in json ? String((json as { code: unknown }).code) : "";
      const headers = new Headers();
      for (const line of xhr.getAllResponseHeaders().trim().split(/[\r\n]+/)) {
        const at = line.indexOf(":");
        if (at > 0) headers.append(line.slice(0, at).trim(), line.slice(at + 1).trim());
      }
      reject(connectCodes.has(code) ? errorFromJson(json as never, headers, new ConnectError(text, Code.Unknown)) : new ConnectError(text || `HTTP ${xhr.status}`, xhr.status === 413 ? Code.ResourceExhausted : Code.Unknown));
    };
    xhr.onerror = () => reject(new ConnectError("the upload did not reach the panel", Code.Unavailable));
    xhr.onabort = () => reject(new ConnectError("the upload was cancelled", Code.Canceled));
    signal?.addEventListener("abort", () => xhr.abort(), { once: true });
    xhr.send(file);
  });
}

/**
 * Waits for the panel to go down and come back after ConfirmRestore: polls the page itself every 2.5 s. Resolves "back"
 * once it answered again after being down (or after 20 s of answering, in case the swap was quicker than a poll), and
 * "timeout" after the limit.
 */
export async function waitForRestart(signal: AbortSignal, limitMs = 180_000): Promise<"back" | "timeout"> {
  const started = Date.now();
  let wentDown = false;
  while (!signal.aborted && Date.now() - started < limitMs) {
    await new Promise((r) => setTimeout(r, 2500));
    let up = false;
    try {
      const res = await fetch(new URL(".", document.baseURI), { cache: "no-store", credentials: "same-origin", signal });
      up = res.status < 500;
    } catch {
      up = false;
    }
    if (!up) wentDown = true;
    else if (wentDown || Date.now() - started > 20_000) return "back";
  }
  return "timeout";
}
