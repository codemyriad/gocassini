import { afterEach, describe, expect, it, vi } from "vitest";
import {
  groupHitsByMeeting,
  materializeSearchResponse,
  mergeSearchResults,
  searchMeetingTranscripts,
  type MeetingSearchHit,
} from "./meetingSearch";

// The viewer base is what decides whether there is an operator to ask at all.
// Mirrors catalog.test.ts: readViewerBase reads window, and these suites run
// without a DOM, so the window is supplied here rather than by an environment.
function withOperator(base: string | null) {
  if (base === null) {
    delete (globalThis as { window?: unknown }).window;
    return;
  }
  globalThis.window = {
    location: { href: "https://nc.example/index.php/apps/app_api/embedded/gocassini/viewer" },
    __CASSINI_VIEWER_BASE__: base,
  } as unknown as Window & typeof globalThis;
}

function respondWith(status: number, body: unknown): typeof fetch {
  return vi.fn(async () =>
    new Response(typeof body === "string" ? body : JSON.stringify(body), {
      status,
      headers: { "content-type": "application/json" },
    }),
  ) as unknown as typeof fetch;
}

afterEach(() => {
  vi.unstubAllGlobals();
  withOperator(null);
});

function hit(overrides: Partial<MeetingSearchHit> = {}): MeetingSearchHit {
  return {
    meetingId: "m1",
    title: "Standup",
    dateLabel: "2026-09-01",
    roomId: "rm_1",
    roomName: "Dev",
    segmentId: "seg_1",
    startMs: 1000,
    endMs: 4000,
    speakerId: "S1",
    matched: "exact",
    snippet: "the roadmap",
    ...overrides,
  };
}

describe("searchMeetingTranscripts", () => {
  it("does not ask the server about a blank query", async () => {
    withOperator("https://nc.example/apps/gocassini/");
    const fetchMock = respondWith(200, {});
    vi.stubGlobal("fetch", fetchMock);

    const outcome = await searchMeetingTranscripts("   ");

    expect(outcome.status).toBe("ok");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("reports a standalone build as unsupported, not as an error", async () => {
    withOperator(null);
    const outcome = await searchMeetingTranscripts("roadmap");
    expect(outcome.status).toBe("unsupported");
  });

  // THE property D-736 asks for: none of these may look like "nothing matched".
  it.each([
    [404, "unsupported"],
    [429, "rateLimited"],
    [503, "indexUnavailable"],
    [502, "failed"],
    [500, "failed"],
  ])("maps HTTP %i to %s rather than an empty result", async (status, expected) => {
    withOperator("https://nc.example/apps/gocassini/");
    vi.stubGlobal("fetch", respondWith(status, { error: "upstream said no" }));

    const outcome = await searchMeetingTranscripts("roadmap");

    expect(outcome.status).toBe(expected);
    if (outcome.status !== "ok" && outcome.status !== "unsupported") {
      expect(outcome.message).toBe("upstream said no");
    }
  });

  it("falls back to its own sentence when the body is not JSON", async () => {
    withOperator("https://nc.example/apps/gocassini/");
    vi.stubGlobal("fetch", respondWith(502, "<html>gateway</html>"));

    const outcome = await searchMeetingTranscripts("roadmap");

    expect(outcome.status).toBe("failed");
    if (outcome.status === "failed") {
      expect(outcome.message).toContain("502");
    }
  });

  it("reports an unreachable operator as failed, never as empty", async () => {
    withOperator("https://nc.example/apps/gocassini/");
    vi.stubGlobal("fetch", vi.fn(async () => {
      throw new TypeError("network down");
    }) as unknown as typeof fetch);

    const outcome = await searchMeetingTranscripts("roadmap");
    expect(outcome.status).toBe("failed");
  });

  it("sends the per-meeting cap and the query", async () => {
    withOperator("https://nc.example/apps/gocassini/");
    const fetchMock = respondWith(200, { hits: [], coverage: { visible: 3, searched: 3 } });
    vi.stubGlobal("fetch", fetchMock);

    await searchMeetingTranscripts("the roadmap", { perMeeting: 3, limit: 40 });

    const called = new URL((fetchMock as unknown as ReturnType<typeof vi.fn>).mock.calls[0][0] as string);
    expect(called.searchParams.get("q")).toBe("the roadmap");
    expect(called.searchParams.get("perMeeting")).toBe("3");
    expect(called.searchParams.get("limit")).toBe("40");
  });
});

describe("materializeSearchResponse", () => {
  it("rejects a body that is not a search response", () => {
    expect(() => materializeSearchResponse({ nope: true })).toThrow();
  });

  it("keeps a hit that is missing optional fields", () => {
    const outcome = materializeSearchResponse({
      hits: [{ meetingId: "m1", segmentId: "s1", startMs: 5, endMs: 9 }],
    });
    expect(outcome.status).toBe("ok");
    if (outcome.status === "ok") {
      expect(outcome.hits).toHaveLength(1);
      expect(outcome.hits[0].snippet).toBe("");
      expect(outcome.hits[0].speakerId).toBe("");
    }
  });

  it("drops a hit with no meeting id, which could not be linked anywhere", () => {
    const outcome = materializeSearchResponse({ hits: [{ segmentId: "s1" }, { meetingId: "m2" }] });
    if (outcome.status === "ok") {
      expect(outcome.hits.map((h) => h.meetingId)).toEqual(["m2"]);
    }
  });
});

describe("groupHitsByMeeting", () => {
  it("preserves the server's ranking across and within meetings", () => {
    const grouped = groupHitsByMeeting([
      hit({ meetingId: "b", segmentId: "b1" }),
      hit({ meetingId: "a", segmentId: "a1" }),
      hit({ meetingId: "b", segmentId: "b2" }),
    ]);

    expect(grouped.map((g) => g.meetingId)).toEqual(["b", "a"]);
    expect(grouped[0].hits.map((h) => h.segmentId)).toEqual(["b1", "b2"]);
  });
});

describe("mergeSearchResults", () => {
  const meetings = [{ id: "m1" }, { id: "m2" }, { id: "m3" }];

  it("keeps name matches first and in catalog order", () => {
    const rows = mergeSearchResults(
      [meetings[0], meetings[1]],
      meetings,
      [{ meetingId: "m3", hits: [hit({ meetingId: "m3" })] }],
    );
    expect(rows.map((r) => r.entry.id)).toEqual(["m1", "m2", "m3"]);
  });

  it("shows a meeting that matched both ways once, with its moments", () => {
    const rows = mergeSearchResults(
      [meetings[0]],
      meetings,
      [{ meetingId: "m1", hits: [hit({ meetingId: "m1" })] }],
    );
    expect(rows).toHaveLength(1);
    expect(rows[0].hits).toHaveLength(1);
  });

  it("skips a hit for a meeting the list has never heard of", () => {
    const rows = mergeSearchResults([], meetings, [
      { meetingId: "ghost", hits: [hit({ meetingId: "ghost" })] },
    ]);
    expect(rows).toEqual([]);
  });

  it("gives a name-only match no moments rather than inventing them", () => {
    const rows = mergeSearchResults([meetings[0]], meetings, []);
    expect(rows[0].hits).toEqual([]);
  });
});
