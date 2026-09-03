import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  buildRunFailureNotice,
  classifyRunError,
  createInsight,
  describeRunProgress,
  isTerminalStatus,
  listInsights,
  pollDelayMs,
  readInsight,
  readRun,
  resolveInsightsUrl,
  retryInsight,
  InsightRequestError,
  MAX_POLL_DELAY_MS,
  type InsightRun,
} from "./client";

// The client's job is the request it builds, the answer it refuses to guess at,
// and the words a reader is given for a run that failed. All three are things a
// wrong version of would be a bug rather than a preference, which is why they
// live here and not in the card.

const PROXY_BASE = "https://cloud.example/index.php/apps/app_api/proxy/gocassini/";

function run(overrides: Partial<InsightRun> = {}): InsightRun {
  return readRun({
    id: "ins_0123456789abcdef",
    status: "queued",
    createdBy: "alice",
    attemptNumber: 1,
    workflowId: "summarise",
    workflowVersion: "v0",
    workflowSha256: "abc",
    meetingIds: ["m1", "m2"],
    roomIds: ["r1"],
    question: "",
    provider: "",
    model: "",
    documentPath: "",
    error: "",
    createdAt: "2026-09-03T10:00:00Z",
    updatedAt: "2026-09-03T10:00:00Z",
    ...overrides,
  });
}

function respondWith(body: string, init?: ResponseInit) {
  return vi.fn(() => Promise.resolve(new Response(body, init)));
}

beforeEach(() => {
  // The embedded page: embedded.ts captures the AppAPI proxy base before mount,
  // and every route this app talks to hangs off it.
  vi.stubGlobal("window", {
    __CASSINI_VIEWER_BASE__: PROXY_BASE,
    location: { href: `${PROXY_BASE}viewer/` },
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("resolveInsightsUrl", () => {
  it("is a sibling of the published archive, not a member of it", () => {
    // `^published\/.+$` is declared GET,HEAD in the manifest. These are the
    // app's first mutating USER routes, so they get their own prefix — and the
    // address is still derived from the archive's, because there must be one
    // answer to "where is the server" on a proxied deployment.
    expect(resolveInsightsUrl()).toBe(`${PROXY_BASE}insights`);
    expect(resolveInsightsUrl("ins_0123456789abcdef")).toBe(
      `${PROXY_BASE}insights/ins_0123456789abcdef`,
    );
    expect(resolveInsightsUrl("ins_0123456789abcdef/retry")).toBe(
      `${PROXY_BASE}insights/ins_0123456789abcdef/retry`,
    );
  });
});

describe("createInsight", () => {
  it("posts the picked ids in pick order", async () => {
    const fetchMock = respondWith(JSON.stringify(run()), { status: 201 });

    await createInsight({ meetingIds: ["b", "a", "c"], workflow: "todos" }, fetchMock);

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(`${PROXY_BASE}insights`);
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({
      meetingIds: ["b", "a", "c"],
      workflow: "todos",
    });
  });

  it("leaves the workflow out when none was chosen", async () => {
    // Absent means "whatever this deployment configured", which is the only
    // thing a non-admin can ask for: the template registry is ADMIN.
    const fetchMock = respondWith(JSON.stringify(run()), { status: 201 });

    await createInsight({ meetingIds: ["a"], workflow: "  ", question: "  " }, fetchMock);

    expect(JSON.parse(String((fetchMock.mock.calls[0] as [string, RequestInit])[1].body))).toEqual({
      meetingIds: ["a"],
    });
  });

  it("sends a typed question", async () => {
    const fetchMock = respondWith(JSON.stringify(run()), { status: 201 });

    await createInsight({ meetingIds: ["a"], question: "  What did we decide?  " }, fetchMock);

    expect(JSON.parse(String((fetchMock.mock.calls[0] as [string, RequestInit])[1].body))).toEqual({
      meetingIds: ["a"],
      question: "What did we decide?",
    });
  });

  it("refuses an empty selection without asking", async () => {
    const fetchMock = respondWith("");

    await expect(createInsight({ meetingIds: [] }, fetchMock)).rejects.toThrow(
      "Pick at least one meeting first.",
    );
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("bypasses the proxy's response cache", async () => {
    // AppAPI caches a proxied GET for an hour, and a run's whole point is that
    // its answer changes while you watch it.
    const fetchMock = respondWith(JSON.stringify(run()), { status: 201 });

    await createInsight({ meetingIds: ["a"] }, fetchMock);

    expect((fetchMock.mock.calls[0] as [string, RequestInit])[1]).toMatchObject({
      cache: "no-store",
    });
  });

  it("repeats what the operator said was wrong with the request", async () => {
    const fetchMock = respondWith(JSON.stringify({ error: "unknown workflow: decisions" }), {
      status: 400,
    });

    await expect(createInsight({ meetingIds: ["a"] }, fetchMock)).rejects.toThrow(
      "unknown workflow: decisions",
    );
  });

  it("says a meeting is unavailable without saying which of the two reasons", async () => {
    const fetchMock = respondWith("404 page not found\n", { status: 404 });

    await expect(createInsight({ meetingIds: ["a"] }, fetchMock)).rejects.toThrow(
      "One of these meetings is not available to you, or this deployment cannot create insights.",
    );
  });

  it("names Nextcloud when the operator could not read it as this user", async () => {
    const fetchMock = respondWith("", { status: 502 });

    await expect(createInsight({ meetingIds: ["a"] }, fetchMock)).rejects.toThrow(
      "Cassini could not read these meetings from Nextcloud.",
    );
  });
});

describe("listInsights", () => {
  it("reads the envelope", async () => {
    const fetchMock = respondWith(JSON.stringify({ insights: [run(), run({ id: "ins_00000000000000ff" })] }));

    await expect(listInsights(fetchMock)).resolves.toHaveLength(2);
  });

  it("does not read an operator that has no insight routes as a caller with no insights", async () => {
    // A caller's own list always exists, so a 404 can only be an install older
    // than these routes. Reading it as "none yet" would hide a whole feature
    // behind an empty shelf.
    const fetchMock = respondWith("404 page not found\n", { status: 404 });

    await expect(listInsights(fetchMock)).rejects.toThrow(
      "This deployment cannot create insights yet.",
    );
  });

  it("does not read a missing list as an empty one", async () => {
    // A deployment whose operator does not serve these routes is not a
    // deployment with no insights, and rendering "none yet" for it would hide
    // the difference.
    const fetchMock = respondWith(JSON.stringify({}));

    await expect(listInsights(fetchMock)).rejects.toThrow(
      "Cassini could not read the list of insights.",
    );
  });
});

describe("readInsight", () => {
  it("carries the document alongside the run", async () => {
    const fetchMock = respondWith(
      JSON.stringify({ ...run({ status: "succeeded" }), document: "# Decisions\n" }),
    );

    await expect(readInsight("ins_0123456789abcdef", fetchMock)).resolves.toMatchObject({
      document: "# Decisions\n",
      run: { status: "succeeded" },
    });
  });

  it("refuses to put something that is not an id in the URL", async () => {
    const fetchMock = respondWith("");

    await expect(readInsight("../../operator/status", fetchMock)).rejects.toThrow(
      "Not an insight id",
    );
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("retryInsight", () => {
  it("reports a race against a running run as its own answer", async () => {
    // The status is the lock: a retry against queued or running is a 409 no-op,
    // which is not a failure — the run is already doing what was asked.
    const fetchMock = respondWith("", { status: 409 });

    await expect(retryInsight("ins_0123456789abcdef", fetchMock)).rejects.toMatchObject({
      status: 409,
      message: "That insight is already running — retrying does nothing until it stops.",
    });
  });

  it("posts to the run's own retry path", async () => {
    const fetchMock = respondWith(JSON.stringify(run({ status: "running", attemptNumber: 2 })));

    await retryInsight("ins_0123456789abcdef", fetchMock);

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(`${PROXY_BASE}insights/ins_0123456789abcdef/retry`);
    expect(init.method).toBe("POST");
  });
});

describe("readRun", () => {
  it("refuses a status this build does not understand", () => {
    // Guessing is worse than saying so either way round: read as terminal, a
    // run still going stops being polled; read as running, a finished one is
    // polled forever.
    expect(() => readRun({ id: "ins_0123456789abcdef", status: "interrupted" })).toThrow(
      InsightRequestError,
    );
  });

  it("refuses a record with no id", () => {
    expect(() => readRun({ status: "queued" })).toThrow(InsightRequestError);
  });
});

describe("the poll schedule", () => {
  it("starts responsive and backs off to a cap", () => {
    // queued -> running happens as soon as the operator picks the run up, so
    // the first question is asked soon; the answer after that changes once in
    // several minutes, so asking every second would be several hundred
    // questions for one answer.
    expect(pollDelayMs(0)).toBe(2_000);
    expect(pollDelayMs(1)).toBeGreaterThan(pollDelayMs(0));
    expect(pollDelayMs(20)).toBe(MAX_POLL_DELAY_MS);
  });

  it("stops on a terminal status and on nothing else", () => {
    expect(isTerminalStatus("succeeded")).toBe(true);
    expect(isTerminalStatus("failed")).toBe(true);
    expect(isTerminalStatus("queued")).toBe(false);
    expect(isTerminalStatus("running")).toBe(false);
  });
});

describe("what a run says while it runs", () => {
  it("names the wait rather than implying a spinner's worth of it", () => {
    // The prototype shows 900ms of "Generating…". A person not told that a
    // model hosted here takes minutes reads a slow run as a broken one.
    expect(describeRunProgress(run({ status: "running" }))).toContain("minutes, not seconds");
    expect(describeRunProgress(run({ status: "queued" }))).toContain("Nothing has been sent");
    expect(describeRunProgress(run({ status: "succeeded" }))).toContain("2 meetings");
  });
});

describe("what a failed run says", () => {
  it("classifies on the operator's reason token, not on its prose", () => {
    // `cassini insight run` maps each reason to an exit code precisely so the
    // classification does not move when someone improves a sentence.
    expect(classifyRunError("insight run failed: no-provider: nothing configured")).toBe(
      "no-provider",
    );
    expect(classifyRunError("provider-refused: 401")).toBe("provider-refused");
    expect(classifyRunError("model-failed: context deadline exceeded")).toBe("model-failed");
    expect(classifyRunError("bad-request: unknown workflow")).toBe("bad-request");
    expect(classifyRunError("the operator fell over")).toBe("unknown");
  });

  it("says what went wrong, and points an administrator at the panel that fixes it", () => {
    const notice = buildRunFailureNotice({
      run: run({ status: "failed", error: "no-provider: no endpoint configured" }),
      isAdmin: true,
    });

    expect(notice?.title).toBe("No AI endpoint is configured");
    expect(notice?.panel).toBe("endpoints");
    expect(notice?.actionLabel).toBe("Open AI providers");
    // The operator's own sentence is carried through rather than replaced: it
    // is the only thing that knows what actually happened.
    expect(notice?.summary).toContain("no-provider: no endpoint configured");
  });

  it("does not promise a retry replays the endpoint that failed", () => {
    // Retry re-resolves provider and model from current settings, which is what
    // makes "add a key" a fix rather than a suggestion.
    const notice = buildRunFailureNotice({
      run: run({ status: "failed", error: "provider-refused: 401 Unauthorized" }),
      isAdmin: true,
    });

    expect(notice?.summary).toContain("as they stand at that moment");
  });

  it("offers a non-admin the fact and never the control", () => {
    // That panel is ADMIN at the proxy and its PUT would 403.
    const notice = buildRunFailureNotice({
      run: run({ status: "failed", error: "no-provider" }),
      isAdmin: false,
    });

    expect(notice?.panel).toBe("");
    expect(notice?.actionLabel).toBe("");
    expect(notice?.summary).toContain("Only a Nextcloud administrator");
  });

  it("sends nobody to AI providers for a request no endpoint would have accepted", () => {
    const notice = buildRunFailureNotice({
      run: run({ status: "failed", error: "bad-request: unknown workflow: decisions" }),
      isAdmin: true,
    });

    expect(notice?.panel).toBe("");
    expect(notice?.summary).toContain("unknown workflow: decisions");
  });

  it("says nothing about a run that has not failed", () => {
    expect(buildRunFailureNotice({ run: run({ status: "running" }), isAdmin: true })).toBeNull();
  });
});
