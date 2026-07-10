import { describe, expect, it } from "vitest";

import {
  applyNextcloudTheme,
  captureViewerBase,
  captureViewerBaseFrom,
  ensureShadowAppRoot,
} from "./embedded";

// These tests run in the default (node) Vitest environment — no jsdom/happy-dom
// dependency — by exercising embedded.ts's pure helpers against hand-rolled
// minimal Document/Window stubs. The helpers are the load-bearing,
// adversarially-verified parts: base capture via a document.scripts scan
// (currentScript is null under `defer`) and #app creation under #content.

describe("applyNextcloudTheme", () => {
  function makeHost(): HTMLElement {
    return {
      style: {
        _vars: {} as Record<string, string>,
        setProperty(name: string, value: string) {
          this._vars[name] = value;
        },
        getPropertyValue(name: string): string {
          return this._vars[name] ?? "";
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

describe("captureViewerBaseFrom", () => {
  it("derives the proxy base from the registered ui/viewer.js src", () => {
    const scripts = [
      { src: "https://nc.example.com/index.php/csrfprotection.js" },
      {
        src: "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/ui/viewer.js",
      },
    ];
    expect(captureViewerBaseFrom(scripts)).toBe(
      "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/",
    );
  });

  it("tolerates a cache-busting query suffix on the src", () => {
    const scripts = [
      {
        src: "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/ui/viewer.js?v=42",
      },
    ];
    expect(captureViewerBaseFrom(scripts)).toBe(
      "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/",
    );
  });

  it("ignores scripts without a src and unrelated scripts", () => {
    const scripts = [
      {},
      { src: "" },
      { src: "https://nc.example.com/apps/other/main.js" },
    ];
    expect(captureViewerBaseFrom(scripts)).toBeNull();
  });

  it("returns null when there are no scripts", () => {
    expect(captureViewerBaseFrom(undefined)).toBeNull();
    expect(captureViewerBaseFrom(null)).toBeNull();
    expect(captureViewerBaseFrom([])).toBeNull();
  });
});

describe("captureViewerBase", () => {
  it("sets window.__CASSINI_VIEWER_BASE__ from a fake <script src=.../ui/viewer.js>", () => {
    const doc = {
      scripts: [
        {
          src: "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/ui/viewer.js",
        },
      ],
    } as unknown as Document;
    const win = {} as Window;

    captureViewerBase(doc, win);

    expect(win.__CASSINI_VIEWER_BASE__).toBe(
      "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/",
    );
  });

  it("leaves the base unset when no matching script is present", () => {
    const doc = { scripts: [{ src: "https://nc.example.com/x.js" }] } as unknown as Document;
    const win = {} as Window;
    captureViewerBase(doc, win);
    expect(win.__CASSINI_VIEWER_BASE__).toBeUndefined();
  });
});

describe("ensureShadowAppRoot", () => {
  // Minimal DOM stub: enough surface for ensureShadowAppRoot's getElementById /
  // createElement / appendChild / attachShadow walk, modelling AppAPI's bare
  // <div id="content"> plus the open shadow root the SPA mounts into.
  interface ShadowStub {
    children: StubEl[];
    appendChild(child: StubEl): void;
    getElementById(id: string): StubEl | null;
  }
  interface StubEl {
    id: string;
    rel?: string;
    href?: string;
    // ensureShadowAppRoot sets host.style.cssText on the created host element;
    // a real Element always has a .style object, so the stub must too or the
    // assignment throws "Cannot set properties of undefined (setting 'cssText')".
    style: { cssText: string };
    children: StubEl[];
    shadowRoot: ShadowStub | null;
    appendChild(child: StubEl): void;
    attachShadow(init: { mode: string }): ShadowStub;
  }
  function makeEl(id: string): StubEl {
    return {
      id,
      style: { cssText: "" },
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
        // #cassini-shadow-host only exists after ensureShadowAppRoot appends it;
        // #app lives in the shadow tree, which document.getElementById can't see.
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
      "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/ui/viewer.css";
    const appRoot = ensureShadowAppRoot(doc, cssHref) as unknown as StubEl;

    // The shadow HOST is in the light DOM under #content; #app is NOT (it lives
    // in the shadow tree), proving the SPA is isolated from NC chrome.
    const host = (content as unknown as StubEl).children.find(
      (c) => c.id === "cassini-shadow-host",
    );
    expect(host).toBeDefined();
    expect(host?.children.some((c) => c.id === "app")).toBe(false);
    const shadow = host?.shadowRoot;
    expect(shadow).not.toBeNull();
    expect(appRoot.id).toBe("app");
    // #app and a <link rel=stylesheet href=.../ui/viewer.css> live in the shadow.
    expect(shadow?.children.some((c) => c.id === "app")).toBe(true);
    const link = shadow?.children.find((c) => c.rel === "stylesheet");
    expect(link?.href).toBe(cssHref);
  });

  it("falls back to <body> when #content is absent", () => {
    const { doc, body } = makeStubDoc({ withContent: false });
    const appRoot = ensureShadowAppRoot(
      doc,
      "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/ui/viewer.css",
    ) as unknown as StubEl;
    expect(appRoot.id).toBe("app");
    const host = (body as unknown as StubEl).children.find(
      (c) => c.id === "cassini-shadow-host",
    );
    expect(host?.shadowRoot?.children.some((c) => c.id === "app")).toBe(true);
  });

  it("omits the stylesheet link when no css href is given (degraded mount)", () => {
    const { doc, content } = makeStubDoc({ withContent: true });
    ensureShadowAppRoot(doc, "");
    const host = (content as unknown as StubEl).children.find(
      (c) => c.id === "cassini-shadow-host",
    );
    expect(host?.shadowRoot?.children.some((c) => c.rel === "stylesheet")).toBe(false);
    expect(host?.shadowRoot?.children.some((c) => c.id === "app")).toBe(true);
  });
});
