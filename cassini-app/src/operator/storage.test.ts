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
            source_root: "Cassini (1)/Recordings",
            destination_root: "Cassini/Recordings",
          },
        }),
      ),
    );

    const status = await new OperatorClient("/operator").putStorage(true);

    expect(status.transition).toMatchObject({
      meetings_moved: 3,
      catalog_moved: true,
      source_root: "Cassini (1)/Recordings",
      leftover_source: "",
      unmapped_groups: [],
    });
  });
});
