import type { StorageSetupStep } from "./types";

// Performing Cassini's setup from the administrator's browser (D-671).
//
// The operator cannot do this. On every currently-shipping Nextcloud, an
// ExApp's act-as-user request has a PHP session but no login token, so
// Nextcloud's password-confirmation middleware refuses every write it guards —
// `POST /cloud/groups`, `POST /cloud/users`, and all the Team-folder writes
// (measured, D-661). The same requests from the administrator's own browser
// session succeed, because that session HAS a token and Nextcloud's own dialog
// can confirm it.
//
//	  ExApp ──act-as──▶ Nextcloud     403, forever
//	  browser ─session─▶ Nextcloud    200, after NC's own confirm dialog
//
// So this runs in the page, on the Nextcloud origin, using Nextcloud's own
// confirmation component. Cassini never sees, holds, or transmits a password:
// `OC.PasswordConfirmation` opens Nextcloud's dialog, the password goes
// straight to `POST /login/confirm`, and what comes back to us is a session
// that Nextcloud considers confirmed for the next 30 minutes.
//
// The one thing this cannot do is install an app. Those routes are annotated
// `PasswordConfirmationRequired(strict: true)`, and strict means the password
// must be ON THE REQUEST — no session, however recently confirmed, satisfies
// it. Nextcloud's own Apps page meets that by attaching the password as HTTP
// Basic; Cassini declines to, which is why `enable_app` steps are marked
// `browser: false` by the operator and handled elsewhere.

// The slice of Nextcloud's page globals this needs. Declared structurally
// rather than imported: everything here is already on the AppAPI embedded page
// (`core-common.js` + `core-main.js` are loaded there, verified), and pulling
// in `@nextcloud/password-confirmation` would add Vue plus a dynamic import —
// which the embedded build forbids (scripts/assert-embedded-single-bundle.mjs).
interface NextcloudGlobals {
  requestToken?: string;
  getRootPath?: () => string;
  PasswordConfirmation?: {
    requiresPasswordConfirmation?: () => boolean;
    requirePasswordConfirmation?: (
      callback: () => void,
      options?: unknown,
      rejectCallback?: () => void,
    ) => void;
  };
}

function nextcloud(): NextcloudGlobals | null {
  const oc = (globalThis as { OC?: NextcloudGlobals }).OC;
  return oc && typeof oc === "object" ? oc : null;
}

// NcSetupError carries a reason the caller branches on, because the three
// failures need three different things from the administrator.
export type NcSetupFailure =
  | "unavailable" // Nextcloud's own scripts are not on this page
  | "cancelled" // the administrator dismissed the password dialog
  | "denied" // Nextcloud refused the write
  | "failed"; // anything else

export class NcSetupError extends Error {
  reason: NcSetupFailure;
  step: string;

  constructor(reason: NcSetupFailure, message: string, step = "") {
    super(message);
    this.name = "NcSetupError";
    this.reason = reason;
    this.step = step;
  }
}

// isSetupAvailable reports whether this page can perform the setup at all.
// False on the standalone build, which is served from Cassini's own origin and
// has neither Nextcloud's scripts nor its session.
export function isSetupAvailable(): boolean {
  const oc = nextcloud();
  return !!oc && typeof oc.PasswordConfirmation?.requirePasswordConfirmation === "function";
}

// nextcloudUrl builds an absolute path on the Nextcloud origin.
//
// It cannot use the operator base path: the SPA is served THROUGH the AppAPI
// proxy (`/index.php/apps/app_api/proxy/gocassini/…`), so a relative URL would
// address the proxy and reach the ExApp instead of Nextcloud. `OC.getRootPath()`
// is the web root of the Nextcloud install itself, which is what these routes
// hang off.
export function nextcloudUrl(path: string): string {
  const root = nextcloud()?.getRootPath?.() ?? "";
  return `${root.replace(/\/+$/, "")}${path}`;
}

// confirmPassword asks Nextcloud to confirm the administrator's identity, using
// Nextcloud's own dialog.
//
// It resolves immediately when the session was confirmed recently — logging in
// counts, so in practice the dialog appears only after about half an hour of
// idling. That is Nextcloud's policy, not ours, and overriding it would mean
// collecting the password ourselves and posting it to `/login/confirm`: two
// lines of code, and the moment Cassini becomes something that handles
// credentials. It stays Nextcloud's business.
export function confirmPassword(): Promise<void> {
  const confirmation = nextcloud()?.PasswordConfirmation;
  if (!confirmation?.requirePasswordConfirmation) {
    return Promise.reject(
      new NcSetupError(
        "unavailable",
        "Nextcloud's password-confirmation dialog is not available on this page.",
      ),
    );
  }
  if (confirmation.requiresPasswordConfirmation?.() === false) {
    return Promise.resolve();
  }
  return new Promise((resolve, reject) => {
    confirmation.requirePasswordConfirmation!(
      () => resolve(),
      {},
      () => reject(new NcSetupError("cancelled", "Password confirmation was cancelled.")),
    );
  });
}

interface OcsEnvelope {
  ocs?: {
    meta?: { status?: string; statuscode?: number; message?: string };
    data?: unknown;
  };
  // alreadyThere is set by ncRequest, not by Nextcloud: the write was accepted
  // because the thing it would have created is already there.
  alreadyThere?: boolean;
}

// ncPost issues one write to Nextcloud as the signed-in administrator.
//
// `requesttoken` is Nextcloud's CSRF token and `OCS-APIRequest` is what makes
// the Group Folders front-page routes answer a JSON OCS envelope instead of
// HTML. `credentials: "same-origin"` carries the session cookie — the whole
// mechanism.
async function ncPost(
  path: string,
  form: Record<string, string>,
  fetchImpl: typeof fetch = fetch,
): Promise<OcsEnvelope> {
  return ncRequest("POST", path, form, fetchImpl);
}

// ncRequest is the same call with the verb as a parameter.
//
// Changing an account's password is `PUT /ocs/v2.php/cloud/users/{id}` with a
// `key`/`value` pair, and it is the only write here that is not a POST — the
// provisioning API models editing a user as a field update rather than as a
// distinct route (D-708).
async function ncRequest(
  method: string,
  path: string,
  form: Record<string, string>,
  fetchImpl: typeof fetch = fetch,
): Promise<OcsEnvelope> {
  const body = new URLSearchParams();
  for (const [key, value] of Object.entries(form)) {
    body.append(key, value);
  }
  const response = await fetchImpl(nextcloudUrl(path), {
    method,
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/x-www-form-urlencoded",
      Accept: "application/json",
      "OCS-APIRequest": "true",
      requesttoken: nextcloud()?.requestToken ?? "",
    },
    body: body.toString(),
  });
  let payload: OcsEnvelope = {};
  try {
    payload = (await response.json()) as OcsEnvelope;
  } catch {
    // A body that is not JSON is only a problem if the status was not a
    // success; the check below decides.
  }
  const meta = payload.ocs?.meta;
  const code = meta?.statuscode;

  // Idempotent already-there answers are successes. A setup flow is re-run
  // after a partial failure more often than it is run clean, so "it is already
  // there" must not read as a failure.
  //
  // The match is on "already" rather than on a specific sentence because the
  // three writes that can hit it say three different things — OCS 102 "group
  // exists" and "User already exists" from the provisioning API, "Group already
  // assigned" from Group Folders. The operator's own provisioner settled on the
  // same loose test for the same reason (nc_provision.go).
  //
  // It is flagged as well as accepted. "The account already existed" and "the
  // account was created just now" are the same success to the flow and opposite
  // answers to the one question the caller has to get right: whether the
  // password it is about to show anybody was actually set (D-708).
  if (code === 102 || /already/i.test(meta?.message ?? "")) {
    return { ...payload, alreadyThere: true };
  }
  if (response.ok && (code === undefined || code === 100 || code === 200)) {
    return payload;
  }
  const message = meta?.message || `HTTP ${response.status}`;
  // A refusal can arrive as an HTTP 403 or as an HTTP **200** carrying 403 in
  // the OCS envelope — which is exactly what every Group Folders write does,
  // and it is the shape that matters most here: those are the steps most likely
  // to straddle the confirmation window. Keying this on the HTTP status alone
  // made the re-confirm-and-retry path below dead for all of them.
  if (response.status === 403 || code === 403) {
    throw new NcSetupError(
      "denied",
      `Nextcloud refused: ${message}. Your confirmation may have expired — try again.`,
    );
  }
  throw new NcSetupError("failed", `Nextcloud refused: ${message}`);
}

// randomPassword mints the service account's credential, in the browser, from
// the platform's own CSPRNG.
//
// It is generated HERE and nowhere else, and that is the whole design of the
// password flow (D-708). Cassini's operator authenticates as this account
// through AppAPI's act-as-user header, signed with the app secret, so it needs
// no password of its own — and a password reaching the operator would be a
// credential at rest, on its volume, for an account nothing authenticates as.
// What an administrator needs is to be able to SIGN IN as it, so the value is
// shown to them once and kept by them.
//
// The shape satisfies every Nextcloud password policy the app ships with: 32
// bytes of entropy, base64url, with an upper/lower/digit/special prefix so a
// policy demanding character classes cannot reject it.
export function randomPassword(): string {
  const bytes = new Uint8Array(32);
  globalThis.crypto.getRandomValues(bytes);
  const base64 = btoa(String.fromCharCode(...bytes))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  return `Cw1!${base64}`;
}

// FolderCache is the one Team-folder lookup a run performs, shared between the
// idempotency check and the steps that map onto the folder.
interface FolderCache {
  id: number | null;
  resolve: (mount: string) => Promise<number | null>;
  invalidate: () => void;
}

export interface SetupProgress {
  step: StorageSetupStep;
  index: number;
  total: number;
}

// SetupOutcome is what a run produced that the caller has to show.
//
// Today that is one thing: the service account's password, when this run created
// the account. It exists as a returned value rather than as anything stored
// because the value must not outlive the render — the panel shows it once, the
// administrator copies it, and nothing else in the app or the operator ever has
// it.
export interface SetupOutcome {
  // createdAccount is the account this run created, or "" when it created none.
  createdAccount: string;
  // password is the credential set on it. Empty unless createdAccount is set.
  password: string;
}

interface RunOptions {
  onProgress?: (progress: SetupProgress) => void;
  fetchImpl?: typeof fetch;
}

// findFolderId resolves the Team folder by mount point.
//
// Every mapping step addresses the folder this way rather than by an id baked
// into the plan, because on a run that also CREATES the folder there is no id
// until it exists — and a stale one would map Cassini's groups onto whatever
// folder happens to hold it now.
async function findFolderId(
  mount: string,
  fetchImpl: typeof fetch,
): Promise<number | null> {
  const response = await fetchImpl(
    nextcloudUrl("/index.php/apps/groupfolders/folders?format=json"),
    {
      credentials: "same-origin",
      headers: { Accept: "application/json", "OCS-APIRequest": "true" },
    },
  );
  if (!response.ok) {
    return null;
  }
  const payload = (await response.json()) as OcsEnvelope;
  const data = payload.ocs?.data;
  const rows: unknown[] = Array.isArray(data) ? data : Object.values(data ?? {});
  const ids: number[] = [];
  for (const row of rows) {
    if (row == null || typeof row !== "object") continue;
    const folder = row as Record<string, unknown>;
    if (folder.mount_point !== mount && folder.mountPoint !== mount) continue;
    const id = Number(folder.id);
    if (Number.isFinite(id)) ids.push(id);
  }
  // Lowest id wins, the same rule the operator uses, so a duplicated mount
  // point resolves to the same folder on both sides rather than flapping.
  return ids.length > 0 ? Math.min(...ids) : null;
}

// runSetupPlan executes the steps the operator said the browser can do.
//
// It confirms once up front — Nextcloud's window is session-wide and covers the
// whole run — and re-confirms once if a step is denied part-way, which is what
// a run straddling the 30-minute boundary looks like.
//
// `enable_app` steps are skipped, not failed: they are the operator's to
// attempt and the administrator's to finish. The caller handles them.
export async function runSetupPlan(
  steps: StorageSetupStep[],
  options: RunOptions = {},
): Promise<SetupOutcome> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const outcome: SetupOutcome = { createdAccount: "", password: "" };
  const browserSteps = steps.filter((step) => step.browser);
  if (browserSteps.length === 0) {
    return outcome;
  }
  if (!isSetupAvailable()) {
    throw new NcSetupError(
      "unavailable",
      "This page cannot perform the setup: Nextcloud's own scripts are not available here.",
    );
  }

  await confirmPassword();

  // Generated ONCE, before the first attempt, and reused by the retry.
  //
  // The retry below re-runs a denied step after re-confirming, and `create_user`
  // used to mint a fresh password each time. Combined with ncPost treating an
  // "already exists" reply as success, that meant a run whose first attempt
  // actually created the account could report a second password that was never
  // set. Invisible while nothing read the value; a lie the moment it is shown.
  const accountPassword = randomPassword();

  // One resolution of the Team folder id, shared by every step that needs it —
  // the existence check that makes creating it idempotent, and the mappings
  // afterwards. Creating invalidates it, because the id it should now return is
  // the one that did not exist a moment ago.
  const folders: FolderCache = {
    id: null,
    async resolve(mount: string) {
      if (this.id === null) {
        this.id = await findFolderId(mount, fetchImpl);
      }
      return this.id;
    },
    invalidate() {
      this.id = null;
    },
  };
  const requireFolder = async (step: StorageSetupStep): Promise<number> => {
    const mount = step.args?.mount ?? "";
    const id = await folders.resolve(mount);
    if (id === null) {
      throw new NcSetupError("failed", `Could not find the ${mount} Team folder.`, step.id);
    }
    return id;
  };

  for (let index = 0; index < browserSteps.length; index += 1) {
    const step = browserSteps[index];
    options.onProgress?.({ step, index, total: browserSteps.length });
    try {
      const existed = await runStep(step, requireFolder, folders, accountPassword, fetchImpl);
      // Only a real creation yields a credential to show. An account that was
      // already there kept whatever password it had, and offering this run's
      // generated one would hand an administrator a string that does not sign
      // in anywhere — the reset control is what covers that case.
      if (step.action === "create_user" && !existed) {
        outcome.createdAccount = step.args?.user ?? "";
        outcome.password = accountPassword;
      }
    } catch (error) {
      // A denial mid-run is almost always the confirmation window closing.
      // Re-confirm and retry the step once; anything else is real.
      if (error instanceof NcSetupError && error.reason === "denied") {
        await confirmPassword();
        try {
          const existed = await runStep(step, requireFolder, folders, accountPassword, fetchImpl);
          if (step.action === "create_user" && !existed) {
            outcome.createdAccount = step.args?.user ?? "";
            outcome.password = accountPassword;
          }
        } catch (retryError) {
          // The retry's failure has to carry the step too, or the second
          // attempt reports less than the first did.
          if (retryError instanceof NcSetupError) {
            retryError.step = step.id;
          }
          throw retryError;
        }
        continue;
      }
      if (error instanceof NcSetupError) {
        error.step = step.id;
      }
      throw error;
    }
  }
  return outcome;
}

// resetServiceAccountPassword mints a new credential for an account that already
// exists, and hands it back to be shown once.
//
// It is a first-class call rather than an eighth plan action, because a plan is
// emitted only for a mode that is NOT ready — a healthy instance has an empty
// plan, so there would be no step to hang it on, which is exactly when an
// administrator who has lost the password needs this.
//
// `PUT /ocs/v2.php/cloud/users/{id}` with `key=password` is guarded by the same
// password-confirmation middleware every other provisioning write is. Whether it
// is the STRICT variant — which no session satisfies, however recently confirmed
// — has not been measured anywhere in this repo. So it is attempted, and a
// refusal is reported as one rather than as a failure, with the `occ` line the
// operator supplies as the way through.
export async function resetServiceAccountPassword(
  user: string,
  options: { fetchImpl?: typeof fetch } = {},
): Promise<string> {
  const fetchImpl = options.fetchImpl ?? fetch;
  if (!isSetupAvailable()) {
    throw new NcSetupError(
      "unavailable",
      "This page cannot change the account's password: Nextcloud's own scripts are not available here.",
    );
  }
  await confirmPassword();
  const password = randomPassword();
  const set = () =>
    ncRequest(
      "PUT",
      `/ocs/v2.php/cloud/users/${encodeURIComponent(user)}?format=json`,
      { key: "password", value: password },
      fetchImpl,
    );
  try {
    await set();
  } catch (error) {
    // The same re-confirm-and-retry the plan uses, for the same reason: a
    // denial is most often the 30-minute confirmation window closing.
    if (error instanceof NcSetupError && error.reason === "denied") {
      await confirmPassword();
      await set();
    } else {
      throw error;
    }
  }
  return password;
}

async function runStep(
  step: StorageSetupStep,
  requireFolder: (step: StorageSetupStep) => Promise<number>,
  folders: FolderCache,
  accountPassword: string,
  fetchImpl: typeof fetch,
): Promise<boolean> {
  const args = step.args ?? {};
  switch (step.action) {
    case "create_group":
      await ncPost("/ocs/v2.php/cloud/groups?format=json", { groupid: args.group ?? "" }, fetchImpl);
      return false;
    case "create_user": {
      const answer = await ncPost(
        "/ocs/v2.php/cloud/users?format=json",
        {
          userid: args.user ?? "",
          // The run's password, not a fresh one: a retried step must set the
          // credential the caller is about to show, not a second one.
          password: accountPassword,
          displayname: args.display_name ?? "",
          // "groups[]", not "groups". OCS decodes this field as a PHP array and
          // answers a bare 400 for a scalar — the same trap the operator's own
          // account creation documents.
          "groups[]": args.group ?? "",
        },
        fetchImpl,
      );
      return answer.alreadyThere === true;
    }
    case "create_team_folder": {
      // Look before creating. `POST /folders` has no idempotency of its own —
      // it makes a NEW folder every time, mount point and all — so a re-run
      // after a partial failure would leave two folders called `Cassini`, and
      // Nextcloud would mount whichever it liked. Everything else here tolerates
      // being done twice; this is the one call that cannot.
      const mount = args.mount ?? "";
      if ((await folders.resolve(mount)) !== null) {
        return true;
      }
      await ncPost(
        "/index.php/apps/groupfolders/folders?format=json",
        { mountpoint: mount },
        fetchImpl,
      );
      // The id the cache should now hand out is the one that did not exist a
      // moment ago.
      folders.invalidate();
      return false;
    }
    case "map_group": {
      const id = await requireFolder(step);
      await ncPost(
        `/index.php/apps/groupfolders/folders/${id}/groups?format=json`,
        { group: args.group ?? "" },
        fetchImpl,
      );
      // Assigning the group and setting its level are two calls; the second is
      // the authoritative one, and re-running it is how a wrong level is fixed.
      await ncPost(
        `/index.php/apps/groupfolders/folders/${id}/groups/${encodeURIComponent(args.group ?? "")}?format=json`,
        { permissions: args.permissions ?? "" },
        fetchImpl,
      );
      return false;
    }
    case "enable_folder_acl": {
      const id = await requireFolder(step);
      await ncPost(`/index.php/apps/groupfolders/folders/${id}/acl?format=json`, { acl: "1" }, fetchImpl);
      return false;
    }
    case "delegate_manager": {
      const id = await requireFolder(step);
      await ncPost(
        `/index.php/apps/groupfolders/folders/${id}/manageACL?format=json`,
        { mappingType: "user", mappingId: args.user ?? "", manageAcl: "1" },
        fetchImpl,
      );
      return false;
    }
    default:
      // An action this build does not know. Skipping would silently produce a
      // half-built substrate that later reads as healthy.
      throw new NcSetupError("failed", `This version of Cassini cannot perform "${step.action}".`, step.id);
  }
}
