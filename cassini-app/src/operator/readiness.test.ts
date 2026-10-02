import { describe, it, expect } from "vitest";
import { readinessTitle, readinessHealthKey, readinessRows, checkStateLabel, checkTone, formatAge, rowActions, talkRoomURL, talkSettingsURL, toneClasses, reportTone, type ReadinessCheck, type RecordingReadiness } from "./readiness";
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

it("labels a configured credential as configured, not verified", () => {
 // The operator sends this row now. What stays the panel's is turning the
 // state into a word, and "Configured" rather than "Passed" is the point: a
 // saved secret proves it was saved, not that Talk accepts it.
 const row = { id: "talk.authentication", state: "passed", code: "internal_secret_configuration", message: "" } as ReadinessCheck;
 expect(checkStateLabel(row)).toBe("Configured");
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
    expect(toneClasses[checkTone(check())]).toBe("text-warning");
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
