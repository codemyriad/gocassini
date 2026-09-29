import { afterEach, describe, expect, it, vi } from "vitest";
import { OperatorClient } from "./client";

afterEach(() => vi.unstubAllGlobals());
function reply(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

describe("recordings storage API", () => {
  it("reads a single setup plan and treats missing browser permission as false", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => reply({
      service_account: { user: "cassini", known: true, exists: false },
      ok: false, state: "unavailable", step: "owner_account",
      setup: [{ id: "owner", action: "create_user", args: { user: "cassini" } }],
    })));
    const status = await new OperatorClient("/operator").getStorage();
    expect(status.setup).toEqual([{
      id: "owner", action: "create_user", title: "", args: { user: "cassini" },
      browser: false, occ: "",
    }]);
    expect(status.service_account.exists).toBe(false);
  });
  it("rechecks using the one supported action", async () => {
    const fetchMock = vi.fn(async () => reply({ ok: true, state: "provisioned", setup: [] }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new OperatorClient("/operator");
    await client.recheckStorage();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(JSON.parse(String(fetchMock.mock.calls[0][1].body))).toEqual({ action: "recheck" });
  });
});

describe("OperatorClient storage", () => {
  it("reads the recording and build folder sizes from the separate usage endpoint", async () => {
    const fetchMock = vi.fn(async () =>
      reply({
        measured_at: "2026-09-22T09:15:00Z",
        duration_ms: 1420.5,
        sources: [
          { id: "published", label: "Published meetings", location: "Nextcloud Files", bytes: 12, duration_ms: 1310.25, files: 1206, collections: 48, requests: 48 },
          { id: "current", label: "Current working archive", location: "Cassini persistent storage", bytes: 7, error: "unavailable" },
        ],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const usage = await new OperatorClient("/operator").getStorageUsage();

    expect(fetchMock.mock.calls[0]?.[0]).toBe("/operator/storage/usage");
    expect(usage).toEqual({
      measured_at: "2026-09-22T09:15:00Z",
      duration_ms: 1420.5,
      sources: [
        { id: "published", label: "Published meetings", location: "Nextcloud Files", bytes: 12, duration_ms: 1310.25, files: 1206, collections: 48, requests: 48, error: "" },
        { id: "current", label: "Current working archive", location: "Cassini persistent storage", bytes: 7, duration_ms: 0, files: 0, collections: 0, requests: 0, error: "unavailable" },
      ],
    });
  });

  it("recalculates the storage index only through an explicit POST", async () => {
    const fetchMock = vi.fn(async () =>
      reply({ measured_at: "2026-09-23T08:30:00Z", duration_ms: 42, sources: [] }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await new OperatorClient("/operator").recalculateStorageUsage();

    expect(fetchMock.mock.calls[0]?.[0]).toBe("/operator/storage/usage");
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe("POST");
  });

  it("normalizes the combined detailed storage index", async () => {
    const fetchMock = vi.fn(async () => reply({
      measured_at: "2026-09-23T12:00:00Z",
      duration_ms: 8.5,
      published: [{ id: "default", label: "Default storage mode", location: "CassiniNoACL/Recordings", bytes: 12 }],
      directories: [{
        id: "current",
        label: "Working archive",
        location: "/jobs/current",
        bytes: 21,
        files: 2,
        collections: 1,
        formats: [
          { extension: ".mkv", bytes: 14, files: 1 },
          { extension: ".json", bytes: 7, files: 1 },
        ],
      }],
    }));
    vi.stubGlobal("fetch", fetchMock);

    const usage = await new OperatorClient("/operator").getDetailedStorageUsage();

    expect(fetchMock.mock.calls[0]?.[0]).toBe("/operator/storage/usage/details");
    expect(usage.published[0]).toMatchObject({ id: "default", bytes: 12 });
    expect(usage.directories[0]).toEqual({
      id: "current",
      label: "Working archive",
      location: "/jobs/current",
      bytes: 21,
      files: 2,
      collections: 1,
      formats: [
        { extension: ".mkv", bytes: 14, files: 1 },
        { extension: ".json", bytes: 7, files: 1 },
      ],
      error: "",
    });
  });

  it("reads lifecycle categories and preserves unknown dates and classification errors", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => reply({ categories: [{
      id: "recordings", bytes: 15, files: 2, undated_bytes: 5, undated_files: 1,
      days: [{ date: "2026-09-01", bytes: 10, files: 1 }],
    }], category_error: "Lifecycle records unavailable" })));
    const usage = await new OperatorClient("/operator").getDetailedStorageUsage();
    expect(usage.category_error).toBe("Lifecycle records unavailable");
    expect(usage.categories[0]).toEqual({ id: "recordings", bytes: 15, files: 2,
      undated_bytes: 5, undated_files: 1, days: [{date: "2026-09-01", bytes: 10, files: 1}],
    });
  });

  it("recalculates the combined detailed index only through an explicit POST", async () => {
    const fetchMock = vi.fn(async () => reply({ measured_at: "", published: [], directories: [] }));
    vi.stubGlobal("fetch", fetchMock);

    await new OperatorClient("/operator").recalculateDetailedStorageUsage();

    expect(fetchMock.mock.calls[0]?.[0]).toBe("/operator/storage/usage/details");
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe("POST");
  });

});
