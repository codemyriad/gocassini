import { describe, it, expect } from "vitest";
import { readinessTitle, readinessHealthKey, readinessRows, checkStateLabel, checkTone, formatAge, hpbGuideURL, rowActions, rowGuide, sharedCheckTime, talkRoomURL, talkSettingsURL, testInFlight, toneClasses, reportTone, type ReadinessCheck, type RecordingReadiness } from "./readiness";
import { readSetupHealth } from "./setupHealth";

describe("recording setup", () => {
 it("does not call unverified checks ready", () => {
  const report = { state: "not_verified", checks: [] } as unknown as RecordingReadiness;
  expect(readinessTitle(report)).toBe("Recording setup needs verification");
  report.state = "needs_action";
  report.checks = [{ id: "talk.hpb", state: "needs_action", code: "hpb_missing", message: "missing" }];
  expect(readinessTitle(report)).toBe("One recording check needs attention");
 });
 it("preserves unknown state on older servers", () => {
  expect(readSetupHealth({ok:true,state:"provisioned"})?.recordingState).toBeUndefined();
  expect(readSetupHealth({ok:true,state:"provisioned",recording_state:"needs_action"})?.recordingState).toBe("needs_action");
  expect(readSetupHealth({ok:true,state:"provisioned",recording_state:"warn"})?.recordingState).toBe("warn");
 });
 // Both of the tests this replaces were built on the `processing` row, removed
 // in the 2026-09-25 review — so they had been pinning unreachable branches of
 // readinessTitle ever since.
 it("takes its verdict from the operator and counts rows only for the wording", () => {
  const report = (over: Partial<RecordingReadiness>): RecordingReadiness => ({
   state: "passed", recording_state: "passed", checks: [], secret_configured: true,
   secret_source: "setup", test_room_url: "", test: { state: "idle", published: false }, ...over,
  } as RecordingReadiness);

  expect(readinessTitle(report({}))).toBe("Recording checks passed");
  expect(readinessTitle(report({ recording_state: "warn" }))).toBe("Recording checks need attention");
  expect(readinessTitle(report({ recording_state: "not_verified" }))).toBe("Recording setup needs verification");

  const broken = { id: "storage", state: "needs_action", code: "storage_incomplete", message: "" } as ReadinessCheck;
  expect(readinessTitle(report({ recording_state: "needs_action", checks: [broken] })))
   .toBe("One recording check needs attention");
  expect(readinessTitle(report({ recording_state: "needs_action", checks: [broken, { ...broken, id: "talk.hpb" }] })))
   .toBe("2 recording checks need attention");
 });

 // Archive coverage is excluded from the recording verdict, so a shortfall
 // there is invisible in the heading unless it is named.
 it("names an archive shortfall beside a working recorder", () => {
  const archive = { id: "archive.search", state: "warn", code: "search_coverage_partial", message: "" } as ReadinessCheck;
  const report = {
   state: "warn", recording_state: "passed", checks: [archive], secret_configured: true,
   secret_source: "setup", test_room_url: "", test: { state: "idle", published: false },
  } as RecordingReadiness;
  expect(readinessTitle(report)).toBe("Recording ready; archive search needs attention");
 });
});

it("notifies health changes while ignoring timestamps and job progress", () => {
 const report = { state:"needs_action", secret_configured:true, checks:[{id:"talk.hpb",state:"needs_action",code:"signaling_auth_failed"}] } as RecordingReadiness;
 const before = readinessHealthKey(report);
 report.checks[0].checked_at = "2026-09-11T00:00:00Z";
 expect(readinessHealthKey(report)).toBe(before);
 report.checks[0].state = "passed";
 expect(readinessHealthKey(report)).not.toBe(before);
});

// "Configured" is gone with the row that needed it. There is no longer a check
// whose green means "a value was saved" — the credential lives on the backend
// row, where green means the backend accepted it.
it("has no word for a check that only means something was saved", () => {
  const row = { id: "talk.hpb", state: "passed", code: "hpb_authenticated", message: "" } as ReadinessCheck;
  expect(checkStateLabel(row)).toBe("Passed");
});

// D-763: the checklist reads at a glance.
describe("check severity", () => {
  const check = (over: Partial<ReadinessCheck> = {}): ReadinessCheck => ({
    id: "storage", state: "passed", code: "storage_ready", message: "", ...over,
  });

  it("is green for a check that looked and succeeded", () => {
    expect(checkTone(check())).toBe("success");
  });

  it("is red for a check that blocks recording", () => {
    expect(checkTone(check({ state: "needs_action", code: "storage_incomplete" }))).toBe("error");
  });

  // "Nobody looked" is not "impaired". Folding them together would make one
  // colour mean two different facts. Aged findings show their time separately.
  it("is neutral, not a warning, for a check nobody has run", () => {
    expect(checkTone(check({ state: "not_verified", code: "storage_not_checked" }))).toBe("neutral");
  });

  // These two look like a middle state and are not. "Configured" claims a
  // secret is saved, which is true; whether it works is talk.hpb's job.
  // "Previously confirmed" claims a past playback, which is also true — and
  // playback confirmation is inherently historical, so amber would be its
  // permanent ceiling on a healthy install.
  it("does not invent a middle state for the synthesised rows", () => {
    expect(checkTone(check({ code: "internal_secret_configuration" }))).toBe("success");
    expect(checkTone(check({ code: "test_playback" }))).toBe("success");
  });
});

describe("the instance's worst news", () => {
  const report = (state: ReadinessCheck["state"], checks: ReadinessCheck[] = []): RecordingReadiness => ({
    state, checks, secret_configured: true, secret_source: "env",
    test_room_url: "", test: { state: "idle", published: false, playback_verified_at: "2026-09-01T00:00:00Z" },
  });

  // The operator's verdict, coloured. It used to be a SECOND aggregation over
  // the rendered rows, which was defensible while the panel synthesised rows
  // the operator had not sent; it no longer does.
  it("colours the verdict the operator reached", () => {
    expect(reportTone(report("needs_action"))).toBe("error");
    expect(reportTone(report("warn"))).toBe("warning");
    expect(reportTone(report("not_verified"))).toBe("neutral");
  });

  // A healthy install MUST be able to reach green, or the colour says nothing.
  it("reaches green when everything the list shows has passed", () => {
    expect(reportTone(report("passed"))).toBe("success");
  });

  // The case that forced this: the operator leaves a test nobody ran out of its
  // verdict, because a tool offering evidence on request is not a fault. A panel
  // re-counting the rows painted that same install grey, so the heading
  // contradicted the verdict beside it.
  it("does not re-count a row the operator excluded from its verdict", () => {
    const untested = { id: "test", state: "not_verified", code: "test_not_run", message: "" } as ReadinessCheck;
    expect(reportTone(report("passed", [untested]))).toBe("success");
  });
});

// Freshness travels beside the verdict rather than inside it (D-798 V1).
describe("how old a check is", () => {
  const now = new Date("2026-09-22T12:00:00Z");
  const ago = (ms: number) => new Date(now.getTime() - ms).toISOString();

  it("says just now inside the first minute", () => {
    expect(formatAge(ago(5_000), now)).toBe("just now");
  });

  it("counts minutes, hours and days", () => {
    expect(formatAge(ago(6 * 60_000), now)).toBe("6 minutes ago");
    expect(formatAge(ago(3 * 3_600_000), now)).toBe("3 hours ago");
    expect(formatAge(ago(2 * 86_400_000), now)).toBe("2 days ago");
  });

  it("does not say 'minutes' for one", () => {
    expect(formatAge(ago(60_000), now)).toBe("1 minute ago");
    expect(formatAge(ago(3_600_000), now)).toBe("1 hour ago");
  });

  // A clock skewed forward must not produce "in 3 minutes"; the reader only
  // needs to know the check is current.
  it("does not go negative on a skewed clock", () => {
    expect(formatAge(new Date(now.getTime() + 180_000).toISOString(), now)).toBe("just now");
  });

  it("says nothing it cannot parse", () => {
    expect(formatAge("", now)).toBe("");
    expect(formatAge("not a date", now)).toBe("");
  });
});

// D-798 V2: amber exists now because the host checks can legitimately produce
// one — a model that downloads on first use, a disk getting full.
describe("the warning tone, now that something can produce it", () => {
  const check = (over: Partial<ReadinessCheck> = {}): ReadinessCheck => ({
    id: "host.model.cache", state: "warn", code: "model.cache", message: "", ...over,
  });

  it("is amber for a check that is impaired but working", () => {
    expect(checkTone(check())).toBe("warning");
  });

  it("still distinguishes the other three", () => {
    expect(checkTone(check({ state: "passed" }))).toBe("success");
    expect(checkTone(check({ state: "needs_action" }))).toBe("error");
    expect(checkTone(check({ state: "not_verified" }))).toBe("neutral");
  });

  it("renders amber for a row that is impaired but working", () => {
    expect(toneClasses[checkTone(check())]).toBe("text-warning-strong");
  });
});

// D-798 R0.1: a reader told what is wrong is owed what to do about it.
describe("what to do about a check", () => {
  const check = (over: Partial<ReadinessCheck> = {}): ReadinessCheck => ({
    id: "configuration", state: "needs_action", code: "setup_store_unreadable", message: "", ...over,
  });

  it("carries a repair the operator can perform, and words otherwise", () => {
    // Steps are words only now. A remedy the operator can perform arrives as
    // `repair` and becomes a button, so no step carries a command to copy.
    expect(check({ steps: [{ label: "do a thing" }] }).steps?.[0].label).toBe("do a thing");
    expect(check({ repair: "backfill_search" }).repair).toBe("backfill_search");
  });

  // A row with no remedy offers no button: "Fix this" with nothing behind it is
  // worse than saying nothing.
  it("survives a check from an operator that sends no steps", () => {
    expect(check().steps).toBeUndefined();
    expect(check({ steps: undefined }).steps).toBeUndefined();
  });
});

describe("where to send an administrator", () => {
  it("finds Nextcloud's Talk settings under a subdirectory install", () => {
    // The deployments most likely to be misdirected are the ones least able to
    // work out why the link 404s.
    expect(talkSettingsURL("https://host/index.php/apps/app_api/proxy/gocassini/operator/"))
      .toBe("https://host/settings/admin/talk");
    expect(talkSettingsURL("https://host/nextcloud/index.php/apps/app_api/proxy/gocassini/operator/"))
      .toBe("https://host/nextcloud/settings/admin/talk");
    expect(talkSettingsURL("https://host/apps/app_api/proxy/gocassini/operator/"))
      .toBe("https://host/settings/admin/talk");
  });

  it("offers no link rather than a wrong one", () => {
    expect(talkSettingsURL("https://host/operator/")).toBe("");
    expect(talkSettingsURL("")).toBe("");
  });

  // The operator builds test_room_url from the base IT reaches Nextcloud on,
  // which behind AppAPI is routinely an internal hostname. Correct for the
  // connection probe; unopenable as a link. The token is the operator's, the
  // origin is this page's.
  it("rebuilds the test room link against the origin serving the page", () => {
    expect(talkRoomURL("https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/operator/", "http://reverse-proxy/call/gc6ehz8e"))
      .toBe("https://cloud.example.com/call/gc6ehz8e");
    expect(talkRoomURL("https://host/nextcloud/apps/app_api/proxy/gocassini/operator/", "http://nextcloud/call/abc123"))
      .toBe("https://host/nextcloud/call/abc123");
  });

  it("offers no room link without a token or a base", () => {
    expect(talkRoomURL("https://host/apps/app_api/proxy/gocassini/operator/", "")).toBe("");
    expect(talkRoomURL("https://host/operator/", "http://nextcloud/call/abc123")).toBe("");
  });
});


// The producers are cassini-operator/internal/operator/recording_readiness.go
// and cassini-go-recorder/internal/talk/readiness.go. Keep this list in step
// with them: an action missing a label renders no button at all, which is
// better than a wrong one but still means a row with no way forward.
describe("every action the backend can send has a button", () => {
  const emitted = ["recheck", "configure_talk", "connect_talk", "setup_storage",
    "test_recording", "repair_configuration"];

  it("names each one", () => {
    for (const action of emitted) {
      const row = { id: "talk.hpb", state: "needs_action", code: "x", message: "", action } as ReadinessCheck;
      const names = rowActions(row).map(item => item.label);
      expect(names.length, `${action} produced no button`).toBeGreaterThan(0);
      // "Configure" was the fallback for an unnamed action, and it told a
      // reader nothing about what the button would do.
      expect(names, `${action} is unnamed`).not.toContain("Configure");
    }
  });

  // Rather than a "Configure" that opens whichever drawer the panel falls
  // through to. That fall-through showed the "restore recording-setup.json"
  // text for faults with nothing to do with that file.
  it("turns setup_hpb into the setup guide rather than a button", () => {
    const row = { id: "talk.hpb", state: "needs_action", code: "hpb_missing", message: "", action: "setup_hpb" } as ReadinessCheck;
    expect(rowActions(row)).toEqual([]);
    expect(rowGuide(row)).toEqual({ href: hpbGuideURL, label: "How to set up a High Performance Backend" });
  });

  it("offers nothing for an action it cannot name", () => {
    const row = { id: "storage", state: "needs_action", code: "x", message: "", action: "invent_a_backend" } as unknown as ReadinessCheck;
    expect(rowActions(row)).toEqual([]);
  });
});

// Reported from staging: "I had to reload the doctor panel for it to catch my
// test recording." The panel does not poll — deliberately — but a recording
// under way is the one thing that moves without the reader touching anything.
describe("following a test recording that is under way", () => {
  const now = new Date("2026-10-05T12:00:00Z");
  const report = (test: Partial<RecordingReadiness["test"]>): RecordingReadiness => ({
    state: "passed", checks: [], secret_configured: true, secret_source: "env", test_room_url: "",
    test: { state: "idle", published: false, ...test },
  });

  it("follows a recording Talk is still working on", () => {
    expect(testInFlight(report({ started_at: "2026-10-05T11:58:00Z", state: "waiting_for_talk" }), now)).toBe(true);
    expect(testInFlight(report({ started_at: "2026-10-05T11:58:00Z", state: "running", job_id: "j1", stage: "upload" }), now)).toBe(true);
  });

  // Both of these are waiting on a person, not on the server: play it back, or
  // go and look at why it stopped. Nothing further arrives on its own.
  it("stops once the next move belongs to a person", () => {
    expect(testInFlight(report({ started_at: "2026-10-05T11:58:00Z", state: "succeeded", published: true }), now)).toBe(false);
    expect(testInFlight(report({ started_at: "2026-10-05T11:58:00Z", state: "failed" }), now)).toBe(false);
  });

  it("follows nothing when no test was armed", () => {
    expect(testInFlight(report({}), now)).toBe(false);
    expect(testInFlight(null, now)).toBe(false);
  });

  // A test armed and abandoned must not make every later visit poll for it.
  it("gives up on a test armed long ago", () => {
    expect(testInFlight(report({ started_at: "2026-10-05T11:00:00Z", state: "waiting_for_talk" }), now)).toBe(false);
  });
});

describe("a blocked row offers nothing", () => {
  // The operator strips the row's own action, but the panel's standing actions
  // were added regardless — so a Talk connection waiting on a missing backend
  // still showed Check and Test room, both of which could only fail.
  it("adds no standing action to a check that is waiting", () => {
    const blocked = {
      id: "talk.discovery", state: "not_verified", code: "check_blocked",
      message: "Not checked: this depends on High Performance Backend, which needs attention first.",
    } as ReadinessCheck;
    expect(rowActions(blocked)).toEqual([]);
  });

  it("still offers them on a row that is not blocked", () => {
    const live = { id: "talk.handoff", state: "passed", code: "talk_reachable", message: "" } as ReadinessCheck;
    expect(rowActions(live).map(a => a.action)).toContain("connect_talk");
  });

  // The test room is Cassini's to create. This button asked a reader to paste a
  // room URL, and the tool behind it refused to run until they did — which is
  // why nobody could ever run it.
  it("no longer asks for a test room to be chosen", () => {
    const live = { id: "talk.discovery", state: "passed", code: "talk_reachable", message: "" } as ReadinessCheck;
    expect(rowActions(live).map(a => a.action)).not.toContain("test_room");
  });
});

describe("the guide a row offers", () => {
  const row = (over: Partial<ReadinessCheck> = {}): ReadinessCheck =>
    ({ id: "talk.hpb", state: "needs_action", code: "hpb_disabled", message: "", ...over });

  it("uses the docs the operator sends", () => {
    expect(rowGuide(row({ docs: "https://example.invalid/guide" })))
      .toEqual({ href: "https://example.invalid/guide", label: "How to set up a High Performance Backend" });
  });

  it("names a guide on another row generically", () => {
    expect(rowGuide(row({ id: "storage", docs: "https://example.invalid/guide" }))?.label).toBe("Read Nextcloud's guide");
  });

  it("offers nothing without docs or a setup action", () => {
    expect(rowGuide(row())).toBeNull();
  });

  it("offers nothing on a row waiting for another check", () => {
    expect(rowGuide(row({ code: "check_blocked", docs: "https://example.invalid/guide" }))).toBeNull();
  });
});

describe("the time a run of checks shares", () => {
  const at = (id: string, checked_at?: string, code = "x"): ReadinessCheck =>
    ({ id, state: "passed", code, message: "", checked_at });

  it("treats checks stamped seconds apart as one run, and reports its oldest time", () => {
    const shared = sharedCheckTime([at("host.workdir", "2026-10-06T10:00:01Z"), at("storage", "2026-10-06T10:00:00Z"), at("talk.hpb", "2026-10-06T10:00:09Z")]);
    expect(shared?.checkedAt).toBe("2026-10-06T10:00:00Z");
    expect([...shared?.ids ?? []].sort()).toEqual(["host.workdir", "storage", "talk.hpb"]);
  });

  it("leaves out a row checked on its own later", () => {
    const shared = sharedCheckTime([at("host.workdir", "2026-10-06T10:00:00Z"), at("storage", "2026-10-06T10:00:02Z"), at("talk.hpb", "2026-10-06T10:20:00Z")]);
    expect(shared?.ids.has("talk.hpb")).toBe(false);
    expect(shared?.ids.size).toBe(2);
  });

  it("leaves out the test row, whose time is a person's playback", () => {
    const shared = sharedCheckTime([at("storage", "2026-10-06T10:00:00Z"), at("test", "2026-10-06T10:00:00Z", "test_playback")]);
    expect(shared?.ids.has("test")).toBe(false);
  });

  it("prefers the later run when two are the same size", () => {
    expect(sharedCheckTime([at("a", "2026-10-04T10:00:00Z"), at("b", "2026-10-06T10:00:00Z")])?.checkedAt).toBe("2026-10-06T10:00:00Z");
  });

  it("has nothing to share when nothing was checked", () => {
    expect(sharedCheckTime([at("storage"), at("talk.hpb", "not a time")])).toBeNull();
  });
});
