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
  const body = new URLSearchParams();
  for (const [key, value] of Object.entries(form)) {
    body.append(key, value);
  }
  const response = await fetchImpl(nextcloudUrl(path), {
    method: "POST",
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
  if (code === 102 || /already/i.test(meta?.message ?? "")) {
    return payload;
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

// randomPassword satisfies the create-account contract and nothing else.
// Nothing ever authenticates with it: every Cassini call acts as the service
// account through AppAPI's act-as-user header, signed with the app secret.
// Mirrors the operator's own randomPassword (nc_provision.go).
function randomPassword(): string {
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
): Promise<void> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const browserSteps = steps.filter((step) => step.browser);
  if (browserSteps.length === 0) {
    return;
  }
  if (!isSetupAvailable()) {
    throw new NcSetupError(
      "unavailable",
      "This page cannot perform the setup: Nextcloud's own scripts are not available here.",
    );
  }

  await confirmPassword();

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
      await runStep(step, requireFolder, folders, fetchImpl);
    } catch (error) {
      // A denial mid-run is almost always the confirmation window closing.
      // Re-confirm and retry the step once; anything else is real.
      if (error instanceof NcSetupError && error.reason === "denied") {
        await confirmPassword();
        try {
          await runStep(step, requireFolder, folders, fetchImpl);
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
}

async function runStep(
  step: StorageSetupStep,
  requireFolder: (step: StorageSetupStep) => Promise<number>,
  folders: FolderCache,
  fetchImpl: typeof fetch,
): Promise<void> {
  const args = step.args ?? {};
  switch (step.action) {
    case "create_group":
      await ncPost("/ocs/v2.php/cloud/groups?format=json", { groupid: args.group ?? "" }, fetchImpl);
      return;
    case "create_user":
      await ncPost(
        "/ocs/v2.php/cloud/users?format=json",
        {
          userid: args.user ?? "",
          password: randomPassword(),
          displayname: args.display_name ?? "",
          // "groups[]", not "groups". OCS decodes this field as a PHP array and
          // answers a bare 400 for a scalar — the same trap the operator's own
          // account creation documents.
          "groups[]": args.group ?? "",
        },
        fetchImpl,
      );
      return;
    case "create_team_folder": {
      // Look before creating. `POST /folders` has no idempotency of its own —
      // it makes a NEW folder every time, mount point and all — so a re-run
      // after a partial failure would leave two folders called `Cassini`, and
      // Nextcloud would mount whichever it liked. Everything else here tolerates
      // being done twice; this is the one call that cannot.
      const mount = args.mount ?? "";
      if ((await folders.resolve(mount)) !== null) {
        return;
      }
      await ncPost(
        "/index.php/apps/groupfolders/folders?format=json",
        { mountpoint: mount },
        fetchImpl,
      );
      // The id the cache should now hand out is the one that did not exist a
      // moment ago.
      folders.invalidate();
      return;
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
      return;
    }
    case "enable_folder_acl": {
      const id = await requireFolder(step);
      await ncPost(`/index.php/apps/groupfolders/folders/${id}/acl?format=json`, { acl: "1" }, fetchImpl);
      return;
    }
    case "delegate_manager": {
      const id = await requireFolder(step);
      await ncPost(
        `/index.php/apps/groupfolders/folders/${id}/manageACL?format=json`,
        { mappingType: "user", mappingId: args.user ?? "", manageAcl: "1" },
        fetchImpl,
      );
      return;
    }
    default:
      // An action this build does not know. Skipping would silently produce a
      // half-built substrate that later reads as healthy.
      throw new NcSetupError("failed", `This version of Cassini cannot perform "${step.action}".`, step.id);
  }
}
