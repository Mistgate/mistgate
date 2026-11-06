import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BackupDownloadError, backupFileName, downloadBackup, lastDownload } from "./backup-download";

const json = (status: number, body: object) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

describe("downloadBackup", () => {
  const fetchMock = vi.fn();
  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
    URL.createObjectURL = vi.fn(() => "blob:backup");
    URL.revokeObjectURL = vi.fn();
    localStorage.clear();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("posts the passphrase, saves the file under the server's name and remembers the time", async () => {
    fetchMock.mockResolvedValue(new Response("age-encrypted", { status: 200, headers: { "X-Mistgate-Filename": "mistgate-backup-panel-v0.1.79-2026-11-06-1542.tar.gz.age" } }));
    const clicked: string[] = [];
    const spy = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push(this.download);
    });
    const name = await downloadBackup("v0.1.79", "correct horse battery");
    expect(name).toBe("mistgate-backup-panel-v0.1.79-2026-11-06-1542.tar.gz.age");
    expect(clicked).toEqual([name]);
    const [url, init] = fetchMock.mock.calls[0]!;
    expect(String(url)).toMatch(/api\/backup\/download$/);
    expect(init).toMatchObject({ method: "POST", body: JSON.stringify({ passphrase: "correct horse battery" }) });
    expect(lastDownload("v0.1.79")).toBeGreaterThan(0);
    spy.mockRestore();
  });

  it("a missing step-up is the RPCs' PERMISSION_DENIED, so the step-up guard handles it", async () => {
    fetchMock.mockResolvedValue(json(403, { code: "permission_denied", message: "confirm it is you first (step-up required)" }));
    const e = await downloadBackup("v0.1.79").catch((x: unknown) => x);
    expect(e).toBeInstanceOf(ConnectError);
    expect((e as ConnectError).code).toBe(Code.PermissionDenied);
  });

  it("the download's own refusals keep their code", async () => {
    fetchMock.mockResolvedValue(json(409, { code: "backup_busy", message: "another backup is already running" }));
    const e = await downloadBackup("v0.1.79").catch((x: unknown) => x);
    expect(e).toBeInstanceOf(BackupDownloadError);
    expect((e as BackupDownloadError).code).toBe("backup_busy");
    expect(lastDownload("v0.1.79")).toBe(0);
  });
});

describe("backupFileName", () => {
  it("names the panel, the version and the UTC minute, and keeps only safe characters", () => {
    expect(backupFileName("Panel.Example.com", "v0.1.80-dev.3", new Date(Date.UTC(2026, 10, 6, 15, 42)))).toBe("mistgate-backup-panel-example-com-v0.1.80-dev.3-2026-11-06-1542.tar.gz.age");
    expect(backupFileName("", "", new Date(Date.UTC(2026, 0, 2, 3, 4)))).toBe("mistgate-backup-panel-dev-2026-01-02-0304.tar.gz.age");
  });
});
