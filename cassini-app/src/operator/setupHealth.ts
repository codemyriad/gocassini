// What the Cassini shell shows when the app was never finished being installed
// (D-585 outcome: the message, not the status code).
//
// The operator already knew. Provisioning records why it stopped, /operator/status
// reports it, and publishing refuses. What none of that reached was the person
// looking at the app: the archive fetch failed, and the viewer said
//
//     Could not load the meeting list (HTTP 502).
//
// which names neither the cause nor anyone who could fix it. Worse, it said the
// same thing to an administrator — the one person who could — because a 503 from
// /status also hid the operator surface behind the same "not an admin" branch.
//
// Two audiences, one check. Which message you get is decided by the SAME probe
// that decides whether you see the operator surface at all (adminProbe.ts): the
// operator API is ADMIN at the proxy, so being able to read /status IS being an
// administrator. There is no second notion of admin here to drift from the first.
//
//	administrator   the diagnosis: which app is missing, what to run, what
//	                Nextcloud actually said. Read from /status.
//	everyone else   that Cassini is not set up, that it is not their account,
//	                and a link to hand to someone who can act. Read from
//	                /setup, the USER-readable half that carries the verdict
//	                and none of the detail.

import type { OperatorPanel } from "../surfaceRouting";

// SetupHealth is GET <base>/setup — readable by any logged-in Nextcloud user.
export interface SetupHealth {
  ok: boolean;
  state: string;
  // What this deployment's AI configuration allows, or null when the operator
  // did not say — an install older than D-722, or a build serving no operator
  // at all. Null is a THIRD state and not a default: silence must read as
  // "unknown", never as "not configured", exactly as the catalog's hasSummary
  // does. Nothing renders an unconfigured notice from a question that was
  // never answered.
  features: SetupFeatures | null;
}

// SetupFeatures is the readiness signal (D-722): the two questions every
// "not configured yet" state in the app reduces to. Both are one bit — the
// endpoint, the model and the key are ADMIN-only and stay that way.
export interface SetupFeatures {
  // A recording will be summarised: the step is on and still resolves to an
  // endpoint.
  summaries: boolean;
  // At least one endpoint exists, which is all insight creation needs.
  insights: boolean;
}

export type SetupFeature = "summaries" | "insights";

// RecordingsAccess is the `recordings_access` block of GET <base>/status —
// ADMIN-only, and the only place the actionable detail exists.
export interface RecordingsAccess {
  ok: boolean;
  state: string;
  step: string;
  detail: string;
  prerequisites: { name: string; state: string }[];
}

export interface SetupNoticeStep {
  label: string;
  // Shell lines to run, verbatim. Empty when the step is not a command.
  commands: string[];
}

// SetupNotice is everything the panel renders. The copy lives HERE, not in the
// component, because this is the part worth testing — a wrong instruction is a
// worse failure than a missing one, and .svelte files are not unit-tested in
// this repo.
export interface SetupNotice {
  // blocking means the archive genuinely cannot be read, so the panel stands in
  // for the meeting list. Advisory means setup is unproven but reads still work,
  // and the list must stay: see blocksBrowsing.
  blocking: boolean;
  title: string;
  summary: string;
  steps: SetupNoticeStep[];
  // detail is the operator's own sentence, shown to an administrator so the
  // panel and the container log read the same. Empty for everyone else.
  detail: string;
  // note qualifies the commands (how occ is invoked here). Empty when there are
  // no commands.
  note: string;
  // shareLabel/shareUrl hand a non-admin the one thing they can actually do.
  // Empty for an administrator, who is already looking at the instructions.
  shareLabel: string;
  shareUrl: string;
  // reference points an administrator at the full report. Empty otherwise.
  reference: string;
}

// The app id is fixed by appinfo/info.xml <id>; AppAPI registers under it and
// occ addresses the app by it.
const APP_ID = "gocassini";

// Nextcloud's own names for the two apps, which is what an administrator will
// search the App Store for — the ids are what occ wants. Both are shown.
const NATIVE_APP_NAMES: Record<string, string> = {
  groupfolders: "Team folders",
  group_everyone: "Everyone Group",
};

const RERUN_SETUP: SetupNoticeStep = {
  // Provisioning is driven by the AppAPI enabled callback, so re-running it
  // means re-firing that edge. A container restart alone does not (D-541).
  label: "Re-run Cassini's setup. It runs when the app is enabled, so disable and re-enable it",
  commands: [`occ app_api:app:disable ${APP_ID}`, `occ app_api:app:enable ${APP_ID}`],
};

const OCC_NOTE =
  "occ here is however your deployment invokes it — for example sudo -u www-data php occ …, " +
  "or docker exec -u www-data <nextcloud-container> php occ …";

const STATUS_REFERENCE =
  "The full report, including every step Cassini tried, is at GET /operator/status under recordings_access.";

// fetchSetupHealth asks the USER-readable endpoint whether this deployment can
// serve recordings.
//
// Returns null when the question could not be asked — a transport failure, or a
// 404 from an install registered before this route existed in the manifest
// (routes reach AppAPI at registration time, so an already-installed app does
// not have it until it is re-registered). Null means "no notice": accusing a
// working install of being unconfigured because one fetch failed is a worse
// error than the one this file exists to fix.
export async function fetchSetupHealth(
  operatorBasePath: string,
  fetchImpl: typeof fetch = fetch,
): Promise<SetupHealth | null> {
  const url = `${operatorBasePath.replace(/\/+$/, "")}/setup`;
  try {
    const response = await fetchImpl(url, {
      method: "GET",
      headers: { Accept: "application/json" },
      // Same reason catalog.json is fetched this way: AppAPI caches a proxied
      // GET for an hour, and both answers here change the moment an
      // administrator acts — finishing setup, or configuring an endpoint. A
      // cached one would leave the app telling everyone the deployment is
      // still broken long after it was fixed.
      cache: "no-store",
    });
    if (response.status !== 200) {
      return null;
    }
    return readSetupHealth(await response.json());
  } catch {
    return null;
  }
}

export function readSetupHealth(body: unknown): SetupHealth | null {
  if (!isRecord(body) || typeof body.ok !== "boolean" || typeof body.state !== "string") {
    return null;
  }
  return { ok: body.ok, state: body.state, features: readSetupFeatures(body.features) };
}

// readSetupFeatures insists on both booleans or neither. A half-answer is an
// operator this build does not understand, and guessing the missing half is how
// a deployment that summarises perfectly well ends up told it does not.
export function readSetupFeatures(value: unknown): SetupFeatures | null {
  if (!isRecord(value) || typeof value.summaries !== "boolean" || typeof value.insights !== "boolean") {
    return null;
  }
  return { summaries: value.summaries, insights: value.insights };
}

// readRecordingsAccess pulls the admin-only detail out of a /status body.
// Returns null for anything that is not a status payload — including the HTML
// error page a proxy serves when the ExApp itself is down.
export function readRecordingsAccess(body: unknown): RecordingsAccess | null {
  if (!isRecord(body)) {
    return null;
  }
  const access = body.recordings_access;
  if (!isRecord(access) || typeof access.state !== "string") {
    return null;
  }
  const prerequisites: { name: string; state: string }[] = [];
  if (Array.isArray(access.prerequisites)) {
    for (const entry of access.prerequisites) {
      if (isRecord(entry) && typeof entry.name === "string" && typeof entry.state === "string") {
        prerequisites.push({ name: entry.name, state: entry.state });
      }
    }
  }
  return {
    ok: access.ok === true,
    state: access.state,
    step: typeof access.step === "string" ? access.step : "",
    detail: typeof access.detail === "string" ? access.detail : "",
    prerequisites,
  };
}

// shareableAppUrl is the address of this Cassini page, without the fragment.
// The fragment carries the viewer's own deep link (#meeting=…, #surface=…), and
// sending an administrator to a meeting that does not exist yet is not the
// point — the page itself is, because opening it as an administrator is what
// shows the instructions.
export function shareableAppUrl(href: string): string {
  try {
    const url = new URL(href);
    url.hash = "";
    return url.toString();
  } catch {
    return href;
  }
}

export function buildSetupNotice(options: {
  // The verdict, from /setup. Preferred over `access` so an administrator and a
  // non-administrator are branching on the same fact.
  health: SetupHealth | null;
  // The diagnosis, from /status. Non-null only for an administrator.
  access: RecordingsAccess | null;
  isAdmin: boolean;
  appUrl: string;
}): SetupNotice | null {
  const { health, access, isAdmin, appUrl } = options;
  // /setup is the verdict; /status is the fallback for an install whose manifest
  // predates the route. Either way, no answer means no notice.
  const verdict = health ?? access;
  if (!verdict || verdict.ok) {
    return null;
  }
  const blocking = blocksBrowsing(verdict.state);
  const title = blocking ? "Cassini is not set up yet" : "Cassini has not finished setting itself up";
  if (!isAdmin) {
    return {
      blocking,
      title,
      summary: blocking
        ? "Recordings cannot be shown until an administrator finishes setting Cassini up on " +
          "this Nextcloud. There is nothing wrong with your account, and nothing for you to fix."
        : "New recordings will not appear until an administrator finishes setting Cassini up on " +
          "this Nextcloud. Anything already published is still listed below, and there is " +
          "nothing wrong with your account.",
      steps: [],
      detail: "",
      note: "",
      shareLabel:
        "Send this link to an administrator. Opening it as an administrator shows them " +
        "exactly what is missing and how to fix it.",
      shareUrl: appUrl,
      reference: "",
    };
  }
  const admin = adminNotice(verdict.state, access);
  return {
    blocking,
    title,
    summary: admin.summary,
    steps: admin.steps,
    detail: access?.detail ?? "",
    note: admin.steps.some((step) => step.commands.length > 0) ? OCC_NOTE : "",
    shareLabel: "",
    shareUrl: "",
    reference: STATUS_REFERENCE,
  };
}

// blocksBrowsing decides whether the panel stands in for the meeting list or
// merely sits above it. The question is not "did setup succeed" — it is "can
// this person still read what is already published", and the two are not the
// same bit.
//
// The read path never consults the provisioning record: ncFilesProxy fetches
// catalog.json and each .opus AS THE CALLER, so what it can see is whatever the
// Team folder, the `everyone` mount and the per-file ACLs say — Nextcloud state
// that outlives this container. Only publishing is gated on the record.
//
//	unknown       READABLE. Setup runs on the AppAPI enabled edge, never at
//	              start (D-541), so a plain container restart of a perfectly
//	              provisioned instance lands here. Publishing is refused and
//	              nothing new will appear — worth saying — but every recording
//	              already there still opens. Replacing the list here would blank
//	              a working archive for the whole instance on every reboot.
//	unavailable   NOT READABLE. Nothing was provisioned, or the app supplying
//	              the mount is gone; the per-caller scan finds no mount and the
//	              catalog fails closed to empty.
//	degraded      NOT READABLE. The steps that abort (migration, catalog
//	              migration, root ACL) all run after the mount root has been
//	              narrowed to owner-only, so nobody can traverse to the
//	              recordings.
//
// Anything unrecognised blocks: an unknown state is not evidence that reading
// works.
function blocksBrowsing(state: string): boolean {
  return state !== "unknown";
}

function adminNotice(
  state: string,
  access: RecordingsAccess | null,
): { summary: string; steps: SetupNoticeStep[] } {
  const missing = missingNativeApps(access);
  if (missing.length > 0) {
    return {
      summary:
        "Cassini keeps recordings in Nextcloud Files and shows each person only the meetings " +
        "they were in. It needs two Nextcloud apps to do that, and an external app cannot " +
        "install them for itself. Until they are enabled, recordings reach nobody.",
      steps: [
        {
          label: `Install and enable ${describeApps(missing)}, either from Apps in Nextcloud or on the server`,
          commands: missing.map((app) => `occ app:install ${app} && occ app:enable ${app}`),
        },
        RERUN_SETUP,
      ],
    };
  }
  if (access?.step === "administrator") {
    return {
      summary:
        "Cassini could not find a Nextcloud administrator account to run its one-time setup " +
        "as, so the recordings folder and its permissions were never created.",
      steps: [
        {
          label:
            "Set CASSINI_NC_ADMIN_USER in Cassini's deploy options to an account in Nextcloud's admin group",
          commands: [],
        },
        RERUN_SETUP,
      ],
    };
  }
  if (state === "unknown") {
    return {
      summary:
        "Cassini has not verified its setup since it last restarted. Setup runs when the app " +
        "is enabled, and the app has not been enabled since — so nothing has confirmed where " +
        "recordings would land. Existing recordings are unaffected and still readable, but " +
        "publishing is refused until setup has run.",
      steps: [RERUN_SETUP],
    };
  }
  if (state === "degraded") {
    return {
      summary:
        "A setup step failed, so Cassini cannot prove that recordings would reach the people " +
        "in the meeting. Nothing is missing that you can install — the call itself did not " +
        "succeed.",
      steps: [
        {
          label:
            "Read the nc provision: lines in the Cassini container log, which name the step and what Nextcloud answered, and fix the cause",
          commands: [],
        },
        RERUN_SETUP,
      ],
    };
  }
  // unavailable with a step that is not one of the above, or a state this build
  // does not know. Name what the operator named and stop guessing.
  return {
    summary:
      "Cassini's setup did not complete, so recordings cannot be served. The operator " +
      "reported the state as " +
      (state || "unknown") +
      (access?.step ? ` and stopped at ${access.step}.` : "."),
    steps: [RERUN_SETUP],
  };
}

// missingNativeApps prefers the per-app list, which names every missing app
// rather than only the first. The step is the fallback for an operator that
// reported one without the other.
function missingNativeApps(access: RecordingsAccess | null): string[] {
  if (!access) {
    return [];
  }
  const missing = access.prerequisites
    .filter((entry) => entry.state === "missing")
    .map((entry) => entry.name);
  if (missing.length > 0) {
    return missing;
  }
  const prefix = "app_missing:";
  if (access.step.startsWith(prefix)) {
    const name = access.step.slice(prefix.length).trim();
    if (name) {
      return [name];
    }
  }
  return [];
}

function describeApps(ids: string[]): string {
  const described = ids.map((id) => (NATIVE_APP_NAMES[id] ? `${NATIVE_APP_NAMES[id]} (${id})` : id));
  if (described.length === 1) {
    return described[0];
  }
  return `${described.slice(0, -1).join(", ")} and ${described[described.length - 1]}`;
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
}

// The one panel behind both facts: providers and the summarise step are edited
// together in AI providers (Settings.svelte maps `endpoints` -> LLMSettingsPanel).
const AI_PANEL: OperatorPanel = "endpoints";

// Only an administrator can act, and only they are told there is somewhere to
// go — see FeatureNotice.panel.
const ADMIN_ACTION = "Open AI providers";
const NOT_YOURS_TO_FIX = " Only a Nextcloud administrator can change that, and there is nothing " +
  "wrong with your account.";

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
      ? "Asking a question of a set of meetings needs an AI endpoint, and this deployment has " +
        "none it can reach — either none is configured, or none is switched on for a step. " +
        "Recording and transcription are unaffected: they run here, and need no endpoint."
      : "A summary needs an AI endpoint and the summarise step switched on, and this deployment " +
        "does not have both. Transcripts are unaffected: they are produced here, and need " +
        "neither.";
  return {
    title:
      feature === "insights" ? "No AI endpoint is available" : "Meetings are not being summarised",
    summary: isAdmin ? summary : summary + NOT_YOURS_TO_FIX,
    panel: isAdmin ? AI_PANEL : "",
    actionLabel: isAdmin ? ADMIN_ACTION : "",
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
