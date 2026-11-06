// Sizes and times of backups, as Settings -> Backups and the restore window show them.

export function formatBytes(size: number, locale: string) {
  if (!Number.isFinite(size) || size < 0) return "—";
  if (size < 1024) return `${size} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let value = size;
  let unit = -1;
  do {
    value /= 1024;
    unit++;
  } while (value >= 1024 && unit < units.length - 1);
  return `${value.toLocaleString(locale, { maximumFractionDigits: 1 })} ${units[unit]}`;
}

export function backupTime(unix: number, locale: string) {
  return unix > 0 ? new Date(unix * 1000).toLocaleString(locale) : "—";
}
