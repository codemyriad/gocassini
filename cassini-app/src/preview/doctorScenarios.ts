// Example data for the Doctor gallery: the real panel rendered against reports
// the operator could send. `npm run design -w cassini-app` opens it.
//
// Hand-written, so it has to be kept in step with what produces it — the
// operator's recording_readiness.go, recording_test_tool.go and
// search_readiness.go, and the recorder's talk/readiness.go. Change a message,
// a code or an action there and change it here in the same commit: the tests
// pin the shape (row order, no removed rows, every action named, checkable and
// probe agreeing), not the wording.
import type { OperatorClient } from "../operator/client";
import { checkLabels, type CheckState, type ReadinessCheck, type RecordingReadiness } from "../operator/readiness";

export const doctorScenarios = [
  { id: "healthy", title: "Healthy recording setup", description: "Every check passing, a saved credential, and a test recording someone played back." },
  { id: "first-run", title: "Checks have not run yet", description: "Nothing established, and nothing claimed: the state a freshly installed Cassini reports." },
  { id: "no-backend", title: "No signaling backend (the common one)", description: "One row to act on. The connection and the test say what they are waiting for and offer nothing." },
  { id: "missing-secret", title: "Missing Talk internal secret", description: "The backend row names the two places the value can be read, and opens the form." },
  { id: "rejected-secret", title: "HPB rejects a saved secret", description: "A credential is saved and the backend refuses it, on the one row that reports both." },
  { id: "missing-hpb", title: "Talk has no signaling server", description: "The backend row points at Nextcloud's own documentation and settings." },
  { id: "connection-unreachable", title: "Cannot reach Nextcloud", description: "A check that ran and failed to reach the server: a warning, not an absence." },
  { id: "recording-handoff", title: "Recording backend needs connecting", description: "Talk does not know Cassini as a recording backend yet." },
  { id: "secret-missing", title: "Cassini has no recording secret", description: "Cassini could not create its own recording secret, so there is nothing to give Talk yet. One row to fix, on Cassini's side." },
  { id: "test-waiting", title: "Test recording: waiting for Talk", description: "The test is armed and waiting for somebody to press record in Talk." },
  { id: "test-published", title: "Test recording: confirm playback", description: "The recording published. Only a person pressing play can finish this check." },
  { id: "test-failed", title: "Test recording failed", description: "The one check whose failure is a finding rather than a missing verdict." },
  { id: "storage-blocked", title: "Recording storage is blocked", description: "Recording cannot start. The storage action explains its destination." },
  { id: "setup-unreadable", title: "Saved configuration is unreadable", description: "Persistent setup needs repair; credentials cannot be confirmed." },
  { id: "search-partial", title: "Archive search is incomplete", description: "Audio is ready. Re-index now simulates progress and completion in this tab." },
  { id: "search-running", title: "Re-indexing is running", description: "Repair progress appears without a duplicate repair button." },
  { id: "search-failed", title: "The last re-index failed", description: "Nextcloud could not list the archive. The row says so, points at Recording storage, and offers to try again." },
  { id: "search-failed-index", title: "Re-index failed: search index unavailable", description: "A retry would fail the same way, so the row offers steps, not a button." },
  { id: "search-failed-environment", title: "Re-index failed: AppAPI settings missing", description: "Cassini is running without the settings AppAPI gives it. No retry, a deployment step instead." },
  { id: "old-findings", title: "Passing checks are two days old", description: "Age is shown separately from the verdict; simulated checks refresh timestamps." },
  { id: "refresh-error", title: "Refreshing diagnostics fails", description: "Cassini stops answering after the first read. The callout says why and offers to try again, and the rows keep their last results." },
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
  // The operator's recordingCapabilityState drops the test row outright: a test
  // nobody ran says nothing about whether the install can record.
  report.recording_state = verdict(report.checks.filter(check =>
    check.id !== "processing" && check.id !== "test" && !check.id.startsWith("archive.")));
}

// The order the operator sends rows in — readinessRowOrder in
// cassini-operator/internal/operator/recording_readiness.go.
//
// Declared here because these fixtures are hand-written arrays, and every one
// of them had drifted: `test` sat below archive search, `configuration` below
// the host rows, and a row added by `set` landed wherever push happened to put
// it. The panel renders what it is given, so a designer judging row order in
// the gallery was judging the gallery's mistakes. Sorting once beats ordering
// eighteen arrays by hand.
const rowOrder = [
  "configuration",
  "host", "host.workdir", "host.tmpdir.writable",
  "storage",
  "talk.hpb",
  "talk.discovery",
  "talk.handoff",
  "test",
  "archive.search",
];
function sortRows(checks: ReadinessCheck[]): ReadinessCheck[] {
  // Stable, and an unknown id keeps its place at the end rather than vanishing
  // — same contract as the operator's sort.
  return checks
    .map((check, index) => ({ check, index, rank: rowOrder.indexOf(check.id) }))
    .sort((a, b) => (a.rank < 0 ? rowOrder.length : a.rank) - (b.rank < 0 ? rowOrder.length : b.rank) || a.index - b.index)
    .map(item => item.check);
}

// The backend row the operator supplies when no probe has established one. It
// is on EVERY report — it decides whether recording can work at all — so a
// fixture that drops it shows a checklist the product never sends.
function uncheckedBackend(): ReadinessCheck {
  return { id: "talk.hpb", state: "not_verified", code: "hpb_not_checked", action: "recheck",
    message: "Whether Talk has a High Performance Backend has not been established yet, and Cassini can only record through one." };
}

// A row waiting on another check, exactly as the operator rewrites one: no
// action, no steps, and a sentence naming what it waits for. Written as data
// because these fixtures do not run the operator's suppression — they show its
// OUTPUT, which is what a designer is laying out.
function blocked(id: string, blocker: string, unchecked = false): ReadinessCheck {
  return { id, state: "not_verified", code: "check_blocked",
    message: unchecked
      ? `Not checked: this depends on ${blocker}, which has not been checked yet. Run all checks first.`
      : `Not checked: this depends on ${blocker}, which needs attention first.` };
}

export function scenarioReport(id: string, now = new Date()): RecordingReadiness {
  if (!doctorScenarios.some(item => item.id === id)) throw new Error(`Unknown Doctor preview: ${id}`);
  const checked_at = new Date(now.getTime() - (id === "old-findings" ? 172800000 : 120000)).toISOString();
  const report: RecordingReadiness = {
    state: "passed", recording_state: "passed", secret_configured: true, secret_source: "setup",
    test_room_url: "https://preview.invalid/call/review-room",
    // The operator sends this per request: the room is the reader's only when
    // they armed it. The gallery's reader is that administrator, so the test
    // row offers "Open test room" as it does for them.
    test_room_mine: true,
    test: { state: "not_started", published: false },
    checks: [
      { id: "host.workdir", state: "passed", code: "workdir", message: "working directory is writable", checked_at },
      { id: "host.tmpdir.writable", state: "passed", code: "tmpdir.writable", message: "temporary directory is writable", checked_at },
      { id: "storage", state: "passed", code: "storage_ready", message: "Cassini can store and share recordings in Nextcloud: its own account exists, its recordings folder is writable, and Nextcloud's sharing API answers.", checked_at },
      // Keeps its credential action even while passing: the form has to stay
      // reachable, or the secret can never be rotated from the panel again.
      { id: "talk.hpb", state: "passed", code: "hpb_authenticated", message: "The signaling server accepted Cassini and advertises media support. A test recording verifies the actual call path.", action: "configure_talk", checked_at },
      { id: "talk.discovery", state: "passed", code: "recording_auth_verified", message: "Talk accepted Cassini's recording credential.", checked_at },
      { id: "archive.search", state: "passed", code: "search_archive_files_accounted_for", message: "12 meetings are searchable. Every recording in the archive is accounted for.", checked_at },
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
    report.test = { state: "succeeded", published: true, job_id: "preview-recording", playback_verified_at: checked_at, viewer_url: "https://preview.invalid/recording" };
    report.checks.push({ id: "test", state: "passed", code: "test_playback", message: "A recording started in Talk was published, and its audio was confirmed by playing it.", action: "test_recording", checked_at });
  }
  switch (id) {
    case "first-run":
      report.checks = [
        // The SAME two rows a check produces, unchecked — not one placeholder
        // standing in for them, which is how a row appeared to rename itself on
        // the first check.
        { id: "host.workdir", state: "not_verified", code: "host_not_checked", message: "Not checked yet.", action: "recheck" },
        { id: "host.tmpdir.writable", state: "not_verified", code: "host_not_checked", message: "Not checked yet.", action: "recheck" },
        { id: "storage", state: "not_verified", code: "storage_not_checked", message: "Nextcloud storage has not been checked yet.", action: "recheck" },
        uncheckedBackend(),
        { id: "talk.discovery", state: "not_verified", code: "connection_not_checked", message: "The Talk connection has not been checked yet.", action: "recheck" },
        { id: "archive.search", state: "not_verified", code: "search_coverage_not_checked", message: "Archive search coverage has not been checked yet.", action: "recheck" },
      ];
      report.test_room_url = "";
      report.test_room_mine = false;
      report.test = { state: "idle", published: false };
      // No test row written here: nothing it depends on has been established,
      // so the rule below blocks it — which is what the operator does, and for
      // the same reason. A test offered here could only fail, for a reason
      // nobody has looked for yet.
      break;
    case "no-backend":
      // What every installation without a High Performance Backend reports, and
      // the layout most worth getting right: one row carries the fault and the
      // three below it carry a sentence each.
      report.secret_configured = false; report.secret_source = "unset";
      set({ id: "talk.hpb", state: "needs_action", code: "hpb_disabled", message: "Recording cannot work until Talk has a High Performance Backend. Cassini records by joining the call as a hidden participant, and Talk only allows that through standalone signaling — here it is signalling by itself. Nothing else about Talk is affected.", docs: "https://nextcloud-talk.readthedocs.io/en/stable/quick-install/", checked_at });
      set(blocked("talk.discovery", "High Performance Backend"));
      report.checks.push(blocked("test", "High Performance Backend"));
      break;
    case "missing-secret":
      report.secret_configured = false; report.secret_source = "unset";
      // One row, because it is one fact: the backend exists and Cassini has no
      // credential for it.
      set({ id: "talk.hpb", state: "needs_action", code: "internal_secret_missing", message: "Talk has a High Performance Backend, and Cassini needs that server's internal secret to join calls invisibly. This is not a Nextcloud setting: it belongs to the signaling server, which is why Cassini cannot read it for you.", action: "configure_talk", checked_at, steps: [
        { label: "On Nextcloud All-in-One, print it with:", commands: ["docker exec nextcloud-aio-talk printenv INTERNAL_SECRET"] },
        { label: "On a standalone signaling server, it is `internalsecret` under `[clients]` in its configuration file" },
        { label: "Paste it unchanged — one differing character fails exactly as a wrong credential would, and nothing can tell the difference until this check runs" },
      ] });
      set(blocked("talk.discovery", "High Performance Backend"));
      break;
    case "rejected-secret":
      set({ id: "talk.hpb", state: "needs_action", code: "signaling_auth_failed", message: "HPB rejected internal-client authentication. Check the internal secret and server authentication configuration.", action: "configure_talk", checked_at }); break;
    case "missing-hpb":
      set({ id: "talk.hpb", state: "needs_action", code: "hpb_missing", message: "Talk has no standalone signaling server configured. Enable its high-performance backend.", action: "setup_hpb", checked_at }); break;
    case "connection-unreachable":
      set({ id: "talk.discovery", state: "warn", code: "nextcloud_host_not_found", message: "Cassini could not find Nextcloud at the address it was given: the name does not resolve from Cassini's container.", action: "recheck", checked_at, steps: [
        { label: "Cassini reaches Nextcloud at `NEXTCLOUD_URL`, or at `CASSINI_TALK_BACKEND_URL` when that is set. The address has to work from inside Cassini's container, not only from your browser" },
        { label: "To test the address, run this on the Nextcloud host. It should print an HTTP status such as 200, not an error:", commands: [`docker exec nc_app_gocassini sh -lc 'curl -k -s -o /dev/null -w "%{http_code}\\n" "\${CASSINI_TALK_BACKEND_URL:-$NEXTCLOUD_URL}/status.php"'`] },
      ] });
      set(blocked("talk.hpb", "Talk connection"));
      set(blocked("test", "Talk connection"));
      break;
    case "recording-handoff":
      set({ id: "talk.discovery", state: "needs_action", code: "recording_auth_rejected", message: "Talk refused Cassini. Cassini is not set up as Talk's recording backend yet, or the recording secret Talk has does not match Cassini's.", action: "connect_talk", checked_at });
      set(blocked("talk.hpb", "Talk connection"));
      break;
    case "secret-missing":
      set({ id: "talk.handoff", state: "needs_action", code: "recording_secret_missing", message: "Cassini has no recording secret. It creates one when it starts and keeps it on its data volume, and that did not succeed.", steps: [
        { label: "Check that Cassini's data volume is mounted and writable: the secret is saved next to its database" },
        { label: "Then restart Cassini by disabling and re-enabling it in Nextcloud's apps, which creates the secret again" },
        { label: "Or set one yourself in `CASSINI_TALK_RECORDING_SECRET`, in Cassini's deploy options. A secret set there always wins" },
      ] });
      set(blocked("talk.discovery", "Recording credential"));
      set(blocked("talk.hpb", "Recording credential"));
      set(blocked("test", "Recording credential"));
      break;
    case "storage-blocked":
      set({ id: "storage", state: "needs_action", code: "storage_admission_blocked", message: "Cassini currently blocks recording on its stored storage status. Review the storage details below and check again after repairing them.", action: "setup_storage", checked_at }); break;
    case "setup-unreadable":
      set({ id: "configuration", state: "needs_action", code: "setup_store_unreadable", message: "Cassini could not read its saved recording setup, so its Talk credentials cannot be confirmed.", action: "repair_configuration", steps: [
        { label: "Check that Cassini's persistent volume is mounted and writable, then restore recording-setup.json from a backup if it is missing" },
        { label: "Disable and re-enable Cassini in Nextcloud, which re-runs its setup" },
      ] }); break;
    case "test-waiting":
      report.test = { state: "waiting_for_talk", published: false, started_at: new Date(now.getTime() - 90000).toISOString() };
      set({ id: "test", state: "not_verified", code: "test_in_progress", message: "Cassini is waiting for a recording started in the test room.", action: "test_recording" });
      break;
    case "test-published":
      report.test = { state: "succeeded", published: true, job_id: "preview-recording", started_at: new Date(now.getTime() - 600000).toISOString(), viewer_url: "https://preview.invalid/recording" };
      set({ id: "test", state: "not_verified", code: "test_awaiting_playback", message: "The test recording was published. Playing it is what confirms the audio arrived.", action: "test_recording" });
      break;
    case "test-failed":
      report.test = { state: "failed", published: false, job_id: "preview-recording", stage: "upload", started_at: new Date(now.getTime() - 600000).toISOString() };
      set({ id: "test", state: "needs_action", code: "test_failed", message: "The test recording did not finish; it stopped at upload.", action: "test_recording" });
      break;
    case "search-partial": case "search-running":
      set({ id: "archive.search", state: "warn", code: "search_coverage_partial", message: "9 meetings are searchable. Of the others, 3 are in the archive but not indexed yet." +
        (id === "search-running" ? " Re-indexing is running now." : ""),
        action: "recheck", repair: id === "search-running" ? undefined : "backfill_search", running: id === "search-running" || undefined,
        steps: id === "search-running" ? undefined : [{ label: "Re-index now adds the 3 recordings that are not in search yet" }], checked_at }); break;
    case "search-failed":
      set({ id: "archive.search", state: "warn", code: "search_reindex_archive_unreadable", message: "9 meetings are searchable. Of the others, 3 are in the archive but not indexed yet. The last re-index could not list the recordings archive in Nextcloud.",
        action: "recheck", repair: "backfill_search", repair_failed: true, steps: [
          { label: "Recording storage uses the same access to the archive. If it needs attention, fix that first" },
          { label: "If it passes, Nextcloud was probably busy or restarting, so try re-indexing again" },
          { label: "The full error is in Cassini's log. On the Docker host:", commands: ["docker logs nc_app_gocassini 2>&1 | grep 'search backfill'"] },
        ], checked_at }); break;
    case "search-failed-environment":
      set({ id: "archive.search", state: "warn", code: "search_reindex_environment", message: "9 meetings are searchable. Of the others, 3 are in the archive but not indexed yet. The last re-index could not read the settings AppAPI gives Cassini, so trying again would fail the same way.",
        action: "recheck", repair_failed: true, steps: [
          { label: "Cassini is running without its AppAPI settings (`NEXTCLOUD_URL`, `APP_SECRET`, `APP_ID`). Deploy it through AppAPI rather than starting its container by hand" },
          { label: "The full error is in Cassini's log. On the Docker host:", commands: ["docker logs nc_app_gocassini 2>&1 | grep 'search backfill'"] },
        ], checked_at }); break;
    case "search-failed-index":
      set({ id: "archive.search", state: "warn", code: "search_reindex_index_unavailable", message: "9 meetings are searchable. Of the others, 3 are in the archive but not indexed yet. The last re-index could not use the search index, so trying again would fail the same way.",
        action: "recheck", repair_failed: true, steps: [
          { label: "Check that Cassini's data volume is mounted and writable: the search index is kept on it" },
          { label: "Then restart Cassini by disabling and re-enabling it in Nextcloud's apps" },
          { label: "The full error is in Cassini's log. On the Docker host:", commands: ["docker logs nc_app_gocassini 2>&1 | grep 'search backfill'"] },
        ], checked_at }); break;
  }
  // The test row is on EVERY report the operator sends — offered where a test
  // could succeed, waiting where it could not. Most fixtures simply omitted it,
  // so the gallery showed a checklist one row shorter than the product's.
  //
  // Which of the two it is gets decided here from the fixture's own rows. That
  // mirrors a rule the operator owns (readinessProvenPrerequisites, with the Go
  // tests that pin it); this copy exists so eighteen fixtures do not have to
  // hand-maintain the answer, and it never ships to the panel.
  if (!report.checks.some(check => check.id === "test")) {
    const proven = (id: string) => {
      const row = report.checks.find(check => check.id === id);
      return !!row && (row.state === "passed" || row.state === "warn");
    };
    const chain = ["storage", "talk.hpb", "talk.discovery"];
    const blocker = chain.find(id => report.checks.find(check => check.id === id)?.state === "needs_action") ?? chain.find(id => !proven(id));
    report.checks.push(blocker
      ? blocked("test", checkLabels[blocker] ?? blocker, report.checks.find(check => check.id === blocker)?.state !== "needs_action")
      : { id: "test", state: "not_verified", code: "test_not_run", action: "test_recording",
          message: "Nothing has been recorded through Talk yet. A short test recording is what proves the whole path, from a call to audio you can play." });
  }
  // What the operator marks on every row it sends: which probe establishes it,
  // and therefore whether the row carries its own Check button. The fixtures set
  // neither, so the gallery rendered a checklist with fewer buttons than the
  // product has — on a passing storage row, none at all where production shows
  // one. Mirrors probeNameFor in readiness_scope.go.
  for (const check of report.checks) {
    const probe = check.id === "host" || check.id.startsWith("host.") ? "host"
      : check.id.startsWith("archive.") ? "archive"
      : ({ storage: "storage", "talk.hpb": "talk", "talk.discovery": "talk" } as Record<string, string>)[check.id] ?? "";
    // A blocked row is not checkable: running its probe cannot succeed while
    // its prerequisite is unmet, so offering the button invites a failure.
    if (probe && check.code !== "check_blocked") {
      check.probe = probe;
      check.checkable = true;
    }
  }
  report.checks = sortRows(report.checks);
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
        message: "12 meetings are searchable. Every recording in the archive is accounted for. The last re-index added 3, left 9 unchanged, found 0 not searchable and failed on 0.",
      } : check);
      updateVerdict(report); repairStarted = 0;
    }
    return structuredClone(report);
  };
  return {
    async getReadiness() {
      if (id === "refresh-error" && readCount++ > 0) throw Object.assign(new Error("503 Service Unavailable"), { status: 503 });
      return read();
    },
    async checkReadiness(only) {
      await delay(650);
      if (id === "refresh-error") throw Object.assign(new Error("503 Service Unavailable"), { status: 503 });
      // The fault remains until a simulated edit or repair fixes it. Rechecking
      // updates freshness, without making an intentionally broken case healthy.
      if (id !== "old-findings" || !initialCheck) for (const check of report.checks) {
        if ((!only || only.includes(check.id)) && check.checked_at && check.code !== "test_playback") check.checked_at = new Date().toISOString();
      }
      initialCheck = false;
      return read();
    },
    async repairReadiness(action) {
      if (action !== "backfill_search") throw new Error("This preview does not support that repair.");
      const check = report.checks.find(row => row.id === "archive.search");
      if (check && check.repair) {
        delete check.repair;
        delete check.steps;
        delete check.repair_failed;
        check.running = true;
        check.message = "9 meetings are searchable. Of the others, 3 are in the archive but not indexed yet. Re-indexing is running now.";
        repairStarted = Date.now();
      }
      return read();
    },
    async updateRecordingSetup(payload) {
      await delay(250);
      if (payload.internal_secret?.trim()) {
        report.secret_configured = true; report.secret_source = "setup";
        const healthy = scenarioReport("healthy");
        report.checks = report.checks.map(check => check.id === "talk.hpb" ? healthy.checks.find(row => row.id === "talk.hpb")! : check);
      }
      if (payload.test_room_url !== undefined) {
        report.test_room_url = "https://preview.invalid/call/review-room";
        // Mirrors the operator: whose room a named one is, is unknown, so it
        // does not claim it is the caller's.
        report.test_room_mine = false;
        const check = report.checks.find(row => row.id === "talk.discovery");
        if (check) Object.assign(check, { state: "not_verified", code: "connection_not_checked", message: "The Talk connection has not been checked yet.", action: "recheck" });
      }
      if (payload.action === "arm_test") {
        report.test = { state: "waiting_for_talk", published: false, started_at: new Date().toISOString() };
        report.test_room_mine = true;
      }
      if (payload.action === "confirm_playback") report.test.playback_verified_at = new Date().toISOString();
      const testRow = report.checks.find(check => check.id === "test");
      if (testRow && payload.action === "arm_test") Object.assign(testRow, { state: "not_verified", code: "test_in_progress", message: "Cassini is waiting for a recording started in the test room.", checked_at: undefined });
      if (testRow && payload.action === "confirm_playback") Object.assign(testRow, { state: "passed", code: "test_playback", message: "A recording started in Talk was published, and its audio was confirmed by playing it.", checked_at: report.test.playback_verified_at });
      updateVerdict(report); return read();
    },
  };
}
