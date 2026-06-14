import { describe, expect, it } from "vitest";

import {
  captureOperatorBasePath,
  captureProxyBaseFrom,
  ensureAppRoot,
} from "./embedded";

// These tests run in the default (node) Vitest environment — no jsdom/happy-dom
// dependency — by exercising embedded.ts's pure helpers against hand-rolled
// minimal Document/Window stubs. The helpers are the load-bearing,
// adversarially-verified parts: proxy-base capture via a document.scripts scan
// (currentScript is null under `defer`), turning it into the authoritative
// window.__CASSINI_CONFIG__.operatorBasePath, and #app creation under #content.

describe("captureProxyBaseFrom", () => {
  it("derives the proxy base from the registered ui/control-panel.js src", () => {
    const scripts = [
      { src: "https://nc.example.com/index.php/csrfprotection.js" },
      {
        src: "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/ui/control-panel.js",
      },
    ];
    expect(captureProxyBaseFrom(scripts)).toBe(
      "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/",
    );
  });

  it("tolerates a cache-busting query suffix on the src", () => {
    const scripts = [
      {
        src: "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/ui/control-panel.js?v=42",
      },
    ];
    expect(captureProxyBaseFrom(scripts)).toBe(
      "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/",
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
      "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/operator",
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
      "https://nc.example.com/index.php/apps/app_api/proxy/gocassini/operator",
    );
  });

  it("leaves config untouched when no matching script is present", () => {
    const doc = { scripts: [{ src: "https://nc.example.com/x.js" }] } as unknown as Document;
    const win = {} as Window;
    captureOperatorBasePath(doc, win);
    expect(win.__CASSINI_CONFIG__).toBeUndefined();
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
