import { get } from "svelte/store";
import { dateFormat, formatCount, LL } from "cassini-viewer/i18n";
import type { StorageUsageCategory } from "./types";
import { retentionDayPresets, type RetentionPolicy, type RetentionSettings } from "./retention";

// Each chart's name and description, read when shown so they follow the
// reader's language. The ids are the operator's storage categories.
type CategoryId = keyof ReturnType<typeof categoryCopy>;
const categoryCopy = () => get(LL).app.operator.storage.category;
function category(id: CategoryId, color: string): { label: string; description: string; color: string } {
  return {
    get label() { return get(LL).app.file_kind[id](); },
    get description() { return categoryCopy()[id](); },
    color,
  };
}

export const categoryPresentation: Record<string, { label: string; description: string; color: string }> = {
  published: category("published", "#6279b8"),
  recordings: category("recordings", "#527eac"),
  current: category("current", "#588c75"),
  history: category("history", "#aa7840"),
  failed_capture: category("failed_capture", "#ae7543"),
  failed_build: category("failed_build", "#b1874c"),
  superseded: category("superseded", "#9075aa"),
  failed_publish: category("failed_publish", "#b57278"),
  logs: category("logs", "#528e9a"),
  other: category("other", "#7d8490"),
};
export const historyIDs = ["failed_capture", "failed_build", "superseded", "failed_publish"];
export function displayStorageCategories(categories: StorageUsageCategory[], splitHistory: boolean): StorageUsageCategory[] {
  if (splitHistory) return categories;
  const history: StorageUsageCategory = { id: "history", bytes: 0, files: 0, undated_bytes: 0, undated_files: 0, days: [] };
  const days = new Map<string, { date: string; bytes: number; files: number }>();
  for (const category of categories.filter(c => historyIDs.includes(c.id))) {
    history.bytes += category.bytes; history.files += category.files;
    history.undated_bytes += category.undated_bytes; history.undated_files += category.undated_files;
    for (const day of category.days) {
      const bucket = days.get(day.date) ?? { date: day.date, bytes: 0, files: 0 };
      bucket.bytes += day.bytes; bucket.files += day.files; days.set(day.date, bucket);
    }
  }
  history.days = [...days.values()].sort((a,b) => a.date.localeCompare(b.date));
  return [...categories.filter(c => ["recordings", "current"].includes(c.id)), history, ...categories.filter(c => !["recordings", "current", ...historyIDs].includes(c.id))];
}
const DAY = 86_400_000;
export function utcDay(value: string): number {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return NaN;
  const time = Date.parse(value + "T00:00:00Z");
  return Number.isFinite(time) && new Date(time).toISOString().slice(0,10) === value ? time : NaN;
}
const iso = (time: number) => new Date(time).toISOString().slice(0,10);
export function storageDateExtent(category: StorageUsageCategory, measuredAt: string): { from: string; to: string } {
  const dates = category.days.map(day => utcDay(day.date)).filter(Number.isFinite);
  const measured = utcDay(measuredAt.slice(0,10));
  const end = Number.isFinite(measured) ? measured : Date.now();
  return { from: iso(dates.length ? Math.min(...dates) : end), to: iso(dates.length ? Math.max(end, ...dates) : end) };
}
// Picks the bar width from the range: at most 60 bars, so every range stays well under the 180-bar cap.
export function defaultStoragePrecision(from: string, to: string): number {
  const days = Math.floor((utcDay(to) - utcDay(from)) / DAY) + 1;
  return [1, ...retentionDayPresets].find(size => days / size <= 60) ?? Math.ceil(days / 60);
}
export function storageRange(range: string, extent: { from: string; to: string }, from: string, to: string) {
  if (range === "custom") return { from, to };
  if (range === "all") return extent;
  return { from: iso(utcDay(extent.to) - (Number(range) - 1) * DAY), to: extent.to };
}
export interface StorageBucket { from: string; to: string; bytes: number; files: number }
export function storageBuckets(category: StorageUsageCategory, from: string, to: string, precision: number): { buckets: StorageBucket[]; error: string } {
  const start = utcDay(from), end = utcDay(to);
  const t = get(LL).app.operator.storage.chart;
  if (!Number.isFinite(start) || !Number.isFinite(end)) return { buckets: [], error: t.error_invalid_dates() };
  if (end < start) return { buckets: [], error: t.error_end_before_start() };
  if (!Number.isInteger(precision) || precision < 1 || precision > 9999) return { buckets: [], error: t.error_precision({ max: formatCount(9999) }) };
  const count = Math.floor((end - start) / (DAY * precision)) + 1;
  const buckets = Array.from({length: count}, (_,i) => ({ from: iso(start+i*precision*DAY), to: iso(Math.min(end,start+((i+1)*precision-1)*DAY)), bytes: 0, files: 0 }));
  for (const day of category.days) {
    const date = utcDay(day.date);
    if (!Number.isFinite(date) || date < start || date > end) continue;
    const bucket = buckets[Math.floor((date-start)/(DAY*precision))];
    bucket.bytes += day.bytes; bucket.files += day.files;
  }
  return { buckets, error: "" };
}
export function chartDate(date: string): string {
  return dateFormat({ day: "numeric", month: "short", year: "numeric", timeZone: "UTC" }).format(new Date(utcDay(date)));
}
export function bucketLabel(bucket: StorageBucket): string {
  if (bucket.from === bucket.to) return chartDate(bucket.from);
  // The locale's own way of writing a range ("3–9 Mar 2026").
  return dateFormat({ day: "numeric", month: "short", year: "numeric", timeZone: "UTC" })
    .formatRange(new Date(utcDay(bucket.from)), new Date(utcDay(bucket.to)));
}
// The saved policy that governs a chart's files, or null when none applies.
// The grouped history chart only has one policy while history is grouped.
export function chartRetentionPolicy(id: string, settings: RetentionSettings | null): RetentionPolicy | null {
  if (!settings) return null;
  if (id === "recordings" || id === "current" || id === "logs") return settings[id];
  if (id === "history") return settings.history.mode === "group" ? settings.history.policy : null;
  if (historyIDs.includes(id)) return settings.history.mode === "fine" ? settings.history.fine[id] ?? null : settings.history.policy;
  if (id === "published") return settings.nextcloud?.meetings ?? null;
  return null;
}
// Ages are UTC calendar days: a file dated D is eligible on D + N days, so the
// next cleanup removes every file dated on or before today (UTC) minus N days.
export function retentionCutoff(policy: RetentionPolicy | null, now = new Date()): string | null {
  if (!policy || policy.forever || !Number.isInteger(policy.count) || policy.count! < 0) return null;
  return iso(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()) - policy.count! * DAY);
}
