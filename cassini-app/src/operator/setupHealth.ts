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

// SetupHealth is GET <base>/setup — readable by any logged-in Nextcloud user.
export interface SetupHealth {
  ok: boolean;
  state: string;
}

// RecordingsAccess is the `recordings_access` block of GET <base>/status —
// ADMIN-only, and the only place the actionable detail exists.
export interface RecordingsAccess {
  ok: boolean;
  state: string;
  step: string;
  detail: string;
  // mode is the storage model this archive is under (D-616): "default",
  // "access_controlled", or "" when no preflight has resolved one. It decides
  // which prerequisite is worth naming — the default model needs no Nextcloud
  // app at all, so telling its administrator to install two would send them
  // after the wrong thing.
  mode: string;
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
  return { ok: body.ok, state: body.state };
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
    mode: typeof access.mode === "string" ? access.mode : "",
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
  // The service account is the one prerequisite BOTH storage models need:
  // every recording is written and read as it, in a Team folder and in a
  // private home alike. It is checked first because in the default model it is
  // the ONLY thing that can be missing, and the missing-apps branch below would
  // otherwise send that administrator to install two apps they do not need
  // (D-616).
  if (access?.step === SERVICE_ACCOUNT_STEP) {
    return {
      summary:
        "Cassini stores every recording as a dedicated Nextcloud service account, and that " +
        "account does not exist on this instance. Nothing can be published or read until it does.",
      steps: [SERVICE_ACCOUNT_SETUP, RERUN_SETUP],
    };
  }
  if (access?.step.startsWith(MODE_MISMATCH_STEP)) {
    return {
      summary:
        "Cassini's storage mode and this Nextcloud disagree about where recordings live, so it " +
        "will not publish into a place the read side is not looking. " +
        (access.detail || ""),
      steps: [
        {
          label:
            "Open the Setup tab above and pick the storage mode you want. Switching moves the recordings that are already published",
          commands: [],
        },
      ],
    };
  }
  const missing = missingNativeApps(access);
  if (missing.length > 0 && access?.mode !== "default") {
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

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
