import { describe, expect, it } from "vitest";

import {
  formatStorageBytes,
  formatStorageDuration,
  storageUsageTotal,
  summarizeStorageLoads,
  type StorageLoadSample,
} from "./storageUsage";

function source(error = "") {
  return {
    id: "published",
    label: "Published",
    location: "Files",
    bytes: 10,
    duration_ms: 12,
    files: 1,
    collections: 1,
    requests: 1,
    error,
  };
}

function sample(endToEnd: number): StorageLoadSample {
  return {
    operation: "lookup",
    measured_at: "2026-09-22T09:15:00Z",
    end_to_end_ms: endToEnd,
    request_ms: endToEnd - 5,
    render_ms: 5,
    operator_ms: endToEnd - 10,
  };
}

describe("storage usage presentation", () => {
  it("formats apparent bytes at readable unit boundaries", () => {
    expect(formatStorageBytes(0)).toBe("0 B");
    expect(formatStorageBytes(1023)).toBe("1023 B");
    expect(formatStorageBytes(1024)).toBe("1.0 KB");
    expect(formatStorageBytes(1536)).toBe("1.5 KB");
    expect(formatStorageBytes(12 * 1024 * 1024)).toBe("12 MB");
  });

  it("withholds the total when any folder could not be measured", () => {
    expect(storageUsageTotal([source()])).toBe(10);
    expect(storageUsageTotal([source("PROPFIND failed")])).toBeNull();
  });

  it("formats and summarizes user-facing load durations", () => {
    expect(formatStorageDuration(418.4)).toBe("418 ms");
    expect(formatStorageDuration(1420)).toBe("1.42 s");
    expect(summarizeStorageLoads([sample(900), sample(300), sample(500), sample(700)])).toEqual({
      count: 4,
      min_ms: 300,
      median_ms: 600,
      max_ms: 900,
    });
    expect(summarizeStorageLoads([])).toBeNull();
  });
});
