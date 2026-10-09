import type { StorageUsageSource } from "./types";

export interface StorageLoadSample {
  operation: "lookup" | "recalculate";
  measured_at: string;
  end_to_end_ms: number;
  request_ms: number;
  render_ms: number;
  operator_ms: number;
}

export interface StorageLoadSummary {
  count: number;
  min_ms: number;
  median_ms: number;
  max_ms: number;
}

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

export function formatStorageDuration(milliseconds: number): string {
  const duration = Number.isFinite(milliseconds) && milliseconds > 0 ? milliseconds : 0;
  if (duration < 1000) {
    return `${Math.round(duration)} ms`;
  }
  return `${(duration / 1000).toFixed(duration < 10_000 ? 2 : 1)} s`;
}

export function summarizeStorageLoads(samples: StorageLoadSample[]): StorageLoadSummary | null {
  if (samples.length === 0) {
    return null;
  }
  const values = samples.map((sample) => sample.end_to_end_ms).sort((left, right) => left - right);
  const middle = Math.floor(values.length / 2);
  const median = values.length % 2 === 0
    ? (values[middle - 1] + values[middle]) / 2
    : values[middle];
  return {
    count: values.length,
    min_ms: values[0],
    median_ms: median,
    max_ms: values[values.length - 1],
  };
}
