import { describe, expect, it } from "vitest";

import { formatStorageBytes, storageUsageTotal } from "./storageUsage";

describe("storage usage presentation", () => {
  it("formats apparent bytes at readable unit boundaries", () => {
    expect(formatStorageBytes(0)).toBe("0 B");
    expect(formatStorageBytes(1023)).toBe("1023 B");
    expect(formatStorageBytes(1024)).toBe("1.0 KB");
    expect(formatStorageBytes(1536)).toBe("1.5 KB");
    expect(formatStorageBytes(12 * 1024 * 1024)).toBe("12 MB");
  });

  it("withholds the total when any folder could not be measured", () => {
    expect(storageUsageTotal([{ id: "published", label: "Published", location: "Files", bytes: 10, error: "" }])).toBe(10);
    expect(storageUsageTotal([{ id: "published", label: "Published", location: "Files", bytes: 10, error: "PROPFIND failed" }])).toBeNull();
  });
});
