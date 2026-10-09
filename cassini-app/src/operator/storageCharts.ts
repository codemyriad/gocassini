import type { StorageUsageCategory } from "./types";
import { retentionDayPresets } from "./retention";

export const categoryPresentation: Record<string, { label: string; description: string; color: string }> = {
  published: { label: "Published in Nextcloud", description: "Files currently present in Nextcloud, including legacy archives, grouped by publication date. Files without a job use their recording’s local calendar date, then their creation date in UTC. Local retention does not delete these files.", color: "#6279b8" },
  recordings: { label: "Source recordings", description: "Original captured audio, video and supporting files, dated when recording finished.", color: "#527eac" },
  current: { label: "Current output archive", description: "The latest published output retained locally, dated when it was published.", color: "#588c75" },
  history: { label: "Attempt history", description: "Failed recordings, failed builds, superseded output and failed publish staging.", color: "#aa7840" },
  failed_capture: { label: "Failed recordings", description: "Attempt history · captures from failed or interrupted attempts, dated when the attempt ended.", color: "#ae7543" },
  failed_build: { label: "Failed build / seal output", description: "Attempt history · processing output from failed or interrupted attempts, dated when the attempt ended.", color: "#b1874c" },
  superseded: { label: "Superseded successful output", description: "Attempt history · older successful output, dated when a later publication replaced it.", color: "#9075aa" },
  failed_publish: { label: "Failed publish staging", description: "Attempt history · staging from failed or interrupted attempts, dated when the attempt ended.", color: "#b57278" },
  logs: { label: "Logs", description: "Attempt stage logs, dated when the attempt ended. Operator service logs are excluded.", color: "#528e9a" },
  other: { label: "Other local files", description: "Work in progress and files without established retention ownership. No lifecycle date is assigned.", color: "#7d8490" },
};
const historyIDs = ["failed_capture", "failed_build", "superseded", "failed_publish"];
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
  if (!Number.isFinite(start) || !Number.isFinite(end)) return { buckets: [], error: "Choose valid start and end dates." };
  if (end < start) return { buckets: [], error: "The end date must be on or after the start date." };
  if (!Number.isInteger(precision) || precision < 1 || precision > 9999) return { buckets: [], error: "Precision must be a whole number from 1 to 9999 days." };
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
  return new Date(utcDay(date)).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric", timeZone: "UTC" });
}
export function bucketLabel(bucket: StorageBucket): string {
  return bucket.from === bucket.to ? chartDate(bucket.from) : `${chartDate(bucket.from)} – ${chartDate(bucket.to)}`;
}
