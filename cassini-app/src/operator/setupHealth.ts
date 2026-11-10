import { get } from "svelte/store";
import { LL } from "cassini-viewer/i18n";
import type { OperatorPanel } from "../surfaceRouting";

export interface SetupFeatures { summaries: boolean; insights: boolean }
export type SetupFeature = "summaries" | "insights";
export interface SetupHealth {
  recordingState?: "passed" | "warn" | "needs_action" | "not_verified";
  ok: boolean;
  state: string;
  mode: string;
  cause: string;
  features: SetupFeatures | null;
  // The caller's own recording shares (D-885): null on an operator too old to
  // report them, which must read as "nobody said" and not as "nothing waiting".
  sharing: SharingStatus | null;
}

// A published meeting whose share the caller has not accepted yet. Nextcloud
// keeps a share out of the recipient's files until they accept it, and nothing
// in Cassini ever accepts on their behalf.
export interface PendingRecording {
  // One entry per meeting, but a person can hold both a user share and a group
  // share of the same file: accepting the meeting means accepting every id.
  shareIds: string[];
  jobId: string;
  // "" when the operator no longer knows the job; the row then shows the date
  // alone, or "A recording" when there is no date either.
  title: string;
  // ISO 8601, or "" when unknown.
  startedAt: string;
  // 0 when unknown.
  durationSeconds: number;
}

// What `GET operator/setup` says about the caller's recording shares (D-885).
//
// `checked` false means the reads as the caller failed or timed out: the
// counts are then zero because nothing was established, not because nothing is
// waiting, and the app shows nothing rather than guess.
export interface SharingStatus {
  checked: boolean;
  pending: PendingRecording[];
  // Published meetings the caller was in the room for, whose share to them
  // Nextcloud refused, one per meeting, newest first.
  refused: RecordingNotice[];
  // Recordings the caller started that were not published because Nextcloud
  // refused to share them with the caller, one per meeting, newest first.
  unpublished: RecordingNotice[];
  // Neither list carries a meeting whose notice the caller dismissed. Those
  // are these job ids, of each kind: the list says nothing more about them,
  // but the meeting opened from a link still says why it is not there.
  dismissedRefused: string[];
  dismissedUnpublished: string[];
}

// One meeting Browse tells the reader about by name: a recording Nextcloud
// refused them, or one they started that was not published. One that was
// never published usually has no title; the room and the time are what the
// person recognises it by.
export interface RecordingNotice {
  jobId: string;
  title: string;
  roomName: string;
  // ISO 8601, or "" when unknown.
  startedAt: string;
}
export interface RecordingsAccess {
  ok: boolean;
  state: string;
  step: string;
  detail: string;
  cause: string;
}
export interface SetupNoticeStep {
  label: string;
  commands: string[];
  action?: "settings";
}
export type SetupNoticeTone = "warning" | "neutral";
export interface SetupNotice {
  tone: SetupNoticeTone;
  title: string;
  summary: string;
  cause: string;
  steps: SetupNoticeStep[];
  detail: string;
  note: string;
  shareLabel: string;
  shareUrl: string;
  reference: string;
}

export async function fetchSetupHealth(operatorBasePath: string, fetchImpl: typeof fetch = fetch): Promise<SetupHealth | null> {
  try {
    const response = await fetchImpl(`${operatorBasePath.replace(/\/+$/, "")}/setup`, {
      method: "GET", headers: { Accept: "application/json" }, cache: "no-store",
    });
    return response.status === 200 ? readSetupHealth(await response.json()) : null;
  } catch { return null; }
}

export function readSetupHealth(body: unknown): SetupHealth | null {
  if (!isRecord(body) || typeof body.ok !== "boolean" || typeof body.state !== "string") return null;
  return {
    ...(body.recording_state === "passed" || body.recording_state === "warn" || body.recording_state === "needs_action" || body.recording_state === "not_verified" ? { recordingState: body.recording_state } : {}),
    ok: body.ok, state: body.state,
    mode: typeof body.mode === "string" ? body.mode : "",
    cause: typeof body.cause === "string" ? body.cause : "",
    features: readSetupFeatures(body.features),
    sharing: readSharing(body.sharing),
  };
}

// readSharing reads the `sharing` object defensively: a missing or malformed
// one is null (an operator that predates it), and a `checked: false` one keeps
// its counts at zero whatever else it carries, because the operator says it
// established nothing. A malformed entry in `pending`, `refused_meetings` or
// `refused_started_meetings` is dropped rather than failing the whole object;
// an entry with no usable share id is nothing the app could accept, so it is
// dropped too.
export function readSharing(value: unknown): SharingStatus | null {
  if (!isRecord(value) || typeof value.checked !== "boolean") return null;
  if (!value.checked) return { checked: false, pending: [], refused: [], unpublished: [], dismissedRefused: [], dismissedUnpublished: [] };
  const pending = Array.isArray(value.pending)
    ? value.pending.flatMap((entry) => {
        const recording = readPendingRecording(entry);
        return recording ? [recording] : [];
      })
    : [];
  return {
    checked: true,
    pending,
    refused: readRecordingNotices(value.refused_meetings),
    unpublished: readRecordingNotices(value.refused_started_meetings),
    dismissedRefused: readJobIds(value.dismissed_refused),
    dismissedUnpublished: readJobIds(value.dismissed_refused_started),
  };
}

// readJobIds keeps the non-empty strings; an operator that predates the
// field sends none, and that is an empty list.
function readJobIds(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.filter((id): id is string => typeof id === "string" && id.trim() !== "").map((id) => id.trim());
}

function readRecordingNotices(value: unknown): RecordingNotice[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((entry) => {
    const notice = readRecordingNotice(entry);
    return notice ? [notice] : [];
  });
}

// An entry without a job id could be neither dismissed nor matched to the
// meeting once the reader can see it, so it is dropped.
function readRecordingNotice(value: unknown): RecordingNotice | null {
  if (!isRecord(value) || typeof value.job_id !== "string" || value.job_id === "") return null;
  return {
    jobId: value.job_id,
    title: typeof value.title === "string" ? value.title : "",
    roomName: typeof value.room_name === "string" ? value.room_name : "",
    startedAt: typeof value.started_at === "string" ? value.started_at : "",
  };
}

function readPendingRecording(value: unknown): PendingRecording | null {
  if (!isRecord(value)) return null;
  const shareIds = Array.isArray(value.share_ids)
    ? value.share_ids.filter((id): id is string => typeof id === "string" && id !== "")
    : [];
  if (shareIds.length === 0) return null;
  return {
    shareIds,
    jobId: typeof value.job_id === "string" ? value.job_id : "",
    title: typeof value.title === "string" ? value.title : "",
    startedAt: typeof value.started_at === "string" ? value.started_at : "",
    durationSeconds: readCount(value.duration_seconds),
  };
}

function readCount(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? Math.floor(value) : 0;
}

// A missing setup answer leaves the audience unknown. Every current recording
// uses Nextcloud shares, regardless of its room's visibility.
export type RecordingAudience = "" | "participants";
export function recordingAudience(health: SetupHealth | null): RecordingAudience {
  return health?.mode === "direct_shares" ? "participants" : "";
}

export function readSetupFeatures(value: unknown): SetupFeatures | null {
  if (!isRecord(value) || typeof value.summaries !== "boolean" || typeof value.insights !== "boolean") return null;
  return { summaries: value.summaries, insights: value.insights };
}

export function readRecordingsAccess(body: unknown): RecordingsAccess | null {
  if (!isRecord(body) || !isRecord(body.recordings_access)) return null;
  const access = body.recordings_access;
  if (typeof access.state !== "string") return null;
  return {
    ok: access.ok === true, state: access.state,
    step: typeof access.step === "string" ? access.step : "",
    detail: typeof access.detail === "string" ? access.detail : "",
    cause: typeof access.cause === "string" ? access.cause : "",
  };
}

export function shareableAppUrl(href: string): string {
  try { const url = new URL(href); url.hash = ""; return url.toString(); }
  catch { return href; }
}

const APP_ID = "gocassini";
// Functions rather than constants, so each notice is in the reader's language.
const rerunSetup = (): SetupNoticeStep => ({
  label: get(LL).app.operator.setup_notice.step_rerun_setup(),
  commands: [`occ app_api:app:disable ${APP_ID}`, `occ app_api:app:enable ${APP_ID}`],
});
const settingsOffer = (): SetupNoticeStep => ({
  label: get(LL).app.operator.setup_notice.step_open_settings(),
  commands: [], action: "settings",
});

export function buildSetupNotice(options: {
  health: SetupHealth | null;
  access: RecordingsAccess | null;
  isAdmin: boolean;
  appUrl: string;
}): SetupNotice | null {
  const { health, access, isAdmin, appUrl } = options;
  const verdict = health ?? access;
  if (!verdict || verdict.ok) return null;
  // The provisioning check gates new publishing only. Reading never consults
  // it, so the notice never replaces the meeting list (D-849). It promises
  // browsing, not that every read succeeds: a Nextcloud outage that fails this
  // check can fail a reader's share scan too, and the list reports that itself.
  const t = get(LL).app.operator.setup_notice;
  const failing = verdict.state !== "unknown";
  const summary = failing ? t.summary_failing() : t.summary_unchecked();
  const base: SetupNotice = {
    tone: failing ? "warning" : "neutral",
    title: failing ? t.title_failing() : t.title_unchecked(),
    summary, cause: "", steps: [], detail: "", note: "", shareLabel: "", shareUrl: "", reference: "",
  };
  if (!isAdmin) {
    return { ...base, shareLabel: t.share_label(), shareUrl: appUrl };
  }
  const step = access?.step ?? "";
  const steps: SetupNoticeStep[] = step === "owner_account"
    ? [settingsOffer(), { label: t.step_create_account({ user: "cassini" }), commands: ["occ user:add cassini"] }, rerunSetup()]
    : step === "administrator"
      ? [{ label: t.step_set_admin_user({ variable: "CASSINI_NC_ADMIN_USER" }), commands: [] }, rerunSetup()]
      : [settingsOffer(), rerunSetup()];
  return {
    ...base,
    // cause and detail are the operator's own sentences, in English.
    cause: access?.cause || health?.cause || t.cause_unknown(),
    steps,
    detail: access?.detail || (step
      ? t.detail_state_at_step({ state: verdict.state, step })
      : t.detail_state({ state: verdict.state })),
    note: t.note_occ({ command: "occ", example: "sudo -u www-data php occ" }),
    reference: t.reference({ endpoint: "GET /operator/status" }),
  };
}

// --- The unconfigured states (D-722) ---
//
// Same discipline as buildSetupNotice above, and for the same reason: the copy
// is the part worth testing, .svelte files are not unit-tested in this repo, and
// a wrong sentence about what leaves this deployment is a worse failure than a
// missing one. NeedsSetupCard renders this and decides nothing.

export interface FeatureNotice {
  title: string;
  summary: string;
  // panel is the operator panel that fixes it, and actionLabel the link to it.
  // BOTH are empty for anyone who is not an administrator — that panel is ADMIN
  // at the proxy and its PUT would 403, so offering the control would be
  // offering a way to fail. They get the fact and who can act on it, which is
  // the whole of the remedy available to them.
  panel: OperatorPanel | "";
  actionLabel: string;
  // actionTitle is the REMEDY as a heading — "Add a provider to create
  // insights" — and its presence is what tells NeedsSetupCard to draw the
  // compact locked card rather than the explanatory block.
  //
  // Set only for an administrator looking at an unconfigured capability, which
  // is the one case where the paragraph is wasted: they know what an endpoint
  // is, the button is right there, and a warning triangle over three sentences
  // is in the way. Everyone else, and every run FAILURE, keeps the prose —
  // "the endpoint rejected the request" is not a state anybody can be expected
  // to infer from a title.
  actionTitle?: string;
  // The compact card's button. Shorter than actionLabel because the title
  // beside it has already said what is missing, and it is the same words as the
  // button on the panel it opens.
  actionShortLabel?: string;
}

// The one panel behind both facts: providers and the summarise step are edited
// together in AI providers (Settings.svelte maps `endpoints` -> LLMSettingsPanel).
const AI_PANEL: OperatorPanel = "endpoints";

// Only an administrator can act, and only they are told there is somewhere to
// go — see FeatureNotice.panel. The compact card's button (action_short) is the
// same words as the button on the panel it opens, so the second press is the
// one the first one promised.

// buildFeatureNotice returns what to say about a capability this deployment does
// not have, or null when there is nothing to say — which is BOTH "it is
// configured" and "nobody answered". A standalone export has no operator to ask,
// and absence there must not read as "not configured": it is the same three-state
// rule the catalog's hasSummary follows.
export function buildFeatureNotice(options: {
  features: SetupFeatures | null;
  feature: SetupFeature;
  isAdmin: boolean;
}): FeatureNotice | null {
  const { features, feature, isAdmin } = options;
  if (features === null || features[feature]) {
    return null;
  }
  const t = get(LL).app.operator.feature_notice;
  const insights = feature === "insights";
  return {
    title: insights ? t.insights_title() : t.summaries_title(),
    summary: insights
      ? isAdmin ? t.insights_summary() : t.insights_summary_ask_admin()
      : isAdmin ? t.summaries_summary() : t.summaries_summary_ask_admin(),
    panel: isAdmin ? AI_PANEL : "",
    actionLabel: isAdmin ? t.action() : "",
    actionTitle: isAdmin ? (insights ? t.insights_action_title() : t.summaries_action_title()) : undefined,
    actionShortLabel: isAdmin ? t.action_short() : undefined,
  };
}

// The one object guard for the operator's answers; sharingNotice.ts reads the
// accept answer with it too.
export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
