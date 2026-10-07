// `warn` arrived with the host checks (D-798 V2): something is impaired but
// audio recording remains possible, such as a disk getting low on space or an
// enabled optional transcription model being unavailable.
export type CheckState = "passed" | "warn" | "needs_action" | "not_verified";
// One thing to do about a check that is not ok. Mirrors SetupNoticeStep, which
// already renders this shape for storage faults: commands behind a disclosure,
// so an administrator who just wants the button never reads a command line.
// Where the operator can do the work, the row carries `repair` and the panel
// renders a button instead (review 2026-09-25). `commands` is only for what
// Cassini cannot read or run for itself, each labelled with where it applies.
export interface ReadinessStep {
  label: string;
  commands?: string[];
}

export function labelParts(label: string): { text: string; code: boolean }[] {
  return label.split("`").map((text, index) => ({ text, code: index % 2 === 1 })).filter(part => part.text !== "");
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
  // Which probe establishes this row. Rows sharing one are refreshed together,
  // so pressing Check on either of them re-runs both.
  probe?: string;
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
// Whether a test recording is still moving on its own.
//
// The only part of this panel whose state advances without the reader touching
// anything: Talk starts the recording, and the job records, uploads, builds and
// publishes. Everything else changes only when somebody presses a button, which
// is why this panel does not poll — and why this one case has to be followed.
//
// It stops being in flight the moment the next step belongs to a person:
// published (play it and confirm) or failed (go and look). The age cap keeps a
// test armed and abandoned days ago from being followed every time the panel is
// opened — nothing is arriving for it.
export function testInFlight(report: RecordingReadiness | null, now: Date = new Date()): boolean {
  const test = report?.test;
  if (!test?.started_at || test.published || test.state === "failed") return false;
  const started = new Date(test.started_at).getTime();
  if (Number.isNaN(started)) return false;
  return now.getTime() - started < 30 * 60 * 1000;
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
  "talk.discovery": "Talk connection",
  // Nextcloud Talk's own term for it, capitalised the way its documentation
  // does — and the same string the operator uses when a blocked row names this
  // one as what it is waiting for. A message naming a row something the reader
  // cannot see on screen sends them hunting.
  "talk.hpb": "High Performance Backend",
  // Only appears when the credential could not be provisioned. The row used to
  // double as "has Talk called us lately", which was inherently historical and
  // could never legitimately read green, so that half is gone.
  "talk.handoff": "Recording credential",
  // Evidence rather than a check: it reports what a person confirmed by
  // playing a recording back, which is the one thing no probe can establish.
  test: "Test recording",
  host: "Recording host",
  "host.workdir": "Recording volume",
  "host.tmpdir.writable": "Temporary space",
};
export const stateLabels: Record<CheckState, string> = {
  passed: "Passed", warn: "Needs attention", needs_action: "Needs action", not_verified: "Not verified",
};
// readinessTitle is the heading, derived from the verdict the OPERATOR reached.
//
// It used to reach its own: re-counting needs_action rows with its own notion of
// which are optional, and composing prose about transcription warnings — from a
// `processing` row removed in the 2026-09-25 review, so that branch had been
// dead and unreachable since. A second aggregation in the panel can disagree
// with the first, and did.
//
// Counting rows for the phrasing is presentation and stays here. Deciding what
// the instance's verdict IS does not.
export function readinessTitle(report: RecordingReadiness): string {
  const verdict = report.recording_state ?? report.state;
  // Archive coverage is excluded from the recording verdict, so a shortfall
  // there would otherwise be invisible in the heading.
  const archive = report.checks.some(c => c.id.startsWith("archive.") && (c.state === "warn" || c.state === "needs_action"))
    ? "archive search needs attention"
    : "";
  if (verdict === "needs_action") {
    const count = report.checks.filter(c => c.state === "needs_action").length;
    return count === 1 ? "One recording check needs attention" : `${count} recording checks need attention`;
  }
  if (verdict === "warn") return "Recording checks need attention";
  if (verdict === "not_verified") {
    return archive ? `Recording setup needs verification; ${archive}` : "Recording setup needs verification";
  }
  return archive ? `Recording ready; ${archive}` : "Recording checks passed";
}
// Ignore check timestamps and job progress: the shell only needs health changes.
export function readinessHealthKey(report: RecordingReadiness | null): string {
  if (!report) return "";
  return JSON.stringify([report.state, report.secret_configured,
    report.checks.map(c => [c.id, c.state, c.code])]);
}

// Keep configuration reachable from its own row even after its check passes.
// readinessRows is the rows to render, in the order the operator sent them.
//
// It used to SYNTHESISE talk.authentication when the operator omitted it,
// choosing its state, message, action, position and its own suppression rule —
// and drifted, carrying wording the operator had replaced. The operator reports
// that row itself now. The panel renders what it is told and decides nothing
// about what a check found.
export function readinessRows(report: RecordingReadiness): ReadinessCheck[] {
  return [...report.checks];
}

export const hpbGuideURL = "https://nextcloud-talk.readthedocs.io/en/stable/quick-install/";

const guideLabels: Record<string, string> = {
  "talk.hpb": "How to set up a High Performance Backend",
};

export function rowGuide(check: ReadinessCheck): { href: string; label: string } | null {
  if (check.code === "check_blocked") return null;
  const href = check.docs ?? (check.action === "setup_hpb" ? hpbGuideURL : undefined);
  if (!href) return null;
  return { href, label: guideLabels[check.id] ?? "Read Nextcloud's guide" };
}

export function rowActions(check: ReadinessCheck): { action: string; label: string }[] {
  // A row waiting on another check offers nothing. Its remedy belongs to the
  // prerequisite, and the operator already strips its action — but the standing
  // actions below are the panel's own and were added regardless, so a blocked
  // Talk connection still showed Check and Test room.
  if (check.code === "check_blocked") return [];
  // "Talk authentication" named the row this button sits on, back when the row
  // was called that. The credential lives on the High Performance Backend row
  // now — one server, one fact — so the button says what it does instead.
  // Every action the operator and the recorder can emit. An action missing from
  // here used to render as "Configure" and open whichever drawer the panel fell
  // through to — which for setup_hpb, the row a backend-less install leads
  // with, meant a button labelled nothing in particular. repair_configuration
  // was the same.
  const labels: Record<string, string> = {
    configure_talk: "Set credential",
    connect_talk: "Connect Talk",
    test_recording: "Record a test",
    recheck: "Check",
    setup_storage: "Set up storage",
    repair_configuration: "How to repair this",
  };
  const actions = check.action ? [check.action] : [];
  // The backend row is not here either: the operator withholds its credential
  // action when there is no backend to authenticate to, and a panel that adds
  // the form back offers a reader a way to configure something that would
  // change nothing. Where the secret IS useful, that row carries the action.
  // talk.discovery no longer keeps a "Test room" button: the room is Cassini's
  // to create, so there is no longer anything for a reader to choose.
  const persistent: Record<string,string> = { "talk.handoff":"connect_talk" };
  if (persistent[check.id] && check.code !== "recording_secret_missing" && !actions.includes(persistent[check.id])) actions.push(persistent[check.id]);
  // An action this build cannot name gets no button, rather than a "Configure"
  // whose effect the panel cannot describe and whose drawer it does not have.
  // Same rule as the repair buttons: offering a control the panel cannot
  // explain is how a reader ends up reading an instruction for another fault.
  const passedLabels: Record<string, string> = { configure_talk: "Change secret" };
  const codeLabels: Record<string, Record<string, string>> = { test_in_progress: { test_recording: "Show steps" } };
  return actions.filter(action => labels[action]).map(action => ({
    action,
    label: codeLabels[check.code]?.[action] ?? (check.state === "passed" && passedLabels[action] ? passedLabels[action] : labels[action]),
  }));
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
  success: "text-success-strong",
  warning: "text-warning-strong",
  error: "text-error-strong",
  neutral: "text-base-content/65",
};

// The instance's worst news, for the header.
//
// The operator's own verdict, coloured — not a second aggregation. This used to
// re-derive the worst state from the rendered rows, which was defensible while
// the panel synthesised rows the operator had not sent. It no longer does, and
// re-deriving had become a way for the heading to disagree with the verdict
// beside it: the operator excludes a test nobody ran from its verdict, and a
// panel counting that row would paint a fully passing install grey.
export function reportTone(report: RecordingReadiness): CheckTone {
  return checkTone({ id: "", state: report.state, code: "", message: "" });
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
const sameRunWindowMs = 60_000;

export function sharedCheckTime(checks: ReadinessCheck[]): { checkedAt: string; ids: Set<string> } | null {
  const timed = checks
    .filter(check => check.code !== "test_playback" && check.checked_at && !Number.isNaN(Date.parse(check.checked_at)))
    .map(check => ({ id: check.id, raw: check.checked_at as string, at: Date.parse(check.checked_at as string) }))
    .sort((a, b) => a.at - b.at);
  if (timed.length === 0) return null;
  const runs: (typeof timed)[] = [];
  for (const item of timed) {
    const run = runs[runs.length - 1];
    if (run && item.at - run[run.length - 1].at <= sameRunWindowMs) run.push(item);
    else runs.push([item]);
  }
  const largest = runs.reduce((best, run) => run.length >= best.length ? run : best);
  return { checkedAt: largest[0].raw, ids: new Set(largest.map(item => item.id)) };
}

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

// talkRoomURL is the test conversation, as a link this browser can follow.
//
// The operator's test_room_url is built from the base URL the operator itself
// reaches Nextcloud on, which behind AppAPI is routinely an internal host like
// http://reverse-proxy — correct for the connection probe, useless as a link.
// The origin a reader can actually open is the one serving this page, and only
// the browser knows it. The token is the operator's; the origin is ours.
export function talkRoomURL(operatorBaseHref: string, roomURL: string): string {
  const token = roomURL.split("/").pop()?.trim() ?? "";
  if (!token) return "";
  const cut = operatorBaseHref.search(/\/(index\.php\/)?apps\//);
  if (cut <= 0) return "";
  return operatorBaseHref.slice(0, cut) + "/call/" + encodeURIComponent(token);
}
