import { describe, expect, it } from "vitest";
import { chartRetentionPolicy, defaultStoragePrecision, displayStorageCategories, retentionCutoff, storageBuckets, storageDateExtent, storageRange, utcDay } from "./storageCharts";
import type { StorageUsageCategory } from "./types";
import type { RetentionSettings } from "./retention";
const category = (id = "recordings"): StorageUsageCategory => ({ id, bytes: 100, files: 4, undated_bytes: 10, undated_files: 1, days: [
  { date: "2024-02-28", bytes: 20, files: 1 }, { date: "2024-02-29", bytes: 30, files: 1 }, { date: "2024-03-02", bytes: 40, files: 1 },
] });
describe("storage chart dates and totals", () => {
  it("keeps inclusive UTC endpoints, leap days and empty buckets", () => {
    const result = storageBuckets(category(), "2024-02-28", "2024-03-02", 1);
    expect(result.error).toBe("");
    expect(result.buckets.map(b => b.bytes)).toEqual([20,30,0,40]);
    expect(result.buckets.map(b => b.from)).toEqual(["2024-02-28","2024-02-29","2024-03-01","2024-03-02"]);
  });
  it("groups from the selected start and clips the final bucket", () => {
    const result = storageBuckets(category(), "2024-02-28", "2024-03-02", 3);
    expect(result.buckets).toEqual([
      { from: "2024-02-28", to: "2024-03-01", bytes:50,files:2 },
      { from: "2024-03-02", to: "2024-03-02", bytes:40,files:1 },
    ]);
  });
  it("excludes undated bytes and dates outside the selected range", () => {
    expect(storageBuckets(category(), "2024-02-29", "2024-03-01", 7).buckets[0].bytes).toBe(30);
  });
  it("rejects impossible dates, reversed ranges and invalid precision", () => {
    expect(Number.isNaN(utcDay("2025-02-29"))).toBe(true);
    for (const [from,to,size] of [["2025-02-29","2025-03-01",1],["2024-03-02","2024-02-28",1],["2024-02-28","2024-03-02",0],["2024-02-28","2024-03-02",1.5],["2024-02-28","2024-03-02",NaN]] as const) {
      expect(storageBuckets(category(),from,to,size).error).not.toBe("");
    }
  });
  it("supports all dates at daily precision without dropping buckets", () => {
    const result = storageBuckets(category(), "2020-01-01", "2026-01-01", 1);
    expect(result.error).toBe("");
    expect(result.buckets).toHaveLength(2193);
    expect(result.buckets[0].from).toBe("2020-01-01");
    expect(result.buckets.at(-1)?.to).toBe("2026-01-01");
    expect(result.buckets.reduce((sum, bucket) => sum + bucket.bytes, 0)).toBe(90);
    expect(defaultStoragePrecision("2020-01-01", "2026-01-01")).toBe(60);
  });
  it("includes old data in an adaptive all-dates range and recent empty days", () => {
    const extent = storageDateExtent(category(), "2024-03-05T12:00:00Z");
    expect(extent).toEqual({from:"2024-02-28",to:"2024-03-05"});
    expect(storageRange("7",extent,"","")).toEqual({from:"2024-02-28",to:"2024-03-05"});
    expect(storageRange("all",extent,"","")).toEqual(extent);
  });
  it("combines history without changing total bytes, files or unknown dates", () => {
    const input = [category(),category("failed_capture"),category("failed_build"),category("logs")];
    const combined = displayStorageCategories(input,false);
    const history = combined.find(c => c.id === "history")!;
    expect(history.bytes).toBe(200); expect(history.undated_bytes).toBe(20); expect(history.files).toBe(8);
    expect(history.days.map(d => d.bytes)).toEqual([40,60,80]);
    expect(combined.reduce((n,c) => n+c.bytes,0)).toBe(input.reduce((n,c) => n+c.bytes,0));
    expect(displayStorageCategories(input,true)).toBe(input);
  });
  it("dates the next cleanup's cutoff in UTC calendar days", () => {
    const now = new Date("2026-10-12T23:30:00Z");
    expect(retentionCutoff({ forever: false, count: 30, unit: "days" }, now)).toBe("2026-09-12");
    expect(retentionCutoff({ forever: false, count: 30, unit: "days" }, new Date("2026-03-02T00:10:00Z"))).toBe("2026-01-31");
    expect(retentionCutoff({ forever: true }, now)).toBeNull();
    expect(retentionCutoff(null, now)).toBeNull();
  });
  it("picks the policy that governs each chart", () => {
    const days = (count: number) => ({ forever: false, count, unit: "days" as const });
    const settings: RetentionSettings = { version: 3, revision: 1, schedule: { time: "03:42", timezone: "UTC" },
      recordings: days(7), current: days(30), logs: days(60), nextcloud: { meetings: days(90) },
      history: { mode: "group", policy: days(14), fine: { failed_capture: days(1), failed_build: days(2), superseded: days(3), failed_publish: days(4) } } };
    expect(chartRetentionPolicy("recordings", settings)?.count).toBe(7);
    expect(chartRetentionPolicy("current", settings)?.count).toBe(30);
    expect(chartRetentionPolicy("logs", settings)?.count).toBe(60);
    expect(chartRetentionPolicy("published", settings)?.count).toBe(90);
    expect(chartRetentionPolicy("history", settings)?.count).toBe(14);
    expect(chartRetentionPolicy("superseded", settings)?.count).toBe(14);
    expect(chartRetentionPolicy("other", settings)).toBeNull();
    const fine = { ...settings, history: { ...settings.history, mode: "fine" as const } };
    expect(chartRetentionPolicy("history", fine)).toBeNull();
    expect(chartRetentionPolicy("superseded", fine)?.count).toBe(3);
    expect(chartRetentionPolicy("failed_publish", fine)?.count).toBe(4);
    expect(chartRetentionPolicy("published", { ...settings, nextcloud: undefined })).toBeNull();
    expect(chartRetentionPolicy("recordings", null)).toBeNull();
  });
});
