import { describe, it, expect } from "vitest";
import { readinessTitle, readinessHealthKey, readinessRows, checkStateLabel, type RecordingReadiness } from "./readiness";
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
