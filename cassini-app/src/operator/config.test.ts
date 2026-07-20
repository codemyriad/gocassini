import { afterEach, describe, expect, it } from "vitest";

import { loadConfig } from "./config";
import { operatorBaseFrom } from "../embedded";

function stubWindow(config: { operatorBasePath?: string }): void {
  (globalThis as unknown as { window: unknown }).window = { __CASSINI_CONFIG__: config };
}

afterEach(() => {
  delete (globalThis as unknown as { window?: unknown }).window;
});

describe("loadConfig operatorBasePath", () => {
  it("accepts an absolute same-origin proxy base (the AppAPI embedded shape)", () => {
    // Regression (D-420 V3): on the embedded page the base is captured as an
    // ABSOLUTE URL; rejecting it made loadConfig throw and the operator tab
    // silently vanished for admins.
    const base = "http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini/operator";
    stubWindow({ operatorBasePath: base });
    expect(loadConfig().operatorBasePath).toBe(base);
  });

  it("end-to-end: an absolute captured viewer base flows to a usable operator base", () => {
    const viewerBase = "http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini/";
    const opBase = operatorBaseFrom(viewerBase);
    expect(opBase).not.toBeNull();
    stubWindow({ operatorBasePath: opBase as string });
    expect(loadConfig().operatorBasePath).toBe(
      "http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini/operator",
    );
  });

  it("accepts a root-relative base and strips a trailing slash", () => {
    stubWindow({ operatorBasePath: "/operator/" });
    expect(loadConfig().operatorBasePath).toBe("/operator");
  });

  it("still rejects a value that is neither a path nor an http(s) URL", () => {
    stubWindow({ operatorBasePath: "operator" });
    expect(() => loadConfig()).toThrow();
  });
});
