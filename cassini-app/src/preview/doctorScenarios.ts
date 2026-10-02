import type { OperatorClient } from "../operator/client";
import type { CheckState, ReadinessCheck, RecordingReadiness } from "../operator/readiness";

export const doctorScenarios = [
  { id: "healthy", title: "Healthy recording setup", description: "Passing checks, configured credentials and previously confirmed playback." },
  { id: "first-run", title: "Checks have not run yet", description: "Unknown findings, without claiming a failure." },
  { id: "missing-secret", title: "Missing Talk internal secret", description: "Open Talk authentication to review the form and installation-specific guidance." },
  { id: "rejected-secret", title: "HPB rejects a saved secret", description: "A saved credential is configured, but authentication still fails." },
  { id: "missing-hpb", title: "No high-performance backend", description: "Review backend requirements and guidance for an administrator or provider." },
  { id: "connection-unreachable", title: "Cannot reach Nextcloud", description: "A connection check cannot establish a verdict. Retry stays simulated." },
  { id: "recording-handoff", title: "Recording backend needs connecting", description: "Review the handoff drawer, deployment choices and advanced instructions." },
  { id: "storage-blocked", title: "Recording storage is blocked", description: "Recording cannot start. The storage action explains its destination." },
  { id: "setup-unreadable", title: "Saved configuration is unreadable", description: "Persistent setup needs repair; credentials cannot be confirmed." },
  { id: "search-partial", title: "Archive search is incomplete", description: "Audio is ready. Re-index now simulates progress and completion in this tab." },
  { id: "search-running", title: "Re-indexing is running", description: "Repair progress appears without a duplicate repair button." },
  { id: "search-failed", title: "The last re-index failed", description: "The failed repair stays visible, with a simulated retry available." },
  { id: "old-findings", title: "Passing checks are two days old", description: "Age is shown separately from the verdict; simulated checks refresh timestamps." },
  { id: "refresh-error", title: "Refreshing diagnostics fails", description: "Previously loaded rows become unverified and show the connection error." },
] as const;

type DoctorClient = Pick<OperatorClient, "getReadiness" | "checkReadiness" | "repairReadiness" | "updateRecordingSetup">;
const delay = (ms: number) => new Promise<void>(resolve => setTimeout(resolve, ms));
const rank: Record<CheckState, number> = { passed: 0, not_verified: 1, warn: 2, needs_action: 3 };
function verdict(checks: ReadinessCheck[]): CheckState {
  return checks.reduce<CheckState>((state, check) => rank[check.state] > rank[state] ? check.state : state, "passed");
}
function updateVerdict(report: RecordingReadiness): void {
  // Mirrors the operator: the test row is evidence offered on request, so its
  // absence must not leave a passing preview reading as unverified.
  report.state = verdict(report.checks.filter(check => check.id !== "test" || check.state === "needs_action"));
  report.recording_state = verdict(report.checks.filter(check => check.id !== "processing" && !check.id.startsWith("archive.")));
}

export function scenarioReport(id: string, now = new Date()): RecordingReadiness {
  if (!doctorScenarios.some(item => item.id === id)) throw new Error(`Unknown Doctor preview: ${id}`);
  const checked_at = new Date(now.getTime() - (id === "old-findings" ? 172800000 : 120000)).toISOString();
  const report: RecordingReadiness = {
    state: "passed", recording_state: "passed", secret_configured: true, secret_source: "setup",
    test_room_url: "https://preview.invalid/call/review-room",
    test: { state: "succeeded", published: true, job_id: "preview-recording", playback_verified_at: checked_at, viewer_url: "https://preview.invalid/recording" },
    checks: [
      { id: "host.workdir", state: "passed", code: "workdir", message: "working directory is writable", checked_at },
      { id: "host.tmpdir.writable", state: "passed", code: "tmpdir.writable", message: "temporary directory is writable", checked_at },
      { id: "storage", state: "passed", code: "storage_ready", message: "Cassini can store and share recordings in Nextcloud: its own account exists, its recordings folder is writable, and Nextcloud's sharing API answers.", checked_at },
      { id: "talk.hpb", state: "passed", code: "hpb_authenticated", message: "The signaling server accepted Cassini and advertises media support. A test recording verifies the actual call path.", checked_at },
      { id: "talk.discovery", state: "passed", code: "recording_auth_verified", message: "Talk accepted Cassini's recording credential.", checked_at },
      { id: "archive.search", state: "passed", code: "search_archive_files_accounted_for", message: "The search index records 12 indexed meeting(s). Every Opus file in the checked archive listing has an index outcome.", checked_at },
    ],
  };
  const set = (check: ReadinessCheck) => {
    const at = report.checks.findIndex(row => row.id === check.id);
    if (at >= 0) report.checks[at] = check;
    else if (check.id.startsWith("host.") || check.id === "configuration") {
      const firstOther = report.checks.findIndex(row => !row.id.startsWith("host."));
      report.checks.splice(firstOther < 0 ? report.checks.length : firstOther, 0, check);
    } else report.checks.push(check);
  };
  // Only where the chain it exercises is intact. The operator suppresses this
  // row behind an unmet prerequisite, and these fixtures do not model
  // suppression — so a row left in the shared base would preview as a green
  // "test passed" beside a backend that needs attention.
  if (id === "healthy" || id === "old-findings") {
    report.checks.push({ id: "test", state: "passed", code: "test_playback", message: "A recording started in Talk was published, and its audio was confirmed by playing it.", checked_at });
  }
  switch (id) {
    case "first-run":
      report.checks = [
        { id: "host", state: "not_verified", code: "host_not_checked", message: "The recording host has not been checked yet.", action: "recheck" },
        { id: "storage", state: "not_verified", code: "storage_not_checked", message: "Nextcloud storage has not been checked yet. Check again to run it.", action: "recheck" },
        { id: "talk.discovery", state: "not_verified", code: "connection_not_checked", message: "The Talk connection has not been checked yet. Check again to run it.", action: "recheck" },
        { id: "archive.search", state: "not_verified", code: "search_coverage_not_checked", message: "Archive search coverage has not been checked yet.", action: "recheck" },
      ];
      report.test_room_url = "";
      report.test = { state: "idle", published: false };
      report.checks.push({ id: "test", state: "not_verified", code: "test_not_run", message: "Nothing has been recorded through Talk yet. A short test recording is what proves the whole path, from a call to audio you can play.", action: "test_recording" });
      break;
    case "missing-secret":
      report.secret_configured = false; report.secret_source = "unset";
      set({ id: "talk.authentication", state: "needs_action", code: "internal_secret_missing", message: "Enter the internal secret from your Talk signaling server.", action: "configure_talk" });
      set({ id: "talk.hpb", state: "not_verified", code: "internal_secret_missing", message: "Talk has standalone signaling configured. Supply its internal client secret to verify HPB authentication.", action: "configure_talk", checked_at });
      break;
    case "rejected-secret":
      set({ id: "talk.hpb", state: "needs_action", code: "signaling_auth_failed", message: "HPB rejected internal-client authentication. Check the internal secret and server authentication configuration.", action: "configure_talk", checked_at }); break;
    case "missing-hpb":
      set({ id: "talk.hpb", state: "needs_action", code: "hpb_missing", message: "Talk has no standalone signaling server configured. Enable its high-performance backend.", action: "setup_hpb", checked_at }); break;
    case "connection-unreachable":
      set({ id: "talk.discovery", state: "not_verified", code: "nextcloud_unreachable", message: "Could not read Talk settings. Check Nextcloud connectivity and TLS, then try again.", action: "recheck", checked_at });
      report.checks = report.checks.filter(check => check.id !== "talk.hpb"); break;
    case "recording-handoff":
      set({ id: "talk.handoff", state: "needs_action", code: "recording_secret_missing", message: "Cassini could not provision its recording credential. Check its persistent storage.", action: "connect_talk" });
      set({ id: "talk.discovery", state: "needs_action", code: "recording_secret_missing", message: "Configure Cassini's recording credential and connect Talk first.", action: "connect_talk", checked_at });
      report.checks = report.checks.filter(check => check.id !== "talk.hpb"); break;
    case "storage-blocked":
      set({ id: "storage", state: "needs_action", code: "storage_admission_blocked", message: "Cassini currently blocks recording on its stored storage status. Review the storage details below and check again after repairing them.", action: "setup_storage", checked_at }); break;
    case "setup-unreadable":
      set({ id: "configuration", state: "needs_action", code: "setup_store_unreadable", message: "Cassini could not read its saved recording setup, so its Talk credentials cannot be confirmed.", action: "repair_configuration", steps: [
        { label: "Check that Cassini's persistent volume is mounted and writable, then restore recording-setup.json from a backup if it is missing" },
        { label: "Disable and re-enable Cassini in Nextcloud, which re-runs its setup" },
      ] }); break;
    case "search-partial": case "search-running": case "search-failed":
      set({ id: "archive.search", state: "warn", code: "search_coverage_partial", message: "The search index records 9 indexed meeting(s); 3 archive Opus recordings without index rows. The checked archive has recordings outside search coverage." +
        (id === "search-running" ? " Re-indexing is running now." : id === "search-failed" ? " The last re-index did not finish: the archive could not be read." : ""),
        action: "recheck", repair: id === "search-running" ? undefined : "backfill_search", steps: [{ label: "Re-index the 3 recording(s) with no index row or an unverified bundle" }], checked_at }); break;
  }
  updateVerdict(report);
  return report;
}

// Deliberately implements only Doctor's four calls, with no fetch, credentials,
// operator URL or persistence. New tabs and reloads always get a fresh fixture.
export function createPreviewClient(id: string): DoctorClient {
  let report = scenarioReport(id);
  let repairStarted = 0;
  let initialCheck = true;
  let readCount = 0;
  const read = () => {
    if (repairStarted && Date.now() - repairStarted >= 4000) {
      const healthy = scenarioReport("healthy");
      report.checks = report.checks.map(check => check.id === "archive.search" ? {
        ...healthy.checks.find(row => row.id === "archive.search")!, checked_at: new Date().toISOString(),
        message: "The search index records 12 indexed meeting(s). Every Opus file in the checked archive listing has an index outcome. Last re-index: 3 indexed, 9 unchanged, 0 not searchable, 0 failed.",
      } : check);
      updateVerdict(report); repairStarted = 0;
    }
    return structuredClone(report);
  };
  return {
    async getReadiness() {
      if (id === "refresh-error" && readCount++ > 0) throw new Error("Preview: could not refresh recording checks. Check the connection and try again.");
      return read();
    },
    async checkReadiness(only) {
      await delay(650);
      if (id === "refresh-error") throw new Error("Preview: could not refresh recording checks. Check the connection and try again.");
      // The fault remains until a simulated edit or repair fixes it. Rechecking
      // updates freshness, without making an intentionally broken case healthy.
      if (id !== "old-findings" || !initialCheck) for (const check of report.checks) {
        if ((!only || only.includes(check.id)) && check.checked_at) check.checked_at = new Date().toISOString();
      }
      initialCheck = false;
      return read();
    },
    async repairReadiness(action) {
      if (action !== "backfill_search") throw new Error("This preview does not support that repair.");
      const check = report.checks.find(row => row.id === "archive.search");
      if (check && check.repair) {
        delete check.repair;
        check.message = "The search index records 9 indexed meeting(s); 3 archive Opus recordings without index rows. The checked archive has recordings outside search coverage. Re-indexing is running now.";
        repairStarted = Date.now();
      }
      return read();
    },
    async updateRecordingSetup(payload) {
      await delay(250);
      if (payload.internal_secret?.trim()) {
        report.secret_configured = true; report.secret_source = "setup";
        report.checks = report.checks.filter(check => check.id !== "talk.authentication");
        const healthy = scenarioReport("healthy");
        report.checks = report.checks.map(check => check.id === "talk.hpb" ? healthy.checks.find(row => row.id === "talk.hpb")! : check);
      }
      if (payload.test_room_url !== undefined) {
        report.test_room_url = "https://preview.invalid/call/review-room";
        const check = report.checks.find(row => row.id === "talk.discovery");
        if (check) Object.assign(check, { state: "not_verified", code: "connection_not_checked", message: "The Talk connection has not been checked yet. Check again to run it.", action: "recheck" });
      }
      if (payload.action === "arm_test") report.test = { state: "waiting_for_talk", published: false, started_at: new Date().toISOString() };
      if (payload.action === "confirm_playback") report.test.playback_verified_at = new Date().toISOString();
      updateVerdict(report); return read();
    },
  };
}
