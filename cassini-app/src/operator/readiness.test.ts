import { describe, it, expect } from "vitest";
import { handoffScript, readinessTitle, readinessHealthKey, type RecordingReadiness } from "./readiness";
import { execFileSync } from "node:child_process";
import { readSetupHealth } from "./setupHealth";

describe("recording setup", () => {
 it("does not call unverified checks ready", () => {
  const report = { state: "not_verified", checks: [] } as unknown as RecordingReadiness;
  expect(readinessTitle(report)).toBe("Recording setup needs verification");
  report.state = "needs_action";
  report.checks = [{ id: "talk.hpb", state: "needs_action", code: "hpb_missing", message: "missing" }];
  expect(readinessTitle(report)).toBe("Recording needs one more step");
 });
 it("quotes URLs without executing shell substitutions", () => {
  const script = handoffScript("https://cloud.test/a'$(touch /tmp/do-not-create)`id`/provisioning", true);
  expect(() => execFileSync("bash", ["-n"], { input: script })).not.toThrow();
  expect(script).toContain("'\\''");
  expect(script).toContain("set -euo pipefail");
  expect(script).toContain("umask 077");
  expect(script).toContain("mktemp");
  expect(script).toContain("recording_servers --default-value=''");
  expect(script).toContain("docker exec -u www-data nextcloud-aio-nextcloud php occ");
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
