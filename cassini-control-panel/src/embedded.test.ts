import { afterEach, describe, expect, it } from "vitest";

import {
  captureOperatorBasePath,
  captureProxyBaseFrom,
  ensureAppRoot,
} from "./embedded";
import { loadConfig } from "./operator/config";

// These tests run in the default (node) Vitest environment — no jsdom/happy-dom
// dependency — by exercising embedded.ts's pure helpers against hand-rolled
// minimal Document/Window stubs. The helpers are the load-bearing,
// adversarially-verified parts: proxy-base capture via a document.scripts scan
// (currentScript is null under `defer`), turning it into the authoritative
// window.__CASSINI_CONFIG__.operatorBasePath, and #app creation under #content.

describe("captureProxyBaseFrom", () => {
  it("derives a ROOT-RELATIVE proxy base from the registered ui/control-panel.js src", () => {
    // In a real DOM, script.src is the resolved ABSOLUTE URL. We must strip the
    // origin so operator/config.ts's strict root-relative validator accepts it
    // (an absolute value would throw and render the config-error alert — the
    // very D-382 blank-panel bug).
    const scripts = [
      { src: "https://nc.example.com/index.php/csrfprotection.js" },
      {
        src: "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/ui/control-panel.js",
      },
    ];
    expect(captureProxyBaseFrom(scripts)).toBe(
      "/index.php/apps/app_api/proxy/gocassini/",
    );
  });

  it("tolerates a cache-busting query suffix on the src", () => {
    const scripts = [
      {
        src: "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/ui/control-panel.js?v=42",
      },
    ];
    expect(captureProxyBaseFrom(scripts)).toBe(
      "/index.php/apps/app_api/proxy/gocassini/",
    );
  });

  it("ignores scripts without a src and unrelated scripts (incl. the viewer's)", () => {
    const scripts = [
      {},
      { src: "" },
      { src: "https://nc.example.com/apps/other/main.js" },
      // The viewer's bundle must NOT match the control-panel pattern.
      { src: "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/ui/viewer.js" },
    ];
    expect(captureProxyBaseFrom(scripts)).toBeNull();
  });

  it("returns null when there are no scripts", () => {
    expect(captureProxyBaseFrom(undefined)).toBeNull();
    expect(captureProxyBaseFrom(null)).toBeNull();
    expect(captureProxyBaseFrom([])).toBeNull();
  });
});

describe("captureOperatorBasePath", () => {
  it("sets window.__CASSINI_CONFIG__.operatorBasePath to <base>operator from a fake script", () => {
    const doc = {
      scripts: [
        {
          src: "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/ui/control-panel.js",
        },
      ],
    } as unknown as Document;
    const win = {} as Window;

    captureOperatorBasePath(doc, win);

    expect(win.__CASSINI_CONFIG__?.operatorBasePath).toBe(
      "/index.php/apps/app_api/proxy/gocassini/operator",
    );
  });

  it("preserves any pre-existing __CASSINI_CONFIG__ fields while overriding operatorBasePath", () => {
    const doc = {
      scripts: [
        {
          src: "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/ui/control-panel.js",
        },
      ],
    } as unknown as Document;
    const win = { __CASSINI_CONFIG__: { operatorBasePath: "/stale" } } as unknown as Window;

    captureOperatorBasePath(doc, win);

    expect(win.__CASSINI_CONFIG__?.operatorBasePath).toBe(
      "/index.php/apps/app_api/proxy/gocassini/operator",
    );
  });

  it("leaves config untouched when no matching script is present", () => {
    const doc = { scripts: [{ src: "https://nc.example.com/x.js" }] } as unknown as Document;
    const win = {} as Window;
    captureOperatorBasePath(doc, win);
    expect(win.__CASSINI_CONFIG__).toBeUndefined();
  });
});

// Integration: captureOperatorBasePath -> loadConfig together. The unit tests
// above pin the captured value in isolation; this one wires the captured base
// through operator/config.ts the same way App.svelte's onMount does, against a
// REALISTIC absolute script.src (what a real DOM exposes). It is the regression
// guard for D-382: an origin-bearing operatorBasePath makes
// normalizeOperatorBasePath throw, which renders the config-error alert (the
// blank/broken panel D-382 fixes). loadConfig() reads the global `window`, so
// stub it here and restore afterward.
describe("captureOperatorBasePath -> loadConfig integration", () => {
  const originalWindow = (globalThis as { window?: Window }).window;

  afterEach(() => {
    if (originalWindow === undefined) {
      delete (globalThis as { window?: Window }).window;
    } else {
      (globalThis as { window?: Window }).window = originalWindow;
    }
  });

  it("loadConfig accepts the captured embedded base without throwing and yields a usable root-relative operator path", () => {
    const doc = {
      scripts: [
        {
          src: "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/ui/control-panel.js",
        },
      ],
    } as unknown as Document;
    // Embedded page URL — proxyPrefixFromPathname's /control-panel fallback does
    // NOT apply here (it would be wrong); the runtime config must win.
    const win = {
      location: {
        pathname: "/index.php/apps/app_api/embedded/gocassini/control-panel",
      },
    } as unknown as Window;
    (globalThis as { window?: Window }).window = win;

    captureOperatorBasePath(doc, win);

    let config: ReturnType<typeof loadConfig> | undefined;
    expect(() => {
      config = loadConfig();
    }).not.toThrow();
    // Root-relative same-origin path that hits the AppAPI proxy (CSP
    // connect-src 'self'); no scheme/host that the validator would reject.
    expect(config?.operatorBasePath).toBe(
      "/index.php/apps/app_api/proxy/gocassini/operator",
    );
    expect(config?.operatorBasePath.startsWith("/")).toBe(true);
  });
});

describe("ensureAppRoot", () => {
  // Minimal DOM stub: enough surface for ensureAppRoot's getElementById /
  // createElement / appendChild walk, modelling AppAPI's bare <div id="content">.
  function makeStubDoc(opts: { withContent: boolean }) {
    const created: StubEl[] = [];
    interface StubEl {
      id: string;
      children: StubEl[];
      appendChild(child: StubEl): void;
    }
    const makeEl = (id: string): StubEl => ({
      id,
      children: [],
      appendChild(child: StubEl) {
        this.children.push(child);
      },
    });
    const content = opts.withContent ? makeEl("content") : null;
    const body = makeEl("");
    const doc = {
      getElementById(id: string): StubEl | null {
        if (id === "content") {
          return content;
        }
        // #app only exists after ensureAppRoot creates + appends it.
        const everything = [content, body].filter(Boolean) as StubEl[];
        for (const root of everything) {
          if (root.id === id) {
            return root;
          }
          const hit = root.children.find((c) => c.id === id);
          if (hit) {
            return hit;
          }
        }
        return null;
      },
      createElement(_tag: string): StubEl {
        const el = makeEl("");
        created.push(el);
        return el;
      },
      body,
    } as unknown as Document;
    return { doc, content, body };
  }

  it("creates #app inside #content when present", () => {
    const { doc, content } = makeStubDoc({ withContent: true });
    const appRoot = ensureAppRoot(doc);
    expect(appRoot.id).toBe("app");
    expect((content as unknown as { children: { id: string }[] }).children).toContainEqual(
      expect.objectContaining({ id: "app" }),
    );
  });

  it("falls back to <body> when #content is absent", () => {
    const { doc, body } = makeStubDoc({ withContent: false });
    const appRoot = ensureAppRoot(doc);
    expect(appRoot.id).toBe("app");
    expect((body as unknown as { children: { id: string }[] }).children).toContainEqual(
      expect.objectContaining({ id: "app" }),
    );
  });
});
