import { describe, expect, it } from "vitest";

import { shouldShowDetailLoading } from "./viewState";

const detail = (id: string) => ({ job: { id } });

describe("shouldShowDetailLoading", () => {
  it("shows loading before the first detail response", () => {
    expect(shouldShowDetailLoading(true, null, "job-1")).toBe(true);
  });

  it("keeps the current job visible during a background refresh", () => {
    expect(shouldShowDetailLoading(true, detail("job-1"), "job-1")).toBe(false);
  });

  it("shows loading instead of stale details while switching jobs", () => {
    expect(shouldShowDetailLoading(true, detail("job-1"), "job-2")).toBe(true);
  });

  it("never shows loading after the request settles", () => {
    expect(shouldShowDetailLoading(false, null, "job-1")).toBe(false);
    expect(shouldShowDetailLoading(false, detail("job-1"), "job-2")).toBe(false);
  });
});
