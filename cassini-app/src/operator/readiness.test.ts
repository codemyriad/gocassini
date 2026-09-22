import { describe, it, expect } from "vitest";
import { readinessTitle, readinessHealthKey, readinessRows, checkStateLabel, checkTone, formatAge, reportTone, type ReadinessCheck, type RecordingReadiness } from "./readiness";
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

it("distinguishes saved credentials and historical playback from current verification", () => {
 const report = {secret_configured:true, secret_source:"setup", checks:[{id:"talk.hpb",state:"needs_action",code:"signaling_auth_failed",message:"Authentication rejected"}], test:{playback_verified_at:"2026-09-11T00:00:00Z"}} as RecordingReadiness;
 const rows = readinessRows(report);
 expect(checkStateLabel(rows.find(c=>c.id==="talk.authentication")!)).toBe("Configured");
 const test = rows.find(c=>c.id==="test")!;
 expect(checkStateLabel(test)).toBe("Previously confirmed");
 expect(test.checked_at).toBe(report.test.playback_verified_at);
 report.checks.push({id:"configuration",state:"needs_action",code:"setup_store_unreadable",message:"Unreadable"});
 expect(readinessRows(report).some(c=>c.id==="talk.authentication")).toBe(false);
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
  // colour mean two different facts, and the operator already downgrades
  // expired evidence to not_verified rather than to a weaker pass.
  it("is neutral, not a warning, for a check nobody has run", () => {
    expect(checkTone(check({ state: "not_verified", code: "storage_check_expired" }))).toBe("neutral");
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
  const report = (checks: ReadinessCheck[]): RecordingReadiness => ({
    state: "passed", checks, secret_configured: true, secret_source: "env",
    test_room_url: "", test: { state: "idle", published: false, playback_verified_at: "2026-09-01T00:00:00Z" },
  });
  const broken = { id: "storage", state: "needs_action", code: "storage_incomplete", message: "" } as ReadinessCheck;
  const unchecked = { id: "talk.discovery", state: "not_verified", code: "connection_not_verified", message: "" } as ReadinessCheck;

  it("reports broken over unchecked", () => {
    expect(reportTone(report([broken, unchecked]))).toBe("error");
    expect(reportTone(report([unchecked]))).toBe("neutral");
  });

  // A healthy install MUST be able to reach green, or the colour says nothing.
  it("reaches green when everything the list shows has passed", () => {
    expect(reportTone(report([]))).toBe("success");
  });

  // The header reads the rows the list renders, including the ones
  // readinessRows synthesises — otherwise it can disagree with what is under it.
  it("counts a synthesised row that has not been verified", () => {
    const noPlayback = { ...report([]), test: { state: "idle", published: false } };
    expect(reportTone(noPlayback)).toBe("neutral");
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
