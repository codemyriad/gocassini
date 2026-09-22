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
  "talk.authentication": "Internal credential",
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
  if (count) return `${count === 1 ? "One recording check needs" : `${count} recording checks need`} attention`;
  if (report.state !== "passed") return "Recording setup needs verification";
  return "Recording checks passed";
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
  const unreadable = rows.some(c => c.code === "setup_store_unreadable");
  if (!unreadable && !rows.some(c => c.id === "talk.authentication")) {
    const at = rows.findIndex(c => c.id.startsWith("talk."));
    rows.splice(at < 0 ? rows.length : at, 0, {
      id: "talk.authentication", state: report.secret_configured ? "passed" : "needs_action",
      code: "internal_secret_configuration", message: report.secret_source === "env"
        ? "The internal secret is managed by deployment configuration. HPB authentication is checked separately."
        : report.secret_configured ? "An internal secret is saved. HPB authentication is checked separately."
        : "Enter the internal secret from your Talk signaling server.",
    });
  }
  if (!rows.some(c => c.id === "test")) rows.push({ id: "test", state: report.test.playback_verified_at ? "passed" : "not_verified",
    code: "test_playback", checked_at: report.test.playback_verified_at, message: report.test.playback_verified_at ? "Playback was previously confirmed for this published test recording. This is historical evidence, not a current connection test." : "Record a short test through Talk, then confirm playback." });
  return rows;
}

export function rowActions(check: ReadinessCheck): { action: string; label: string }[] {
  const labels: Record<string, string> = { configure_talk:"Talk authentication", test_room:"Test room", connect_talk:"Connect Talk", test_recording:"Test a recording", recheck:"Check again", setup_storage:"Set up storage" };
  const actions = check.action ? [check.action] : [];
  const persistent: Record<string,string> = { "talk.authentication":"configure_talk", "talk.discovery":"test_room", "talk.handoff":"connect_talk", test:"test_recording" };
  if (persistent[check.id] && !actions.includes(persistent[check.id])) actions.push(persistent[check.id]);
  return actions.map(action => ({ action, label:labels[action] ?? "Configure" }));
}

// How loudly a check should read. Colour is redundant with the label text
// beside it on purpose — the state word is always rendered, so nothing here is
// the only carrier of meaning for a reader who cannot see the difference.
//
// THREE tones, and deliberately no amber. Every check here answers one binary
// question — will recording work? — and the operator already resolves the
// middle ground itself: evidence older than its TTL is downgraded to
// not_verified at the source (recording_readiness.go) rather than reported as a
// weaker pass. So there is no state left that means "working but impaired".
//
// The two rows readinessRows synthesises look like candidates and are not.
// "Configured" claims only that a secret is saved, which is true and verified;
// whether it WORKS is the separate talk.hpb check. "Previously confirmed"
// claims a past playback, which is also true — and a playback confirmation is
// inherently historical, so amber would be its permanent ceiling. A colour a
// healthy install can never clear is one people learn to ignore.
//
// Amber belongs to coverage instead — "search can read 129 of 138 meetings" is
// working-but-incomplete, which is a different question from readiness and has
// its own numbers. It is not represented here yet.
export type CheckTone = "success" | "error" | "neutral";

export function checkTone(check: ReadinessCheck): CheckTone {
  if (check.state === "needs_action") return "error";
  // Nobody looked, or the evidence expired. NOT a fault: a colour that means
  // both "impaired" and "unknown" means neither.
  if (check.state === "not_verified") return "neutral";
  return "success";
}

export const toneClasses: Record<CheckTone, string> = {
  success: "text-success",
  error: "text-error",
  neutral: "text-base-content/60",
};

// The instance's worst news, for the header. Ordered by how much it costs to
// ignore: something broken outranks something nobody has checked. Read from the
// SAME rows the list renders, so a synthesised row cannot make the header
// disagree with what is under it.
export function reportTone(report: RecordingReadiness): CheckTone {
  const tones = readinessRows(report).map(checkTone);
  if (tones.includes("error")) return "error";
  if (tones.includes("neutral")) return "neutral";
  return "success";
}

// How long ago a check established what it established.
//
// Relative rather than absolute because the question a reader has is "is this
// still true?", and "6 minutes ago" answers it where "22/09/2026, 12:45:00"
// makes them do arithmetic. The absolute time stays available as a title, for
// the reader who wants to correlate with a log.
//
// This carries the freshness that used to be smuggled into the state itself:
// an aged check keeps its verdict and says how old it is, rather than decaying
// into "not verified" and making an idle panel look broken (D-798).
export function formatAge(checkedAt: string, now: Date = new Date()): string {
  const at = new Date(checkedAt);
  if (Number.isNaN(at.getTime())) {
    return "";
  }
  const seconds = Math.round((now.getTime() - at.getTime()) / 1000);
  // A clock skewed forward should not produce "in 3 minutes"; the reader only
  // needs to know it is current.
  if (seconds < 60) {
    return "just now";
  }
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) {
    return `${minutes} minute${minutes === 1 ? "" : "s"} ago`;
  }
  const hours = Math.round(minutes / 60);
  if (hours < 24) {
    return `${hours} hour${hours === 1 ? "" : "s"} ago`;
  }
  const days = Math.round(hours / 24);
  return `${days} day${days === 1 ? "" : "s"} ago`;
}

export function checkStateLabel(check: ReadinessCheck): string {
  if (check.code === "internal_secret_configuration" && check.state === "passed") return "Configured";
  if (check.code === "test_playback" && check.state === "passed") return "Previously confirmed";
  return stateLabels[check.state];
}
