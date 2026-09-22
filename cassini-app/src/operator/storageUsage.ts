import type { StorageUsageSource } from "./types";

// The API reports apparent file bytes, so these labels must never imply free
// disk capacity or allocated blocks. Keeping the presentation helper here lets
// the panel stay declarative and makes boundary values independently testable.
export function formatStorageBytes(bytes: number): string {
  const amount = Number.isFinite(bytes) && bytes > 0 ? bytes : 0;
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let value = amount;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const digits = unit > 0 && value < 10 ? 1 : 0;
  return `${value.toFixed(digits)} ${units[unit]}`;
}

// A total is useful only when it covers every row. Returning null makes the
// incomplete result explicit instead of presenting a precise-looking subtotal.
export function storageUsageTotal(sources: StorageUsageSource[]): number | null {
  if (sources.some((source) => source.error !== "")) {
    return null;
  }
  return sources.reduce((total, source) => total + source.bytes, 0);
}
