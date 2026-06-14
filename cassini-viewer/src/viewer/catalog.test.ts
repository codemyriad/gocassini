import { afterEach, describe, expect, it, vi } from "vitest";

import { loadMeetingCatalog, sortMeetingCatalogEntries, validateMeetingCatalog } from "./catalog";

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

    expect(catalog.meetings[0]?.audioPath).toBe("./daily-meeting-2026-03-18--12:30.opus");
    expect(catalog.meetings[0]?.speakerCount).toBeUndefined();
  });

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
        href:
          "http://nextcloud.example.com/index.php/apps/app_api/embedded/gocassini/viewer",
      },
      __CASSINI_VIEWER_BASE__: viewerBase,
    } as Window;
    const wantCatalogUrl =
      "http://nextcloud.example.com/index.php/apps/app_api/proxy/gocassini/published/catalog.json";
    globalThis.fetch = vi.fn(async (input: string | URL) => {
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
    }) as typeof fetch;

    const catalog = await loadMeetingCatalog();

    expect(catalog?.meetings).toHaveLength(1);
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
    expect(catalog?.meetings[0]?.audioPath).toBe("http://127.0.0.1:8765/meeting-a.opus");
    expect(calls).toContain("http://127.0.0.1:8765/catalog.json");
    expect(calls).not.toContain("http://127.0.0.1:8765/preview/catalog.json");
  });
});
