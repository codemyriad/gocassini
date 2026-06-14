import { describe, expect, it } from "vitest";

import { captureViewerBase, captureViewerBaseFrom, ensureAppRoot } from "./embedded";

// These tests run in the default (node) Vitest environment — no jsdom/happy-dom
// dependency — by exercising embedded.ts's pure helpers against hand-rolled
// minimal Document/Window stubs. The helpers are the load-bearing,
// adversarially-verified parts: base capture via a document.scripts scan
// (currentScript is null under `defer`) and #app creation under #content.

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
