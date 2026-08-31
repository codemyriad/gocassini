// Registration of the capture service worker, called from the Cassini page.
//
// The service worker is the only same-origin way an ExApp can get code onto
// Talk's call page (see sw.ts for why), and it can only be registered by a page
// on that origin — which means the user has to open Cassini once. That visit is
// also where consent is recorded, so the two happen together: enabling source
// capture registers the worker, disabling it unregisters and forgets.

import { talkCallScopes } from "./protocol";

export const CONSENT_STORAGE_KEY = "cassini.sourceCapture.consent";

// SW_FILENAME is served by the operator with `Service-Worker-Allowed: /`, which
// is what lets a script under the ExApp's proxy path claim a scope elsewhere on
// the origin. The header widens the MAXIMUM permitted scope; the narrow Talk
// scopes below are what we actually claim.
const SW_FILENAME = "ui/capture-sw.js";

export function serviceWorkerURL(proxyBase: string): string {
  return proxyBase.replace(/\/+$/, "") + "/" + SW_FILENAME;
}

export function isEnabled(storage: Pick<Storage, "getItem">): boolean {
  try {
    return storage.getItem(CONSENT_STORAGE_KEY) === "granted";
  } catch {
    return false;
  }
}

export interface RegistrationOutcome {
  scope: string;
  ok: boolean;
  error?: string;
}

// registerAll claims each Talk call scope. Two registrations because a
// registration has exactly one scope and Nextcloud serves calls at both
// "<root>/call/<token>" and "<root>/index.php/call/<token>"; an install that
// only ever uses one shape simply gets one dormant registration.
//
// Note these scopes are narrower than the "/" scope Nextcloud's Files app
// registers its own preview service worker at. That is deliberate and load
// bearing: same-scope registration REPLACES, so claiming "/" would silently
// disable core's worker, while a narrower scope only wins on the pages it
// covers.
export async function registerAll(
  container: ServiceWorkerContainer,
  proxyBase: string,
  rootPath: string,
): Promise<RegistrationOutcome[]> {
  const url = serviceWorkerURL(proxyBase);
  const outcomes: RegistrationOutcome[] = [];
  for (const scope of talkCallScopes(rootPath)) {
    try {
      await container.register(url, { scope });
      outcomes.push({ scope, ok: true });
    } catch (error) {
      outcomes.push({ scope, ok: false, error: String(error) });
    }
  }
  return outcomes;
}

export async function unregisterAll(
  container: ServiceWorkerContainer,
  rootPath: string,
): Promise<void> {
  const scopes = new Set(talkCallScopes(rootPath));
  const registrations = await container.getRegistrations();
  for (const registration of registrations) {
    const path = new URL(registration.scope).pathname;
    if (scopes.has(path)) {
      await registration.unregister();
    }
  }
}

// setUp registers the worker when the user has opted in, and does nothing at
// all otherwise. Called on every Cassini page load: re-registering an identical
// script and scope is a no-op in the browser, and it repairs a registration the
// user cleared through site data.
export async function setUp(
  container: ServiceWorkerContainer | undefined,
  storage: Pick<Storage, "getItem">,
  proxyBase: string,
  rootPath: string,
): Promise<RegistrationOutcome[]> {
  if (!container || !isEnabled(storage)) {
    return [];
  }
  return registerAll(container, proxyBase, rootPath);
}
