import type { OperatorPanel } from "../surfaceRouting";

export interface SetupFeatures { summaries: boolean; insights: boolean }
export type SetupFeature = "summaries" | "insights";
export interface SetupHealth {
  recordingState?: "passed" | "needs_action" | "not_verified";
  ok: boolean;
  state: string;
  mode: string;
  cause: string;
  features: SetupFeatures | null;
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
  blocking: boolean;
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
    ...(body.recording_state === "passed" || body.recording_state === "needs_action" || body.recording_state === "not_verified" ? { recordingState: body.recording_state } : {}),
    ok: body.ok, state: body.state,
    mode: typeof body.mode === "string" ? body.mode : "",
    cause: typeof body.cause === "string" ? body.cause : "",
    features: readSetupFeatures(body.features),
  };
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
const RERUN_SETUP: SetupNoticeStep = {
  label: "Re-run Cassini's setup by disabling and enabling the app",
  commands: [`occ app_api:app:disable ${APP_ID}`, `occ app_api:app:enable ${APP_ID}`],
};
const SETTINGS_OFFER: SetupNoticeStep = {
  label: "Open Operator › Publish pipeline to check the recordings account",
  commands: [], action: "settings",
};

export function buildSetupNotice(options: {
  health: SetupHealth | null;
  access: RecordingsAccess | null;
  isAdmin: boolean;
  appUrl: string;
}): SetupNotice | null {
  const { health, access, isAdmin, appUrl } = options;
  const verdict = health ?? access;
  if (!verdict || verdict.ok) return null;
  // The provisioning check gates new publishing. An unverified check after a
  // restart does not revoke current Nextcloud shares, so reads remain visible.
  const blocking = verdict.state !== "unknown";
  const summary = blocking
    ? "Calls will still run, but their recordings will fail until this is fixed."
    : "Recordings that are already here still open, but new ones will fail until this check runs.";
  const base: SetupNotice = {
    blocking, tone: blocking ? "warning" : "neutral",
    title: blocking ? "Cassini can't save recordings right now" : "Cassini hasn't checked that it can save recordings",
    summary, cause: "", steps: [], detail: "", note: "", shareLabel: "", shareUrl: "", reference: "",
  };
  if (!isAdmin) {
    return { ...base, shareLabel: "Send this link to an administrator for setup instructions.", shareUrl: appUrl };
  }
  const step = access?.step ?? "";
  const steps: SetupNoticeStep[] = step === "owner_account"
    ? [SETTINGS_OFFER, { label: "Create the cassini account in Nextcloud", commands: ["occ user:add cassini"] }, RERUN_SETUP]
    : step === "administrator"
      ? [{ label: "Set CASSINI_NC_ADMIN_USER to a Nextcloud administrator", commands: [] }, RERUN_SETUP]
      : [SETTINGS_OFFER, RERUN_SETUP];
  return {
    ...base,
    cause: access?.cause || health?.cause || "Cassini could not confirm its recordings setup.",
    steps,
    detail: access?.detail || `The operator reported ${verdict.state}${step ? ` at ${step}` : ""}.`,
    note: "occ is however your deployment invokes it, for example sudo -u www-data php occ.",
    reference: "See GET /operator/status for the full recordings access report.",
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
// go — see FeatureNotice.panel.
const ADMIN_ACTION = "Open AI providers";
// The button on the compact card. Same words as the button on the panel it
// opens, so the second press is the one the first one promised.
const ADMIN_ACTION_SHORT = "Add a provider";
const NOT_YOURS_TO_FIX = " Ask a Nextcloud administrator.";

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
  const summary =
    feature === "insights"
      ? "Insights need an AI endpoint, and this deployment has none."
      : "Summaries need an AI endpoint and the summarise step switched on.";
  return {
    title:
      feature === "insights" ? "No AI endpoint is available" : "Meetings are not being summarised",
    summary: isAdmin ? summary : summary + NOT_YOURS_TO_FIX,
    panel: isAdmin ? AI_PANEL : "",
    actionLabel: isAdmin ? ADMIN_ACTION : "",
    actionTitle: isAdmin
      ? feature === "insights"
        ? "Add a provider to create insights"
        : "Add a provider to write summaries"
      : undefined,
    actionShortLabel: isAdmin ? ADMIN_ACTION_SHORT : undefined,
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
