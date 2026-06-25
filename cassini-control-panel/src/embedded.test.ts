import { afterEach, describe, expect, it } from "vitest";

import {
  applyNextcloudTheme,
  captureOperatorBasePath,
  captureProxyBaseFrom,
  controlPanelStylesheetHref,
  ensureShadowAppRoot,
} from "./embedded";
import { loadConfig } from "./operator/config";

// These tests run in the default (node) Vitest environment — no jsdom/happy-dom
// dependency — by exercising embedded.ts's pure helpers against hand-rolled
// minimal Document/Window stubs. The helpers are the load-bearing,
// adversarially-verified parts: proxy-base capture via a document.scripts scan
// (currentScript is null under `defer`), turning it into the authoritative
// window.__CASSINI_CONFIG__.operatorBasePath, and #app creation under #content.

describe("applyNextcloudTheme", () => {
  function makeHost(): HTMLElement {
    return {
      style: {
        _vars: {} as Record<string, string>,
        setProperty(name: string, value: string) {
          this._vars[name] = value;
        },
      },
      dataset: {} as DOMStringMap,
    } as unknown as HTMLElement;
  }

  it("returns false and makes no changes when primaryColor is absent", () => {
    const host = makeHost();
    expect(applyNextcloudTheme(host, undefined, undefined)).toBe(false);
    expect(applyNextcloudTheme(host, "dark", null)).toBe(false);
    expect(applyNextcloudTheme(host, "dark", "")).toBe(false);
    expect((host.dataset as Record<string, unknown>).ncTheme).toBeUndefined();
  });

  it("sets --nc-primary inline style from primaryColor", () => {
    const host = makeHost();
    applyNextcloudTheme(host, "", "#00679e");
    const style = host.style as unknown as { _vars: Record<string, string> };
    expect(style._vars["--nc-primary"]).toBe("#00679e");
  });

  it("sets data-nc-theme=light when no dark or highcontrast flags", () => {
    const host = makeHost();
    applyNextcloudTheme(host, "", "#00679e");
    expect((host.dataset as Record<string, string>).ncTheme).toBe("light");
  });

  it("sets data-nc-theme=dark when themes includes 'dark'", () => {
    const host = makeHost();
    applyNextcloudTheme(host, "dark", "#00679e");
    expect((host.dataset as Record<string, string>).ncTheme).toBe("dark");
  });

  it("sets data-nc-theme=highcontrast when themes includes 'highcontrast'", () => {
    const host = makeHost();
    applyNextcloudTheme(host, "highcontrast", "#00679e");
    expect((host.dataset as Record<string, string>).ncTheme).toBe("highcontrast");
  });

  it("sets data-nc-theme=dark-highcontrast when themes includes both", () => {
    const host = makeHost();
    applyNextcloudTheme(host, "dark-highcontrast", "#00679e");
    expect((host.dataset as Record<string, string>).ncTheme).toBe("dark-highcontrast");
  });

  it("returns true when primaryColor is present", () => {
    const host = makeHost();
    expect(applyNextcloudTheme(host, "", "#00679e")).toBe(true);
  });
});

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

describe("ensureShadowAppRoot", () => {
  // Minimal DOM stub modelling AppAPI's bare <div id="content"> plus the open
  // shadow root the panel mounts into.
  interface ShadowStub {
    children: StubEl[];
    appendChild(child: StubEl): void;
    getElementById(id: string): StubEl | null;
  }
  interface StubEl {
    id: string;
    rel?: string;
    href?: string;
    children: StubEl[];
    shadowRoot: ShadowStub | null;
    appendChild(child: StubEl): void;
    attachShadow(init: { mode: string }): ShadowStub;
  }
  function makeEl(id: string): StubEl {
    return {
      id,
      children: [],
      shadowRoot: null,
      appendChild(child: StubEl) {
        this.children.push(child);
      },
      attachShadow(_init: { mode: string }): ShadowStub {
        const shadow: ShadowStub = {
          children: [],
          appendChild(child: StubEl) {
            this.children.push(child);
          },
          getElementById(id: string): StubEl | null {
            return this.children.find((c) => c.id === id) ?? null;
          },
        };
        this.shadowRoot = shadow;
        return shadow;
      },
    };
  }
  function makeStubDoc(opts: { withContent: boolean }) {
    const content = opts.withContent ? makeEl("content") : null;
    const body = makeEl("");
    const doc = {
      getElementById(id: string): StubEl | null {
        if (id === "content") {
          return content;
        }
        const roots = [content, body].filter(Boolean) as StubEl[];
        for (const root of roots) {
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
        return makeEl("");
      },
      body,
    } as unknown as Document;
    return { doc, content, body };
  }

  it("mounts #app inside a shadow root under #content and injects the stylesheet", () => {
    const { doc, content } = makeStubDoc({ withContent: true });
    const cssHref =
      "/index.php/apps/app_api/proxy/gocassini/ui/control-panel.css";
    const appRoot = ensureShadowAppRoot(doc, cssHref) as unknown as StubEl;

    const host = (content as unknown as StubEl).children.find(
      (c) => c.id === "cassini-shadow-host",
    );
    expect(host).toBeDefined();
    // #app is in the shadow tree, NOT a light-DOM child — proving isolation.
    expect(host?.children.some((c) => c.id === "app")).toBe(false);
    expect(appRoot.id).toBe("app");
    const shadow = host?.shadowRoot;
    expect(shadow?.children.some((c) => c.id === "app")).toBe(true);
    const link = shadow?.children.find((c) => c.rel === "stylesheet");
    expect(link?.href).toBe(cssHref);
  });

  it("falls back to <body> when #content is absent", () => {
    const { doc, body } = makeStubDoc({ withContent: false });
    const appRoot = ensureShadowAppRoot(
      doc,
      "/index.php/apps/app_api/proxy/gocassini/ui/control-panel.css",
    ) as unknown as StubEl;
    expect(appRoot.id).toBe("app");
    const host = (body as unknown as StubEl).children.find(
      (c) => c.id === "cassini-shadow-host",
    );
    expect(host?.shadowRoot?.children.some((c) => c.id === "app")).toBe(true);
  });
});

describe("controlPanelStylesheetHref", () => {
  it("derives <base>ui/control-panel.css from the registered script src", () => {
    const doc = {
      scripts: [
        {
          src: "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/ui/control-panel.js",
        },
      ],
    } as unknown as Document;
    // Root-relative (origin stripped) — matches captureProxyBaseFrom's contract
    // and keeps the stylesheet fetch same-origin through the AppAPI proxy.
    expect(controlPanelStylesheetHref(doc)).toBe(
      "/index.php/apps/app_api/proxy/gocassini/ui/control-panel.css",
    );
  });

  it("mirrors the ?v=... version query from the JS src so CSS cache expires with the bundle", () => {
    const doc = {
      scripts: [
        {
          src: "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/ui/control-panel.js?v=abc123-0",
        },
      ],
    } as unknown as Document;
    expect(controlPanelStylesheetHref(doc)).toBe(
      "/index.php/apps/app_api/proxy/gocassini/ui/control-panel.css?v=abc123-0",
    );
  });

  it("falls back to a relative href when no matching script is present", () => {
    const doc = { scripts: [{ src: "https://nc.example.com/x.js" }] } as unknown as Document;
    expect(controlPanelStylesheetHref(doc)).toBe("ui/control-panel.css");
  });
});
