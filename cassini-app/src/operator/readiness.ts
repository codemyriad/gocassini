// `warn` arrived with the host checks (D-798 V2): something is impaired but
// audio recording remains possible, such as a disk getting low on space or an
// enabled optional transcription model being unavailable.
export type CheckState = "passed" | "warn" | "needs_action" | "not_verified";
// One thing to do about a check that is not ok. Mirrors SetupNoticeStep, which
// already renders this shape for storage faults: commands behind a disclosure,
// so an administrator who just wants the button never reads a command line.
// A remedy in words. No commands: this panel is ADMIN-only and the operator can
// do the work, so where a repair is possible the row carries `repair` and the
// panel renders a button (review 2026-09-25). Printing a shell line asked a
// reader to find a terminal and the right container to trigger something the
// process showing them the message could simply do — and how `occ` is invoked
// varies by deployment, so the instruction was a guess as often as not.
export interface ReadinessStep {
  label: string;
}

export interface ReadinessCheck {
  id: string;
  state: CheckState;
  code: string;
  message: string;
  action?: string;
  // The remedy, for a check that is not ok (D-798 R0.1). Distinct from
  // `action`: plenty of remedies are not a place to navigate to, but a command
  // to run on a host this app cannot reach.
  steps?: ReadinessStep[];
  // Something the operator can do about this check itself, rendered as a
  // button. Replaces printing a command for an administrator to go and run:
  // this panel is ADMIN-only and the operator can already do the work.
  repair?: string;
  // Whether a probe establishes THIS row, so it can be re-checked on its own.
  // The operator says so — the row-to-probe mapping is readinessScopeFor's, and
  // a second copy here drifted into a spinner for a probe that never ran.
  checkable?: boolean;
  // Where to read about a fault the operator cannot repair. A link, not a
  // procedure: an instruction we cannot verify is worse than a reference.
  docs?: string;
  checked_at?: string;
}


export interface RecordingReadiness {
  state: CheckState;
  // Audio recording remains available when optional processing warns.
  recording_state?: CheckState;
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
// What a repair button says. Offered only for actions the operator can itself
// perform.
export const repairLabels: Record<string, string> = {
  backfill_search: "Re-index now",
};

export const checkLabels: Record<string, string> = {
  configuration: "Saved configuration",
  storage: "Recording storage",
  "archive.search": "Archive search",
  // Named for whose credential it is. "Internal credential" read as something
  // of Cassini's or Nextcloud's, and an administrator went looking for it in
  // Nextcloud's configuration, where it has never been.
  "talk.authentication": "Signaling server credential",
  "talk.discovery": "Talk connection",
  "talk.hpb": "High-performance backend",
  // Only appears when the credential could not be provisioned. The row used to
  // double as "has Talk called us lately", which was inherently historical and
  // could never legitimately read green, so that half is gone.
  "talk.handoff": "Recording credential",
  host: "Recording host",
  "host.workdir": "Recording volume",
  "host.tmpdir.writable": "Temporary space",
};
export const stateLabels: Record<CheckState, string> = {
  passed: "Passed", warn: "Needs attention", needs_action: "Needs action", not_verified: "Not verified",
};
export function readinessTitle(report: RecordingReadiness): string {
  const recordingState = report.recording_state ?? report.state;
  const optional = (id: string) => id === "processing" || id.startsWith("archive.");
  const transcriptionWarns = report.checks.some(c => c.id === "processing" && c.state === "warn");
  const archiveWarns = report.checks.some(c => c.id.startsWith("archive.") && c.state === "warn");
  const archiveUnknown = report.checks.some(c => c.id.startsWith("archive.") && c.state === "not_verified");
  const warning = transcriptionWarns && archiveWarns
    ? "transcription and archive search need attention"
    : transcriptionWarns ? "transcription needs attention" : archiveWarns ? "archive search needs attention" : "";
  const optionalDetail = [warning, archiveUnknown ? "archive search coverage not verified" : ""].filter(Boolean).join("; ");
  if (recordingState === "needs_action") {
    const count = report.checks.filter(c => c.state === "needs_action" &&
      (report.recording_state === undefined || !optional(c.id))).length;
    return count ? `${count === 1 ? "One recording check needs" : `${count} recording checks need`} attention` : "Recording setup needs attention";
  }
  if (recordingState === "not_verified") {
    return optionalDetail ? `Recording setup needs verification; ${optionalDetail}` : "Recording setup needs verification";
  }
  if (recordingState === "warn") return "Recording checks need attention";
  if (optionalDetail) return `Recording ready; ${optionalDetail}`;
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
    // After talk.hpb, never before it. The backend has to exist before its
    // credential means anything, and a reader who meets the credential first
    // reads the requirement before the reason it does not apply.
    const at = rows.findIndex(c => c.id.startsWith("talk.") && c.id !== "talk.hpb");
    const noBackend = rows.some(c => c.code === "hpb_disabled");
    rows.splice(at < 0 ? rows.length : at, 0, {
      id: "talk.authentication", state: report.secret_configured ? "passed" : "needs_action",
      // Changing a saved secret is worth offering — unless there is no backend
      // for it to authenticate to, in which case editing it achieves nothing.
      ...(noBackend ? {} : { action: "configure_talk" }),
      // No "HPB authentication is checked separately" trailer. It pointed at a
      // check that only happens once a backend exists and the probe gets that
      // far, so on an install with no High Performance Backend it told a reader
      // nothing at all. The Talk connection and backend rows report that.
      code: "internal_secret_configuration", message: report.secret_source === "env"
        ? "The internal secret comes from Cassini's deployment configuration."
        : report.secret_configured ? "An internal secret is saved."
        : "Enter the internal secret from your Talk signaling server.",
    });
  }
  return rows;
}

export function rowActions(check: ReadinessCheck): { action: string; label: string }[] {
  const labels: Record<string, string> = { configure_talk:"Talk authentication", test_room:"Test room", connect_talk:"Connect Talk", test_recording:"Test a recording", recheck:"Check again", setup_storage:"Set up storage" };
  const actions = check.action ? [check.action] : [];
  // talk.authentication is NOT here. The operator withholds its action when the
  // secret cannot be useful — no High Performance Backend to authenticate to —
  // and a panel that adds the form back offers a reader a way to configure
  // something that will change nothing. Where the secret IS useful, the row
  // carries the action itself.
  const persistent: Record<string,string> = { "talk.discovery":"test_room", "talk.handoff":"connect_talk" };
  if (persistent[check.id] && !actions.includes(persistent[check.id])) actions.push(persistent[check.id]);
  return actions.map(action => ({ action, label:labels[action] ?? "Configure" }));
}

// How loudly a check should read. Colour is redundant with the label text
// beside it on purpose — the state word is always rendered, so nothing here is
// the only carrier of meaning for a reader who cannot see the difference.
//
// Host checks, optional processing and archive coverage can warn without
// blocking audio.
// An aged connection finding keeps its verdict and exposes its time separately.
//
// The two rows readinessRows synthesises look like candidates and are not.
// "Configured" claims only that a secret is saved, which is true and verified;
// whether it WORKS is the separate talk.hpb check. "Previously confirmed"
// claims a past playback, which is also true — and a playback confirmation is
// inherently historical, so amber would be its permanent ceiling. A colour a
// healthy install can never clear is one people learn to ignore.
//
export type CheckTone = "success" | "warning" | "error" | "neutral";

export function checkTone(check: ReadinessCheck): CheckTone {
  if (check.state === "needs_action") return "error";
  // Impaired but working. #322 deliberately shipped no amber because nothing
  // could legitimately produce one; the host checks can, so it exists now for
  // a real producer rather than an invented one.
  if (check.state === "warn") return "warning";
  // Nobody looked, or the incoming handoff is no longer recent. NOT a fault:
  // a colour that means both "impaired" and "unknown" means neither.
  if (check.state === "not_verified") return "neutral";
  return "success";
}

export const toneClasses: Record<CheckTone, string> = {
  success: "text-success",
  warning: "text-warning",
  error: "text-error",
  neutral: "text-base-content/60",
};

// The instance's worst news, for the header. Ordered by how much it costs to
// ignore: something broken outranks something nobody has checked. Read from the
// SAME rows the list renders, so a synthesised row cannot make the header
// disagree with what is under it.
export function reportTone(report: RecordingReadiness): CheckTone {
  const tones = readinessRows(report).map(checkTone);
  // Ordered by how much it costs to ignore.
  if (tones.includes("error")) return "error";
  if (tones.includes("warning")) return "warning";
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

// talkSettingsURL is Nextcloud's own Talk administration page, derived from the
// operator's base URL rather than assumed to sit at the origin root.
//
// A subdirectory install serves Nextcloud under a prefix, so a link built on
// that assumption 404s on exactly the deployments least equipped to debug it.
// Returns "" when the base does not look like an app URL, because no link beats
// a wrong one.
export function talkSettingsURL(operatorBaseHref: string): string {
  const cut = operatorBaseHref.search(/\/(index\.php\/)?apps\//);
  if (cut <= 0) return "";
  return operatorBaseHref.slice(0, cut) + "/settings/admin/talk";
}
