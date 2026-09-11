import { afterEach, describe, expect, it, vi } from "vitest";

import { OperatorClient, OperatorHttpError } from "./client";

// GET/PUT <base>/storage as the settings section sees it (D-616).

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
      // Both roots are reported for both modes, always — an operator that sends
      // neither degrades to "nobody looked", which must never render as empty.
      root: "",
      archive: { probed: false, present: false, meetings: 0, catalog: false },
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
  // "default" here would make the settings section claim a decision nobody made.
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

  // An operator that predates migration_clean must not make the settings section
  // offer a cleanup, because that cleanup DELETES from a root.
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

describe("OperatorClient overwrite confirmation", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("omits confirmation when the destination was empty", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(READY_STORAGE));
    vi.stubGlobal("fetch", fetchMock);

    await new OperatorClient("/operator").putStorage(true);

    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({
      access_control_enabled: true,
    });
  });

  it("sends the explicit overwrite confirmation", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(READY_STORAGE));
    vi.stubGlobal("fetch", fetchMock);

    await new OperatorClient("/operator").putStorage(false, true);

    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({
      access_control_enabled: false,
      confirm_overwrite: true,
    });
  });

  it("previews the fixed overwrite operation without sending a mode", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(READY_STORAGE));
    vi.stubGlobal("fetch", fetchMock);

    await new OperatorClient("/operator").previewStorageSwitch(true);

    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({
      action: "preview",
      access_control_enabled: true,
    });
  });

  it("carries the destination overwrite list through", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          ...READY_STORAGE,
          preview: {
            mode: "access_controlled",
            ready: true,
            source_root: "CassiniNoACL/Recordings",
            destination_root: "Cassini/Recordings",
            source_readable: true,
            destination_readable: true,
            meetings: 3,
            destination_meetings: 2,
            overwrite_names: ["both.opus", "old.opus", "catalog.json"],
            overwrite_required: true,
            warnings: [],
          },
        }),
      ),
    );

    const status = await new OperatorClient("/operator").previewStorageSwitch(true);

    expect(status.preview?.overwrite_required).toBe(true);
    expect(status.preview?.overwrite_names).toEqual(["both.opus", "old.opus", "catalog.json"]);
  });
});

// --- D-757: the fields "Who can see recordings" reads -----------------------
//
// A block of its own at the end of the file so a branch adding other /storage
// coverage does not collide with this one.

describe("OperatorClient recording access", () => {
  it("carries the first-run flag and a running switch", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          ...READY_STORAGE,
          first_run: true,
          migration: { active: true, phase: "verifying", done: 12, total: 134 },
        }),
      ),
    );

    const status = await new OperatorClient("/operator").getStorage();

    expect(status.first_run).toBe(true);
    expect(status.migration).toEqual({
      active: true,
      phase: "verifying",
      done: 12,
      total: 134,
    });
  });

  // An operator predating these fields has been serving recordings for a while
  // and has no switch in flight it could tell us about. Both absences have one
  // safe reading, and it is this one.
  it("reads both absences as a settled install with nothing running", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(READY_STORAGE)));

    const status = await new OperatorClient("/operator").getStorage();

    expect(status.first_run).toBe(false);
    expect(status.migration).toBeNull();
  });

  // A row that is present but not active means the same thing as no row, so the
  // UI has one test rather than two.
  it("reads a finished migration as no migration", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          ...READY_STORAGE,
          migration: { active: false, phase: "clearing", done: 134, total: 134 },
        }),
      ),
    );

    expect((await new OperatorClient("/operator").getStorage()).migration).toBeNull();
  });

  // The steps are ordered and a switch only moves forward through them, so the
  // earliest phase is the one guess that cannot claim work is finished when it
  // is not.
  it("reads a phase it has never heard of as the first one", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          ...READY_STORAGE,
          migration: { active: true, phase: "reticulating", done: -3, total: 5 },
        }),
      ),
    );

    expect((await new OperatorClient("/operator").getStorage()).migration).toEqual({
      active: true,
      phase: "copying",
      done: 0,
      total: 5,
    });
  });

  it("acknowledges the first-run dialog on the existing storage route", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ ...READY_STORAGE, first_run: false }));
    vi.stubGlobal("fetch", fetchMock);

    const status = await new OperatorClient("/operator").acknowledgeFirstRun();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/operator/storage");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ action: "acknowledge_first_run" });
    expect(status.first_run).toBe(false);
  });
});

// The first-run flag as D-756 reads it (D-757 covers the route call above).
describe("acknowledgeFirstRun (D-756)", () => {
  it("reads an absent flag as answered, never as a fresh install", async () => {
    // An operator predating the flag has been running for however long, and a
    // first-run dialog on top of an install with recordings in it would be a
    // question about something that happened a year ago.
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ ...READY_STORAGE })));

    expect((await new OperatorClient("/operator").getStorage()).first_run).toBe(false);
  });
});
