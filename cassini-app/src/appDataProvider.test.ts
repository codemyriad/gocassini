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
    // ONE comma-separated `ids`, in pick order — a repeated `id` would be
    // collapsed to its last value by AppAPI's PHP proxy, which answers 200 with
    // a bundle of one meeting rather than failing.
    expect(url.searchParams.get("ids")).toBe("b,a,c");
    expect(url.searchParams.getAll("id")).toEqual([]);
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

describe("AppDataProvider insights", () => {
  // The DataProvider seam is how the browse list learns that this build has an
  // operator behind it (D-721). Everything about the request itself lives in
  // insights/client.ts, so what is asserted here is only that these two are
  // wired to it and relay what it returns.

  it("lists the caller's runs from the insight routes, whatever their status", () => {
    const fetchMock = respondWith(
      JSON.stringify({
        insights: [
          { id: "ins_0123456789abcdef", status: "running", meetingIds: ["a"] },
          { id: "ins_00000000000000ff", status: "succeeded", meetingIds: ["b"] },
        ],
      }),
    );

    return new AppDataProvider().listInsights().then((insights) => {
      expect(new URL(fetchMock.mock.calls[0][0] as string).pathname).toBe(
        `${new URL(PROXY_BASE).pathname}insights`,
      );
      // A queued or running run belongs in the list too: it is a record that
      // exists before its content does, and dropping it would put the card back
      // to materialising out of nowhere a minute later.
      expect(insights.map((insight) => insight.status)).toEqual(["running", "succeeded"]);
    });
  });

  it("fetches one insight's document on its own", async () => {
    // Not part of the listing: N insights must not carry N documents over the
    // wire.
    const fetchMock = respondWith(
      JSON.stringify({
        id: "ins_0123456789abcdef",
        status: "succeeded",
        document: "# What we decided\n",
      }),
    );

    await expect(
      new AppDataProvider().loadInsightDocument("ins_0123456789abcdef"),
    ).resolves.toBe("# What we decided\n");
    expect(new URL(fetchMock.mock.calls[0][0] as string).pathname).toBe(
      `${new URL(PROXY_BASE).pathname}insights/ins_0123456789abcdef`,
    );
  });
});

describe("AppDataProvider annotations", () => {
  const job = {
    id: "job_1",
    kind: "merge",
    tagId: "tag_a",
    into: "tag_b",
    state: "running",
    total: 3,
    done: 0,
    failed: [],
    actor: "alice",
    startedAtUtc: "2026-09-11T10:00:00Z",
  };
  const calls = (fetchMock: ReturnType<typeof respondWith>) =>
    (fetchMock.mock.calls as unknown as [string, RequestInit][]).map(([url, init]) => ({
      path: url.replace(PROXY_BASE, ""),
      method: init.method ?? "GET",
      body: init.body,
      cache: init.cache,
    }));

  it("reads the vocabulary beside the published archive, past the proxy's cache", async () => {
    const fetchMock = respondWith(JSON.stringify({ tags: [], meetings: [], coverage: { visible: 0, indexed: 0 } }));

    await new AppDataProvider().loadTagVocabulary();

    expect(calls(fetchMock)).toEqual([
      { path: "annotations/tags", method: "GET", body: undefined, cache: "no-store" },
    ]);
  });

  it("reads and writes one meeting's marks by its catalog id", async () => {
    const fetchMock = respondWith(JSON.stringify({ meetingId: "m 1", revision: 1, annotations: null, resolved: null }));
    const provider = new AppDataProvider();
    const request = { ops: [{ op: "unmark" as const, itemId: "mk_1" }], expectRevision: 1 };

    await provider.loadMeetingAnnotations(entry("m 1"));
    await provider.applyAnnotationOps(entry("m 1"), request);

    expect(calls(fetchMock)).toEqual([
      { path: "annotations/meetings/m%201", method: "GET", body: undefined, cache: "no-store" },
      { path: "annotations/meetings/m%201", method: "POST", body: JSON.stringify(request), cache: "no-store" },
    ]);
  });

  it("changes a tag across the archive and hands back the job doing it", async () => {
    const fetchMock = respondWith(JSON.stringify({ tag: {}, job }));
    const provider = new AppDataProvider();

    await provider.updateTag("tag_a", { color: "red" });
    await expect(provider.mergeTag("tag_a", "tag_b")).resolves.toEqual(job);
    await expect(provider.deleteTag("tag_a")).resolves.toEqual(job);

    expect(calls(fetchMock).map(({ path, method, body }) => [path, method, body])).toEqual([
      ["annotations/tags/tag_a", "POST", '{"color":"red"}'],
      ["annotations/tags/tag_a/merge", "POST", '{"into":"tag_b"}'],
      ["annotations/tags/tag_a/delete", "POST", "{}"],
    ]);
  });

  it("polls the tag job, and reads no job as null", async () => {
    const fetchMock = respondWith(JSON.stringify({ job: null }));

    await expect(new AppDataProvider().loadTagJob()).resolves.toBeNull();
    expect(calls(fetchMock)).toEqual([
      { path: "annotations/tags/job", method: "GET", body: undefined, cache: "no-store" },
    ]);
  });

  it("carries the operator's error code, and the tag a label already belongs to", async () => {
    respondWith(JSON.stringify({ error: "label-exists", tagId: "tag_b" }), { status: 409 });

    await expect(new AppDataProvider().updateTag("tag_a", { label: "budget" })).rejects.toMatchObject({
      name: "AnnotationError",
      status: 409,
      code: "label-exists",
      tagId: "tag_b",
    });
  });

  it("keeps the status when the refusal is not JSON", async () => {
    respondWith("404 page not found\n", { status: 404 });

    await expect(new AppDataProvider().loadTagVocabulary()).rejects.toMatchObject({
      name: "AnnotationError",
      status: 404,
      code: "",
    });
  });
});
