import { describe, expect, it } from "vitest";

import { captureOperatorBase, operatorBaseFrom } from "./embedded";

describe("operatorBaseFrom", () => {
  it("appends /operator to the captured proxy base", () => {
    expect(operatorBaseFrom("/index.php/apps/app_api/proxy/gocassini/")).toBe(
      "/index.php/apps/app_api/proxy/gocassini/operator",
    );
  });

  it("tolerates a base without a trailing slash", () => {
    expect(operatorBaseFrom("/proxy/gocassini")).toBe("/proxy/gocassini/operator");
  });

  it("returns null when no base was captured (standalone / dev)", () => {
    expect(operatorBaseFrom(null)).toBeNull();
    expect(operatorBaseFrom(undefined)).toBeNull();
    expect(operatorBaseFrom("")).toBeNull();
  });
});

describe("captureOperatorBase", () => {
  it("publishes operatorBasePath from the viewer base, preserving other config", () => {
    const win = {
      __CASSINI_VIEWER_BASE__: "/proxy/gocassini/",
      __CASSINI_CONFIG__: { operatorBasePath: "/stale" },
    } as unknown as Window;
    captureOperatorBase(win);
    expect(win.__CASSINI_CONFIG__?.operatorBasePath).toBe("/proxy/gocassini/operator");
  });

  it("is a no-op when no viewer base was captured", () => {
    const win = {} as unknown as Window;
    captureOperatorBase(win);
    expect(win.__CASSINI_CONFIG__).toBeUndefined();
  });
});
