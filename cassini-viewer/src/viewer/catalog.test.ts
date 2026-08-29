import { afterEach, describe, expect, it, vi } from "vitest";

import {
  filterMeetingCatalogEntries,
  formatMeetingDate,
  formatMeetingDateShort,
  loadMeetingCatalog,
  parseDateLabelMs,
  sortMeetingCatalogEntries,
  validateMeetingCatalog,
} from "./catalog";

describe("validateMeetingCatalog", () => {
  const originalWindow = globalThis.window;
  const originalFetch = globalThis.fetch;

  afterEach(() => {
    globalThis.window = originalWindow;
    globalThis.fetch = originalFetch;
    vi.unstubAllEnvs();
    vi.restoreAllMocks();
  });

  it("accepts a valid runtime meeting catalog", () => {
    const catalog = validateMeetingCatalog({
      version: "cassini.viewer.catalog.v1",
      meetings: [
        {
          id: "daily-meeting--2026-03-10--15-04-53",
          artifactPath: "./meetings/daily-meeting--2026-03-10--15-04-53",
          title: "Daily Meeting",
          dateLabel: "2026-03-10 15:04",
          speakerCount: 6,
          segmentCount: 9,
          digestDurationMs: 34536,
        },
      ],
    });

    expect(catalog.meetings).toHaveLength(1);
    expect(catalog.meetings[0]?.artifactPath).toBe(
      "./meetings/daily-meeting--2026-03-10--15-04-53",
    );
  });

  it("accepts audio-backed meeting entries with runtime metadata", () => {
    const catalog = validateMeetingCatalog({
      version: "cassini.viewer.catalog.v1",
      meetings: [
        {
          id: "daily-meeting-2026-03-18--12:30",
          audioPath: "./daily-meeting-2026-03-18--12:30.opus",
          title: "Daily Meeting",
          dateLabel: "2026-03-18 12:30",
        },
      ],
    });

    expect(catalog.meetings[0]?.audioPath).toBe(
      "./daily-meeting-2026-03-18--12:30.opus",
    );
    expect(catalog.meetings[0]?.speakerCount).toBeUndefined();
  });

  it("carries the room a meeting was recorded in", () => {
    // parseMeetingCatalogEntry rebuilds each entry from an explicit literal, so
    // a field it does not name is dropped at load with no error — even though
    // it was in the catalog the browser just fetched (D-622).
    const catalog = validateMeetingCatalog({
      version: "cassini.viewer.catalog.v1",
      meetings: [
        {
          id: "01JZ8K3M4N5P6Q7R8S9T0VWXYZ",
          audioPath: "./meetings/01JZ8K3M4N5P6Q7R8S9T0VWXYZ.opus",
          title: "Weekly Sync (Parakeet Tdt-0.6b-v2)",
          dateLabel: "2026-08-11 10:32",
          roomId: "a7bc3k9x",
          roomName: "Weekly Sync",
        },
      ],
    });

    expect(catalog.meetings[0]?.roomId).toBe("a7bc3k9x");
    expect(catalog.meetings[0]?.roomName).toBe("Weekly Sync");
  });

  it("accepts a meeting with no room, and one with only a room name", () => {
    // Both are ordinary states, not errors: a meeting published before the
    // field existed has neither, and one whose name was backfilled from the
    // published file has only the name — the Talk token was never written into
    // the artifact and cannot be recovered from it.
    const catalog = validateMeetingCatalog({
      version: "cassini.viewer.catalog.v1",
      meetings: [
        {
          id: "no-room",
          audioPath: "./meetings/no-room.opus",
          title: "Untitled meeting",
          dateLabel: "2026-03-18 12:30",
        },
        {
          id: "name-only",
          audioPath: "./meetings/name-only.opus",
          title: "Old Standup",
          dateLabel: "2026-03-19 12:30",
          roomName: "Old Standup",
        },
      ],
    });

    expect(catalog.meetings[0]?.roomId).toBeUndefined();
    expect(catalog.meetings[0]?.roomName).toBeUndefined();
    expect(catalog.meetings[1]?.roomId).toBeUndefined();
    expect(catalog.meetings[1]?.roomName).toBe("Old Standup");
  });

  it("rejects invalid catalog versions", () => {
    expect(() =>
      validateMeetingCatalog({
        version: "old-version",
        meetings: [],
      }),
    ).toThrow(/unsupported catalog version/i);
  });

  it("rejects malformed meeting entries", () => {
    expect(() =>
      validateMeetingCatalog({
        version: "cassini.viewer.catalog.v1",
        meetings: [
          {
            id: "meeting-a",
            artifactPath: "",
            title: "Meeting A",
            dateLabel: "2026-03-10 15:04",
            speakerCount: 3,
            segmentCount: 7,
            digestDurationMs: 12000,
          },
        ],
      }),
    ).toThrow(/artifactPath/i);
  });

  it("resolves the catalog against window.__CASSINI_VIEWER_BASE__ in the embedded build", async () => {
    // On AppAPI's embedded page the SPA runs at the Nextcloud route
    // /index.php/apps/app_api/embedded/gocassini/viewer, but the published
    // archive (catalog + recordings) is served by the operator THROUGH the
    // proxy at <base>published/, where <base> is the proxy base
    // src/embedded.ts captured into window.__CASSINI_VIEWER_BASE__. Resolving
    // relative to the embedded-page pathname would 404; the viewer-base branch
    // must win.
    const viewerBase =
      "http://nextcloud.example.com/index.php/apps/app_api/proxy/gocassini/";
    const calls: string[] = [];
    globalThis.window = {
      location: {
        href: "http://nextcloud.example.com/index.php/apps/app_api/embedded/gocassini/viewer",
      },
      __CASSINI_VIEWER_BASE__: viewerBase,
    } as Window;
    const wantCatalogUrl =
      "http://nextcloud.example.com/index.php/apps/app_api/proxy/gocassini/published/catalog.json";
    const fetchMock = vi.fn(async (input: string | URL) => {
      const url = String(input);
      calls.push(url);
      if (url === wantCatalogUrl) {
        return {
          ok: true,
          url,
          json: async () => ({
            version: "cassini.viewer.catalog.v1",
            meetings: [
              {
                id: "meeting-x",
                audioPath: "./meeting-x.opus",
                title: "Meeting X",
                dateLabel: "2026-05-19 10:00",
              },
            ],
          }),
        } as Response;
      }
      return { ok: false } as Response;
    });
    globalThis.fetch = fetchMock as typeof fetch;

    const catalog = await loadMeetingCatalog();

    expect(catalog?.meetings).toHaveLength(1);
    expect(fetchMock).toHaveBeenCalledWith(wantCatalogUrl, {
      cache: "no-store",
    });
    // audioPath follows the fetched catalog's response.url automatically.
    expect(catalog?.meetings[0]?.audioPath).toBe(
      "http://nextcloud.example.com/index.php/apps/app_api/proxy/gocassini/published/meeting-x.opus",
    );
    expect(calls).toEqual([wantCatalogUrl]);
  });

  it("loads the default catalog from the app base on nested routes", async () => {
    const calls: string[] = [];
    globalThis.window = {
      location: {
        href: "http://127.0.0.1:8765/preview/",
      },
    } as Window;
    globalThis.fetch = vi.fn(async (input: string | URL) => {
      const url = String(input);
      calls.push(url);
      if (url === "http://127.0.0.1:8765/catalog.json") {
        return {
          ok: true,
          url,
          json: async () => ({
            version: "cassini.viewer.catalog.v1",
            meetings: [
              {
                id: "meeting-a",
                audioPath: "./meeting-a.opus",
                title: "Meeting A",
                dateLabel: "2026-03-18 12:30",
              },
            ],
          }),
        } as Response;
      }
      return { ok: false } as Response;
    }) as typeof fetch;

    const catalog = await loadMeetingCatalog();

    expect(catalog?.meetings).toHaveLength(1);
    expect(catalog?.meetings[0]?.audioPath).toBe(
      "http://127.0.0.1:8765/meeting-a.opus",
    );
    expect(calls).toContain("http://127.0.0.1:8765/catalog.json");
    expect(calls).not.toContain("http://127.0.0.1:8765/preview/catalog.json");
  });

  it("returns null only when the site genuinely has no catalog", async () => {
    // null tells the caller to load a single bundled artifact instead, which is
    // right for a standalone one-meeting export. A 404 is the only status that
    // means that.
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/viewer" },
    } as Window;
    globalThis.fetch = vi.fn(
      async () => ({ ok: false, status: 404 }) as Response,
    ) as typeof fetch;

    await expect(loadMeetingCatalog()).resolves.toBeNull();
  });

  it("throws when a catalog that should exist cannot be reached", async () => {
    // Returning null here would send an ExApp viewer looking for a bundled
    // artifact its build does not contain, turning a Nextcloud outage into a
    // confusing empty state rather than a reported error (D-550).
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/viewer" },
    } as Window;
    for (const status of [500, 502, 503]) {
      globalThis.fetch = vi.fn(
        async () => ({ ok: false, status }) as Response,
      ) as typeof fetch;

      await expect(loadMeetingCatalog()).rejects.toThrow(String(status));
    }
  });
});

describe("sortMeetingCatalogEntries", () => {
  it("sorts meetings newest first", () => {
    const sorted = sortMeetingCatalogEntries([
      {
        id: "daily-meeting--2026-03-05--12:38:29",
        audioPath: "./daily-meeting--2026-03-05--12:38:29.opus",
        title: "Daily Meeting",
        dateLabel: "2026-03-05 12:38",
      },
      {
        id: "daily-meeting-2026-03-18--12:30",
        audioPath: "./daily-meeting-2026-03-18--12:30.opus",
        title: "Daily Meeting",
        dateLabel: "2026-03-18 12:30",
      },
      {
        id: "daily-meeting-2026-03-12--12:29",
        audioPath: "./daily-meeting-2026-03-12--12:29.opus",
        title: "Daily Meeting",
        dateLabel: "2026-03-12 12:29",
      },
    ]);

    expect(sorted.map((meeting) => meeting.id)).toEqual([
      "daily-meeting-2026-03-18--12:30",
      "daily-meeting-2026-03-12--12:29",
      "daily-meeting--2026-03-05--12:38:29",
    ]);
  });

  it("sorts entries with unparseable date labels after every dated entry", () => {
    const sorted = sortMeetingCatalogEntries([
      {
        id: "01AAAAAAAAAAAAAAAAAAAAAAAA",
        audioPath: "./a.opus",
        title: "01AAAAAAAAAAAAAAAAAAAAAAAA",
        // Legacy catalogs shipped the raw id as the date label.
        dateLabel: "01AAAAAAAAAAAAAAAAAAAAAAAA",
      },
      {
        id: "daily-meeting-2026-03-12--12:29",
        audioPath: "./b.opus",
        title: "Daily Meeting",
        dateLabel: "2026-03-12 12:29",
      },
      {
        id: "daily-meeting-2026-03-18--12:30",
        audioPath: "./c.opus",
        title: "Daily Meeting",
        dateLabel: "2026-03-18 12:30",
      },
    ]);

    expect(sorted.map((meeting) => meeting.id)).toEqual([
      "daily-meeting-2026-03-18--12:30",
      "daily-meeting-2026-03-12--12:29",
      "01AAAAAAAAAAAAAAAAAAAAAAAA",
    ]);
  });

  it("breaks date ties by id, newest-looking id first", () => {
    const sorted = sortMeetingCatalogEntries([
      {
        id: "meeting-a",
        audioPath: "./a.opus",
        title: "A",
        dateLabel: "2026-03-12 12:29",
      },
      {
        id: "meeting-b",
        audioPath: "./b.opus",
        title: "B",
        dateLabel: "2026-03-12 12:29",
      },
    ]);

    expect(sorted.map((meeting) => meeting.id)).toEqual([
      "meeting-b",
      "meeting-a",
    ]);
  });
});

describe("parseDateLabelMs", () => {
  it("parses date-only and date-time labels", () => {
    expect(parseDateLabelMs("2026-03-12")).toBe(Date.UTC(2026, 2, 12));
    expect(parseDateLabelMs("2026-03-12 12:29")).toBe(
      Date.UTC(2026, 2, 12, 12, 29),
    );
    expect(parseDateLabelMs("2026-03-12 12:29:31")).toBe(
      Date.UTC(2026, 2, 12, 12, 29, 31),
    );
  });

  it("returns null for labels that are not dates", () => {
    expect(parseDateLabelMs("01KWEKPZVEJWP9BYBPBX9ZRNDQ")).toBeNull();
    expect(parseDateLabelMs("")).toBeNull();
  });

  it("returns null for date-shaped labels with out-of-range fields", () => {
    expect(parseDateLabelMs("2026-13-05")).toBeNull();
    expect(parseDateLabelMs("2026-03-32")).toBeNull();
    expect(parseDateLabelMs("2026-03-12 24:00")).toBeNull();
  });
});

describe("filterMeetingCatalogEntries", () => {
  const meetings = [
    {
      id: "m1",
      audioPath: "./m1.opus",
      title: "Daily Meeting",
      dateLabel: "2026-03-12 12:29",
    },
    {
      id: "m2",
      audioPath: "./m2.opus",
      title: "Planning Session",
      dateLabel: "2026-04-02 09:00",
    },
    {
      id: "m3",
      audioPath: "./m3.opus",
      title: "Untitled meeting",
      dateLabel: "2026-04-15 15:30",
    },
  ];

  it("returns all meetings for an empty or whitespace query", () => {
    expect(filterMeetingCatalogEntries(meetings, "")).toEqual(meetings);
    expect(filterMeetingCatalogEntries(meetings, "   ")).toEqual(meetings);
  });

  it("matches titles case-insensitively", () => {
    expect(
      filterMeetingCatalogEntries(meetings, "planning").map((m) => m.id),
    ).toEqual(["m2"]);
    expect(
      filterMeetingCatalogEntries(meetings, "MEETING").map((m) => m.id),
    ).toEqual(["m1", "m3"]);
  });

  it("matches raw date labels", () => {
    expect(
      filterMeetingCatalogEntries(meetings, "2026-04").map((m) => m.id),
    ).toEqual(["m2", "m3"]);
  });

  it("matches dates the way the cards render them", () => {
    // Cards show "12 Mar 2026" (en-GB short format).
    expect(
      filterMeetingCatalogEntries(meetings, "mar 2026").map((m) => m.id),
    ).toEqual(["m1"]);
  });

  it("returns an empty list when nothing matches", () => {
    expect(filterMeetingCatalogEntries(meetings, "retrospective")).toEqual([]);
  });

  it("filters entries whose date label is not a date without crashing", () => {
    // Legacy catalogs shipped the raw ULID id as the date label.
    const legacy = [
      {
        id: "01KWEKPZVEJWP9BYBPBX9ZRNDQ",
        audioPath: "./legacy.opus",
        title: "01KWEKPZVEJWP9BYBPBX9ZRNDQ",
        dateLabel: "01KWEKPZVEJWP9BYBPBX9ZRNDQ",
      },
      ...meetings,
    ];
    expect(
      filterMeetingCatalogEntries(legacy, "01kwek").map((m) => m.id),
    ).toEqual(["01KWEKPZVEJWP9BYBPBX9ZRNDQ"]);
    expect(
      filterMeetingCatalogEntries(legacy, "planning").map((m) => m.id),
    ).toEqual(["m2"]);
  });
});

describe("formatMeetingDateShort", () => {
  it("renders a compact en-GB date with the meeting's start time (D-685)", () => {
    expect(formatMeetingDateShort("2026-03-12 12:29")).toBe("12 Mar 2026, 12:29");
  });

  it("keeps the label's own digits — no timezone shift, no am/pm (D-484)", () => {
    // 24-hour, wall clock as written: the label makes no timezone claim, and a
    // list card has no room for a "pm" suffix.
    expect(formatMeetingDateShort("2026-03-12T00:15")).toBe("12 Mar 2026, 00:15");
  });

  it("renders date only when the label carries no time", () => {
    expect(formatMeetingDateShort("2026-04-08")).toBe("8 Apr 2026");
  });

  it("drops an out-of-range time rather than rendering it", () => {
    expect(formatMeetingDateShort("2026-04-08 99:99")).toBe("8 Apr 2026");
  });

  it("passes through labels that are not dates", () => {
    expect(formatMeetingDateShort("01KWEKPZVEJWP9BYBPBX9ZRNDQ")).toBe(
      "01KWEKPZVEJWP9BYBPBX9ZRNDQ",
    );
  });
});

describe("formatMeetingDate", () => {
  it("renders the label's wall-clock time without asserting a timezone (D-484)", () => {
    // The dateLabel carries no timezone, so the formatter must not tack on a
    // "GMT+1" / "UTC" suffix — that would claim an instant it can't justify
    // (a UTC-stamped "21:11" rendered as "9:11 PM GMT+1" is an hour off and
    // falsely precise for a CET viewer).
    const rendered = formatMeetingDate("2026-03-09 21:11");
    // No trailing timezone token of any kind: "GMT+1", "UTC", or a named
    // abbreviation like "WET"/"CET"/"PST" (the last token must be the time or an
    // AM/PM day-period, never a 3-4 letter zone). This is the real regression
    // guard — before the fix the rendered string ended with the viewer's zone.
    expect(rendered).not.toMatch(/\s(?:GMT|UTC|[A-Z]{3,4})[+\-\d:]*$/);
    // The label's own wall-clock digits survive: constructing the Date in local
    // time and formatting in local time cancel out, so this holds regardless of
    // the runtime timezone (12h "9:11" or 24h "21:11" depending on locale).
    expect(rendered).toMatch(/(?:21:11|9:11)/);
    expect(rendered).toContain("2026");
  });

  it("renders a date-only label with no time and no timezone", () => {
    const rendered = formatMeetingDate("2026-03-09");
    expect(rendered).not.toMatch(/GMT|UTC/i);
    expect(rendered).not.toMatch(/\d{1,2}:\d{2}/);
    expect(rendered).toContain("2026");
  });

  it("passes through labels that are not dates", () => {
    expect(formatMeetingDate("01KWEKPZVEJWP9BYBPBX9ZRNDQ")).toBe(
      "01KWEKPZVEJWP9BYBPBX9ZRNDQ",
    );
  });
});
