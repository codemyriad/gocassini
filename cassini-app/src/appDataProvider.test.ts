import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { AppDataProvider } from "./appDataProvider";

// The provider's whole job is the request it builds and the answer it reports
// (D-626): the bundle itself is produced by the operator, from the same
// implementation `cassini meetings context` uses, so there is nothing here that
// formats a document — and a test that asserted a shape would be inventing a
// second opinion about it.

const PROXY_BASE = "https://cloud.example/index.php/apps/app_api/proxy/gocassini/";

type Entry = { id: string; title: string; dateLabel: string };

function entry(id: string): Entry {
  return { id, title: `Meeting ${id}`, dateLabel: "2026-08-18 14:30" };
}

function respondWith(body: string, init?: ResponseInit) {
  const fetchMock = vi.fn(() => Promise.resolve(new Response(body, init)));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

beforeEach(() => {
  // The embedded page: src/embedded.ts captures the AppAPI proxy base before
  // mount, and the published archive — catalog.json and this endpoint alike —
  // is served under it.
  vi.stubGlobal("window", {
    __CASSINI_VIEWER_BASE__: PROXY_BASE,
    location: { href: `${PROXY_BASE}control-panel/` },
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("AppDataProvider.loadContextBundle", () => {
  it("asks the published endpoint for the picked ids, in pick order", async () => {
    const fetchMock = respondWith("# Meeting b\n");

    await new AppDataProvider().loadContextBundle([entry("b"), entry("a"), entry("c")]);

    const url = new URL(fetchMock.mock.calls[0][0] as string);
    expect(url.origin + url.pathname).toBe(`${PROXY_BASE}published/meetings-context`);
    // Repeated id params, not a joined list, and the order is the order the
    // document prints in.
    expect(url.searchParams.getAll("id")).toEqual(["b", "a", "c"]);
    expect(url.searchParams.get("format")).toBe("markdown");
  });

  it("hands back the response body untouched", async () => {
    // Byte-identity with the CLI is the point: anything this method did to the
    // body would be a second implementation of the format.
    const bundle = "# One\n\n- id: a\n\n---\n\n# Two\n\n- id: b\n";
    respondWith(bundle);

    await expect(new AppDataProvider().loadContextBundle([entry("a"), entry("b")])).resolves.toBe(
      bundle,
    );
  });

  it("bypasses the proxy's response cache", async () => {
    const fetchMock = respondWith("# Meeting a\n");

    await new AppDataProvider().loadContextBundle([entry("a")]);

    expect(fetchMock.mock.calls[0][1]).toMatchObject({ cache: "no-store" });
  });

  it("refuses an empty selection without asking", async () => {
    const fetchMock = respondWith("");

    await expect(new AppDataProvider().loadContextBundle([])).rejects.toThrow(
      "Pick at least one meeting first.",
    );
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("repeats what the endpoint said was wrong with the request", async () => {
    // Go's http.Error writes the reason as one plain-text line, and it is the
    // only thing that knows the cap or which id was malformed.
    respondWith("a context bundle holds at most 20 meetings, got 25\n", { status: 400 });

    await expect(new AppDataProvider().loadContextBundle([entry("a")])).rejects.toThrow(
      "a context bundle holds at most 20 meetings, got 25",
    );
  });

  it("reads a JSON error envelope too", async () => {
    respondWith(JSON.stringify({ error: 'format must be "markdown" or "json"' }), { status: 400 });

    await expect(new AppDataProvider().loadContextBundle([entry("a")])).rejects.toThrow(
      'format must be "markdown" or "json"',
    );
  });

  it("does not paste a served error page into the panel", async () => {
    respondWith("<html><body>Gateway problem</body></html>", { status: 400 });

    await expect(new AppDataProvider().loadContextBundle([entry("a")])).rejects.toThrow(
      "Cassini could not read that request for a bundle.",
    );
  });

  it("says a meeting is unavailable without saying which of the two reasons", async () => {
    // Not the served body: `http.NotFound` writes "404 page not found", which
    // says nothing about meetings.
    respondWith("404 page not found\n", { status: 404 });

    await expect(new AppDataProvider().loadContextBundle([entry("a")])).rejects.toThrow(
      "One of these meetings is not available to you, or this deployment cannot assemble bundles.",
    );
  });

  it("names Nextcloud when the operator could not read it as this user", async () => {
    respondWith("", { status: 502 });

    await expect(new AppDataProvider().loadContextBundle([entry("a")])).rejects.toThrow(
      "Cassini could not read these meetings from Nextcloud.",
    );
  });

  it("falls back to the status for anything else", async () => {
    respondWith("", { status: 503 });

    await expect(new AppDataProvider().loadContextBundle([entry("a")])).rejects.toThrow(
      "Could not prepare the bundle (HTTP 503).",
    );
  });
});
