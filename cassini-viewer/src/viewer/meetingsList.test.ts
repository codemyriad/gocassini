import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { loadMeetingsList, resolveMeetingsListUrl } from "./catalog";

const VIEWER_BASE =
  "http://nextcloud.example.com/index.php/apps/app_api/proxy/gocassini/";
const LIST_URL =
  "http://nextcloud.example.com/index.php/apps/app_api/proxy/gocassini/published/meetings-list";

const originalWindow = globalThis.window;
const originalFetch = globalThis.fetch;

function embedded(): void {
  globalThis.window = {
    location: {
      href: "http://nextcloud.example.com/index.php/apps/app_api/embedded/gocassini/viewer",
    },
    __CASSINI_VIEWER_BASE__: VIEWER_BASE,
  } as Window;
}

function respond(init: Partial<Response> & { json?: () => Promise<unknown> }): void {
  globalThis.fetch = vi.fn(async () => ({ url: LIST_URL, ...init }) as Response) as typeof fetch;
}

beforeEach(() => embedded());

afterEach(() => {
  globalThis.window = originalWindow;
  globalThis.fetch = originalFetch;
  vi.restoreAllMocks();
});

describe("loadMeetingsList", () => {
  it("reads the list from the operator endpoint and resolves asset paths against it", async () => {
    const fetchMock = vi.fn(async (input: string | URL) => {
      expect(String(input)).toBe(LIST_URL);
      return {
        ok: true,
        status: 200,
        url: LIST_URL,
        json: async () => ({
          version: "cassini.viewer.catalog.v1",
          meetings: [
            {
              id: "m1",
              title: "Standup",
              dateLabel: "2026-08-01 09:00",
              audioPath: "./meetings/JOB1.opus",
            },
          ],
          filter: { from: "2026-08-01 00:00:00" },
          excluded: { total: 1, undated: 1 },
        }),
      } as Response;
    });
    globalThis.fetch = fetchMock as typeof fetch;

    const result = await loadMeetingsList();

    expect(result.status).toBe("ok");
    if (result.status !== "ok") return;
    expect(result.catalog.meetings).toHaveLength(1);
    // The endpoint's last path segment is file-shaped, so a relative asset
    // resolves beside catalog.json rather than under a directory of its own.
    expect(result.catalog.meetings[0]?.audioPath).toBe(
      "http://nextcloud.example.com/index.php/apps/app_api/proxy/gocassini/published/meetings/JOB1.opus",
    );
    expect(fetchMock).toHaveBeenCalledWith(LIST_URL, { cache: "no-store" });
  });

  it("reports the route as absent on 404 so the caller can fall back", async () => {
    respond({ ok: false, status: 404 });
    await expect(loadMeetingsList()).resolves.toEqual({ status: "absent" });
  });

  it("is absent outside the embedded build, without fetching", async () => {
    globalThis.window = { location: { href: "http://127.0.0.1:8765/" } } as Window;
    const fetchMock = vi.fn();
    globalThis.fetch = fetchMock as unknown as typeof fetch;

    await expect(loadMeetingsList()).resolves.toEqual({ status: "absent" });
    expect(fetchMock).not.toHaveBeenCalled();
    expect(resolveMeetingsListUrl()).toBe("");
  });

  // The whole point of the endpoint: a substrate failure must NOT arrive as an
  // empty list, because the viewer would render that as "you have no meetings".
  it("throws on a substrate failure, surfacing the operator's own message", async () => {
    respond({
      ok: false,
      status: 502,
      json: async () => ({
        error: "the recordings archive is unreachable; this is not an empty result",
      }),
    });
    await expect(loadMeetingsList()).rejects.toThrow(/not an empty result/);
  });

  it("falls back to the status when the error body is not JSON", async () => {
    respond({
      ok: false,
      status: 502,
      json: async () => {
        throw new Error("not json");
      },
    });
    await expect(loadMeetingsList()).rejects.toThrow(/HTTP 502/);
  });

  it("passes an empty list through as a real, empty answer", async () => {
    respond({
      ok: true,
      status: 200,
      json: async () => ({ version: "cassini.viewer.catalog.v1", meetings: [] }),
    });
    const result = await loadMeetingsList();
    expect(result).toEqual({
      status: "ok",
      catalog: { version: "cassini.viewer.catalog.v1", meetings: [] },
    });
  });
});
