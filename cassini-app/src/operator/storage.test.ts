import { afterEach, describe, expect, it, vi } from "vitest";

import { OperatorClient, OperatorHttpError } from "./client";

// GET/PUT <base>/storage as the Setup tab sees it (D-616).

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const READY_STORAGE = {
  mode: "default",
  mode_source: "derived",
  ok: true,
  state: "provisioned",
  checked_at: "2026-09-02T08:00:00.000000000Z",
  modes: [
    {
      mode: "default",
      label: "Default",
      active: true,
      available: true,
      summary: "Everyone who can open Cassini can read all of them.",
      consequence: "All of their access rules will be dropped.",
    },
    {
      mode: "access_controlled",
      label: "Access controlled",
      active: false,
      available: false,
      summary: "Each recording is readable only by the people who were in the meeting.",
      consequence: "Every recording already published will be moved.",
      blocker: 'the "groupfolders" app is not enabled',
      step: "app_missing:groupfolders",
      instructions: ["occ app:install groupfolders && occ app:enable groupfolders"],
    },
  ],
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("OperatorClient storage", () => {
  it("reads both modes, their blockers and their instructions", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(READY_STORAGE)));

    const status = await new OperatorClient("/operator").getStorage();

    expect(status.mode).toBe("default");
    expect(status.mode_source).toBe("derived");
    expect(status.modes).toHaveLength(2);
    expect(status.modes[0]).toEqual({
      mode: "default",
      label: "Default",
      active: true,
      available: true,
      summary: "Everyone who can open Cassini can read all of them.",
      consequence: "All of their access rules will be dropped.",
      blocker: "",
      step: "",
      instructions: [],
      setup: [],
    });
    expect(status.modes[1].available).toBe(false);
    expect(status.modes[1].instructions).toEqual([
      "occ app:install groupfolders && occ app:enable groupfolders",
    ]);
    expect(status.transition).toBeNull();
  });

  it("sends the flag the operator's config file uses, so there is one vocabulary", async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      jsonResponse({ ...READY_STORAGE, mode: "access_controlled" }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await new OperatorClient("/operator").putStorage(true);

    expect(fetchMock.mock.calls[0]?.[0]).toBe("/operator/storage");
    const init = fetchMock.mock.calls[0]?.[1];
    expect(init?.method).toBe("PUT");
    expect(JSON.parse(String(init?.body))).toEqual({ access_control_enabled: true });
  });

  it("surfaces the operator's own refusal, which names what is missing", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({ error: 'the target storage mode is not ready: the "groupfolders" app' }, 409),
      ),
    );

    await expect(new OperatorClient("/operator").putStorage(true)).rejects.toMatchObject({
      status: 409,
      message: expect.stringContaining("groupfolders"),
    });
    await expect(new OperatorClient("/operator").putStorage(true)).rejects.toBeInstanceOf(
      OperatorHttpError,
    );
  });

  // An unresolved mode is a third answer, not a missing one. Coercing it to
  // "default" here would make the Setup tab claim a decision nobody made.
  it("keeps an unresolved mode empty rather than defaulting it", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ ok: false, state: "unknown", modes: [] })),
    );

    const status = await new OperatorClient("/operator").getStorage();

    expect(status.mode).toBe("");
    expect(status.modes).toEqual([]);
  });

  // `available` is the one field that decides whether the UI offers to move an
  // entire archive, so it must never be inferred from a missing value.
  it("never invents availability for a mode the server did not call available", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          mode: "default",
          modes: [{ mode: "access_controlled", label: "Access controlled" }],
        }),
      ),
    );

    const status = await new OperatorClient("/operator").getStorage();

    expect(status.modes[0].available).toBe(false);
    expect(status.modes[0].active).toBe(false);
  });

  it("drops a mode row this build does not recognise instead of rendering it", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          mode: "default",
          modes: [{ mode: "some_future_mode", available: true }, { mode: "default" }],
        }),
      ),
    );

    const status = await new OperatorClient("/operator").getStorage();

    expect(status.modes.map((entry) => entry.mode)).toEqual(["default"]);
  });

  it("reports what a completed transition actually moved", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          ...READY_STORAGE,
          mode: "access_controlled",
          transition: {
            mode: "access_controlled",
            meetings_moved: 3,
            catalog_moved: true,
            source_root: "CassiniNoACL/Recordings",
            destination_root: "Cassini/Recordings",
            meetings_replaced: 1,
            source_cleared: true,
          },
        }),
      ),
    );

    const status = await new OperatorClient("/operator").putStorage(true);

    expect(status.transition).toMatchObject({
      meetings_moved: 3,
      catalog_moved: true,
      source_root: "CassiniNoACL/Recordings",
      meetings_replaced: 1,
      source_cleared: true,
      leftover_source: "",
    });
  });

  // `source_cleared` is the difference between "the switch worked" and "the
  // switch worked and there is nothing left to do". An operator that omits it —
  // any build before this one — must not read as a finished tidy-up.
  it("does not read a missing source_cleared as a finished tidy-up", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          ...READY_STORAGE,
          transition: { mode: "default", meetings_moved: 1 },
        }),
      ),
    );

    const status = await new OperatorClient("/operator").putStorage(false);

    expect(status.transition?.source_cleared).toBe(false);
  });

  // An operator that predates migration_clean must not make the Setup tab offer
  // a cleanup, because that cleanup DELETES from a root.
  it("reads an absent migration_clean as a settled instance", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ ...READY_STORAGE })));

    const status = await new OperatorClient("/operator").getStorage();

    expect(status.migration_clean).toBe(true);
    expect(status.pending_cleanup).toBe("");
  });

  it("carries an unfinished migration and the root that holds the leftovers", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          ...READY_STORAGE,
          migration_clean: false,
          pending_cleanup: "Cassini/Recordings",
          stranded_root: "Cassini/Recordings",
          stranded_recordings: 4,
        }),
      ),
    );

    const status = await new OperatorClient("/operator").getStorage();

    expect(status.migration_clean).toBe(false);
    expect(status.pending_cleanup).toBe("Cassini/Recordings");
    expect(status.stranded_recordings).toBe(4);
  });

  it("asks the operator to finish an interrupted migration", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ ...READY_STORAGE }));
    vi.stubGlobal("fetch", fetchMock);

    await new OperatorClient("/operator").finishStorageMigration();

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ action: "finish_migration" });
  });
});

describe("previewStorageSwitch", () => {
  it("asks for a preview of the named mode and does not move anything", async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      jsonResponse({
        mode: "default",
        preview: {
          mode: "access_controlled",
          ready: true,
          source_root: "Cassini (1)/Recordings",
          destination_root: "Cassini/Recordings",
          meetings: 3,
          catalog_present: true,
          destination_meetings: 0,
          nothing_to_move: false,
          source_readable: true,
          warnings: ["all three become readable by every account"],
        },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const status = await new OperatorClient("/operator").previewStorageSwitch(true);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toContain("/storage");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({
      action: "preview",
      access_control_enabled: true,
    });
    expect(status.preview?.meetings).toBe(3);
    expect(status.preview?.source_root).toBe("Cassini (1)/Recordings");
    expect(status.preview?.warnings).toEqual(["all three become readable by every account"]);
  });

  it("keeps a missing preview null rather than inventing an empty one", async () => {
    // "no preview was asked for" and "a preview that found nothing" render
    // differently, and conflating them would let a dialog claim there is
    // nothing to move when nobody has looked.
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ mode: "default" })));

    const status = await new OperatorClient("/operator").getStorage();

    expect(status.preview).toBeNull();
  });

  it("will not turn a nonsense count into a number it would state as fact", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          mode: "default",
          preview: { mode: "access_controlled", meetings: "lots", destination_meetings: -4 },
        }),
      ),
    );

    const status = await new OperatorClient("/operator").previewStorageSwitch(true);

    expect(status.preview?.meetings).toBe(0);
    expect(status.preview?.destination_meetings).toBe(0);
    expect(status.preview?.ready).toBe(false);
    expect(status.preview?.warnings).toEqual([]);
  });
});

// A preview that could not read the source must not arrive as one that found
// nothing. That conflation IS the QA report: a healthy default install with five
// recordings was previewed as "no published recordings to move".
describe("preview readability", () => {
  it("does not read an absent source_readable as a readable source", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          mode: "default",
          preview: {
            mode: "access_controlled",
            ready: true,
            source_root: "CassiniNoACL/Recordings",
            destination_root: "Cassini/Recordings",
            meetings: 0,
            nothing_to_move: false,
          },
        }),
      ),
    );

    const status = await new OperatorClient("/operator").previewStorageSwitch(true);

    expect(status.preview?.source_readable).toBe(false);
    expect(status.preview?.nothing_to_move).toBe(false);
  });

  it("carries the pending cleanup a switch would run first", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          mode: "default",
          preview: {
            mode: "access_controlled",
            ready: true,
            source_root: "CassiniNoACL/Recordings",
            destination_root: "Cassini/Recordings",
            source_readable: true,
            meetings: 5,
            nothing_to_move: false,
            pending_cleanup: "Cassini/Recordings",
          },
        }),
      ),
    );

    const status = await new OperatorClient("/operator").previewStorageSwitch(true);

    expect(status.preview?.pending_cleanup).toBe("Cassini/Recordings");
    expect(status.preview?.meetings).toBe(5);
  });
});
