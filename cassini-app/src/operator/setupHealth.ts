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
  // mode is the storage model in force: "default", "access_controlled", or ""
  // when the operator did not say — an install predating D-755, or a build
  // serving no operator at all (D-756).
  //
  // It is on the USER-readable half deliberately. Who can see a recording is
  // not administrator detail: it is the one fact every reader of the meeting
  // list needs, and the audience chip renders it for everybody. It names no
  // account, no path and no folder id, which is what makes it safe here.
  //
  // The enum names stop at this file. recordingAudience() below turns them into
  // the audience the viewing layer knows about, so no component ever sees
  // `access_controlled`.
  mode: string;
  // cause is why recordings cannot be served, in one plain sentence, or "" when
  // they can be — and also "" when the operator has one but it cannot be told
  // at this level. The operator's table (storage_causes.go) decides that, and
  // withholds every sentence that would name an account, a path or an app.
  //
  // It is read here rather than composed here for the same reason the step is
  // not: the operator is the only side that knows which check stopped, and a
  // sentence assembled from a step name on this side would be this file
  // guessing at the operator's diagnosis all over again.
  cause: string;
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
  // detail is the operator's sentence for whoever is going to FIX this: it
  // quotes Nextcloud, names the folder id and carries the command to run. It
  // belongs in the details block, and nowhere else.
  detail: string;
  // cause is the same failure in one plain sentence, from the operator's own
  // table (storage_causes.go): no command, no path, no enum name. It is what
  // the notice opens with. Empty when this operator is older than the table, or
  // has nothing to say about the step it recorded — the notice falls back to
  // its own sentence rather than to silence.
  cause: string;
  // mode is the storage model this archive is under (D-616): "default",
  // "access_controlled", or "" when no preflight has resolved one. It decides
  // which prerequisite is worth naming — the default model needs no Nextcloud
  // app at all, so telling its administrator to install two would send them
  // after the wrong thing.
  mode: string;
  // modeConfirmed says a person (or a dev/CI deploy option) chose the mode. An
  // unconfirmed mode governs where the archive is and still refuses to publish,
  // because the two models differ in who can read a recording.
  modeConfirmed: boolean;
  prerequisites: { name: string; state: string }[];
}

export interface SetupNoticeStep {
  label: string;
  // Shell lines to run, verbatim. Empty when the step is not a command.
  commands: string[];
  // action turns a step into something to PRESS rather than to find.
  // "settings" opens Operator › Settings, where "Who can see recordings" and
  // the service-account controls live since D-757 — telling an administrator to
  // go and look for it is a navigation they have to perform on the app's
  // behalf. The Setup tab this used to name is gone (D-756).
  action?: "settings";
}

// The two tones a notice can be drawn in. They are decided HERE, with the
// words, because they say the same thing the words do:
//
//	warning  something is broken and recordings are failing now.
//	neutral  nothing is broken as far as anybody knows; a check has not run.
//
// A warning triangle over "a check has not run yet" is the shell shouting about
// a state that is ordinary after every container restart, which is what taught
// administrators to read past this notice.
export type SetupNoticeTone = "warning" | "neutral";

// SetupNotice is everything the panel renders. The copy lives HERE, not in the
// component, because this is the part worth testing — a wrong instruction is a
// worse failure than a missing one, and .svelte files are not unit-tested in
// this repo.
export interface SetupNotice {
  // blocking means the archive genuinely cannot be read, so the panel stands in
  // for the meeting list. Advisory means setup is unproven but reads still work,
  // and the list must stay: see blocksBrowsing.
  blocking: boolean;
  tone: SetupNoticeTone;
  title: string;
  // summary is the CONSEQUENCE, in one sentence: what this costs the person
  // reading it. It is the same sentence for every fault, and the same sentence
  // for both audiences, because the consequence does not depend on which check
  // failed and a non-administrator is owed exactly this much (D-759).
  summary: string;
  // cause is why, in one sentence, and it is the operator's own words wherever
  // the operator has them. Empty for everyone who is not an administrator: the
  // verdict is not private, the diagnosis is.
  cause: string;
  // steps are the technical remedy, and they live INSIDE the details block with
  // the commands. An administrator who wants them opens it; an administrator
  // who wants to press Try again never has to read a command line.
  steps: SetupNoticeStep[];
  // detail is the operator's own sentence, shown to an administrator inside the
  // details block so the panel and the container log read the same. It falls
  // back to the state and the step when the operator sent no sentence, because
  // those are what a bug report needs and the opening sentences no longer carry
  // them. Empty for everyone else.
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

// The dedicated recordings owner, and the narrow group that gives it a
// write-capable mount. Both ids are compile-time constants in the operator
// (webdav_upload.go / nc_provision.go), so they are literals here too.
const SERVICE_ACCOUNT = "cassini";
const SERVICE_ACCOUNT_GROUP = "cassini";

// Machine-readable steps this file branches on. They are the operator's own,
// from nc_storage_probe.go — keyed rather than matched on prose so a reworded
// message cannot silently change which instructions an administrator gets.
const SERVICE_ACCOUNT_STEP = "owner_account";
const MODE_MISMATCH_STEP = "mode_mismatch";

const SERVICE_ACCOUNT_SETUP: SetupNoticeStep = {
  label: `Create the ${SERVICE_ACCOUNT} account and its group. Cassini does not create accounts for you`,
  commands: [
    `occ group:add ${SERVICE_ACCOUNT_GROUP}`,
    `occ user:add --group=${SERVICE_ACCOUNT_GROUP} ${SERVICE_ACCOUNT}`,
  ],
};

// The offer, not the recipe (D-671). Cassini can now perform most of its own
// setup, as the administrator, using Nextcloud's own password confirmation — so
// the first thing to say is "there is a button", and the commands become the
// alternative rather than the only way.
//
// It names Operator › Settings, not the Setup tab: the tab is gone (D-756) and
// the controls it carried are a section of the operator's own settings now.
const SETTINGS_OFFER: SetupNoticeStep = {
  label:
    "Open Operator › Settings. Cassini can make these changes for you, and Nextcloud will ask you " +
    "to confirm your password. Cassini never sees it",
  commands: [],
  action: "settings",
};

// The rule, which is a different offer from the setup one: nothing is missing
// and nothing is broken, and the section is the whole remedy.
const CHOOSE_AUDIENCE_OFFER: SetupNoticeStep = {
  label: "Open Operator › Settings and check who should be able to see recordings",
  commands: [],
  action: "settings",
};

// --- The two sentences every notice opens with (D-759) ---
//
// One on the consequence, one on the cause, and the title says which of the two
// kinds of trouble this is. Everything else — the step, the command, the
// operator's own log line, the address of the full report — is behind "Show
// details", where it was always the right content and never the right opening.

// A fault: recordings are failing right now. There is no "not set up yet" any
// more (D-756): a fresh install resolves a mode on enable and records, so every
// blocking notice this file produces means something broke.
const BLOCKING_TITLE = "Cassini can't save recordings right now";
const BLOCKING_CONSEQUENCE =
  "Calls will still run, but their recordings will fail until this is fixed.";

// Not a fault: the check has not run. Provisioning runs on the AppAPI enabled
// edge and never at start (D-541), so this is the state of every container that
// has been restarted, and the archive underneath it is fine.
const ADVISORY_TITLE = "Cassini hasn't checked that it can save recordings";
const ADVISORY_CONSEQUENCE =
  "Recordings that are already here still open, but new ones will fail until this check runs.";

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
  // An absent mode reads as "" — nobody said — and the chip renders nothing.
  // Guessing one would put a sentence about who can see recordings on screen on
  // the strength of a question that was never answered.
  return {
    ok: body.ok,
    state: body.state,
    mode: typeof body.mode === "string" ? body.mode : "",
    cause: typeof body.cause === "string" ? body.cause : "",
    features: readSetupFeatures(body.features),
  };
}

// RecordingAudience is who can see a recording, in the terms the viewing layer
// renders: the two audience names this whole change is about, plus "" for an
// answer nobody gave.
//
// It exists so the storage enum stops here. `default` and `access_controlled`
// are the operator's words for where the bytes live; "everyone with a Nextcloud
// account" and "meeting participants" are what that means to a person, and the
// chip in the meeting list is a viewing-layer control that must not have to
// know either enum.
export type RecordingAudience = "" | "everyone" | "participants";

export function recordingAudience(health: SetupHealth | null): RecordingAudience {
  switch (health?.mode) {
    case "default":
      return "everyone";
    case "access_controlled":
      return "participants";
    default:
      return "";
  }
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
    cause: typeof access.cause === "string" ? access.cause : "",
    mode: typeof access.mode === "string" ? access.mode : "",
    modeConfirmed: access.mode_confirmed === true,
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
  // There is no "somebody has to decide" state any more (D-756). The operator
  // resolves the mode when it is enabled and records from then on, so every
  // notice this file produces is now a fault: something is missing or something
  // is broken. A fresh install shows no notice at all.
  const blocking = blocksBrowsing(verdict.state);
  const title = blocking ? BLOCKING_TITLE : ADVISORY_TITLE;
  const summary = blocking ? BLOCKING_CONSEQUENCE : ADVISORY_CONSEQUENCE;
  const tone: SetupNoticeTone = blocking ? "warning" : "neutral";
  if (!isAdmin) {
    // The same first sentence an administrator gets, and nothing else. The
    // consequence is not privileged — it is what this person is living with —
    // and every word after it is either a diagnosis they may not see or an
    // instruction they cannot act on.
    //
    // The link survives, because it is not an instruction: it is the whole of
    // the remedy available to them, and opening it as an administrator is what
    // shows the diagnosis to somebody who can use it.
    return {
      blocking,
      tone,
      title,
      summary,
      cause: "",
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
    tone,
    title,
    summary,
    // The operator's own sentence first, in both its forms, and this file's
    // fallback last. Whichever check stopped, the operator is the only side that
    // knows it — a sentence chosen here from a step name is this file guessing,
    // and it guesses only when the operator carried nothing (an install older
    // than the cause table, or a step that build has no copy for).
    cause: access?.cause || health?.cause || admin.cause,
    steps: admin.steps,
    detail: technicalDetail(verdict.state, access),
    note: admin.steps.some((step) => step.commands.length > 0) ? OCC_NOTE : "",
    shareLabel: "",
    shareUrl: "",
    reference: STATUS_REFERENCE,
  };
}

// technicalDetail is the line inside the details block that ties this notice to
// the container log and to /status: the operator's own sentence when there is
// one, and the state and step verbatim when there is not.
//
// The state and step USED to be in the summary, which is how an administrator
// came to be greeted by `storage_mode_undecided`. They are facts worth keeping
// — a monitor keys on them and so does a bug report — so they moved rather than
// went away (D-759).
function technicalDetail(state: string, access: RecordingsAccess | null): string {
  if (access?.detail) {
    return access.detail;
  }
  const step = access?.step ?? "";
  return step
    ? `The operator reported the state ${state || "unknown"} and stopped at ${step}.`
    : `The operator reported the state ${state || "unknown"}.`;
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
): { cause: string; steps: SetupNoticeStep[] } {
  // Two things per branch, and no more: why this happened, in words, and the
  // technical remedy behind the disclosure (D-759). The consequence sentence is
  // the same for every branch and is written once, in buildSetupNotice — what
  // differs between a missing app and a refused ACL is the cause, not the cost.
  //
  // The cause here is a FALLBACK. The operator carries its own sentence per step
  // (storage_causes.go) and buildSetupNotice prefers it; these are what an
  // install older than that table shows, and they are held to the same rule: no
  // command, no path, no enum name.
  //
  // There is no undecided and no unconfirmed mode to branch on any more
  // (D-756): the operator resolves one when it is enabled, and a resolved mode
  // counts as confirmed. Every branch below is a fault.
  //
  // The service account is the one prerequisite BOTH storage models need:
  // every recording is written and read as it, in a Team folder and in a
  // private home alike. It is checked first because in the default model it is
  // the ONLY thing that can be missing, and the missing-apps branch below would
  // otherwise send that administrator to install two apps they do not need
  // (D-616).
  if (access?.step === SERVICE_ACCOUNT_STEP) {
    return {
      cause:
        `Nextcloud is not letting the ${SERVICE_ACCOUNT} account write to its files. This usually ` +
        "means the account was removed or its group changed.",
      steps: [SETTINGS_OFFER, SERVICE_ACCOUNT_SETUP, RERUN_SETUP],
    };
  }
  if (access?.step === "storage_mode_declared_conflict") {
    return {
      cause:
        "A deploy option names a rule for who can see recordings that this Nextcloud does not " +
        "match, so nothing was written down.",
      steps: [CHOOSE_AUDIENCE_OFFER, RERUN_SETUP],
    };
  }
  if (access?.step.startsWith(MODE_MISMATCH_STEP)) {
    return {
      cause:
        "The rule for who can see recordings and the way this Nextcloud is set up disagree, so " +
        "Cassini will not write a recording into a place the reading side is not looking.",
      steps: [
        {
          label:
            "Open Operator › Settings and pick who should be able to see recordings. Switching carries the recordings that are already published, and nothing is removed until they have arrived",
          commands: [],
          action: "settings",
        },
      ],
    };
  }
  const missing = missingNativeApps(access);
  if (missing.length > 0 && access?.mode !== "default") {
    return {
      cause:
        "A Nextcloud app that Cassini needs to show each person only their own recordings is " +
        "switched off, and an external app cannot install it.",
      steps: [
        SETTINGS_OFFER,
        {
          // Installing an app is the one step Cassini may not be able to take:
          // Nextcloud demands the administrator's password on that request
          // itself, which Cassini does not have and will not ask for. It tries,
          // and hands off to Nextcloud's own Apps page when it is refused.
          label: `Install and enable ${describeApps(missing)}, either from Apps in Nextcloud or on the server`,
          commands: missing.map((app) => `occ app:install ${app} && occ app:enable ${app}`),
        },
        RERUN_SETUP,
      ],
    };
  }
  if (access?.step === "administrator") {
    return {
      cause:
        "Cassini could not find a Nextcloud administrator account to act as, so the recordings " +
        "folder and its permissions were never created.",
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
      cause:
        "Cassini checks this Nextcloud when the app is enabled, and it has not been enabled " +
        "since this server started.",
      steps: [RERUN_SETUP],
    };
  }
  if (state === "degraded") {
    return {
      cause:
        "A setup step failed while Cassini was talking to Nextcloud, so nothing here can prove " +
        "a recording would reach the people in the meeting.",
      steps: [
        {
          label:
            "Read the nc provision: lines in the Cassini container log, which name the step and what Nextcloud answered, and fix the cause",
          commands: [],
        },
        SETTINGS_OFFER,
        RERUN_SETUP,
      ],
    };
  }
  // unavailable with a step that is not one of the above, or a state this build
  // does not know. Say that, and no more: the operator's state and step are
  // facts worth keeping, and technicalDetail keeps them where they belong.
  return {
    cause:
      "Cassini's last check of this Nextcloud did not finish, and it did not name a reason " +
      "this version of the app understands.",
    steps: [SETTINGS_OFFER, RERUN_SETUP],
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
