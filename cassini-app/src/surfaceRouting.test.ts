import { describe, expect, it } from "vitest";

import { readSurface, surfaceHash } from "./surfaceRouting";

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
