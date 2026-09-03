import { describe, expect, it } from "vitest";

import {
  ADMIN_SURFACES,
  applyJob,
  applySurface,
  readJob,
  readSurface,
  surfaceHash,
} from "./surfaceRouting";

describe("readSurface", () => {
  it("reads the operator surface from the hash", () => {
    expect(readSurface("#surface=operator")).toBe("operator");
  });

  it("defaults to browse when absent, empty, or unknown", () => {
    expect(readSurface("")).toBe("browse");
    expect(readSurface("#")).toBe("browse");
    expect(readSurface("#surface=nonsense")).toBe("browse");
  });

  it("ignores the viewer's meeting/tx/t params alongside it", () => {
    expect(readSurface("#meeting=abc&tx=v3&t=1200ms")).toBe("browse");
    expect(readSurface("#surface=operator&meeting=abc")).toBe("operator");
  });
});

describe("surfaceHash", () => {
  it("marks the operator surface", () => {
    expect(surfaceHash("operator")).toBe("#surface=operator");
  });

  it("emits no marker for browse (keeps standalone/share URLs clean)", () => {
    expect(surfaceHash("browse")).toBe("");
  });

  it("round-trips through readSurface", () => {
    expect(readSurface(surfaceHash("operator"))).toBe("operator");
    expect(readSurface(surfaceHash("browse"))).toBe("browse");
  });
});

describe("applySurface", () => {
  it("adds the operator surface while preserving meeting/tx/t in order", () => {
    expect(applySurface("#meeting=abc&tx=v3&t=1200ms", "operator")).toBe(
      "#surface=operator&meeting=abc&tx=v3&t=1200ms",
    );
  });

  it("removes the surface param when switching back to browse, keeping the rest", () => {
    expect(applySurface("#surface=operator&meeting=abc&tx=v3&t=1200ms", "browse")).toBe(
      "#meeting=abc&tx=v3&t=1200ms",
    );
  });

  it("keeps t= last so parseTimeHash still matches", () => {
    expect(applySurface("#meeting=abc&t=5s", "operator").endsWith("t=5s")).toBe(true);
  });

  it("does not duplicate an existing surface param", () => {
    expect(applySurface("#surface=operator&meeting=abc", "operator")).toBe(
      "#surface=operator&meeting=abc",
    );
  });

  it("returns an empty hash for browse with no other params", () => {
    expect(applySurface("#surface=operator", "browse")).toBe("");
    expect(applySurface("", "browse")).toBe("");
  });
});

describe("readJob", () => {
  it("reads the selected run id from the hash", () => {
    expect(readJob("#surface=operator&job=01KXZV5QZP")).toBe("01KXZV5QZP");
  });

  it("returns an empty string when absent", () => {
    expect(readJob("")).toBe("");
    expect(readJob("#surface=operator")).toBe("");
  });
});

describe("applyJob", () => {
  it("adds the job param while preserving the surface", () => {
    expect(applyJob("#surface=operator", "abc")).toBe("#surface=operator&job=abc");
  });

  it("replaces an existing job rather than appending a second", () => {
    expect(applyJob("#surface=operator&job=old", "new")).toBe("#surface=operator&job=new");
  });

  it("clears the job param when given an empty id", () => {
    expect(applyJob("#surface=operator&job=abc", "")).toBe("#surface=operator");
    expect(applyJob("#job=abc", "")).toBe("");
  });

  it("round-trips through readJob", () => {
    expect(readJob(applyJob("#surface=operator", "01KXZV5QZP"))).toBe("01KXZV5QZP");
  });
});

// The Setup surface (D-616). It rides the same param as the operator surface,
// so the risk is that one of readSurface / surfaceHash / applySurface learns
// about it and the others do not — which is why each is asserted separately
// rather than only through a round-trip.
describe("the setup surface", () => {
  it("is read from the hash like any other admin surface", () => {
    expect(readSurface("#surface=setup")).toBe("setup");
    expect(readSurface("#surface=setup&meeting=abc")).toBe("setup");
  });

  it("gets its own marker, and round-trips", () => {
    expect(surfaceHash("setup")).toBe("#surface=setup");
    expect(readSurface(surfaceHash("setup"))).toBe("setup");
  });

  it("preserves the viewer's params and replaces a sibling surface", () => {
    expect(applySurface("#meeting=abc&t=5s", "setup")).toBe("#surface=setup&meeting=abc&t=5s");
    expect(applySurface("#surface=operator&meeting=abc", "setup")).toBe(
      "#surface=setup&meeting=abc",
    );
    expect(applySurface("#surface=setup&meeting=abc", "browse")).toBe("#meeting=abc");
  });

  it("is listed as an admin surface, which is what gates the tab on the probe", () => {
    expect([...ADMIN_SURFACES].sort()).toEqual(["operator", "setup"]);
  });
});
