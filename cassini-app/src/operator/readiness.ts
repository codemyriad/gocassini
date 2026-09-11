export type CheckState = "passed" | "needs_action" | "not_verified";
export interface ReadinessCheck {
  id: string;
  state: CheckState;
  code: string;
  message: string;
  action?: string;
  checked_at?: string;
}
export interface RecordingReadiness {
  state: CheckState;
  checks: ReadinessCheck[];
  secret_configured: boolean;
  secret_source: "env" | "setup" | "unset";
  test_room_url: string;
  test: {
    started_at?: string;
    job_id?: string;
    stage?: string;
    state: string;
    published: boolean;
    playback_verified_at?: string;
    viewer_url?: string;
  };
}
export interface RecordingSetupUpdate {
  internal_secret?: string;
  test_room_url?: string;
  action?: "arm_test" | "confirm_playback";
  job_id?: string;
}
export const checkLabels: Record<string, string> = {
  configuration: "Saved configuration",
  storage: "Recording storage",
  processing: "Speech processing",
  "talk.authentication": "Talk authentication",
  "talk.discovery": "Talk connection",
  "talk.hpb": "High-performance backend",
  "talk.handoff": "Recording connection",
  test: "Test recording",
};
export const stateLabels: Record<CheckState, string> = {
  passed: "Passed", needs_action: "Needs action", not_verified: "Not verified",
};
export function readinessTitle(report: RecordingReadiness): string {
  const count = report.checks.filter(c => c.state === "needs_action").length;
  if (count) return `Recording needs ${count === 1 ? "one more step" : `${count} more steps`}`;
  if (report.state !== "passed") return "Recording setup needs verification";
  return "Recording checks passed";
}
const shellQuote = (s: string) => "'" + s.replaceAll("'", "'\\''") + "'";

// Commands contain no credentials. curl prompts for the administrator's app
// password; the backend supplies its generated credential directly to occ.
export function handoffScript(provisioningUrl: string, aio: boolean): string {
  const occ = aio ? "docker exec -u www-data nextcloud-aio-nextcloud php occ" : "sudo -u www-data php occ";
  return [
    "#!/usr/bin/env bash", "set -euo pipefail", "umask 077",
    ...(aio ? [
      `docker exec nextcloud-aio-nextcloud sh -c 'test "$TALK_RECORDING_ENABLED" != yes && test "$REMOVE_DISABLED_APPS" != yes' || {`,
      `  printf '%s\\n' 'Disable AIO Talk Recording and set NEXTCLOUD_KEEP_DISABLED_APPS=true on the mastercontainer. Recreate through AIO, then retry.' >&2`,
      `  exit 1`,
      `}`,
    ] : []),
    `backup=$(mktemp ./cassini-recording-backend.XXXXXX)`,
    `${occ} config:app:get spreed recording_servers --default-value='' > "$backup"`,
    `printf 'Previous recording backend saved to %s\\n' "$backup"`,
    `read -r -p 'Nextcloud administrator username: ' admin_user`,
    `recording_servers=$(curl --fail --silent --show-error --user "$admin_user" ${shellQuote(provisioningUrl)} | jq -ce '.recording_servers | select(.servers | length > 0)')`,
    `${occ} config:app:set spreed recording_servers --value="$recording_servers"`,
    `${occ} config:app:set spreed call_recording --value=yes`,
    `unset recording_servers`,
  ].join("\n");
}

// Ignore check timestamps and job progress: the shell only needs health changes.
export function readinessHealthKey(report: RecordingReadiness | null): string {
  if (!report) return "";
  return JSON.stringify([report.state, report.secret_configured,
    report.checks.map(c => [c.id, c.state, c.code])]);
}

// Keep configuration reachable from its own row even after its check passes.
export function readinessRows(report: RecordingReadiness): ReadinessCheck[] {
  const rows = [...report.checks];
  if (!rows.some(c => c.id === "talk.authentication")) {
    const at = rows.findIndex(c => c.id.startsWith("talk."));
    rows.splice(at < 0 ? rows.length : at, 0, {
      id: "talk.authentication", state: report.secret_configured ? "passed" : "needs_action",
      code: "internal_secret_configuration", message: report.secret_source === "env"
        ? "The internal secret is managed by deployment configuration."
        : report.secret_configured ? "An internal secret is saved. HPB authentication is checked separately."
        : "Enter the internal secret from your Talk signaling server.",
    });
  }
  if (!rows.some(c => c.id === "test")) rows.push({ id: "test", state: report.test.playback_verified_at ? "passed" : "not_verified",
    code: "test_playback", message: report.test.playback_verified_at ? "Playback was confirmed for the published test recording." : "Record a short test through Talk, then confirm playback." });
  return rows;
}

export function rowActions(check: ReadinessCheck): { action: string; label: string }[] {
  const labels: Record<string, string> = { configure_talk:"Talk authentication", test_room:"Test room", connect_talk:"Connect Talk", test_recording:"Test a recording", recheck:"Check again", setup_storage:"Set up storage" };
  const actions = check.action ? [check.action] : [];
  const persistent: Record<string,string> = { "talk.authentication":"configure_talk", "talk.discovery":"test_room", "talk.handoff":"connect_talk", test:"test_recording" };
  if (persistent[check.id] && !actions.includes(persistent[check.id])) actions.push(persistent[check.id]);
  return actions.map(action => ({ action, label:labels[action] ?? "Configure" }));
}
